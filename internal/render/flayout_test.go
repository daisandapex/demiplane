// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"strings"
	"testing"
)

const fDoc = "# Plan\n\nLead text.\n\n## First part\n\nBody.\n\n### A detail\n\nMore.\n\n## Second part\n\nEnd.\n"

// TestFRailCarriesOutline: a masthead page with sections gets the right rail,
// after the main column, listing h2s and their h3s with the h3s keyed to their
// section.
func TestFRailCarriesOutline(t *testing.T) {
	out := string(Markdown([]byte(fDoc), Options{Header: true}))
	if !strings.Contains(out, `<body class="f has-rail">`) {
		t.Errorf("body should carry the F and rail classes:\n%s", out)
	}
	rail := strings.Index(out, `<aside class="frail" aria-label="About this page">`)
	main := strings.Index(out, "</main>")
	if rail < 0 || main < 0 || rail < main {
		t.Fatalf("rail missing or not after the main column (rail %d, /main %d):\n%s", rail, main, out)
	}
	for _, want := range []string{
		`<li class="l2" data-sec="first-part"><a href="#first-part">First part</a></li>`,
		`<li class="l3" data-sec="first-part"><a href="#a-detail">A detail</a></li>`,
		`<li class="l2" data-sec="second-part"><a href="#second-part">Second part</a></li>`,
	} {
		if !strings.Contains(out[rail:], want) {
			t.Errorf("outline missing %q:\n%s", want, out[rail:])
		}
	}
	if strings.Contains(out, `<section class="rail-facts"`) {
		t.Errorf("no frontmatter, so no facts block:\n%s", out)
	}
}

// TestFNoRailWithoutContent: a page with fewer than two headings and no facts
// has nothing for a rail to carry, so it renders none.
func TestFNoRailWithoutContent(t *testing.T) {
	out := string(Markdown([]byte("# Note\n\nJust prose.\n\n## Only one\n\nMore.\n"), Options{Header: true}))
	if strings.Contains(out, `<aside`) || !strings.Contains(out, `<body class="f">`) {
		t.Errorf("a page with one heading should render no rail:\n%s", out)
	}
	if !strings.Contains(out, `<body class="f">`) {
		t.Errorf("body should still carry the F class:\n%s", out)
	}
}

// TestFRailCarriesFacts: the frontmatter meta-header moves out of the column
// into the rail.
func TestFRailCarriesFacts(t *testing.T) {
	src := "---\ndate: 2026-06-20\nowner: ops\n---\n# Report\n\nBody.\n"
	out := string(Markdown([]byte(src), Options{Header: true, MetaHeader: true}))
	rail := strings.Index(out, `<aside class="frail"`)
	if rail < 0 {
		t.Fatalf("facts alone should still make a rail:\n%s", out)
	}
	mainHTML := out[strings.Index(out, `<main class="wrap">`):strings.Index(out, "</main>")]
	if strings.Contains(mainHTML, `class="metahead"`) {
		t.Errorf("meta-header should leave the column:\n%s", mainHTML)
	}
	if !strings.Contains(out[rail:], `<section class="rail-facts"><p class="rail-h">Details</p><div class="metahead">`) ||
		!strings.Contains(out[rail:], "<dd>ops</dd>") {
		t.Errorf("rail should carry the facts:\n%s", out[rail:])
	}
	if !strings.Contains(out, ".frail .metahead .metarow{") {
		t.Errorf("rail facts styling missing:\n%s", out)
	}
}

// TestFTreeOnlyWithSeries: the tree, its button and the has-tree class appear
// only for a series family of two or more; the current page is marked and
// unlinked, the others link through the content base.
func TestFTreeOnlyWithSeries(t *testing.T) {
	for name, opts := range map[string]Options{
		"no slug":   {Header: true},
		"no series": {Header: true, Slug: "lesson-02"},
		"singleton": {Header: true, Slug: "lesson-02", Siblings: []string{"lesson-02", ""}},
	} {
		out := string(Markdown([]byte(fDoc), opts))
		if !strings.Contains(out, `<body class="f has-rail">`) {
			t.Errorf("%s: body should not carry has-tree", name)
		}
		for _, bad := range []string{`<nav class="ftree"`, `<button class="treebtn"`} {
			if strings.Contains(out, bad) {
				t.Errorf("%s: %q present without a series:\n%s", name, bad, out)
			}
		}
	}
	out := string(Markdown([]byte(fDoc), Options{Header: true, Slug: "lesson-02",
		Siblings: []string{"lesson-03", "lesson-01"}, ContentBase: "https://c.example"}))
	if !strings.Contains(out, `<body class="f has-tree has-rail">`) {
		t.Errorf("series page should carry has-tree:\n%s", out)
	}
	want := `<nav class="ftree" id="ftree" aria-label="Series"><p class="rail-h">Series</p><ol>` +
		`<li><a href="https://c.example/lesson-01">lesson-01</a></li>` +
		`<li><span aria-current="page">lesson-02</span></li>` +
		`<li><a href="https://c.example/lesson-03">lesson-03</a></li></ol></nav>`
	if !strings.Contains(out, want) {
		t.Errorf("tree should list the family in order:\n%s", out)
	}
	// the button is hidden until the script that makes it work runs
	if !strings.Contains(out, `aria-controls="ftree" aria-expanded="false" hidden `) || !strings.Contains(out, "btn.hidden=false") {
		t.Errorf("tree button should start hidden and be revealed by script:\n%s", out)
	}
	if !strings.Contains(out, `<button class="treebtn" type="button" aria-controls="ftree"`) {
		t.Errorf("series page should carry the tree button:\n%s", out)
	}
}

