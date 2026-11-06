// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"strings"
	"testing"
)

// TestDocsReadmeDropsRawHTML: the README's leading <p><picture> logo block is
// GitHub-only markup; /docs/readme must not print it as escaped text.
func TestDocsReadmeDropsRawHTML(t *testing.T) {
	ts := newTestServer(t, "")
	_, body := get(t, ts.URL+"/docs/readme")
	page := string(body)
	for _, bad := range []string{"&lt;p align", "&lt;picture", "&lt;img", "&lt;/p&gt;"} {
		if strings.Contains(page, bad) {
			t.Errorf("/docs/readme prints raw HTML %q", bad)
		}
	}
	if !strings.Contains(page, ">demiplane") || !strings.Contains(page, "Your private publishing plane.") {
		t.Errorf("/docs/readme lost the README body:\n%s", page)
	}
}

// TestStripLeadingHTML pins what the docs page strips: one leading raw HTML
// block, and nothing when the doc opens with markdown.
func TestStripLeadingHTML(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"<p align=\"center\">\n  <img src=x>\n</p>\n\n# T\n", "# T\n"},
		{"\n<div>\n</div>\n\n# T\n", "# T\n"},
		{"# T\n\n<p>x</p>\n", "# T\n\n<p>x</p>\n"},
		{"<p>only</p>", ""},
		{"", ""},
	} {
		if got := string(stripLeadingHTML([]byte(c.in))); got != c.want {
			t.Errorf("stripLeadingHTML(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
