// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package theme

import (
	"regexp"
	"strings"
	"testing"
)

// rule returns the declaration block of the first rule whose selector is
// exactly sel, or "" when there is none.
func rule(css, sel string) string {
	m := regexp.MustCompile(`(?m)(?:^|\})` + regexp.QuoteMeta(sel) + `\{([^}]*)\}`).FindStringSubmatch(css)
	if m == nil {
		return ""
	}
	return m[1]
}

// TestInlineCodeWrapsOnPhones: a long inline-code URL breaks instead of
// running off the right edge of a phone.
func TestInlineCodeWrapsOnPhones(t *testing.T) {
	code := rule(contentCSS, "code")
	if code == "" {
		t.Fatal("no code rule in the content sheet")
	}
	if !strings.Contains(code, "overflow-wrap:break-word") {
		t.Errorf("inline code does not wrap: code{%s}", code)
	}
	if strings.Contains(code, "overflow-wrap:anywhere") {
		t.Errorf("inline code in prose should break only a token that cannot fit, keeping ports whole: code{%s}", code)
	}
	if cell := rule(contentCSS, ":is(td,th) code"); !strings.Contains(cell, "overflow-wrap:anywhere") {
		t.Errorf("inline code in a table cell must break anywhere so the cell can shrink: {%s}", cell)
	}
}

// TestTouchAnchorsSkipTheTitle: on touch screens the permalink mark shows on
// section headings only, never beside the page title.
func TestTouchAnchorsSkipTheTitle(t *testing.T) {
	m := regexp.MustCompile(`@media \(hover:none\)\{([^}]*)\{`).FindStringSubmatch(contentCSS)
	if m == nil {
		t.Fatal("no hover:none anchor rule")
	}
	sel := m[1]
	if sel == ".heading-anchor" || strings.Contains(sel, "h1") {
		t.Errorf("touch anchor rule reaches the H1: %q", sel)
	}
	if !strings.Contains(sel, "h2") {
		t.Errorf("touch anchor rule should still reach section headings: %q", sel)
	}
}

// TestShortCodeNoWrap: a short inline-code token stays on one line in prose,
// and may wrap again inside a phone table cell so the cell can shrink.
func TestShortCodeNoWrap(t *testing.T) {
	if r := rule(contentCSS, ":not(pre)>code.nw"); !strings.Contains(r, "white-space:nowrap") {
		t.Errorf("short code should not wrap: {%s}", r)
	}
	if r := rule(contentCSS, ":is(td,th) code.nw"); !strings.Contains(r, "white-space:normal") {
		t.Errorf("phone table cells should let short code wrap: {%s}", r)
	}
	if strings.Contains(rule(contentCSS, "pre code"), "nowrap") {
		t.Error("block code must keep its own white-space")
	}
}

// TestLinkStatesDiffer: in prose, a visited link is a quieter oxblood and a
// hovered one gains a soft wash, so all four states read apart.
func TestLinkStatesDiffer(t *testing.T) {
	if r := rule(contentCSS, "main :is(p,li,td,dd,blockquote) a:visited"); !strings.Contains(r, "color:color-mix(in oklab,var(--accent) 55%,var(--muted))") {
		t.Errorf("visited prose links should be a quieter oxblood: {%s}", r)
	}
	if r := rule(contentCSS, "main :is(p,li,td,dd,blockquote) a:is(:hover,:active)"); !strings.Contains(r, "background:color-mix(in srgb,var(--accent) 14%,transparent)") {
		t.Errorf("hovered prose links should gain a wash: {%s}", r)
	}
}

// TestCodeBlockScrollCue: a code block that scrolls sideways shows a soft
// shadow at the hidden edge, covered again once that edge is reached.
func TestCodeBlockScrollCue(t *testing.T) {
	r := rule(contentCSS, "pre")
	for _, want := range []string{"no-repeat local", "radial-gradient(farthest-side at 100% 50%,color-mix(in srgb,var(--ink) 35%,transparent),transparent) right/1.25rem 100% no-repeat scroll"} {
		if !strings.Contains(r, want) {
			t.Errorf("pre should carry a scroll cue (%q): {%s}", want, r)
		}
	}
}

// TestNavEntriesAvoidOrphans: a margin-nav entry that wraps never strands
// one short word ("/") on its last line.
func TestNavEntriesAvoidOrphans(t *testing.T) {
	if !strings.Contains(frameCSS, "text-decoration:none;overflow-wrap:anywhere;text-wrap:pretty}") {
		t.Error("margin-nav entries should wrap with text-wrap:pretty")
	}
}

// TestTableCodeChipsTighter: code chips in a table cell carry less padding
// than in prose, so a cell of several chips stays calm.
func TestTableCodeChipsTighter(t *testing.T) {
	if r := rule(contentCSS, ":is(th,td) code"); !strings.Contains(r, "padding:.05em .22em") {
		t.Errorf("table code chips should be tighter: {%s}", r)
	}
}

// TestPhoneCodeWraps: on a phone a code block wraps its long lines instead
// of hiding them past the right edge; wider screens keep the scrolling slab.
func TestPhoneCodeWraps(t *testing.T) {
	if !strings.Contains(contentCSS, "@media (max-width:40rem){pre{white-space:pre-wrap;overflow-wrap:anywhere}}") {
		t.Error("phone code blocks should wrap")
	}
	if strings.Contains(rule(contentCSS, "pre"), "pre-wrap") {
		t.Error("only phones wrap code; wide screens keep the slab")
	}
}

// TestPhoneStackedTableLabelsTight: a stacked table row's label column is
// narrow and small, so a phone shows more records per screen.
func TestPhoneStackedTableLabelsTight(t *testing.T) {
	for _, want := range []string{"tr{padding:.4rem 0}", "padding:.15rem .875rem .15rem 6rem", "width:4.75rem;color:var(--muted);font-weight:600;font-size:.75rem"} {
		if !strings.Contains(contentCSS, want) {
			t.Errorf("stacked table rows should be tighter: missing %q", want)
		}
	}
}

// TestShortCodeWrapsOnNarrowPhones: a 40-character span is wider than the
// text column below 390px, where html clips sideways overflow, so there short
// code may wrap rather than lose its tail. 390px and up keeps it whole.
func TestShortCodeWrapsOnNarrowPhones(t *testing.T) {
	const mq = "@media (max-width:24.35rem){"
	i := strings.Index(contentCSS, mq)
	if i < 0 {
		t.Fatalf("no %s block", mq)
	}
	if r := rule(contentCSS[i+len(mq):], ":not(pre)>code.nw"); !strings.Contains(r, "white-space:normal") {
		t.Errorf("short code should wrap below 390px: {%s}", r)
	}
}