// TestFTreeClosedBelow1410Remembered pins the owner's rule: the tree opens by
// default only at 1410 px and wider, a stored choice wins, `[` toggles and
// stores it, and the stored choice is applied in <head> before paint.
func TestFTreeClosedBelow1410Remembered(t *testing.T) {
	out := string(Markdown([]byte(fDoc), Options{Header: true, Slug: "a", Siblings: []string{"b"}}))
	head := out[:strings.Index(out, "</head>")]
	for _, want := range []string{
		"localStorage.getItem('demiplane-nav')",
		"matchMedia('(min-width:1410px)')",
		"if(p==='open'||(p!=='closed'&&w))r.classList.add('nav-on')",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("head init script missing %q:\n%s", want, head)
		}
	}
	for _, want := range []string{
		"if(e.key!=='['",
		"localStorage.setItem(K,o?'open':'closed')",
		"else m.addListener(f)", // MediaQueryList without addEventListener
		"if(d)d.open=!narrow.matches",
		// without script every h3 stays listed in the narrow flow
		".fjs .frail .outline .l3,.fjs .frail .outline .l3.open{display:none}",
		"html.nav-on .f.has-tree .ftree{display:block}",
		".ftree{grid-area:tree;display:none}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tree toggle missing %q", want)
		}
	}
}

// TestFHeaderlessKeepsColumn: without the masthead there is no F chrome and no
// script, so the JS-free reply flow is unchanged.
func TestFHeaderlessKeepsColumn(t *testing.T) {
	out := string(Markdown([]byte(fDoc), Options{Footer: true, ReplySlug: "q-1"}))
	for _, bad := range []string{`class="f`, "fgrid", "<aside", "<script"} {
		if strings.Contains(out, bad) {
			t.Errorf("headerless page should carry no %q:\n%s", bad, out)
		}
	}
}

// TestFOutlineEscapes: heading text reaches the outline escaped, never as markup.
func TestFOutlineEscapes(t *testing.T) {
	out := string(Markdown([]byte("# T\n\n## <script>x</script> one\n\na\n\n## `code` two\n\nb\n"), Options{Header: true}))
	rail := out[strings.Index(out, `<aside`):strings.Index(out, `</aside>`)]
	if strings.Contains(rail, "<script>") {
		t.Errorf("raw markup reached the outline:\n%s", rail)
	}
	if !strings.Contains(rail, `&lt;script&gt;x&lt;/script&gt; one</a>`) || !strings.Contains(rail, `>code two</a>`) {
		t.Errorf("outline text should be the escaped plain heading:\n%s", rail)
	}
}

// TestFColumnSharesOneEdge: in F every child of the column starts at the
// column edge (no centring) and stops at the measure; the footer sits in the
// same grid cell as the title.
func TestFColumnSharesOneEdge(t *testing.T) {
	out := string(Markdown([]byte(fDoc), Options{Header: true, Footer: true}))
	for _, want := range []string{
		".f main.wrap>*{max-width:var(--measure);margin-inline:0}",
		".dochead .doctitle{max-width:var(--measure)",
		`<footer class="docfoot"><div class="wrap"><div class="fcell">Generated by`,
		".f .docfoot .fcell{grid-area:head}",
		// one column below 51.25rem: the bar's tools rejoin the kicker's row
		"body.f.has-rail .docbar .tools{grid-area:head}\nhtml.nav-drawer",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// TestFCodeFirstHangs: a paragraph opening with inline code is tagged so the
// chip hangs by its padding; one with code mid-line, or in a list, is not.
func TestFCodeFirstHangs(t *testing.T) {
	out := string(Markdown([]byte("# T\n\n`lead.py` opens.\n\n## S\n\n`check.py` runs.\n\nRun `x` now.\n\n- `item`\n"), Options{Header: true}))
	for _, want := range []string{
		`<p class="lead code-first"><code>lead.py</code>`,
		`<p class="code-first"><code>check.py</code>`,
		`<p>Run <code>x</code> now.</p>`,
		`<li><code>item</code>`,
		".f main.wrap>p.code-first>code:first-child{margin-left:-.3em}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}
