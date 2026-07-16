// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/theme"
)

func render(src string) string { return string(Markdown([]byte(src), Options{})) }

func TestMarkdownBlocks(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"h1", "# Title", `<h1 id="title">Title`},
		{"h3", "### Sub", `<h3 id="sub">Sub`},
		{"paragraph", "hello world", "<p>hello world</p>"},
		{"hr", "---", "<hr>"},
		{"blockquote", "> quoted", "<blockquote>quoted</blockquote>"},
		{"fenced code", "```\nx := 1\n```", "<pre><code>x := 1</code></pre>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(tc.src); !strings.Contains(got, tc.want) {
				t.Errorf("render(%q) missing %q\ngot: %s", tc.src, tc.want, got)
			}
		})
	}
}

func TestMarkdownLists(t *testing.T) {
	ul := render("- one\n- two")
	if !strings.Contains(ul, "<ul>") || strings.Count(ul, "<li>") != 2 {
		t.Errorf("unordered list wrong: %s", ul)
	}
	ol := render("1. first\n2. second")
	if !strings.Contains(ol, "<ol>") || strings.Count(ol, "<li>") != 2 {
		t.Errorf("ordered list wrong: %s", ol)
	}
}

// TestMarkdownNestedLists is the C2 fix: indentation nests child lists inside the
// parent's <li> instead of being flattened. Two-spaces-per-level.
func TestMarkdownNestedLists(t *testing.T) {
	out := render("- a\n  - b\n  - c\n- d")
	// b and c nest inside a's item: the inner <ul> opens before a's </li> closes.
	if !strings.Contains(out, "<li>a<ul>\n<li>b</li>\n<li>c</li>\n</ul>\n</li>") {
		t.Errorf("nested unordered list not structured correctly:\n%s", out)
	}
	// d returns to the top level as a sibling of a.
	if !strings.Contains(out, "</li>\n<li>d</li>\n</ul>") {
		t.Errorf("outer sibling after a nested list misplaced:\n%s", out)
	}
	// Two top-level <ul> (one outer) and exactly one nested <ul>.
	if strings.Count(out, "<ul>") != 2 || strings.Count(out, "</ul>") != 2 {
		t.Errorf("want exactly two <ul> pairs (outer + one nested), got:\n%s", out)
	}
	if strings.Count(out, "<li>") != strings.Count(out, "</li>") {
		t.Errorf("unbalanced <li> tags:\n%s", out)
	}
}

// TestMarkdownNestedListsDepth exercises three levels of nesting (arbitrary depth).
func TestMarkdownNestedListsDepth(t *testing.T) {
	out := render("- a\n  - b\n    - c\n- d")
	if !strings.Contains(out, "<li>a<ul>\n<li>b<ul>\n<li>c</li>\n</ul>\n</li>\n</ul>\n</li>\n<li>d</li>\n</ul>") {
		t.Errorf("three-level nesting not structured correctly:\n%s", out)
	}
	if strings.Count(out, "<ul>") != strings.Count(out, "</ul>") ||
		strings.Count(out, "<li>") != strings.Count(out, "</li>") {
		t.Errorf("unbalanced list tags at depth:\n%s", out)
	}
}

// TestMarkdownNestedListsMixed nests an ordered list inside an unordered item and
// keeps the marker kinds distinct at each level.
func TestMarkdownNestedListsMixed(t *testing.T) {
	out := render("- parent\n  1. first\n  2. second\n- sibling")
	if !strings.Contains(out, "<li>parent<ol>\n<li>first</li>\n<li>second</li>\n</ol>\n</li>") {
		t.Errorf("ordered child inside unordered parent not nested:\n%s", out)
	}
	if strings.Count(out, "<ol>") != 1 || strings.Count(out, "<ul>") != 1 {
		t.Errorf("want one <ol> nested in one <ul>:\n%s", out)
	}
}

// TestMarkdownMixedMarkersSameLevel switches marker kind at the same indent: the
// current list closes and the other kind opens.
func TestMarkdownMixedMarkersSameLevel(t *testing.T) {
	out := render("- bullet\n1. number")
	if !strings.Contains(out, "<ul>\n<li>bullet</li>\n</ul>\n<ol>\n<li>number</li>\n</ol>") {
		t.Errorf("a marker-kind switch at the same level should close/reopen the list:\n%s", out)
	}
}

// TestMarkdownNestedListEscapes: nested item content is still escape-first.
func TestMarkdownNestedListEscapes(t *testing.T) {
	out := render("- ok\n  - <script>alert(1)</script>")
	if strings.Contains(out, "<script>") {
		t.Errorf("nested list item not escaped — XSS:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected escaped script in nested item:\n%s", out)
	}
}

func TestMarkdownInline(t *testing.T) {
	cases := map[string]string{
		"**bold**":             "<strong>bold</strong>",
		"*italic*":             "<em>italic</em>",
		"_italic_":             "<em>italic</em>",
		"`code`":               "<code>code</code>",
		"[link](https://x.io)": `<a href="https://x.io" rel="noopener noreferrer">link</a>`,
	}
	for src, want := range cases {
		if got := render(src); !strings.Contains(got, want) {
			t.Errorf("render(%q) missing %q\ngot: %s", src, want, got)
		}
	}
}

