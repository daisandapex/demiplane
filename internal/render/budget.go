// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"context"
	"errors"
	"fmt"
)

// budget.go bounds the work and output of one render. The input cap the
// server applies (8 MiB of markdown) does not bound the output: a table pads
// every body row to the header's width, so a few hundred kilobytes of pipes
// can describe a billion cells. Render therefore counts table columns and
// cells from cheap delimiter counts before any cell is split or rendered,
// caps the total output, and checks the caller's context as it goes.

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
	// MaxOutputBytes caps the rendered document, checked after every block
	// and on the finished page.
	MaxOutputBytes int
}

// DefaultLimits is the render policy when a Limits field is zero.
//
// The output default sits above the worst ordinary expansion measured for an
// 8 MiB input (TestDefaultOutputCapFitsMaxInput, full chrome): the README as
// prose renders at 1.68x its source (14.1 MB) and highlighted Go at 1.84x
// (15.4 MB). 32 MiB leaves about twice that for denser markup and escapes,
// and still bounds the transient copies a render makes of its output.
var DefaultLimits = Limits{
	MaxTableColumns: 128,
	MaxTableCells:   1_000_000,
	MaxOutputBytes:  32 << 20,
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
	return l
}

// unlimited is the policy for trusted, operator-shipped documents (Body).
var unlimited = Limits{
	MaxTableColumns: int(^uint(0) >> 1),
	MaxTableCells:   int(^uint(0) >> 1),
	MaxOutputBytes:  int(^uint(0) >> 1),
}

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

// state is the per-render bookkeeping: the limits, the context, the cells
// used so far, and the heading ids handed out.
type state struct {
	ctx   context.Context
	lim   Limits
	cells int
	ticks int
	slugs slugger
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

// inlineFits fails when inline text s, once escaped, would carry output past
// the cap given used bytes already emitted. It runs before inline() so an
// oversized block is refused before it is escaped and formatted (each
// formatting pass copies the escaped text). Formatting only adds markup, apart
// from the address of a link it refuses, so the escaped size is a fair floor.
func (st *state) inlineFits(s string, used int) error {
	if escapedLen(s) > st.lim.MaxOutputBytes-used {
		return &BudgetError{Limit: "output bytes", Max: st.lim.MaxOutputBytes}
	}
	return nil
}

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
