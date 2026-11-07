// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests here render hostile documents in a child process (the test binary
// re-run with memChildEnv set) so each render's memory can be read in
// isolation. The bound is on the render's peak live heap: the most heap any
// garbage collection in the child marked reachable, read from the child's
// GODEBUG=gctrace=1 lines. That is what the output budget and input caps
// bound (the memory a render holds), and every cycle reports it, so a render
// that churns gigabytes is still measured at each collection.
//
// The child collects stop-the-world (gcstoptheworld=1), so a cycle marks only
// what is reachable at that instant. A concurrent cycle also counts as live
// everything allocated while it marks, and on a busy runner a slow mark lets
// that reach the heap goal, inflating the reading towards twice the live heap
// (as MemStats.Sys, which adds idle spans on top, did in CI). Stop-the-world,
// a reading can only fall short of the render's true peak, never exceed it,
// so a slow or busy runner cannot fail a render that fits. It falls short by
// at most what the render allocated since the last cycle: GOGC=25 starts a
// cycle before the heap grows by a quarter of the last live heap, so memory
// held past that is seen (the bound enforced is memBound plus that quarter).
// Sys is still logged, not asserted; it adds the collector's headroom, which
// the output budget does not bound.

const (
	memChildEnv   = "DEMIPLANE_RENDER_MEM_CHILD"  // payload name
	memBudgetEnv  = "DEMIPLANE_RENDER_MEM_BUDGET" // output budget, bytes
	maxRenderSize = 8 << 20                       // the server's input cap
	memConstant   = 40 << 20                      // the input and what the input caps bound
	memPerBudget  = 4                             // k: see TestRenderMemoryProperty
)

// memBound is the most live heap a render under budget may reach: k times
// the output budget plus a constant.
func memBound(budget int) uint64 { return uint64(memConstant + memPerBudget*budget) }

