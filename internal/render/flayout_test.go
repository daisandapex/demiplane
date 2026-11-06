// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"strings"
	"testing"
	"time"
)

const fDoc = "# Plan\n\nLead text.\n\n## First part\n\nBody.\n\n### A detail\n\nMore.\n\n## Second part\n\nEnd.\n\n## Third part\n\nDone.\n"

// between returns the part of s from the first from to the next to after it.
func between(t *testing.T, s, from, to string) string {
	t.Helper()
	i := strings.Index(s, from)
	if i < 0 {
		t.Fatalf("no %q in:\n%s", from, s)
	}
	j := strings.Index(s[i:], to)
	if j < 0 {
		t.Fatalf("no %q after %q", to, from)
	}
	return s[i : i+j+len(to)]
}

// TestFOpensWithTheDocument: the first thing in the body after the skip link
// is the title, then the byline with the toggle at its end, then the text.
// There is no top bar.
func TestFOpensWithTheDocument(t *testing.T) {
	out := string(Markdown([]byte(fDoc), Options{Header: true}))
	if strings.Contains(out, `class="docbar"`) || strings.Contains(out, `class="treebtn"`) {
		t.Errorf("F page still carries a top bar:\n%s", out)
	}
	want := `<a class="skip" href="#dp-main">Skip to content</a>
<div class="frame">
<header class="dochead"><h1 class="doctitle">Plan</h1>
<div class="byline"><p class="facts"></p><button class="themetoggle"`
	if !strings.Contains(out, want) {
		t.Errorf("masthead is not title, byline, toggle:\n%s", out)
	}
	head := between(t, out, `<header class="dochead">`, "</header>")
	if strings.Index(out, "</header>") > strings.Index(out, `<main class="wrap doc" id="dp-main">`) {
		t.Errorf("text starts before the masthead closes:\n%s", head)
	}
}

// TestFBylineCarriesTheFacts: the frontmatter becomes one line under the
// title, in a fixed order, the title field is not repeated, any other field
// is labelled, and the date is the frontmatter's.
func TestFBylineCarriesTheFacts(t *testing.T) {
	src := "---\ntitle: Plan\ntags: seats, security\nauthor: Ben\ndate: 2026-11-05\nstatus: draft\naudience: ops\n---\n# Plan\n\nText.\n"
	out := string(Markdown([]byte(src), Options{Header: true, MetaHeader: true,
		Published: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)}))
	line := between(t, out, `<p class="facts">`, "</p>")
	sep := `<span class="sep" aria-hidden="true"> · </span>`
	want := `<p class="facts"><time class="tstamp" datetime="2026-11-05">2026-11-05</time>` + sep +
		`<span class="who">by Ben</span>` + sep + `<span class="status">draft</span>` + sep +
		`<span class="tags">seats, security</span>` + sep + `<span>Audience: ops</span></p>`
	if line != want {
		t.Errorf("byline\n got %s\nwant %s", line, want)
	}
	if strings.Contains(out, `class="metahead"`) || strings.Contains(out, "2026-11-06</time>") {
		t.Errorf("F page should carry the facts once, in the byline:\n%s", out)
	}
}

// TestFBylineFallsBackToPublishDate: with no frontmatter the byline still
// dates the page, from the publish date, and a long page shows its read time.
func TestFBylineFallsBackToPublishDate(t *testing.T) {
	long := "# Long\n\n" + strings.Repeat("word ", 700) + "\n"
	out := string(Markdown([]byte(long), Options{Header: true,
		Published: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)}))
	if !strings.Contains(out, `<p class="facts"><time class="tstamp" datetime="2026-11-06">2026-11-06</time>`+
		`<span class="sep" aria-hidden="true"> · </span><span class="rt">3 min read</span></p>`) {
		t.Errorf("byline should carry the publish date and the read time:\n%s", between(t, out, `<div class="byline">`, "</div>"))
	}
	short := string(Markdown([]byte("# Short\n\nThree words here.\n"), Options{Header: true}))
	if strings.Contains(short, "min read") {
		t.Error("a short note should not show a read time")
	}
}

