// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build audit

package render

import (
	"strings"
	"testing"
)

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