// memPayloads are the documents a child can render, by name. Each builds its
// input in one allocation where it can, so generating it adds little to the
// child's memory before the render starts.
var memPayloads = map[string]func() []byte{
	// One very long h2 and many h3s: every outline entry once repeated the
	// h2's id.
	"outline": func() []byte {
		return []byte("## " + strings.Repeat("a", 65536) + "\n" + strings.Repeat("### b\n", 20000))
	},
	// Exactly the input cap of empty frontmatter fields.
	"meta-fields": func() []byte {
		return fill("---\n", "a:\n", "---\n", maxRenderSize)
	},
	"meta-long-values": func() []byte {
		return fill("---\n", "key: "+strings.Repeat("v", 60<<10)+"\n", "---\n", maxRenderSize)
	},
	"headings":         func() []byte { return fill("", "# a\n", "", maxRenderSize) },
	"outline-headings": func() []byte { return fill("", "## x\n### y\n", "", maxRenderSize) },
	"long-heading":     func() []byte { return fill("## ", "a", "\n", maxRenderSize) },
	"long-title":       func() []byte { return fill("# ", "`a` ", "\n", maxRenderSize) },
	"deep-list": func() []byte {
		var b bytes.Buffer
		b.Grow(maxRenderSize)
		for d := 0; ; d = (d + 1) % 512 {
			line := strings.Repeat(" ", d) + "- x\n"
			if b.Len()+len(line) > maxRenderSize {
				return b.Bytes()
			}
			b.WriteString(line)
		}
	},
	"list-items":   func() []byte { return fill("", "- a\n", "", maxRenderSize) },
	"loose-list":   func() []byte { return fill("- a\n", "\n  b\n", "", maxRenderSize) },
	"wide-table":   func() []byte { return fill(tableHead(128), strings.Repeat("|", 129)+"\n", "", maxRenderSize) },
	"table-pad":    func() []byte { return fill(tableHead(128), "|a\n", "", maxRenderSize) },
	"table-cells":  func() []byte { return fill(tableHead(2), "|`a` `a` `a` `a`|[a](b)|\n", "", maxRenderSize) },
	"links":        func() []byte { return fill("", "[a](b) ", "\n", maxRenderSize) },
	"code-spans":   func() []byte { return fill("", "`a` ", "\n", maxRenderSize) },
	"emphasis":     func() []byte { return fill("", "**a** *b* _c_ ", "\n", maxRenderSize) },
	"escapes":      func() []byte { return fill("", `"<&>'`, "\n", maxRenderSize) },
	"one-line":     func() []byte { return fill("", "x", "", maxRenderSize) },
	"blank-lines":  func() []byte { return fill("", "\n", "", maxRenderSize) },
	"paragraphs":   func() []byte { return fill("", "a\n\n", "", maxRenderSize) },
	"wrapped-para": func() []byte { return fill("", "`a`\n", "", maxRenderSize) },
	"quote":        func() []byte { return fill("> ", "`a` ", "\n", maxRenderSize) },
	"quote-lines":  func() []byte { return fill("", "> `a`\n", "", maxRenderSize) },
	"code-go":      func() []byte { return fill("```go\n", "f(x) // <&>\n", "```\n", maxRenderSize) },
	"code-plain":   func() []byte { return fill("```\n", "\"<&>'\n", "```\n", maxRenderSize) },
	"code-lines":   func() []byte { return fill("```go\n", "\n", "```\n", maxRenderSize) },
	"code-fences":  func() []byte { return fill("", "```go\nf()\n```\n", "", maxRenderSize) },
	"hrules":       func() []byte { return fill("", "---\n", "", maxRenderSize) },
	"crlf":         func() []byte { return fill("", "## a\r\n`a`\r\n\r\n", "", maxRenderSize) },
	// The same shapes with lines long enough to stay under the line cap, so
	// each reaches the stage it targets instead of being refused by line count.
	"meta-fields-9": func() []byte { return fill("---\n", "abcdefg:\n", "---\n", maxRenderSize) },
	"meta-fields-small": func() []byte {
		return fill("---\n", "abcdefg: v\n", "---\n", 60<<10)
	},
	"headings-9":    func() []byte { return fill("", "## abcde\n", "", maxRenderSize) },
	"list-items-9":  func() []byte { return fill("", "- abcdef\n", "", maxRenderSize) },
	"loose-list-9":  func() []byte { return fill("- a\n", "\n  bbbbbbb\n", "", maxRenderSize) },
	"paragraphs-9":  func() []byte { return fill("", "abcdefgh\n", "", maxRenderSize) },
	"quote-lines-9": func() []byte { return fill("", "> abcdef\n", "", maxRenderSize) },
	"table-pad-9":   func() []byte { return fill(tableHead(128), "|abcdefg\n", "", maxRenderSize) },
	"code-lines-9":  func() []byte { return fill("```go\n", "f(x)//<\n", "```\n", maxRenderSize) },
	"hrules-9":      func() []byte { return fill("", "--------\n", "", maxRenderSize) },
	// Documents sized to fit a 4 MiB budget, so inline formatting runs on as
	// much text as the budget admits: the worst case for its per-match work.
	"code-spans-fit":   func() []byte { return fill("", "`a` ", "\n", 600<<10) },
	"links-fit":        func() []byte { return fill("", "[a](b) ", "\n", 560<<10) },
	"emphasis-fit":     func() []byte { return fill("", "**a** *b* _c_ ", "\n", 400<<10) },
	"code-spans-lines": func() []byte { return fill("", "`a` `b` `c`\n", "", 600<<10) },
	"escapes-fit":      func() []byte { return fill("", "&", "\n", 780<<10) },
	"table-cells-fit":  func() []byte { return fill(tableHead(2), "|`a` `a` `a` `a`|[a](b)|\n", "", 600<<10) },
	// The same, sized to fit the 32 MiB default budget (TestRenderMemoryAtDefaultBudget).
	"code-spans-fit32":   func() []byte { return fill("", "`a` ", "\n", 4900<<10) },
	"links-fit32":        func() []byte { return fill("", "[a](b) ", "\n", 4400<<10) },
	"emphasis-fit32":     func() []byte { return fill("", "**a** *b* _c_ ", "\n", 3200<<10) },
	"code-spans-lines32": func() []byte { return fill("", "`a` `b` `c`\n", "", 4900<<10) },
	"escapes-fit32":      func() []byte { return fill("", "&", "\n", 6400<<10) },
	"table-cells-fit32":  func() []byte { return fill(tableHead(2), "|`a` `a` `a` `a`|[a](b)|\n", "", 4800<<10) },
	"deep-list-under":    func() []byte { return fill("", "- a\n  - b\n    - c\n      - d\n", "", maxRenderSize) },
}

