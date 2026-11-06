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
