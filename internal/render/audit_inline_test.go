// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build audit

package render

import (
	"regexp"
	"strings"
	"testing"
)

var auditHref = regexp.MustCompile(`href="([^"]*)"`)

// TestAuditEmphasisDoesNotRewriteLinkDestinations: inline() turns a link into
// an <a href> first and then runs the bold and italic passes over the whole
// string, the generated tag included. A * or _ inside the destination that
// pairs with one outside it puts <em> or </em> inside the href: the link no
// longer goes where safeURL checked, and when the closing tag lands in the
// attribute the emphasis opened before the link is never closed, so every
// block after it on the page (the baked reply box and colophon too) renders
// in it.
// audit/10-emphasis-rewrites-link-destination.md.
func TestAuditEmphasisDoesNotRewriteLinkDestinations(t *testing.T) {
	for _, src := range []string{
		"[a](/x*y) *z",
		"*a [b](/c*d) e",
		"**a [b](/c**d) e",
		"[a](/_b) c_ d",
	} {
		got := render(src)
		for _, m := range auditHref.FindAllStringSubmatch(got, -1) {
			if strings.ContainsAny(m[1], "<>") {
				t.Errorf("render(%q): markup inside a link destination: href=%q", src, m[1])
			}
		}
	}
}

// TestAuditHeadingCannotTakeTheReplyBoxID: heading ids are made unique among
// headings and against dp-main only. A heading that slugs to dp-reply-h gets
// the id the baked reply box's own heading uses, ahead of it in the page, so
// the form's aria-labelledby resolves to text the document supplied.
// audit/11-heading-id-shadows-reply-box.md.
func TestAuditHeadingCannotTakeTheReplyBoxID(t *testing.T) {
	page := string(Markdown([]byte("# Lesson\n\n## dp reply h\n\ntext\n"), Options{ReplySlug: "lesson", ReplyToken: "tok"}))
	if n := strings.Count(page, `id="dp-reply-h"`); n != 1 {
		t.Errorf(`id="dp-reply-h" appears %d times, want 1 (the reply box's own heading)`, n)
	}
}