// mixtures is how many seeded mixed documents the property test renders.
const mixtures = 8

func init() {
	for i := 0; i < mixtures; i++ {
		seed := int64(i + 1)
		memPayloads["mix-"+strconv.Itoa(i)] = func() []byte { return mixture(seed) }
	}
}

// mixture assembles a document of up to the input cap from runs of the
// hostile shapes above, chosen and sized from seed.
func mixture(seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	pieces := []string{
		"## x\n", "### y\n", "# t\n", "- a\n", "  - b\n", "1. c\n", "> `q`\n", "`a` ", "[a](b) ",
		"**a** ", "_b_ ", `"<&>'`, "\n", "a\n", "---\n", "|a|b|\n", "x",
		"```go\nf(x)\n```\n", "\n  cont\n", strings.Repeat(" ", 40) + "- deep\n",
	}
	var b bytes.Buffer
	b.Grow(maxRenderSize)
	if r.Intn(2) == 0 {
		b.WriteString("---\ntitle: t\ndate: 2026-01-02\n")
		for n := r.Intn(64); n > 0; n-- {
			fmt.Fprintf(&b, "k%d: %s\n", n, strings.Repeat("v", r.Intn(512)))
		}
		b.WriteString("---\n")
	}
	if r.Intn(2) == 0 {
		b.WriteString(tableHead(1 + r.Intn(128)))
	}
	for {
		p := pieces[r.Intn(len(pieces))]
		n := 1 + r.Intn(1<<r.Intn(17))
		if b.Len()+n*len(p) > maxRenderSize {
			return b.Bytes()
		}
		for ; n > 0; n-- {
			b.WriteString(p)
		}
	}
}

// fill returns head, then unit repeated, then tail, at most max bytes long.
func fill(head, unit, tail string, max int) []byte {
	n := (max - len(head) - len(tail)) / len(unit)
	b := make([]byte, 0, len(head)+n*len(unit)+len(tail))
	b = append(b, head...)
	for i := 0; i < n; i++ {
		b = append(b, unit...)
	}
	return append(b, tail...)
}

// tableHead is a header and separator row of cols columns.
func tableHead(cols int) string {
	return "|" + strings.Repeat("h|", cols) + "\n|" + strings.Repeat("-|", cols) + "\n"
}

// allOptions turns on every part of the page a render can produce.
func allOptions(budget int) Options {
	return Options{
		Title: "t", Header: true, Footer: true, FooterLink: "https://example.com/",
		MetaHeader: true, ReplySlug: "s-1", ReplyToken: "tok", ReplyNext: "s-2",
		Colophon: true, Slug: "s-1", Siblings: []string{"s-0", "s-2"},
		ContentBase: "https://c.example", IndexURL: "https://example.com/",
		Published: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Limits:    Limits{MaxOutputBytes: budget},
	}
}

type memResult struct {
	Name     string `json:"name"`
	InputLen int    `json:"input_len"`
	Budget   bool   `json:"budget"`
	Err      string `json:"err"`
	OutLen   int    `json:"out_len"`
	Sys      uint64 `json:"sys"`
	PeakLive uint64 `json:"-"` // MiB, the parent's reading of the child's gctrace
	GCs      int    `json:"-"`
	Elapsed  string `json:"elapsed"`
}

