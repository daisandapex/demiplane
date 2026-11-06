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
	if r := rule(contentCSS, "main :is(p,li,td,dd,blockquote) a:visited"); !strings.Contains(r, "color:color-mix(in oklab,var(--accent) 72%,var(--muted))") {
		t.Errorf("visited prose links should be a quieter oxblood: {%s}", r)
	}
	if r := rule(contentCSS, "main :is(p,li,td,dd,blockquote) a:is(:hover,:active)"); !strings.Contains(r, "background:color-mix(in srgb,var(--accent) 9%,transparent)") {
		t.Errorf("hovered prose links should gain a wash: {%s}", r)
	}
}
