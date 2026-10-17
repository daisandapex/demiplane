// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// measureWork renders src and returns the work the slug and code-span passes
// reported through workProbe.
func measureWork(t *testing.T, src string) int {
	t.Helper()
	total := 0
	workProbe = func(n int) { total += n }
	defer func() { workProbe = nil }()
	if _, err := Render(context.Background(), []byte(src), Options{}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return total
}

// assertLinear fails when doubling the input more than triples the work.
func assertLinear(t *testing.T, name string, gen func(n int) string) {
	t.Helper()
	a := measureWork(t, gen(2000))
	b := measureWork(t, gen(4000))
	c := measureWork(t, gen(8000))
	t.Logf("%s work: n=2000 %d, n=4000 %d, n=8000 %d", name, a, b, c)
	if b > 3*a+16 || c > 3*b+16 {
		t.Fatalf("%s work grows faster than linear: %d, %d, %d", name, a, b, c)
	}
}

func TestHeadingSlugWorkLinear(t *testing.T) {
	assertLinear(t, "repeated heading", func(n int) string { return strings.Repeat("## x\n", n) })
	// Natural ids that sit in the suffix space must not make it superlinear.
	assertLinear(t, "interleaved suffixes", func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "## x\n## x %d\n", i+1)
		}
		return b.String()
	})
}

func TestCodeSpanWorkLinear(t *testing.T) {
	assertLinear(t, "code spans", func(n int) string { return strings.Repeat("`a` ", n) })
}

// oldUniqueSlug is the original search-from-one algorithm, kept as the
// reference the counters must agree with.
func oldUniqueSlug(text string, used map[string]bool) string {
	base := slugify(text)
	if base == "" {
		base = "section"
	}
	s := base
	for i := 1; used[s]; i++ {
		s = base + "-" + strconv.Itoa(i)
	}
	used[s] = true
	return s
}

func TestSluggerMatchesSearchFromOne(t *testing.T) {
	words := []string{"x", "x", "x 1", "x-1", "x 2", "x-1-1", "x 1 1", "", "!!", "section", "section 1", "y", "y 3"}
	rng := rand.New(rand.NewSource(1))
	for round := 0; round < 200; round++ {
		used := map[string]bool{}
		sg := newSlugger()
		for i := 0; i < 60; i++ {
			w := words[rng.Intn(len(words))]
			want := oldUniqueSlug(w, used)
			if got := sg.unique(w); got != want {
				t.Fatalf("round %d step %d: %q got id %q, want %q", round, i, w, got, want)
			}
		}
	}
}

// wideTable is the reported payload: a 10,000-column header, one separator
// and 100,000 one-cell rows, which the old renderer padded to a billion cells.
func wideTable() []byte {
	var b strings.Builder
	b.WriteString("|" + strings.Repeat("h|", 10000) + "\n|---|\n")
	b.WriteString(strings.Repeat("|x|\n", 100000))
	return []byte(b.String())
}

func TestWideTableRejectedBeforeAllocation(t *testing.T) {
	src := wideTable()
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	_, err := Render(context.Background(), src, Options{})
	runtime.ReadMemStats(&m1)
	alloc := m1.TotalAlloc - m0.TotalAlloc
	t.Logf("input %d bytes, allocated %d bytes", len(src), alloc)
	var be *BudgetError
	if !errors.As(err, &be) || !errors.Is(err, ErrBudget) || be.Limit != "table columns" {
		t.Fatalf("err = %v, want a table columns budget error", err)
	}
	if alloc > 16<<20 {
		t.Fatalf("rejection allocated %d bytes, want under 16 MiB", alloc)
	}
}

func TestTableCellBudget(t *testing.T) {
	table := func(cols, rows int) []byte {
		var b strings.Builder
		b.WriteString("|" + strings.Repeat("h|", cols) + "\n|" + strings.Repeat("-|", cols) + "\n")
		// Body rows are padded to the header width, so a one-cell row costs
		// cols cells.
		b.WriteString(strings.Repeat("|x|\n", rows))
		return []byte(b.String())
	}
	lim := Limits{MaxTableCells: 1000}
	if _, err := Render(context.Background(), table(20, 50), Options{Limits: lim}); err != nil {
		t.Fatalf("1000 cells at the limit: %v", err)
	}
	_, err := Render(context.Background(), table(20, 51), Options{Limits: lim})
	var be *BudgetError
	if !errors.As(err, &be) || be.Limit != "table cells" {
		t.Fatalf("1020 cells: err = %v, want a table cells budget error", err)
	}
	// The budget is per document: two tables share it.
	two := append(table(20, 30), append([]byte("\n"), table(20, 30)...)...)
	if _, err := Render(context.Background(), two, Options{Limits: lim}); !errors.Is(err, ErrBudget) {
		t.Fatalf("two tables of 600 cells: err = %v, want a budget error", err)
	}
	// Columns at the limit pass, one more is rejected.
	if _, err := Render(context.Background(), table(128, 1), Options{}); err != nil {
		t.Fatalf("128 columns: %v", err)
	}
	if _, err := Render(context.Background(), table(129, 1), Options{}); !errors.Is(err, ErrBudget) {
		t.Fatalf("129 columns: err = %v, want a budget error", err)
	}
}