// TestFFrontmatterTitleBeatsSlug: a page with no H1 takes its title from the
// frontmatter before the slug fallback.
func TestFFrontmatterTitleBeatsSlug(t *testing.T) {
	out := string(Markdown([]byte("---\ntitle: A <b> plan\n---\nText.\n"), Options{Header: true, MetaHeader: true, Title: "slug-1"}))
	if !strings.Contains(out, `<h1 class="doctitle">A &lt;b&gt; plan</h1>`) {
		t.Errorf("frontmatter title should head the page, escaped:\n%s", out)
	}
}

// TestFContentsFromThreeSections: the contents list appears from three h2
// sections, lists h2s and their h3s keyed to their section, and sits after the
// masthead, before the text.
func TestFContentsFromThreeSections(t *testing.T) {
	out := string(Markdown([]byte(fDoc), Options{Header: true}))
	if !strings.Contains(out, `<body class="f has-toc">`) {
		t.Errorf("body should carry has-toc:\n%s", out)
	}
	toc := between(t, out, `<details class="mnav toc">`, "</details>")
	for _, want := range []string{
		`<summary>Contents</summary><nav aria-label="Contents"><ol class="outline">`,
		`<li class="l2" data-sec="s1"><a href="#first-part">First part</a></li>`,
		`<li class="l3" data-sec="s1"><a href="#a-detail">A detail</a></li>`,
		`<li class="l2" data-sec="s3"><a href="#third-part">Third part</a></li>`,
	} {
		if !strings.Contains(toc, want) {
			t.Errorf("contents missing %q:\n%s", want, toc)
		}
	}
	if strings.Index(out, `class="mnav toc"`) < strings.Index(out, "</header>") ||
		strings.Index(out, `class="mnav toc"`) > strings.Index(out, `<main`) {
		t.Error("contents should sit between the masthead and the text")
	}
	two := string(Markdown([]byte("# T\n\n## One\n\na\n\n## Two\n\nb\n"), Options{Header: true}))
	if strings.Contains(two, `class="mnav toc"`) || !strings.Contains(two, `<body class="f">`) {
		t.Errorf("a two-section page needs no contents:\n%s", two)
	}
}

// TestFOutlineEscapes: heading text reaches the contents escaped, never as markup.
func TestFOutlineEscapes(t *testing.T) {
	out := string(Markdown([]byte("# T\n\n## <script>x</script> one\n\na\n\n## `code` two\n\nb\n\n## three\n\nc\n"), Options{Header: true}))
	toc := between(t, out, `<details class="mnav toc">`, "</details>")
	if strings.Contains(toc, "<script>") {
		t.Errorf("raw markup reached the contents:\n%s", toc)
	}
	if !strings.Contains(toc, `&lt;script&gt;x&lt;/script&gt; one</a>`) || !strings.Contains(toc, `>code two</a>`) {
		t.Errorf("contents text should be the escaped plain heading:\n%s", toc)
	}
}

// TestFSeriesList: a series page lists its family in order with the current
// page marked and unlinked, under a summary that says where the reader is; a
// page with no family has none.
func TestFSeriesList(t *testing.T) {
	for name, opts := range map[string]Options{
		"no slug":   {Header: true, Siblings: []string{"b"}},
		"no series": {Header: true, Slug: "a"},
		"singleton": {Header: true, Slug: "a", Siblings: []string{"a", ""}},
	} {
		if out := string(Markdown([]byte(fDoc), opts)); strings.Contains(out, "has-tree") || strings.Contains(out, "mnav seq") {
			t.Errorf("%s: no family, so no series list:\n%s", name, out)
		}
	}
	out := string(Markdown([]byte(fDoc), Options{Header: true, Slug: "lesson-02", ContentBase: "https://c.example",
		Siblings: []string{"lesson-03", "lesson-01"}}))
	if !strings.Contains(out, `<body class="f has-tree has-toc">`) {
		t.Errorf("series page should carry has-tree:\n%s", out)
	}
	want := `<details class="mnav seq" id="ftree"><summary>Series <span class="pos">2 of 3</span></summary>` +
		`<nav aria-label="Series"><ol><li><a href="https://c.example/lesson-01">Part 1</a></li>` +
		`<li><span aria-current="page">Plan</span></li><li><a href="https://c.example/lesson-03">Part 3</a></li></ol></nav></details>`
	if !strings.Contains(out, want) {
		t.Errorf("series list wrong:\n%s", out)
	}
	if strings.Contains(out, "localStorage.setItem('demiplane-nav'") || strings.Contains(out, "e.key!=='['") {
		t.Error("the series list has no stored state and no shortcut to persist")
	}
}

