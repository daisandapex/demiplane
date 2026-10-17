// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// budget.go bounds the work, output and memory of one render. The input cap
// the server applies (8 MiB of markdown) does not bound the output: a table
// pads every body row to the header's width, and every heading, link or
// frontmatter field adds markup, so a small document can describe a huge
// page. The render therefore keeps two kinds of bound:
//
//   - One output budget (MaxOutputBytes). Every byte the render produces, in
//     the body, the meta-header, the rail, the outline and the page chrome, is
//     written through an out (below), which charges it to the budget before
//     appending it and fails, sticky, once the budget would be exceeded. Text
//     that is built before it is written (a paragraph, a code block, a table
//     row, a heading) is held first to the remaining budget by an upper bound
//     on what it can expand to, so nothing proportional to the input is built
//     past the budget.
//   - Input caps on everything the render keeps per input element that is
//     not output: lines (an offset each), headings (an id and an outline
//     entry), list nesting (a stack frame), and frontmatter fields and bytes.
//
// The context is checked as lines are consumed.

// Limits bounds one render. A zero field takes the matching DefaultLimits
// value, so the zero Limits is the default policy.
type Limits struct {
	// MaxTableColumns caps the columns of any one table, counted from the
	// header row before it is split.
	MaxTableColumns int
	// MaxTableCells caps the body cells of all tables in the document
	// together, counted as rows times header columns (the padded width the
	// table is emitted at).
	MaxTableCells int
	// MaxOutputBytes caps the rendered document: every byte is charged to it
	// as it is produced.
	MaxOutputBytes int
	// MaxLines caps the lines of the input, counted before they are indexed.
	MaxLines int
	// MaxHeadings caps the headings of a document (each keeps an id, and on a
	// masthead page an outline entry).
	MaxHeadings int
	// MaxListDepth caps list nesting.
	MaxListDepth int
	// MaxMetaFields caps the frontmatter fields rendered as the meta-header.
	MaxMetaFields int
	// MaxMetaBytes caps the frontmatter block rendered as the meta-header.
	MaxMetaBytes int
	// MaxSeriesMembers caps the series family a page navigates (itself
	// included). A larger series renders without the series tree and the
	// prev/next row.
	MaxSeriesMembers int
}

// Per-field frontmatter caps for the meta-header.
const (
	maxMetaKey   = 256
	maxMetaValue = 8 << 10
)

// DefaultLimits is the render policy when a Limits field is zero.
//
// The output default sits above the worst ordinary expansion measured for an
// 8 MiB input (TestDefaultOutputCapFitsMaxInput, full chrome): the README as
// prose renders at 1.68x its source (14.1 MB) and highlighted Go at 1.84x
// (15.4 MB). 32 MiB leaves about twice that for denser markup and escapes,
// and still bounds the transient copies a render makes of its output.
//
// The input caps sit far above ordinary documents: the README repeated to
// 8 MiB has about 13,000 headings and 170,000 lines.
var DefaultLimits = Limits{
	MaxTableColumns: 128,
	MaxTableCells:   1_000_000,
	MaxOutputBytes:  32 << 20,
	MaxLines:        1 << 21,
	MaxHeadings:     50_000,
	MaxListDepth:    64,
	MaxMetaFields:   256,
	MaxMetaBytes:    64 << 10,
	// MaxSeriesMembers: the server asks the store for at most this many
	// siblings, so a larger series is seen as one and renders no navigation.
	MaxSeriesMembers: 1000,
}

// withDefaults fills zero fields from DefaultLimits.
func (l Limits) withDefaults() Limits {
	if l.MaxTableColumns <= 0 {
		l.MaxTableColumns = DefaultLimits.MaxTableColumns
	}
	if l.MaxTableCells <= 0 {
		l.MaxTableCells = DefaultLimits.MaxTableCells
	}
	if l.MaxOutputBytes <= 0 {
		l.MaxOutputBytes = DefaultLimits.MaxOutputBytes
	}
	if l.MaxLines <= 0 {
		l.MaxLines = DefaultLimits.MaxLines
	}
	if l.MaxHeadings <= 0 {
		l.MaxHeadings = DefaultLimits.MaxHeadings
	}
	if l.MaxListDepth <= 0 {
		l.MaxListDepth = DefaultLimits.MaxListDepth
	}
	if l.MaxMetaFields <= 0 {
		l.MaxMetaFields = DefaultLimits.MaxMetaFields
	}
	if l.MaxMetaBytes <= 0 {
		l.MaxMetaBytes = DefaultLimits.MaxMetaBytes
	}
	if l.MaxSeriesMembers <= 0 {
		l.MaxSeriesMembers = DefaultLimits.MaxSeriesMembers
	}
	return l
}

// unlimited is the policy for trusted, operator-shipped documents (Body).
var unlimited = Limits{
	MaxTableColumns:  maxInt,
	MaxTableCells:    maxInt,
	MaxOutputBytes:   maxInt,
	MaxLines:         maxInt,
	MaxHeadings:      maxInt,
	MaxListDepth:     maxInt,
	MaxMetaFields:    maxInt,
	MaxMetaBytes:     maxInt,
	MaxSeriesMembers: maxInt,
}

const maxInt = int(^uint(0) >> 1)

// ErrBudget is matched (errors.Is) by every limit rejection.
var ErrBudget = errors.New("markdown render limit exceeded")