func TestExtraRowCellsAreDropped(t *testing.T) {
	src := "| a | b |\n|---|---|\n| 1 | 2 | 3 | 4 |\n"
	out := string(Markdown([]byte(src), Options{}))
	if !strings.Contains(out, "<tr><td>1</td><td>2</td></tr>") || strings.Contains(out, "<td>3</td>") {
		t.Fatalf("row not truncated to the header width:\n%s", out)
	}
}

func TestOutputCap(t *testing.T) {
	para := strings.Repeat("A plain sentence of prose for the cap. ", 20) + "\n\n"
	src := []byte(strings.Repeat(para, 250)) // about 200 KB
	lim := Limits{MaxOutputBytes: 64 << 10}
	_, err := Render(context.Background(), src, Options{Limits: lim})
	var be *BudgetError
	if !errors.As(err, &be) || be.Limit != "output bytes" {
		t.Fatalf("err = %v, want an output bytes budget error", err)
	}
	// The cap covers the finished page, chrome included.
	small := []byte(para)
	out, err := Render(context.Background(), small, Options{Header: true, Footer: true, Colophon: true})
	if err != nil {
		t.Fatalf("small render: %v", err)
	}
	if _, err := Render(context.Background(), small, Options{Header: true, Footer: true, Colophon: true,
		Limits: Limits{MaxOutputBytes: len(out) - 1}}); !errors.Is(err, ErrBudget) {
		t.Fatalf("one byte under the page size: err = %v, want a budget error", err)
	}
}

// fillTo repeats unit until the result reaches n bytes.
func fillTo(unit string, n int) []byte {
	return []byte(strings.Repeat(unit, n/len(unit)+1)[:n])
}

func TestDefaultOutputCapFitsMaxInput(t *testing.T) {
	if testing.Short() {
		t.Skip("renders two 8 MiB documents")
	}
	code, err := os.ReadFile("highlight.go")
	if err != nil {
		t.Fatal(err)
	}
	prose, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	const max = 8 << 20
	for _, c := range []struct {
		name string
		src  []byte
	}{
		{"highlighted go", fillTo("```go\n"+string(code)+"\n```\n\n", max)},
		{"readme prose", fillTo(string(prose)+"\n\n", max)},
	} {
		out, err := Render(context.Background(), c.src, Options{Header: true, Footer: true, Colophon: true})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		t.Logf("%s: %d input bytes render to %d bytes (%.2fx)", c.name, len(c.src), len(out),
			float64(len(out))/float64(len(c.src)))
	}
}

// countingCtx reports cancellation from its nth Err call on, so a test can
// stop a render at a known point without timing.
type countingCtx struct {
	context.Context
	calls, after int64
}

func (c *countingCtx) Err() error {
	if atomic.AddInt64(&c.calls, 1) > c.after {
		return context.Canceled
	}
	return nil
}

func TestRenderStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, []byte("# hi\n"), Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled before start: err = %v", err)
	}
	src := []byte(strings.Repeat("a line of text\n\n", 200000) + strings.Repeat("- item\n", 100000) +
		"|a|b|\n|-|-|\n" + strings.Repeat("|1|2|\n", 100000))
	for _, after := range []int64{2, 400, 1100} {
		ctx := &countingCtx{Context: context.Background(), after: after}
		_, err := Render(ctx, src, Options{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("after %d checks: err = %v, want context.Canceled", after, err)
		}
		if ctx.calls != after+1 {
			t.Fatalf("render kept going: %d context checks, want %d", ctx.calls, after+1)
		}
	}
}

