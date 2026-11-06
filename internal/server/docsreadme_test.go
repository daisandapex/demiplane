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
// block, and nothing when the doc opens with markdown, even markdown that
// starts with "<" such as an autolink.
func TestStripLeadingHTML(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"<p align=\"center\">\n  <img src=x>\n</p>\n\n# T\n", "# T\n"},
		{"\n<div>\n</div>\n\n# T\n", "# T\n"},
		{"# T\n\n<p>x</p>\n", "# T\n\n<p>x</p>\n"},
		{"<p>only</p>", ""},
		{"<p align=\"center\">\r\n  <img src=x>\r\n</p>\r\n\r\n# T\r\n", "# T\n"},
		{"<p>\n</p>\n  \t\n# T\n", "# T\n"},
		{"", ""},
		{"<!-- note -->\n\n# T\n", "# T\n"},
		{"</div>\n\n# T\n", "# T\n"},
		{"<!DOCTYPE html>\n\n# T\n", "# T\n"},
		{"<?xml version=\"1.0\"?>\n\n# T\n", "# T\n"},
		{"<![CDATA[x]]>\n\n# T\n", "# T\n"},
		{"<br/>\n\n# T\n", "# T\n"},
		{"<div title=\"a < b\" hidden data-x='y' z=1>\nlogo\n</div>\n\n# T\n", "# T\n"},
		// One case per CommonMark 0.31.2 §4.6 start condition. Kinds 1-5 run
		// to their end marker, across blank lines; 6 and 7 run to a blank line.
		{"<script>\nvar a;\n\nvar b;\n</script>\n# T\n", "# T\n"},
		{"<!-- a\n\nb -->\n# T\n", "# T\n"},
		{"<?php\n\necho 1;\n?>\n# T\n", "# T\n"},
		{"<!DOCTYPE html>\n# T\n", "# T\n"},
		{"<![CDATA[\n\nx\n]]>\n# T\n", "# T\n"},
		{"<div\n  class=\"logo\">\nlogo\n</div>\n\n# T\n", "# T\n"},
		{"<picture>\n<img src=x>\n</picture>\n\n# T\n", "# T\n"},
		{"<div>\n</div>\n\n    code\n", "    code\n"},
		{"<SCRIPT>\n\n</Style>\n# T\n", "# T\n"},
		{"<div>\r\r# T\r", "# T\n"},
		// Markdown that opens with "<" is not an HTML block.
		{"<span>Note</span> see below\n# Title\n\ntext", "<span>Note</span> see below\n# Title\n\ntext"},
		{"<pre/>\n\n# T\n", "<pre/>\n\n# T\n"},
		{"    <div>\n\n# T\n", "    <div>\n\n# T\n"},
		// Tag names match by ASCII case only: U+017F folds to "s" in Unicode.
		{"<\u017fcript>\n\n# T\n", "<\u017fcript>\n\n# T\n"},
		{"<\u017fection>\n\n# T\n", "<\u017fection>\n\n# T\n"},
		{"<hello = world>\n\n# T\n", "<hello = world>\n\n# T\n"},
		{"<hello world\n\n# T\n", "<hello world\n\n# T\n"},
		{"<https://example.com> is the home page.\n\n# T\n", "<https://example.com> is the home page.\n\n# T\n"},
		{"<https://example.com>\nno blank line", "<https://example.com>\nno blank line"},
		{"<me@example.com> wrote this.\n", "<me@example.com> wrote this.\n"},
		{"< not a tag\n", "< not a tag\n"},
	} {
		if got := string(stripLeadingHTML([]byte(c.in))); got != c.want {
			t.Errorf("stripLeadingHTML(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