// TestMarkdownEscapesRawHTML is the security-critical test: markdown is served
// as text/html same-origin, so raw HTML/JS in the source must be neutralized.
func TestMarkdownEscapesRawHTML(t *testing.T) {
	out := render("Hello <script>alert(1)</script> & <img onerror=x>")
	if strings.Contains(out, "<script>") || strings.Contains(out, "<img onerror") {
		t.Errorf("raw HTML not escaped — XSS risk:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected escaped script tag, got:\n%s", out)
	}
}

// TestMarkdownReplyBox verifies the ?reply=question box is a JS-free, same-origin
// form: it posts to the slug's /answer/<slug> endpoint via an absolute-path
// action (same origin, not a hard-coded cross-origin URL), carries the single
// free-text field the content-plane handler expects, and contains no client-side
// success machinery (the dogfood incident's onsubmit/setTimeout).
func TestMarkdownReplyBox(t *testing.T) {
	off := string(Markdown([]byte("# Lesson"), Options{}))
	if strings.Contains(off, "dp-reply") {
		t.Errorf("reply box emitted without Options.ReplySlug:\n%s", off)
	}

	on := string(Markdown([]byte("# Lesson\n\nBody."), Options{ReplySlug: "lesson-01"}))
	for _, want := range []string{
		`class="dp-reply"`,
		`action="/answer/lesson-01"`,
		`method="post"`,
		`name="body"`,
		`required`,
	} {
		if !strings.Contains(on, want) {
			t.Errorf("reply box missing %q:\n%s", want, on)
		}
	}
	// Same-origin: the action is an absolute PATH, never an absolute URL to some
	// other host/port (the cross-origin trap from the incident).
	if strings.Contains(on, `action="http`) || strings.Contains(on, `action="//`) {
		t.Errorf("reply form action must be a same-origin path, not a cross-origin URL:\n%s", on)
	}
	// No client-side "success" machinery — the confirmation is server-rendered.
	for _, bad := range []string{"onsubmit", "settimeout", "<script", "fetch(", "iframe"} {
		if strings.Contains(strings.ToLower(on), bad) {
			t.Errorf("reply box must be JS-free, found %q:\n%s", bad, on)
		}
	}
	// Without ReplyNext there is no hidden forward pointer.
	if strings.Contains(on, `name="next"`) {
		t.Errorf("hidden next field emitted without Options.ReplyNext:\n%s", on)
	}
}

// TestMarkdownReplyBoxNext verifies the ?next= forward-flow pointer rides the
// baked form as a hidden field (still JS-free — the flow is server-driven).
func TestMarkdownReplyBoxNext(t *testing.T) {
	on := string(Markdown([]byte("# Lesson"), Options{ReplySlug: "lesson-01", ReplyNext: "lesson-02"}))
	if !strings.Contains(on, `<input type="hidden" name="next" value="lesson-02">`) {
		t.Errorf("reply box missing hidden next field:\n%s", on)
	}
	if strings.Contains(strings.ToLower(on), "<script") {
		t.Errorf("forward flow must stay JS-free:\n%s", on)
	}
}

func TestMarkdownDropsDangerousLinkSchemes(t *testing.T) {
	for _, src := range []string{
		"[click](javascript:alert(1))",
		"[x](JavaScript:alert(1))",
		"[y](data:text/html,xss)",
		"[z](vbscript:msgbox)",
		"[w](unknownscheme:payload)",
	} {
		out := strings.ToLower(render(src))
		if strings.Contains(out, "<a href") {
			t.Errorf("non-allowlisted scheme became a link: %q\n%s", src, out)
		}
	}
	// Allowlisted + relative links still render.
	for _, ok := range []string{
		"[a](https://example.com)", "[b](http://x.io)",
		"[c](mailto:me@x.io)", "[d](/rel/path)", "[e](#frag)", "[f](page.html)",
	} {
		if got := render(ok); !strings.Contains(got, "<a href=") {
			t.Errorf("safe link dropped: %q → %s", ok, got)
		}
	}
}

// TestMarkdownLinkAttributeInjection is the reviewer's exact case: a quote in
// the URL must NOT break out of the href attribute. Because the source is
// escaped before link processing, the quote arrives as &#34; and stays inside.
func TestMarkdownLinkAttributeInjection(t *testing.T) {
	out := render(`[x](https://a"onmouseover="alert(1))`)
	// A RAW quote would close the href and start a new attribute. The quote must
	// survive only in escaped form (&#34;), keeping the payload inside the value.
	if strings.Contains(out, `a"onmouseover`) {
		t.Errorf("raw quote broke out of href (attribute injection):\n%s", out)
	}
	if !strings.Contains(out, `&#34;`) {
		t.Errorf("quote not escaped in href:\n%s", out)
	}
}

// TestMarkdownLinkTextEscaped ensures the link TEXT is escaped too.
func TestMarkdownLinkTextEscaped(t *testing.T) {
	out := render(`[<img src=x onerror=alert(1)>](https://ok.io)`)
	if strings.Contains(out, "<img") {
		t.Errorf("link text not escaped:\n%s", out)
	}
}