func benchDoubling(b *testing.B, gen func(n int) string) {
	for _, n := range []int{10000, 20000, 40000, 80000} {
		src := []byte(gen(n))
		b.Run("n="+strconv.Itoa(n), func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				if _, err := Render(context.Background(), src, Options{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkHeadings(b *testing.B) {
	benchDoubling(b, func(n int) string { return strings.Repeat("## x\n", n) })
}

func BenchmarkCodeSpans(b *testing.B) {
	benchDoubling(b, func(n int) string { return strings.Repeat("`a` ", n) })
}

func BenchmarkTableRows(b *testing.B) {
	benchDoubling(b, func(n int) string { return "|a|b|c|\n|-|-|-|\n" + strings.Repeat("|1|2|3|\n", n) })
}

// callFence is a go fence of n bytes of "a(" call tokens on 2000-byte lines;
// each token highlights to a 26-byte span, a 13x expansion.
func callFence(n int) []byte {
	line := strings.Repeat("a(", 1000) + "\n"
	return []byte("```go\n" + strings.Repeat(line, n/len(line)+1) + "```\n")
}

func TestExpandingBlockAllocationBounded(t *testing.T) {
	quotes := func(n int) []byte { return []byte(strings.Repeat(`"`, n) + "\n") }
	for _, c := range []struct {
		name string
		src  []byte
		cap  int
	}{
		{"code block, small cap", callFence(2 << 20), 1 << 20},
		{"code block, default cap", callFence(8 << 20), 0},
		{"unhighlighted fence", []byte("```\n" + strings.Repeat(`"<`, 2<<20) + "\n```\n"), 2 << 20},
		{"paragraph", quotes(4 << 20), 2 << 20},
		{"list item", append([]byte("- "), quotes(4<<20)...), 2 << 20},
		{"blockquote", append([]byte("> "), quotes(4<<20)...), 2 << 20},
	} {
		t.Run(c.name, func(t *testing.T) {
			lim := Limits{MaxOutputBytes: c.cap}.withDefaults()
			var m0, m1 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m0)
			_, err := Render(context.Background(), c.src, Options{Limits: lim})
			runtime.ReadMemStats(&m1)
			alloc := m1.TotalAlloc - m0.TotalAlloc
			// The input's own copies (string, lines, join) plus at most three
			// times the output cap.
			bound := uint64(3*lim.MaxOutputBytes + 4*len(c.src))
			t.Logf("input %d, cap %d: allocated %d (bound %d)", len(c.src), lim.MaxOutputBytes, alloc, bound)
			if !errors.Is(err, ErrBudget) {
				t.Fatalf("err = %v, want a budget error", err)
			}
			if alloc > bound {
				t.Fatalf("allocated %d bytes, want at most %d", alloc, bound)
			}
		})
	}
}

func TestCancelInsideOneCodeBlock(t *testing.T) {
	// One fence of one long line: no line ticks, so only the highlighter can
	// notice the cancellation.
	src := []byte("```go\n" + strings.Repeat("a(", 1<<20) + "\n```\n")
	ctx := &countingCtx{Context: context.Background(), after: 1}
	_, err := Render(ctx, src, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if ctx.calls != 2 {
		t.Fatalf("highlighting kept going: %d context checks, want 2", ctx.calls)
	}
}

func TestHighlightCountMatchesOutput(t *testing.T) {
	code, err := os.ReadFile("highlight.go")
	if err != nil {
		t.Fatal(err)
	}
	st := newState(context.Background(), unlimited)
	for _, lang := range []string{"go", "bash", "json", "yaml", "python", "ruby", ""} {
		for _, src := range []string{string(code), `x := "<a href='&'>" // <b>`, "echo $HOME ${X} $? 'a\"b'", ""} {
			count := &emitter{}
			spec := langs[canonLang(lang)]
			if spec == nil {
				count.n = escapedLen(src)
			} else if err := scanHighlight(st, spec, src, count, unlimited.MaxOutputBytes); err != nil {
				t.Fatal(err)
			}
			out, err := highlightTo(st, count.n, lang, src)
			if err != nil || len(out) != count.n {
				t.Fatalf("%s: counted %d, wrote %d (err %v)", lang, count.n, len(out), err)
			}
			// One byte less is refused, so the limit is exact.
			if count.n > 0 {
				if _, err := highlightTo(st, count.n-1, lang, src); !errors.Is(err, ErrBudget) {
					t.Fatalf("%s: limit one under the output: err = %v", lang, err)
				}
			}
		}
	}
}

// TestInputCaps: every input-side cap refuses with ErrBudget naming its
// limit, before the render builds the elements it counts, and a document
// within every cap still renders.
func TestInputCaps(t *testing.T) {
	small := Limits{MaxLines: 50, MaxHeadings: 3, MaxListDepth: 2, MaxMetaFields: 2, MaxMetaBytes: 64}
	for _, c := range []struct {
		name, src, limit string
	}{
		{"lines", strings.Repeat("a\n", 60), "lines"},
		{"headings", "# a\n## b\n### c\n#### d\n##### e\n", "headings"},
		{"list depth", "- a\n  - b\n    - c\n", "list depth"},
		{"frontmatter fields", "---\na: 1\nb: 2\nc: 3\n---\nbody\n", "frontmatter fields"},
		{"frontmatter bytes", "---\na: " + strings.Repeat("v", 80) + "\n---\nbody\n", "frontmatter bytes"},
		{"frontmatter key", "---\n" + strings.Repeat("k", maxMetaKey+1) + ": v\n---\n", "frontmatter key bytes"},
		{"frontmatter value", "---\nk: " + strings.Repeat("v", maxMetaValue+1) + "\n---\n", "frontmatter value bytes"},
	} {
		lim := small
		if strings.HasPrefix(c.limit, "frontmatter k") || strings.HasPrefix(c.limit, "frontmatter v") {
			lim.MaxMetaBytes = 1 << 20
		}
		_, err := Render(context.Background(), []byte(c.src), Options{Header: true, MetaHeader: true, Limits: lim})
		var be *BudgetError
		if !errors.Is(err, ErrBudget) || !errors.As(err, &be) || be.Limit != c.limit {
			t.Errorf("%s: err %v, want ErrBudget for %q", c.name, err, c.limit)
		}
	}
	ok := "---\na: 1\n---\n# T\n## s\n- a\n  - b\ntext\n"
	if _, err := Render(context.Background(), []byte(ok), Options{Header: true, MetaHeader: true, Limits: small}); err != nil {
		t.Errorf("a document within every cap was refused: %v", err)
	}
	// With the meta-header off, frontmatter is skipped without being parsed,
	// so its caps do not apply.
	big := "---\na: 1\nb: 2\nc: 3\n---\nbody\n"
	if _, err := Render(context.Background(), []byte(big), Options{Limits: small}); err != nil {
		t.Errorf("frontmatter with the meta-header off was refused: %v", err)
	}
}

// TestSeriesNavigationCapped: a family within MaxSeriesMembers gets its tree
// and prev/next row; a larger one renders without either, and the tree is
// charged to the output budget.
func TestSeriesNavigationCapped(t *testing.T) {
	sib := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("s-%04d", i)
		}
		return out
	}
	opts := func(n int, lim Limits) Options {
		return Options{Header: true, Colophon: true, Slug: "s-5000", Siblings: sib(n),
			ContentBase: "https://c.example", Limits: lim}
	}
	lim := Limits{MaxSeriesMembers: 10}
	out, err := Render(context.Background(), []byte("# T\n\nbody\n"), opts(9, lim))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `class="ftree"`) || !strings.Contains(string(out), `class="nav"`) {
		t.Error("a family at the cap lost its navigation")
	}
	out, err = Render(context.Background(), []byte("# T\n\nbody\n"), opts(10, lim))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `class="ftree"`) || strings.Contains(string(out), `class="nav"`) {
		t.Error("a family past the cap still rendered navigation")
	}
	// The tree counts against the budget: a budget that holds the page but
	// not its tree refuses it.
	small := Limits{MaxOutputBytes: 40 << 10}
	if _, err := Render(context.Background(), []byte("# T\n\nbody\n"), opts(900, small)); !errors.Is(err, ErrBudget) {
		t.Errorf("a tree past the budget: err %v, want ErrBudget", err)
	}
}

// TestLongFenceInfoIsNotLowercased: a fence info string far longer than any
// language name selects no highlighter.
func TestLongFenceInfoIsNotLowercased(t *testing.T) {
	if got := canonLang(strings.Repeat("GO", 1000)); got != "" {
		t.Errorf("canonLang(long) = %q, want none", got)
	}
	if got := canonLang("Go title=x"); got != "go" {
		t.Errorf("canonLang(Go title=x) = %q", got)
	}
}

// TestSeriesNavigationAllocationBounded: a page in a very large series with a
// long content origin allocates a small multiple of its output budget, not the
// size of the whole navigation.
func TestSeriesNavigationAllocationBounded(t *testing.T) {
	sib := make([]string, 100_000)
	for i := range sib {
		sib[i] = fmt.Sprintf("series-member-%06d", i)
	}
	base := "https://" + strings.Repeat("h", 200) + ".example"
	opts := Options{Header: true, Colophon: true, Slug: "series-member-x", Siblings: sib,
		ContentBase: base, Limits: Limits{MaxOutputBytes: 1 << 20}}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, _ = Render(context.Background(), []byte("# T\n\nbody\n"), opts)
	runtime.ReadMemStats(&after)
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("allocated %d KiB rendering a page with %d siblings", alloc>>10, len(sib))
	if alloc > 16<<20 {
		t.Errorf("rendering allocated %d MiB, want at most 16 MiB (budget 1 MiB)", alloc>>20)
	}
}
