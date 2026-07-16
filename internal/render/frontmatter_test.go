// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"strings"
	"testing"
)

const fmDoc = `---
date: 2026-06-20T13:46:00Z
repo: demiplane
branch: feat/render-theme
---
# Overnight Report

Body text.
`

// renderMeta renders with the meta-header (and masthead) enabled, the publish
// path's normal configuration.
func renderMeta(src string) string {
	return string(Markdown([]byte(src), Options{Header: true, MetaHeader: true}))
}

func TestFrontmatterMetaHeader(t *testing.T) {
	out := renderMeta(fmDoc)

	// The localized date: a <time> with the UTC ISO datetime, the server UTC text,
	// the localize hook, and the client localize script.
	for _, want := range []string{
		`class="metahead"`,
		`<time class="tstamp" data-localize datetime="2026-06-20T13:46:00Z">`,
		"2026-06-20 · 13:46 UTC",
		"data-localize",
		"timeZoneName", // the localize script is present
		"document.querySelectorAll('time.tstamp[data-localize]')",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("meta-header missing %q\ngot:\n%s", want, out)
		}
	}

	// Each remaining field on its own labeled line, title-cased label.
	for _, want := range []string{
		"<dt>Repo</dt><dd>demiplane</dd>",
		"<dt>Branch</dt><dd>feat/render-theme</dd>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("meta-header missing field %q\ngot:\n%s", want, out)
		}
	}

	// The H1 still becomes the masthead title; frontmatter is NOT in the body.
	if !strings.Contains(out, `<h1 class="doctitle">Overnight Report</h1>`) {
		t.Errorf("masthead title missing/wrong\ngot:\n%s", out)
	}
	if strings.Contains(out, "date: 2026-06-20") || strings.Contains(out, "branch: feat") {
		t.Errorf("raw frontmatter leaked into the body\ngot:\n%s", out)
	}
	if strings.Contains(out, "<hr>") {
		t.Errorf("frontmatter fence rendered as a horizontal rule\ngot:\n%s", out)
	}
}

func TestFrontmatterMetaHeaderOff(t *testing.T) {
	// meta_header off: frontmatter is stripped, no meta-header, no leak.
	out := string(Markdown([]byte(fmDoc), Options{Header: true, MetaHeader: false}))
	if strings.Contains(out, "metahead") {
		t.Errorf("meta-header rendered despite MetaHeader=false\ngot:\n%s", out)
	}
	if strings.Contains(out, "date: 2026-06-20") || strings.Contains(out, "<hr>") {
		t.Errorf("frontmatter not stripped when meta-header off\ngot:\n%s", out)
	}
	// The body (H1 + text) still renders.
	if !strings.Contains(out, "Overnight Report") || !strings.Contains(out, "Body text.") {
		t.Errorf("body lost when stripping frontmatter\ngot:\n%s", out)
	}
}

func TestNoFrontmatterUnchanged(t *testing.T) {
	src := "# Title\n\nJust prose.\n"
	with := string(Markdown([]byte(src), Options{Header: true, MetaHeader: true}))
	if strings.Contains(with, "metahead") {
		t.Errorf("meta-header rendered for a doc with no frontmatter\ngot:\n%s", with)
	}
	// A leading horizontal rule (--- not followed by a closing ---) is NOT
	// mistaken for frontmatter.
	hr := string(Markdown([]byte("---\n\ncontent\n"), Options{MetaHeader: true}))
	if strings.Contains(hr, "metahead") {
		t.Errorf("a lone --- was misread as frontmatter\ngot:\n%s", hr)
	}
	if !strings.Contains(hr, "<hr>") {
		t.Errorf("a leading --- should still render as <hr>\ngot:\n%s", hr)
	}
}

func TestFrontmatterDateVariants(t *testing.T) {
	// A bare date (no time): rendered, but no localize hook/script (nothing to
	// localize to a zone).
	dateOnly := string(Markdown([]byte("---\ndate: 2026-06-20\nrepo: x\n---\nbody\n"),
		Options{MetaHeader: true}))
	if !strings.Contains(dateOnly, `<time class="tstamp" datetime="2026-06-20">2026-06-20</time>`) {
		t.Errorf("bare date not rendered as plain date\ngot:\n%s", dateOnly)
	}
	if strings.Contains(dateOnly, "data-localize") || strings.Contains(dateOnly, "timeZoneName") {
		t.Errorf("a bare date should not get the localize hook/script\ngot:\n%s", dateOnly)
	}

	// "published" is also a recognized date key.
	pub := string(Markdown([]byte("---\npublished: 2026-01-02 09:30\n---\nbody\n"),
		Options{MetaHeader: true}))
	if !strings.Contains(pub, "2026-01-02 · 09:30 UTC") || !strings.Contains(pub, "data-localize") {
		t.Errorf("published key not localized\ngot:\n%s", pub)
	}

	// A non-timestamp date value is shown as-is, no <time>.
	asis := string(Markdown([]byte("---\ndate: sometime soon\n---\nbody\n"),
		Options{MetaHeader: true}))
	if !strings.Contains(asis, `<span class="tstamp">sometime soon</span>`) {
		t.Errorf("non-timestamp date value not shown as-is\ngot:\n%s", asis)
	}
}

func TestFrontmatterEscaping(t *testing.T) {
	out := string(Markdown([]byte("---\nrepo: <script>alert(1)</script>\n---\nbody\n"),
		Options{MetaHeader: true}))
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Errorf("frontmatter value not escaped\ngot:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected escaped value\ngot:\n%s", out)
	}
}

func TestParseFrontmatterUnit(t *testing.T) {
	fields, rest, ok := parseFrontmatter([]byte("---\na: 1\nb: two words\n# c: comment\nnocolon\n---\nbody\n"))
	if !ok {
		t.Fatal("expected frontmatter")
	}
	if len(fields) != 2 {
		t.Fatalf("want 2 fields (comment + colon-less line ignored), got %d: %+v", len(fields), fields)
	}
	if fields[0].key != "a" || fields[0].value != "1" || fields[1].key != "b" || fields[1].value != "two words" {
		t.Errorf("fields parsed wrong: %+v", fields)
	}
	if strings.TrimSpace(string(rest)) != "body" {
		t.Errorf("body not split cleanly: %q", rest)
	}
}

func TestTitleCase(t *testing.T) {
	cases := map[string]string{
		"repo":        "Repo",
		"footer_link": "Footer Link",
		"build-host":  "Build Host",
	}
	for in, want := range cases {
		if got := titleCase(in); got != want {
			t.Errorf("titleCase(%q) = %q, want %q", in, got, want)
		}
	}
}