// TestRenderMemChild is the child half of the tests below; it does nothing
// unless a parent started it.
func TestRenderMemChild(t *testing.T) {
	name := os.Getenv(memChildEnv)
	if name == "" {
		t.Skip("child process only")
	}
	gen := memPayloads[name]
	if gen == nil {
		t.Fatalf("no payload %q", name)
	}
	budget, _ := strconv.Atoi(os.Getenv(memBudgetEnv))
	src := gen()
	start := time.Now()
	out, err := Render(context.Background(), src, allOptions(budget))
	// One collection with the page and its input still held, so even a render
	// that allocated too little to start a cycle reports a live heap.
	runtime.GC()
	runtime.KeepAlive(src)
	runtime.KeepAlive(out)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	res := memResult{
		Name: name, InputLen: len(src), Budget: errors.Is(err, ErrBudget),
		OutLen: len(out), Sys: ms.Sys, Elapsed: time.Since(start).Round(time.Millisecond).String(),
	}
	if err != nil {
		res.Err = err.Error()
	}
	_ = json.NewEncoder(os.Stdout).Encode(res)
}

// renderInChild renders payload name under budget in a child process.
func renderInChild(t *testing.T, name string, budget int) memResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRenderMemChild$", "-test.count=1")
	// Render is synchronous; one P keeps the host's CPU count or cgroup quota
	// out of the allocator. gctrace=1 prints one line per collection to
	// stderr; gcstoptheworld=1 and GOGC=25 are explained at the top of the
	// file. GOMEMLIMIT is only a soft backstop, well above every asserted
	// bound; it must not be lowered to make an unbounded render pass.
	godebug := "gctrace=1,gcstoptheworld=1"
	if v := os.Getenv("GODEBUG"); v != "" {
		godebug = v + "," + godebug
	}
	cmd.Env = append(os.Environ(), memChildEnv+"="+name, memBudgetEnv+"="+strconv.Itoa(budget),
		"GOMEMLIMIT=1GiB", "GOGC=25", "GOMAXPROCS=1", "GODEBUG="+godebug)
	var trace bytes.Buffer
	cmd.Stderr = &trace
	out, err := cmd.Output()
	if ctx.Err() != nil {
		t.Fatalf("%s: child render did not finish in 3m", name)
	}
	peak, gcs := peakLive(trace.String())
	tail := trace.String()
	if len(tail) > 4096 {
		tail = tail[len(tail)-4096:]
	}
	// A child that failed (a data race, a panic after its result) fails the
	// render, even if it printed a result first.
	if err != nil {
		t.Fatalf("%s: child failed: %v: %s\n%s", name, err, out, tail)
	}
	line := ""
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "{") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("%s: child gave no result: %s\n%s", name, out, tail)
	}
	var res memResult
	if jerr := json.Unmarshal([]byte(line), &res); jerr != nil {
		t.Fatal(jerr)
	}
	if gcs == 0 {
		t.Fatalf("%s: child printed no gctrace lines: %s", name, tail)
	}
	res.PeakLive, res.GCs = peak, gcs
	t.Logf("%-17s in=%7d budget=%-5v out=%8d live=%3d MiB bound=%d MiB gcs=%-5d sys=%4d MiB %s %s",
		name, res.InputLen, res.Budget, res.OutLen, res.PeakLive, memBound(budget)>>20, res.GCs,
		res.Sys>>20, res.Elapsed, res.Err)
	return res
}

// gcLine matches the heap sizes of a gctrace line (format in the runtime
// package documentation): at the start of the cycle, at its end, and marked
// live, in MiB. If the format changes, no line matches and renderInChild
// fails rather than passing unmeasured.
var gcLine = regexp.MustCompile(`^gc \d+ @.* (\d+)->(\d+)->(\d+) MB, `)

// peakLive is the largest heap any collection in trace marked live, in MiB,
// and the number of collections.
func peakLive(trace string) (peak uint64, gcs int) {
	for _, l := range strings.Split(trace, "\n") {
		m := gcLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		gcs++
		if v, _ := strconv.ParseUint(m[3], 10, 64); v > peak {
			peak = v
		}
	}
	return peak, gcs
}

// checkMem fails unless the render fit its budget or was refused for it, and
// stayed within memBound.
func checkMem(t *testing.T, res memResult, budget int) {
	t.Helper()
	// gctrace truncates to whole MiB; count the reading as the next MiB up.
	if (res.PeakLive+1)<<20 > memBound(budget) {
		t.Errorf("%s: render kept up to %d MiB live, want under %d MiB (budget %d MiB)",
			res.Name, res.PeakLive, memBound(budget)>>20, budget>>20)
	}
	if !res.Budget && (res.Err != "" || res.OutLen > budget) {
		t.Errorf("%s: render neither fit the budget nor was refused for it: out %d, err %q", res.Name, res.OutLen, res.Err)
	}
}