// TestFNavsFoldOnNarrowScreens: the margin navs open from 78.5rem, where the
// frame has gutters for them, and fold to one-line disclosures below it.
func TestFNavsFoldOnNarrowScreens(t *testing.T) {
	out := string(Markdown([]byte(fDoc), Options{Header: true}))
	for _, want := range []string{
		"wide=matchMedia('(min-width:78.5rem)')",
		"navs[i].open=wide.matches",
		"@media (min-width:78.5rem){\n.frame{grid-template-columns:minmax(0,1fr) minmax(0,var(--wide)) minmax(0,1fr)",
		`grid-template-areas:". head ." "seq body toc"`,
		".frame>.mnav{position:sticky",
		`<details class="mnav toc"><summary>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, `<details class="mnav toc" open`) {
		t.Error("contents should start folded so a phone opens on the text")
	}
}

// TestFOneAxis: the text column, the wide blocks and the title share one
// centre: text at the measure, code and tables to the frame, the title from
// the text's left edge to the frame's right edge.
func TestFOneAxis(t *testing.T) {
	out := string(Markdown([]byte(fDoc), Options{Header: true}))
	for _, want := range []string{
		":root{--measure:30rem;--wide:46rem;",
		".doc>*{max-width:var(--measure);margin-inline:auto}",
		".f main.wrap>pre,.f main.wrap>.table-wrap{max-width:100%}",
		".dochead>*,.doc>h1{margin-left:max(0px,calc((100% - var(--measure)) / 2));margin-right:0}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// TestFCreditJoinsTheColophon: with a colophon, the credit is the last item
// of its one line under its one rule; there is no second footer rule.
func TestFCreditJoinsTheColophon(t *testing.T) {
	opts := Options{Header: true, Footer: true, Colophon: true, Slug: "s",
		Published: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)}
	out := string(Markdown([]byte(fDoc), opts))
	if strings.Contains(out, `<footer class="docfoot"`) {
		t.Errorf("credit should live in the colophon, not a second footer:\n%s", out)
	}
	if !strings.Contains(out, `<span>&middot;</span><span class="credit">Generated by <span class="mark">demiplane</span></span></div></div>`) {
		t.Errorf("credit missing from the colophon line:\n%s", out)
	}
	opts.Footer = false
	if out := string(Markdown([]byte(fDoc), opts)); strings.Contains(out, "Generated by") {
		t.Error("--footer=false should drop the credit")
	}
}

// TestFHeaderlessKeepsColumn: without the masthead there is no F chrome and no
// script, so the JS-free reply flow is unchanged.
func TestFHeaderlessKeepsColumn(t *testing.T) {
	out := string(Markdown([]byte(fDoc), Options{Footer: true, ReplySlug: "q-1"}))
	for _, bad := range []string{`class="f`, `class="frame"`, `class="mnav`, `class="byline"`, "<script"} {
		if strings.Contains(out, bad) {
			t.Errorf("headerless page should carry no %q:\n%s", bad, out)
		}
	}
}

// TestFCodeFirstHangs: a paragraph opening with inline code is tagged so the
// chip hangs by its padding; one with code mid-line, or in a list, is not.
func TestFCodeFirstHangs(t *testing.T) {
	out := string(Markdown([]byte("# T\n\n`lead.py` opens.\n\n## S\n\n`check.py` runs.\n\nRun `x` now.\n\n- `item`\n"), Options{Header: true}))
	for _, want := range []string{
		`<p class="lead code-first"><code class="nw">lead.py</code>`,
		`<p class="code-first"><code class="nw">check.py</code>`,
		`<p>Run <code class="nw">x</code> now.</p>`,
		`<li><code class="nw">item</code>`,
		".f main.wrap>p.code-first>code:first-child{margin-left:-.3em}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// TestFSkippedUnderCustomCSS: an instance's --css sheet owns the look, so a
// masthead page there keeps the plain masthead and single column: no F markup,
// no F styles, no F scripts, the facts stay in the column. Frontmatter, a series
// family and an outline are all present, so every F part would otherwise show.
func TestFSkippedUnderCustomCSS(t *testing.T) {
	src := "---\nauthor: Ben\n---\n# T\n\nLead.\n\n## One\n\n`x` first.\n\n## Two\n\n### Deep\n\ntext\n"
	out := string(Markdown([]byte(src), Options{Header: true, Footer: true, MetaHeader: true,
		CSS: "body{color:red}", Slug: "lesson-02", Siblings: []string{"lesson-01", "lesson-03"}}))
	for _, bad := range []string{
		`<body class="f`, `class="frame"`, `class="mnav`, `class="byline"`, `class="themetoggle"`,
		`class="dochead"`, "code-first", "classList.add('fjs')", ".f main.wrap", ".mnav{",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("--css page must not carry F part %q", bad)
		}
	}
	for _, want := range []string{
		"<style>body{color:red}",
		`<a class="skip" href="#dp-main">Skip to content</a>
<header class="docbar"><div class="wrap"><div class="masthead"><h1 class="doctitle">T</h1></div></div></header>`,
		".docbar .masthead{display:flex",
		"<body>\n",
		"<main class=\"wrap\" id=\"dp-main\">\n<div class=\"metahead\">",
		`<footer class="docfoot"><div class="wrap">Generated by`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("--css page missing %q:\n%s", want, out)
		}
	}
}

// TestFSeriesLabels: series members are named by what sets them apart, never
// by the raw slug, and the shared prefix is cut at a hyphen.
func TestFSeriesLabels(t *testing.T) {
	for _, c := range []struct {
		fam  []string
		want string
	}{
		{[]string{"audit-series-01", "audit-series-02"}, "audit-series-"},
		{[]string{"lesson-10", "lesson-11"}, "lesson-"},
		{[]string{"alpha", "beta"}, ""},
		{[]string{"notes-setup", "notes-teardown"}, "notes-"},
	} {
		if got := seriesPrefix(c.fam); got != c.want {
			t.Errorf("seriesPrefix(%v) = %q, want %q", c.fam, got, c.want)
		}
	}
	for _, c := range []struct{ slug, prefix, want string }{
		{"audit-series-02", "audit-series-", "Part 2"},
		{"lesson-10", "lesson-", "Part 10"},
		{"notes-setup-guide", "notes-", "Setup guide"},
		{"alpha", "", "Alpha"},
		{"notes-", "notes-", "Notes "},
		{"2fa-intro", "", "2fa intro"},
	} {
		if got := seriesLabel(c.slug, c.prefix); got != c.want {
			t.Errorf("seriesLabel(%q, %q) = %q, want %q", c.slug, c.prefix, got, c.want)
		}
	}
	out := string(Markdown([]byte("# A *&* title\n\nText.\n"), Options{Header: true, Slug: "x-1", Siblings: []string{"x-2"}}))
	if !strings.Contains(out, `<span aria-current="page">A &amp; title</span>`) {
		t.Errorf("current member should be named by its escaped plain title:\n%s", between(t, out, `<details class="mnav seq"`, `</details>`))
	}
}

// TestFHeadHoldsToTheMeasure: the title and the byline (its rule and toggle)
// end at the text's right edge, so a page has one set of edges.
func TestFHeadHoldsToTheMeasure(t *testing.T) {
	out := string(Markdown([]byte(fDoc), Options{Header: true}))
	if !strings.Contains(out, ".dochead>.doctitle,.dochead>.byline{max-width:var(--measure)}") {
		t.Error("title and byline should hold to the measure")
	}
}

// TestFToggleAnchorsToFirstLine: a byline that wraps keeps the toggle level
// with its first line.
func TestFToggleAnchorsToFirstLine(t *testing.T) {
	out := string(Markdown([]byte(fDoc), Options{Header: true}))
	if !strings.Contains(out, ".byline{display:flex;align-items:flex-start;") {
		t.Error("byline should align the toggle to its first line")
	}
}