func TestMarkdownTable(t *testing.T) {
	src := "| Param | Effect |\n|---|---|\n| `?slug=x` | named |\n| `?ttl=2h` | expires |"
	out := render(src)
	if !strings.Contains(out, "<table>") || !strings.Contains(out, "<thead>") || !strings.Contains(out, "<tbody>") {
		t.Fatalf("table not rendered:\n%s", out)
	}
	if strings.Count(out, "<th>") != 2 {
		t.Errorf("want 2 header cells, got:\n%s", out)
	}
	if strings.Count(out, "<tr>") != 3 { // 1 header + 2 body
		t.Errorf("want 3 rows, got:\n%s", out)
	}
	// Inline formatting inside cells still works (code spans).
	if !strings.Contains(out, "<code>?slug=x</code>") {
		t.Errorf("cell inline not rendered:\n%s", out)
	}
	// No raw pipe characters leak into the output.
	if strings.Contains(out, "| Param |") {
		t.Errorf("raw pipe row leaked:\n%s", out)
	}
}

// TestMarkdownTableScrollWrapper is the M1 fix: a table is wrapped in a
// .table-wrap container that scrolls horizontally on its own, so a wide table
// does not force the whole document body to scroll sideways on mobile.
func TestMarkdownTableScrollWrapper(t *testing.T) {
	src := "| A | B |\n|---|---|\n| 1 | 2 |"
	out := render(src)
	if !strings.Contains(out, `<div class="table-wrap">`) {
		t.Errorf("table not wrapped in a scroll container:\n%s", out)
	}
	// The wrapper opens before the table and closes after it.
	i := strings.Index(out, `<div class="table-wrap">`)
	tbl := strings.Index(out, "<table>")
	end := strings.Index(out, "</table>")
	div := strings.Index(out[end:], "</div>")
	if !(i >= 0 && tbl > i && end > tbl && div >= 0) {
		t.Errorf("table-wrap does not enclose the table:\n%s", out)
	}
	// The stylesheet gives the wrapper horizontal scrolling.
	if !strings.Contains(out, ".table-wrap{overflow-x:auto") {
		t.Errorf("stylesheet missing the overflow-x:auto table-wrap rule:\n%s", out)
	}
}

func TestMarkdownTableAlignmentSeparators(t *testing.T) {
	for _, sep := range []string{"|---|---|", "| :-- | --: |", ":-:|:-:", "|----------|----------|"} {
		src := "| A | B |\n" + sep + "\n| 1 | 2 |"
		if out := render(src); !strings.Contains(out, "<table>") {
			t.Errorf("separator %q not recognized:\n%s", sep, out)
		}
	}
	// A pipe line NOT followed by a separator is a normal paragraph, not a table.
	if out := render("a | b | c"); strings.Contains(out, "<table>") {
		t.Errorf("non-table pipe line became a table:\n%s", out)
	}
}

// TestMarkdownTableCellEscaped: the escape-first posture must hold inside cells.
func TestMarkdownTableCellEscaped(t *testing.T) {
	out := render("| col |\n|---|\n| <script>alert(1)</script> |")
	if strings.Contains(out, "<script>") {
		t.Errorf("table cell not escaped — XSS:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected escaped script in cell:\n%s", out)
	}
}

func TestMarkdownDocStructure(t *testing.T) {
	out := render("# Hi")
	if !strings.HasPrefix(out, "<!DOCTYPE html>") || !strings.Contains(out, "</html>") {
		t.Errorf("not a complete HTML document:\n%s", out)
	}
}

// TestMarkdownDefaultUsesHouseStyle asserts the zero-Options render carries the
// shared house-style markers (design tokens + serif font var), NOT the old bare
// system-sans stylesheet. This is the demiplane-7fp fix.
func TestMarkdownDefaultUsesHouseStyle(t *testing.T) {
	out := string(Markdown([]byte("# Title"), Options{}))
	for _, marker := range []string{
		"--accent:oklch(0.485", // house accent token (true red)
		"var(--serif)",         // headings use the serif token
		"--bg:oklch(0.972",     // cool near-neutral background token
		`class="wrap"`,         // centered content container
	} {
		if !strings.Contains(out, marker) {
			t.Errorf("default render missing house-style marker %q:\n%s", marker, out)
		}
	}
	// The old bare (un-tokenized) default stylesheet must be gone from the
	// on-screen sheet. The print sheet legitimately forces light hex values, so
	// scope the check to the CSS before the @media print block.
	screen := out
	if i := strings.Index(out, "@media print"); i >= 0 {
		screen = out[:i]
	}
	if strings.Contains(screen, "color:#1a1a1a") {
		t.Errorf("old bare default stylesheet still present:\n%s", out)
	}
}

