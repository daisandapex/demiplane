// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"regexp"
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
		// Backslashes: browsers treat "\\" as "/" in http(s) URLs, so each
		// of these resolves to another host. Rejected outright.
		{"\\\\attacker.com", false},
		{"/\\attacker.com", false},
		{"\\/attacker.com", false},
		{"\\attacker.com", false},
		{"/\\/attacker.com", false},
		{"https:\\\\attacker.com", false},
		{"page\\x.html", false},
		// Tab, LF and CR are stripped anywhere in a URL by the browser's
		// parser, so "/\t/host" becomes "//host"; other controls are refused too.
		{"/\t/attacker.com", false},
		{"/\n/attacker.com", false},
		{"/\r/attacker.com", false},
		{"\x01//attacker.com", false},
		{"/a\x7fb", false},
		{"java\tscript:alert(1)", false},
		// Userinfo in a web URL disguises the real host.
		{"https://trusted.example@attacker.com/", false},
		{"http://user:pw@attacker.com", false},
		{"https://attacker.com/path@x", true}, // "@" in the path is not userinfo
		{"https://attacker.com/?q=a@b", true},
		{"mailto:a@b.com?subject=x", true},
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

var anchorToEvil = regexp.MustCompile(`<a href="[^"]*evil\.example`)

// TestMarkdownBackslashHostNotLinked: the end-to-end effect for the
// backslash and userinfo forms. None may become an anchor.
func TestMarkdownBackslashHostNotLinked(t *testing.T) {
	for _, dest := range []string{`/\evil.example`, `\\evil.example`, `https://ok.example@evil.example`} {
		out := string(Markdown([]byte("[go]("+dest+")"), Options{}))
		if anchorToEvil.MatchString(out) {
			t.Errorf("[go](%s) rendered as a link:\n%s", dest, out)
		}
	}
}
