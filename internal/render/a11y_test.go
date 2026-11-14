// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"strings"
	"testing"
)

// TestOneBannerAndSkipLink: an F page has one banner landmark (the title's
// header; the tool bar is not a header), and a masthead page of either kind
// opens with a skip link to its main column.
func TestOneBannerAndSkipLink(t *testing.T) {
	for name, opts := range map[string]Options{
		"F layout":   {Header: true},
		"css header": {Header: true, CSS: "body{color:red}"},
	} {
		out := string(Markdown([]byte(fDoc), opts))
		if n := strings.Count(out, "<header"); n != 1 {
			t.Errorf("%s: %d <header> elements, want 1:\n%s", name, n, out)
		}
		body := strings.Index(out, "<body")
		skip := strings.Index(out, `<a class="skip" href="#dp-main">Skip to content</a>`)
		if skip < 0 || skip < body {
			t.Errorf("%s: no skip link at the top of the body:\n%s", name, out)
		}
		if !strings.Contains(out, `<main class="wrap" id="dp-main">`) && !strings.Contains(out, `<main class="wrap doc" id="dp-main">`) {
			t.Errorf("%s: main column is not the skip target:\n%s", name, out)
		}
		if !strings.Contains(out, `.skip{`) {
			t.Errorf("%s: skip link is unstyled, so it would always show", name)
		}
	}
	// The skip target's id is reserved, so a heading cannot take it.
	out := string(Markdown([]byte("# T\n\n## dp main\n\nx\n"), Options{Header: true}))
	if strings.Count(out, `id="dp-main"`) != 1 || !strings.Contains(out, `id="dp-main-1"`) {
		t.Errorf("heading took the skip target's id:\n%s", out)
	}
	// A bare body render (no chrome) has nothing to skip.
	if strings.Contains(string(Markdown([]byte(fDoc), Options{})), `class="skip"`) {
		t.Error("a chrome-less render should not carry a skip link")
	}
}

// TestHeadingCannotTakeTheReplyBoxID: a heading that slugs to an id the page
// chrome writes (here dp-reply-h, the baked reply box's own heading) gets a
// suffix instead. Otherwise it would come first in the page and the form's
// aria-labelledby would resolve to text the document supplied.
// See audit/11-heading-id-shadows-reply-box.md.
func TestHeadingCannotTakeTheReplyBoxID(t *testing.T) {
	page := string(Markdown([]byte("# Lesson\n\n## dp reply h\n\ntext\n"), Options{ReplySlug: "lesson", ReplyToken: "tok"}))
	if n := strings.Count(page, `id="dp-reply-h"`); n != 1 {
		t.Errorf(`id="dp-reply-h" appears %d times, want 1 (the reply box's own heading)`, n)
	}
}

// TestHeadingsAvoidChromeIDs: every fixed id the page chrome writes is
// reserved, so a heading that slugs to one gets a suffix.
func TestHeadingsAvoidChromeIDs(t *testing.T) {
	for _, id := range []string{mainID, replyHeadingID, seriesTreeID} {
		src := "# Title\n\n## " + strings.ReplaceAll(id, "-", " ") + "\n"
		page := string(Markdown([]byte(src), Options{}))
		if !strings.Contains(page, `<h2 id="`+id+`-1">`) {
			t.Errorf("heading slugging to %q: want id %q-1 in\n%s", id, id, page)
		}
	}
}