// TestOutlineHonoursRenderBudget: the outline payload either fits its budget
// or is refused for it, within the memory bound. At 1 MiB the page does not
// fit, so it must be refused.
func TestOutlineHonoursRenderBudget(t *testing.T) {
	if os.Getenv(memChildEnv) != "" {
		t.Skip("parent only")
	}
	for _, budget := range []int{4 << 20, 1 << 20} {
		res := renderInChild(t, "outline", budget)
		checkMem(t, res, budget)
		if budget == 1<<20 && !res.Budget {
			t.Errorf("a page over the budget was not refused (out %d, err %q)", res.OutLen, res.Err)
		}
	}
}

// TestMetaHeaderHonoursRenderBudget: a document of nothing but frontmatter
// fields, at the input cap, is refused before its meta-header is built.
func TestMetaHeaderHonoursRenderBudget(t *testing.T) {
	if os.Getenv(memChildEnv) != "" {
		t.Skip("parent only")
	}
	for _, budget := range []int{4 << 20, DefaultLimits.MaxOutputBytes} {
		res := renderInChild(t, "meta-fields", budget)
		checkMem(t, res, budget)
		if !res.Budget {
			t.Errorf("meta-fields at budget %d MiB: want ErrBudget, got out %d err %q", budget>>20, res.OutLen, res.Err)
		}
	}
}

// TestRenderMemoryAtDefaultBudget renders the documents sized to fill the
// 32 MiB default budget with inline formatting, one at a time, and holds
// each to the same bound at that budget.
func TestRenderMemoryAtDefaultBudget(t *testing.T) {
	if os.Getenv(memChildEnv) != "" {
		t.Skip("parent only")
	}
	if testing.Short() {
		t.Skip("renders documents of up to 8 MiB at the default budget")
	}
	budget := DefaultLimits.MaxOutputBytes
	for _, name := range []string{"code-spans-fit32", "links-fit32", "emphasis-fit32", "code-spans-lines32", "escapes-fit32", "table-cells-fit32"} {
		checkMem(t, renderInChild(t, name, budget), budget)
	}
}

// TestRenderMemoryProperty renders every hostile shape and seeded mixture,
// each at the input cap with every page option on, and holds each render to
// memBound: k times the output budget plus a constant, on the peak live heap.
//
// k = 4 is the render's own accounting with some room. Every byte of the
// page is charged to the budget before it is held. The body is built in one
// budgeted buffer (growth adds up to a quarter), the page keeps that string
// without copying it, and the finished document is one more copy: 2.25
// copies at the end. The peak is earlier, while a block is formatted: inline
// holds the escaped text, each pass's result and the regexp's buffer at once,
// each held to the remaining budget, so a single block near the budget
// (escapes-fit32, code-spans-fit32) reaches about three copies. The constant,
// five times the 8 MiB input cap, holds what the output budget does not
// cover: the generated input, the render's string copy of it (and a third
// while CRLF is folded), its line index (4 bytes a line, at most 8 MiB), the
// per-element state the input caps bound (heading ids, outline entries, list
// frames), and runtime overhead. Measured worst under -race: 33 MiB of the
// 56 MiB bound at 4 MiB (outline-headings, over 40 runs, 10 of them on a busy
// host), 106 MiB of 168 MiB at the 32 MiB default (escapes-fit32). Letting
// the render hold four times its output budget before refusing a page fails
// the bound.
func TestRenderMemoryProperty(t *testing.T) {
	if os.Getenv(memChildEnv) != "" {
		t.Skip("parent only")
	}
	const budget = 4 << 20
	names := make([]string, 0, len(memPayloads))
	for name := range memPayloads {
		if !strings.HasSuffix(name, "32") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			checkMem(t, renderInChild(t, name, budget), budget)
		})
	}
}