// BudgetError names the limit a render exceeded.
type BudgetError struct {
	Limit string // e.g. "table columns"
	Max   int
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("markdown render limit exceeded: %s (max %d)", e.Limit, e.Max)
}

// Is makes errors.Is(err, ErrBudget) true for every BudgetError.
func (e *BudgetError) Is(target error) bool { return target == ErrBudget }

// ctxCheckEvery is how many lines a render consumes between context checks.
const ctxCheckEvery = 256

// state is the per-render bookkeeping: the limits, the context, the output
// and cells used so far, the heading ids handed out, and the page facts the
// body produces as it is rendered.
type state struct {
	ctx   context.Context
	lim   Limits
	used  int   // output bytes charged so far, across every out of the render
	err   error // the first limit the render hit; sticky
	cells int
	ticks int
	slugs slugger

	headings int
	// lead tags the body's first block, when it is a paragraph, as the lead;
	// codeFirst tags a top-level paragraph that opens with inline code (the F
	// layout hangs its chip); outline collects h2/h3 entries for the rail.
	lead, codeFirst, collectOutline bool
	blocks                          int // top-level blocks written so far
	outline                         []outlineItem
}

func newState(ctx context.Context, lim Limits) *state {
	return &state{ctx: ctx, lim: lim, slugs: newSlugger()}
}

// tick counts one consumed line and checks the context every ctxCheckEvery.
func (st *state) tick() error {
	st.ticks++
	if st.ticks%ctxCheckEvery != 0 {
		return nil
	}
	return st.ctxErr()
}

func (st *state) ctxErr() error {
	if err := st.ctx.Err(); err != nil {
		return fmt.Errorf("markdown render stopped: %w", err)
	}
	return nil
}

// remaining is the output budget not yet charged.
func (st *state) remaining() int { return st.lim.MaxOutputBytes - st.used }

// overBudget records and returns the output budget error.
func (st *state) overBudget() error {
	if st.err == nil {
		st.err = &BudgetError{Limit: "output bytes", Max: st.lim.MaxOutputBytes}
	}
	return st.err
}

// fits fails, recording the error, unless n more output bytes fit the budget.
// It charges nothing; the bytes are charged when they are written.
func (st *state) fits(n int) error {
	if st.err != nil {
		return st.err
	}
	if n > st.remaining() {
		return st.overBudget()
	}
	return nil
}

// charge counts n bytes of output against the budget.
func (st *state) charge(n int) error {
	if err := st.fits(n); err != nil {
		return err
	}
	st.used += n
	return nil
}

// limit records and returns a limit other than the output budget.
func (st *state) limit(name string, max int) error {
	if st.err == nil {
		st.err = &BudgetError{Limit: name, Max: max}
	}
	return st.err
}

// out is a budgeted output buffer. Every write is charged to the render's
// one output budget before it is appended; a write that does not fit is
// dropped and the render's error is set, so a buffer never holds more than
// the budget allows. A render may keep several (body, meta-header, rail);
// they share the budget.
type out struct {
	st *state
	b  strings.Builder
}

func (st *state) newOut() *out { return &out{st: st} }

func (o *out) WriteString(s string) {
	if o.st.charge(len(s)) == nil {
		o.b.WriteString(s)
	}
}

func (o *out) Len() int       { return o.b.Len() }
func (o *out) String() string { return o.b.String() }

// keep appends s, which an earlier out already charged, without charging it
// again (the meta-header moving into the rail).
func (o *out) keep(s string) { o.b.WriteString(s) }

// output fails once n bytes exceed the output cap.
func (st *state) output(n int) error {
	if n > st.lim.MaxOutputBytes {
		return &BudgetError{Limit: "output bytes", Max: st.lim.MaxOutputBytes}
	}
	return nil
}

// escapedLen is the length of html.EscapeString(s), counted without
// allocating it.
func escapedLen(s string) int {
	n := len(s)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&', '\'', '"':
			n += 4
		case '<', '>':
			n += 3
		}
	}
	return n
}

// inlineUpper is an upper bound on len(inline(s)), counted without
// allocating: the escaped text plus the most markup each construct can add.
// A code span turns two backticks into <code></code> (+11), a link turns
// "[", "](" and ")" into an anchor tag (+37; 40 is counted per "]"), strong
// and emphasis add at most 7 per "*" and 4 per "_", and a NUL becomes U+FFFD
// (+2). Every intermediate string inline builds is no longer than its result.
// TestInlineMatchesRegexOracle checks the bound against random text.
func inlineUpper(s string) int {
	n := escapedLen(s)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '`':
			n += 6
		case ']':
			n += 40
		case '*':
			n += 7
		case '_':
			n += 4
		case 0:
			n += 2
		}
	}
	return n
}

// inlineFits fails unless inline text s, formatted, fits the remaining
// output budget. It runs before inline(), so an oversized block is refused
// before it is escaped and formatted (each formatting pass copies the text).
func (st *state) inlineFits(s string) error { return st.fits(inlineUpper(s)) }

// tableColumns admits a table of cols columns.
func (st *state) tableColumns(cols int) error {
	if cols > st.lim.MaxTableColumns {
		return &BudgetError{Limit: "table columns", Max: st.lim.MaxTableColumns}
	}
	return nil
}

// tableRow admits one more body row of cols cells.
func (st *state) tableRow(cols int) error {
	if cols > st.lim.MaxTableCells-st.cells {
		return &BudgetError{Limit: "table cells", Max: st.lim.MaxTableCells}
	}
	st.cells += cols
	return nil
}
