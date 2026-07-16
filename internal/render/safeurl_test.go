// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"strings"
	"testing"
)

// TestSafeURLRejectsProtocolRelative locks the open-redirect fix: a
// protocol-relative URL ("//host") is an external navigation and must NOT be
// treated as a safe same-origin relative reference. Regression for the audit
// finding that [text](//attacker.com) rendered as an off-site link.
func TestSafeURLRejectsProtocolRelative(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		// The regression: protocol-relative externals must be rejected.
		{"//attacker.com", false},
		{"//attacker.com/evil", false},
		{" //attacker.com", false}, // leading space (TrimSpace) still rejected
		{"//Attacker.com", false},  // case-folded still rejected
		{"\\\\attacker.com", true}, // backslashes are NOT protocol-relative; harmless relative text
		// Genuine same-origin relative refs still allowed.
		{"/reports", true},
		{"/", true},
		{"#anchor", true},
		{"?q=1", true},
		{"page.html", true},
		{"sub/page.html", true},
		// Absolute web + mail schemes allowed; active-content schemes rejected.
		{"https://example.com", true},
		{"http://example.com", true},
		{"mailto:a@b.com", true},
		{"javascript:alert(1)", false},
		{"data:text/html,x", false},
		{"vbscript:msgbox", false},
	}
	for _, c := range cases {
		if got := safeURL(c.url); got != c.want {
			t.Errorf("safeURL(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

// TestMarkdownProtocolRelativeNotLinked confirms the end-to-end effect: a
// protocol-relative markdown link does not emit an off-site anchor href.
func TestMarkdownProtocolRelativeNotLinked(t *testing.T) {
	out := string(Markdown([]byte("[click](//evil.example)"), Options{}))
	if strings.Contains(out, `href="//evil.example"`) {
		t.Errorf("protocol-relative link was rendered as an off-site anchor:\n%s", out)
	}
}