// TestMarkdownPrintStylesheet asserts a rendered artifact ships the print sheet
// (demiplane-rwj item 5): a forced page-friendly light palette, unstuck
// masthead, hidden toggle, and non-clipping code — regardless of the on-screen
// theme. A custom --css override opts out (the operator's sheet owns print).
func TestMarkdownPrintStylesheet(t *testing.T) {
	// Even a dark on-screen theme must carry the print sheet forcing light paper.
	out := string(Markdown([]byte("# Title\n\nbody"), Options{Theme: "dracula", Header: true}))
	pi := strings.Index(out, "@media print")
	if pi < 0 {
		t.Fatalf("rendered doc missing @media print block:\n%s", out)
	}
	printCSS := out[pi:]
	for _, marker := range []string{
		"--bg:#ffffff",              // forced light paper
		".themetoggle{display:none", // interactive chrome hidden
		".docbar{position:static",   // masthead unstuck
		"white-space:pre-wrap",      // code wraps instead of clipping
		"@page{margin:2cm}",         // page margins
	} {
		if !strings.Contains(printCSS, marker) {
			t.Errorf("print sheet missing %q:\n%s", marker, printCSS)
		}
	}
	// A full --css override suppresses the built-in print sheet.
	over := string(Markdown([]byte("# T"), Options{CSS: "body{}", Header: true}))
	if strings.Contains(over, "@media print") {
		t.Errorf("custom --css override should not inject the built-in print sheet:\n%s", over)
	}
}

// TestMarkdownThemeDarkSwitchesTokens asserts --theme dark swaps the token block
// while keeping the shared typography.
func TestMarkdownThemeDarkSwitchesTokens(t *testing.T) {
	dark := string(Markdown([]byte("# Title"), Options{Theme: "dark"}))
	if !strings.Contains(dark, "--bg:oklch(0.225") {
		t.Errorf("dark theme did not apply dark background token:\n%s", dark)
	}
	if strings.Contains(dark, "--bg:oklch(0.972") {
		t.Errorf("dark theme leaked the light background token:\n%s", dark)
	}
	// Typography is still the shared one.
	if !strings.Contains(dark, "var(--serif)") {
		t.Errorf("dark theme lost shared typography:\n%s", dark)
	}
	// An unknown theme name falls back to the default (light).
	bogus := string(Markdown([]byte("# Title"), Options{Theme: "neon"}))
	if !strings.Contains(bogus, "--bg:oklch(0.972") {
		t.Errorf("unknown theme should fall back to default house style:\n%s", bogus)
	}
}

// TestMarkdownPinnedThemePinsPaletteAndDropsToggle asserts a named dark theme
// emits its single palette and suppresses the light/dark toggle: no both-sheet
// ToggleCSS override, no toggle button, even with the masthead header on.
func TestMarkdownPinnedThemePinsPaletteAndDropsToggle(t *testing.T) {
	out := string(Markdown([]byte("# Title\n\nbody"), Options{Theme: "dracula", Header: true}))
	if !strings.Contains(out, "--accent:oklch(0.742 0.149 302)") {
		t.Errorf("pinned dracula theme did not emit its accent token:\n%s", out)
	}
	if !strings.Contains(out, "--bg:oklch(0.288 0.022 278)") {
		t.Errorf("pinned dracula theme did not emit its background token:\n%s", out)
	}
	// ToggleCSS ships both sheets via an html[data-theme="dark"] override; a
	// pinned theme must NOT, and must not render the toggle button.
	if strings.Contains(out, `html[data-theme="dark"]{`) {
		t.Errorf("pinned theme leaked the two-sheet toggle override:\n%s", out)
	}
	if strings.Contains(out, `class="themetoggle"`) {
		t.Errorf("pinned theme should not render the light/dark toggle button:\n%s", out)
	}
	// The default palette must not bleed through.
	if strings.Contains(out, "--bg:oklch(0.972") {
		t.Errorf("pinned theme leaked the default light background:\n%s", out)
	}
}

// TestMarkdownDefaultThemeKeepsToggle guards that the toggle survives for the
// unpinned warm palette (regression fence for the Pinned() gate).
func TestMarkdownDefaultThemeKeepsToggle(t *testing.T) {
	out := string(Markdown([]byte("# Title\n\nbody"), Options{Theme: "dark", Header: true}))
	if !strings.Contains(out, `html[data-theme="dark"]{`) || !strings.Contains(out, `class="themetoggle"`) {
		t.Errorf("default palette lost its light/dark toggle:\n%s", out)
	}
}

// TestMarkdownCustomCSSReplacesTheme asserts --css replaces the built-in theme
// entirely (self-host branding).
func TestMarkdownCustomCSSReplacesTheme(t *testing.T) {
	const custom = "body{background:hotpink}"
	out := string(Markdown([]byte("# Title"), Options{Theme: "dark", CSS: custom}))
	if !strings.Contains(out, custom) {
		t.Errorf("custom CSS not present:\n%s", out)
	}
	// The built-in theme tokens must NOT be emitted when --css is set.
	if strings.Contains(out, "--accent:oklch(0.485") || strings.Contains(out, "--bg:oklch(0.225") {
		t.Errorf("custom CSS should fully replace the built-in theme:\n%s", out)
	}
}

// TestMarkdownRendererSharesThemeWithChrome guards the DRY contract: the markup
// the renderer emits is the same stylesheet the theme package hands the chrome.
func TestMarkdownRendererSharesThemeWithChrome(t *testing.T) {
	out := string(Markdown([]byte("# Title"), Options{Theme: theme.Default}))
	if !strings.Contains(out, theme.CSS(theme.Default)) {
		t.Errorf("renderer did not embed the shared theme stylesheet verbatim")
	}
}

// TestMarkdownHeaderLiftsH1 asserts the masthead shows the first H1 and that the
// H1 is lifted out of the body (no duplicate giant title).
func TestMarkdownHeaderLiftsH1(t *testing.T) {
	out := string(Markdown([]byte("# My Doc\n\nbody text"), Options{Header: true, Title: "slug-fallback"}))
	if !strings.Contains(out, `class="docbar"`) || !strings.Contains(out, `class="doctitle">My Doc</h1>`) {
		t.Errorf("masthead title missing:\n%s", out)
	}
	if n := strings.Count(out, "<h1"); n != 1 {
		t.Errorf("want exactly one <h1> (lifted into masthead), got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "body text") {
		t.Errorf("body content lost when lifting H1:\n%s", out)
	}
}

// TestDoctitleMobileTwoLineClamp is p3 (demiplane-ycd): on narrow screens the
// wide masthead's single-line ellipsis relaxes to a 2-line clamp so long titles
// aren't lost, while the wide layout keeps its single-row clip.
func TestDoctitleMobileTwoLineClamp(t *testing.T) {
	out := string(Markdown([]byte("# Title\n\nbody"), Options{Header: true}))
	// Wide face still clips to one line.
	if !strings.Contains(out, "white-space:nowrap;overflow:hidden;text-overflow:ellipsis") {
		t.Errorf("wide masthead should keep single-line ellipsis:\n%s", out)
	}
	// A mobile media query relaxes the title to a 2-line clamp.
	if !strings.Contains(out, "@media (max-width:34rem){.docbar .doctitle{white-space:normal") ||
		!strings.Contains(out, "-webkit-line-clamp:2;line-clamp:2") {
		t.Errorf("mobile 2-line clamp missing from doctitle CSS:\n%s", out)
	}
}

// TestMastheadTitleDoesNotClipDescenders guards the descender-clip fix: the
// .doctitle rule must give glyphs vertical room (a roomy line-height plus bottom
// padding) so descenders (g/y/p/j) aren't sheared by its overflow:hidden box.
func TestMastheadTitleDoesNotClipDescenders(t *testing.T) {
	out := string(Markdown([]byte("# Overnight pages: a deep g/y/p/j\n\nbody"),
		Options{Header: true}))
	// The masthead title renders verbatim (the descender glyphs are present).
	if !strings.Contains(out, `class="doctitle">Overnight pages: a deep g/y/p/j</h1>`) {
		t.Errorf("masthead title not rendered:\n%s", out)
	}
	// The CSS no longer pairs a tight line-height with zero padding (the clip).
	if strings.Contains(out, "line-height:1.15;letter-spacing:-.018em;border:none;padding:0;") {
		t.Errorf(".doctitle still has the descender-clipping line-height/padding:\n%s", out)
	}
	if !strings.Contains(out, "padding:0 0 .14em") {
		t.Errorf(".doctitle missing the bottom padding that clears descenders:\n%s", out)
	}
}

// TestMarkdownHeaderFallsBackToTitle: no H1 → masthead shows the slug fallback.
func TestMarkdownHeaderFallsBackToTitle(t *testing.T) {
	out := string(Markdown([]byte("no heading here"), Options{Header: true, Title: "my-slug"}))
	if !strings.Contains(out, `class="doctitle">my-slug</h1>`) {
		t.Errorf("expected slug fallback title:\n%s", out)
	}
}

// TestMarkdownNoHeaderKeepsH1: without a masthead, the H1 stays in the body.
func TestMarkdownNoHeaderKeepsH1(t *testing.T) {
	out := string(Markdown([]byte("# Body Title\n\ntext"), Options{}))
	if !strings.Contains(out, `<h1 id="body-title">Body Title`) {
		t.Errorf("H1 should remain in body when Header is off:\n%s", out)
	}
	if strings.Contains(out, `class="docbar"`) {
		t.Errorf("masthead should be absent when Header is off")
	}
}

// TestMarkdownToggleShipsBothTokenSets: the toggle needs both palettes inline.
func TestMarkdownToggleShipsBothTokenSets(t *testing.T) {
	out := string(Markdown([]byte("# T"), Options{Header: true}))
	if !strings.Contains(out, ":root{") || !strings.Contains(out, `html[data-theme="dark"]{`) {
		t.Errorf("both token sets must be present for the toggle:\n%s", out)
	}
	if !strings.Contains(out, "--bg:oklch(0.972") || !strings.Contains(out, "--bg:oklch(0.225") {
		t.Errorf("light and dark bg tokens both required for an instant client swap")
	}
	if !strings.Contains(out, `class="themetoggle"`) {
		t.Errorf("toggle button missing from masthead")
	}
	if !strings.Contains(out, "localStorage") || !strings.Contains(out, "prefers-color-scheme") {
		t.Errorf("toggle init script missing localStorage/prefers-color-scheme logic:\n%s", out)
	}
	if !strings.Contains(out, "prefers-reduced-motion") {
		t.Errorf("expected a prefers-reduced-motion guard for the masthead transition")
	}
}

// TestMarkdownToggleUsesSunMoonSVG: the bare glyph is gone; the toggle carries a
// sun + moon inline SVG that CSS swaps by data-theme.
func TestMarkdownToggleUsesSunMoonSVG(t *testing.T) {
	out := string(Markdown([]byte("# T"), Options{Header: true}))
	if strings.Contains(out, "◐") {
		t.Errorf("the bare glyph toggle should be replaced by an SVG:\n%s", out)
	}
	if !strings.Contains(out, `class="i-sun"`) || !strings.Contains(out, `class="i-moon"`) {
		t.Errorf("toggle should carry both a sun and moon SVG:\n%s", out)
	}
	if !strings.Contains(out, `<svg`) || !strings.Contains(out, `stroke="currentColor"`) {
		t.Errorf("toggle icons should be inline SVG drawn in currentColor:\n%s", out)
	}
	if !strings.Contains(out, `html[data-theme="dark"] .themetoggle .i-moon`) {
		t.Errorf("CSS should swap sun/moon by data-theme:\n%s", out)
	}
}

// TestMarkdownLeadParagraph: the first body paragraph after a masthead is tagged
// class="lead" by the renderer (not a positional selector) and the .lead rule
// exists, so the dek survives even when a frontmatter meta-header is injected
// ahead of the body.
func TestMarkdownLeadParagraph(t *testing.T) {
	out := string(Markdown([]byte("# Doc\n\n**Date:** today\n\nrest"), Options{Header: true}))
	if !strings.Contains(out, `.lead{font-size:1.2rem`) {
		t.Errorf("expected a .lead rule for the dek:\n%s", out)
	}
	if !strings.Contains(out, `<p class="lead">`) {
		t.Errorf("renderer should tag the first body paragraph class=\"lead\":\n%s", out)
	}
	if strings.Contains(out, "main.wrap>p:first-child") {
		t.Errorf("the fragile positional lead selector must be gone:\n%s", out)
	}
	// Editorial measure is applied to the document column.
	if !strings.Contains(out, "max-width:43rem") {
		t.Errorf("expected a capped editorial measure for the document column:\n%s", out)
	}
}

// TestMarkdownLeadOnlyWhenBodyLeadsWithProse: a document that opens with a
// heading or list (not a paragraph) gets no .lead tag.
func TestMarkdownLeadOnlyWhenBodyLeadsWithProse(t *testing.T) {
	out := string(Markdown([]byte("# Doc\n\n## Section\n\nbody"), Options{Header: true}))
	if strings.Contains(out, `<p class="lead">`) {
		t.Errorf("a body leading with a heading should not get a lead paragraph:\n%s", out)
	}
}

// TestMarkdownEmitsTitle: every rendered artifact must carry a <title> so shared
// links, tabs, and bookmarks are not blank. The title is plain text: inline
// markup lifted from an H1 is stripped, and the slug is the fallback.
func TestMarkdownEmitsTitle(t *testing.T) {
	// H1 becomes the title.
	out := string(Markdown([]byte("# My Doc\n\nbody"), Options{Header: true, Title: "slug-fallback"}))
	if !strings.Contains(out, "<title>My Doc</title>") {
		t.Errorf("expected <title> from the H1:\n%s", out)
	}
	// Inline markup in the H1 is stripped for the element title.
	code := string(Markdown([]byte("# Deploy `demiplane` now\n\nbody"), Options{Header: true}))
	if !strings.Contains(code, "<title>Deploy demiplane now</title>") {
		t.Errorf("expected inline markup stripped from <title>:\n%s", code)
	}
	// No H1 → the slug/Title fallback fills the element title.
	fb := string(Markdown([]byte("no heading here"), Options{Header: true, Title: "my-slug"}))
	if !strings.Contains(fb, "<title>my-slug</title>") {
		t.Errorf("expected the slug fallback in <title>:\n%s", fb)
	}
}

// TestMastheadToggleAriaPressed: the theme toggle exposes its state to assistive
// tech via aria-pressed (true when the initial theme is dark).
func TestMastheadToggleAriaPressed(t *testing.T) {
	light := string(Markdown([]byte("# T"), Options{Header: true, Theme: "light"}))
	if !strings.Contains(light, `class="themetoggle"`) || !strings.Contains(light, `aria-pressed="false"`) {
		t.Errorf("light toggle should report aria-pressed=false:\n%s", light)
	}
	dark := string(Markdown([]byte("# T"), Options{Header: true, Theme: "dark"}))
	if !strings.Contains(dark, `aria-pressed="true"`) {
		t.Errorf("dark toggle should report aria-pressed=true:\n%s", dark)
	}
}

// TestMarkdownToggleInitialTheme: server theme sets the initial data-theme; an
// unset theme leaves the init script to fall back to prefers-color-scheme.
func TestMarkdownToggleInitialTheme(t *testing.T) {
	d := string(Markdown([]byte("# T"), Options{Header: true, Theme: "dark"}))
	if !strings.Contains(d, `<html lang="en" data-theme="dark">`) {
		t.Errorf("explicit dark not reflected in <html data-theme>:\n%s", d)
	}
	u := string(Markdown([]byte("# T"), Options{Header: true}))
	if !strings.Contains(u, `<html lang="en" data-theme="light">`) {
		t.Errorf("unset theme should render light server-side (no-JS):\n%s", u)
	}
	if !strings.Contains(u, "d=''") {
		t.Errorf("unset server theme must be empty in the init script so prefers-color-scheme applies:\n%s", u)
	}
}

// TestMarkdownCustomCSSDisablesToggle: a --css override owns the look, so the
// toggle (which needs both token sets) is suppressed, but the masthead remains.
func TestMarkdownCustomCSSDisablesToggle(t *testing.T) {
	out := string(Markdown([]byte("# T"), Options{Header: true, CSS: "body{color:red}"}))
	if strings.Contains(out, `class="themetoggle"`) {
		t.Errorf("custom CSS should disable the theme toggle:\n%s", out)
	}
	if !strings.Contains(out, `class="doctitle"`) {
		t.Errorf("masthead title should still render under --css")
	}
	if strings.Contains(out, `html[data-theme="dark"]{`) {
		t.Errorf("custom CSS should not ship the built-in dark token set")
	}
}

// TestMarkdownFooter: the vanity footer honors on/off and the link target.
func TestMarkdownFooter(t *testing.T) {
	on := string(Markdown([]byte("# T"), Options{Footer: true, FooterLink: "https://example.com/repo"}))
	if !strings.Contains(on, "Generated by") || !strings.Contains(on, `href="https://example.com/repo"`) {
		t.Errorf("footer with link missing:\n%s", on)
	}
	off := string(Markdown([]byte("# T"), Options{Footer: false}))
	if strings.Contains(off, "Generated by") {
		t.Errorf("footer should be absent when Footer is off:\n%s", off)
	}
	nolink := string(Markdown([]byte("# T"), Options{Footer: true}))
	if !strings.Contains(nolink, "Generated by") || strings.Contains(nolink, "<a href") {
		t.Errorf("empty footer link should render the wordmark unlinked:\n%s", nolink)
	}
}

// TestHeadingSlugify covers the id derivation directly: lowercasing, spaces to
// hyphens, punctuation and entities stripped, whitespace/hyphen runs collapsed,
// and non-ASCII dropped. The output alphabet is [a-z0-9-] so it is inert in both
// an id attribute and an href fragment.
func TestHeadingSlugify(t *testing.T) {
	cases := map[string]string{
		"Hello World":             "hello-world",
		"  Trim  Me  ":            "trim-me",
		"Already-Hyphenated":      "already-hyphenated",
		"Punctuation! & Symbols?": "punctuation-symbols",
		"Multiple   Spaces":       "multiple-spaces",
		"snake_case name":         "snake-case-name",
		"Café déjà vu":            "caf-dj-vu",
		"v1.2.3 Release":          "v123-release",
		"---leading/trailing---":  "leadingtrailing",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHeadingSlugFromMarkdown verifies the slug is taken from the heading's TEXT,
// not its rendered markup: inline code, bold, and links collapse to their text
// before slugging, so the id is clean and stable.
func TestHeadingSlugFromMarkdown(t *testing.T) {
	out := render("## The `Body` renders **inline** [links](https://x.io)")
	if !strings.Contains(out, `<h2 id="the-body-renders-inline-links">`) {
		t.Errorf("slug should derive from heading text, not markup:\n%s", out)
	}
}

// TestHeadingSlugDedupe: repeated heading text yields deterministic, unique ids
// with numeric suffixes.
func TestHeadingSlugDedupe(t *testing.T) {
	out := render("## Intro\n\n## Intro\n\n## Intro\n\n## Intro")
	for _, want := range []string{
		`<h2 id="intro">`, `<h2 id="intro-1">`, `<h2 id="intro-2">`, `<h2 id="intro-3">`,
	} {
		if strings.Count(out, want) != 1 {
			t.Errorf("want exactly one %q in:\n%s", want, out)
		}
	}
}

// TestHeadingSlugDedupeGuardsNaturalCollision: a later heading whose own slug
// equals an already-generated suffix ("Intro 1" → intro-1, after two "Intro"s
// already claimed intro and intro-1) gets bumped again rather than duplicating.
func TestHeadingSlugDedupeGuardsNaturalCollision(t *testing.T) {
	out := render("## Intro\n\n## Intro\n\n## Intro 1")
	for _, want := range []string{`<h2 id="intro">`, `<h2 id="intro-1">`, `<h2 id="intro-1-1">`} {
		if strings.Count(out, want) != 1 {
			t.Errorf("want exactly one %q in:\n%s", want, out)
		}
	}
}

// TestHeadingEmptySlugFallback: a heading with no sluggable characters still
// gets a stable id (the "section" fallback), deduped across repeats.
func TestHeadingEmptySlugFallback(t *testing.T) {
	out := render("## !!!\n\n## ???")
	if !strings.Contains(out, `<h2 id="section">`) || !strings.Contains(out, `<h2 id="section-1">`) {
		t.Errorf("punctuation-only headings should fall back to section/section-1:\n%s", out)
	}
}

// TestHeadingAnchor: every heading carries a hover-'#' permalink pointing at its
// own id, with a Permalink aria-label for assistive tech.
func TestHeadingAnchor(t *testing.T) {
	out := render("### Threat Hunting")
	if !strings.Contains(out, `<h3 id="threat-hunting">`) {
		t.Errorf("heading missing id:\n%s", out)
	}
	if !strings.Contains(out, `<a class="heading-anchor" href="#threat-hunting" aria-label="Permalink to Threat Hunting">#</a>`) {
		t.Errorf("heading missing permalink anchor:\n%s", out)
	}
}

// TestHeadingIDInjectionSafe is security-critical: an id/aria-label is built from
// attacker-controlled heading text, so neither sink may carry raw HTML or an
// attribute breakout. The id alphabet is [a-z0-9-]; the aria-label is escaped.
func TestHeadingIDInjectionSafe(t *testing.T) {
	out := render(`## "><script>alert(1)</script>`)
	if strings.Contains(out, "<script>") {
		t.Errorf("heading text must not inject raw HTML:\n%s", out)
	}
	// The id must contain no quote, angle bracket, or space that would break the
	// attribute or the tag.
	for _, bad := range []string{`id="">`, `id=""`, `<script`} {
		if strings.Contains(out, bad) {
			t.Errorf("id attribute breakout via %q:\n%s", bad, out)
		}
	}
	// The aria-label carries the heading text escaped, never as live markup.
	if !strings.Contains(out, `aria-label="Permalink to `) || strings.Contains(out, `aria-label="Permalink to "><script>`) {
		t.Errorf("aria-label not safely escaped:\n%s", out)
	}
}

// TestMarkdownColorScheme is the m2 fix: the document advertises color-scheme so
// UA controls/scrollbars follow the theme, and a <meta name="theme-color"> tints
// the mobile browser chrome. The toggle sheet carries light in :root and a dark
// override; a pinned theme carries a single value.
func TestMarkdownColorScheme(t *testing.T) {
	tog := string(Markdown([]byte("# T\n\nbody"), Options{Header: true}))
	if !strings.Contains(tog, "color-scheme:light") {
		t.Errorf("toggle sheet missing :root color-scheme:light:\n%s", tog)
	}
	if !strings.Contains(tog, "color-scheme:dark") {
		t.Errorf("toggle sheet missing the dark color-scheme override:\n%s", tog)
	}
	if !strings.Contains(tog, `<meta name="theme-color" content="oklch(0.972 0.004 250)">`) {
		t.Errorf("missing light theme-color meta:\n%s", tog)
	}
	// A pinned dark theme carries a single color-scheme:dark and its own bar color.
	pinned := string(Markdown([]byte("# T\n\nbody"), Options{Header: true, Theme: "dracula"}))
	if !strings.Contains(pinned, "color-scheme:dark") || strings.Contains(pinned, "color-scheme:light") {
		t.Errorf("pinned dark theme should carry only color-scheme:dark:\n%s", pinned)
	}
	if !strings.Contains(pinned, `<meta name="theme-color" content="oklch(0.288 0.022 278)">`) {
		t.Errorf("pinned theme-color meta should reflect its --bg:\n%s", pinned)
	}
}

// TestMarkdownChromeNoBurntOrange is the m1 fix: the docChromeCSS token fallbacks
// must match the current rojo palette, not the retired burnt-orange one. None of
// the old warm hues (47/60/62/78/80) may appear in a rendered document.
func TestMarkdownChromeNoBurntOrange(t *testing.T) {
	out := string(Markdown([]byte("# T\n\nbody"), Options{Header: true, Footer: true}))
	for _, stale := range []string{
		"0.972 0.009 78", "0.895 0.013 80", "0.300 0.030 60",
		"0.555 0.162 47", "0.265 0.020 60", "0.495 0.022 62",
	} {
		if strings.Contains(out, stale) {
			t.Errorf("stale burnt-orange chrome fallback %q still present:\n%s", stale, out)
		}
	}
	// The rojo accent fallback is what should be there instead.
	if !strings.Contains(out, "var(--accent,oklch(0.485 0.135 27))") {
		t.Errorf("chrome accent fallback not synced to the rojo token:\n%s", out)
	}
}

// TestHeadingAnchorTouchAndPrintCSS is the m3/m4 fix: heading anchors are shown
// (not invisible-but-tappable) on touch, are not text-selectable, and are hidden
// in print.
func TestHeadingAnchorTouchAndPrintCSS(t *testing.T) {
	out := render("## Section")
	for _, rule := range []string{
		"@media (hover:none){.heading-anchor{opacity:.45}}",
		"@media print{.heading-anchor{display:none}}",
		"user-select:none",
	} {
		if !strings.Contains(out, rule) {
			t.Errorf("stylesheet missing %q:\n%s", rule, out)
		}
	}
	// h5/h6 join the hover-reveal set (M3), so their anchors are reachable.
	if !strings.Contains(out, "h5:hover .heading-anchor,h6:hover .heading-anchor") {
		t.Errorf("h5/h6 anchors not added to the hover-reveal rule:\n%s", out)
	}
}

// TestScrollPaddingPresent: the rendered stylesheet sets scroll-padding-top on
// the scroll root so an #anchor jump clears the sticky masthead.
func TestScrollPaddingPresent(t *testing.T) {
	out := string(Markdown([]byte("# T\n\n## S"), Options{Header: true}))
	if !strings.Contains(out, "scroll-padding-top:6rem") {
		t.Errorf("stylesheet missing scroll-padding-top for the sticky masthead:\n%s", out)
	}
}
