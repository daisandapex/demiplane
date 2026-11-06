// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"fmt"
	"html"
	"log"
	"net/http"
	"regexp"
	"strings"

	demiplane "github.com/daisandapex/demiplane/v3"
	"github.com/daisandapex/demiplane/v3/internal/logsafe"
	"github.com/daisandapex/demiplane/v3/internal/render"
)

// docPage is one curated, embedded markdown document surfaced at /docs/<slug>.
type docPage struct {
	Slug  string
	Title string
	File  string // path within the embedded DocsFS
	Blurb string
}

// docPages is the curated set. Internal build briefs are intentionally excluded.
var docPages = []docPage{
	{"readme", "Overview", "README.md", "What demiplane is, the API, and how to deploy it."},
	{"api", "API", "API.md", "Full REST reference with curl, Python, JavaScript, and Go examples."},
	{"security", "Security", "SECURITY.md", "Threat model, the same-origin footgun, hardening, disclosure."},
	{"contributing", "Contributing", "CONTRIBUTING.md", "Dev setup, gates, conventions, and the CLA."},
	{"changelog", "Changelog", "CHANGELOG.md", "What shipped, by milestone."},
}

func findDocPage(slug string) (docPage, bool) {
	for _, p := range docPages {
		if p.Slug == slug {
			return p, true
		}
	}
	return docPage{}, false
}

// handleDocsIndex renders the /docs landing: the positioning pitch + nav tiles.
// The pitch leads with demiplane's inverse-of-public posture (its closest analog,
// ht-ml.app, makes everything public; demiplane keeps your data home).
func (s *Server) handleDocsIndex(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString(`<span class="kicker">Documentation</span>`)
	b.WriteString(`<h1>demiplane docs</h1>`)
	b.WriteString(`<span class="ethos">Sealed by default, shared by choice.</span>`)
	b.WriteString(`<p>Self-hosted, internal-first static &amp; HTML publishing with a REST API: ` +
		`a superset of pico's <code>pgs</code> with first-class HTTP + SSH ingest, per-artifact ` +
		`privacy, passwords, and TTL. This very page is rendered by demiplane's own markdown engine ` +
		`from the docs embedded in the binary.</p>`)
	b.WriteString(`<div class="grid">`)
	for _, p := range docPages {
		fmt.Fprintf(&b, `<a class="tile" href="/docs/%s"><div class="t">%s</div><div class="d">%s</div></a>`,
			html.EscapeString(p.Slug), html.EscapeString(p.Title), html.EscapeString(p.Blurb))
	}
	b.WriteString(`</div>`)
	b.WriteString(`<p><span class="kicker">For agents</span><br>` +
		`Machine-readable references: <a href="/llms.txt">/llms.txt</a> (one-fetch plain-text) ` +
		`and <a href="/help.json">/help.json</a> (self-describing JSON).</p>`)

	writeHTML(w, s.pageHTML("docs · demiplane", topNav("/docs"), b.String()))
}

// handleDocsPage renders one embedded markdown doc through the M5 engine, wrapped
// in the docs chrome with a sub-nav across the curated pages.
func (s *Server) handleDocsPage(w http.ResponseWriter, r *http.Request) {
	page, ok := findDocPage(r.PathValue("page"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	src, err := demiplane.DocsFS.ReadFile(page.File)
	if err != nil {
		log.Printf("docs read %q failed: %s", page.File, logsafe.Err(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var b strings.Builder
	// Sub-nav across the curated docs (full-width pills, active highlighted).
	b.WriteString(`<p class="kicker">Documentation · `)
	for i, p := range docPages {
		if i > 0 {
			b.WriteString(" · ")
		}
		if p.Slug == page.Slug {
			fmt.Fprintf(&b, `<strong>%s</strong>`, html.EscapeString(p.Title))
		} else {
			fmt.Fprintf(&b, `<a href="/docs/%s">%s</a>`, html.EscapeString(p.Slug), html.EscapeString(p.Title))
		}
	}
	b.WriteString(`</p>`)
	b.WriteString(render.Body(stripLeadingHTML(src)))

	// The API reference is the target of the top-nav "API" link (/docs/api), so
	// mark that entry active there; every other curated doc keeps "Docs" active.
	active := "/docs"
	if page.Slug == "api" {
		active = "/docs/api"
	}
	writeHTML(w, s.pageHTML(page.Title+" · demiplane", topNav(active), b.String()))
}

// stripLeadingHTML drops the raw HTML block a doc opens with, if it opens with
// one. README.md starts with a GitHub-only <p><picture> logo; the markdown
// engine escapes raw HTML, so served as-is it would print the markup as text
// above the H1. A doc that opens with anything CommonMark does not treat as an
// HTML block, such as a <https://...> autolink or an inline tag followed by
// text, comes back unchanged.
func stripLeadingHTML(src []byte) []byte {
	norm := bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	lines := bytes.SplitAfter(bytes.ReplaceAll(norm, []byte("\r"), []byte("\n")), []byte("\n"))
	start := 0
	for start < len(lines) && isBlankLine(lines[start]) {
		start++
	}
	if start == len(lines) {
		return src
	}
	end, ok := htmlBlockEnd(bytes.TrimSuffix(lines[start], []byte("\n")))
	if !ok {
		return src
	}
	next := start + 1
	if end != nil {
		// The line holding the end marker, the start line included, is the
		// block's last line; with no such line the block runs to the end.
		for next = start; next < len(lines) && !end.Match(asciiLower(lines[next])); next++ {
		}
		next++
	} else {
		for next < len(lines) && !isBlankLine(lines[next]) {
			next++
		}
	}
	for next < len(lines) && isBlankLine(lines[next]) {
		next++
	}
	if next >= len(lines) {
		return nil // the doc is one HTML block: nothing to render
	}
	return bytes.Join(lines[next:], nil)
}

// isBlankLine reports whether line holds only spaces or tabs.
func isBlankLine(line []byte) bool {
	return len(bytes.Trim(line, " \t\n")) == 0
}

// htmlBlockEnd reports whether line opens an HTML block under one of the seven
// start conditions of CommonMark 0.31.2 §4.6 and returns the marker whose
// line ends it. A nil marker means the block ends at a blank line (kinds 6
// and 7). Kind 7 cannot interrupt a paragraph, which never matters here: the
// line checked is the doc's first non-blank line.
func htmlBlockEnd(line []byte) (end *regexp.Regexp, ok bool) {
	lower := asciiLower(line)
	for _, k := range htmlBlockKinds {
		subject := line
		if k.fold {
			subject = lower
		}
		if k.start.Match(subject) {
			return k.end, true
		}
	}
	m := htmlTagLine.FindSubmatch(line)
	if m == nil {
		return nil, false
	}
	// The spec bars these four names from kind 7 as open tags only; a
	// complete closing tag qualifies whatever its name.
	switch strings.ToLower(string(m[1])) {
	case "pre", "script", "style", "textarea":
		return nil, false
	}
	return nil, true
}

// asciiLower lowercases ASCII letters only. Tag names compare by ASCII case;
// regexp's (?i) folds Unicode too, so "<\u017fcript>" would pass as <script>.
func asciiLower(b []byte) []byte {
	return bytes.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, b)
}

// htmlBlockKinds are CommonMark's HTML block kinds 1 to 6, in spec order. Each
// start pattern allows the up to three spaces of indent a block may carry.
// Kinds 1 and 6 match tag names in any ASCII case (fold): their patterns are
// lowercase and see the line through asciiLower, as kind 1's end marker does.
var htmlBlockKinds = []struct {
	start, end *regexp.Regexp
	fold       bool
}{
	{regexp.MustCompile(`^ {0,3}<(?:pre|script|style|textarea)(?:[ \t>]|$)`),
		regexp.MustCompile(`</(?:pre|script|style|textarea)>`), true},
	{regexp.MustCompile(`^ {0,3}<!--`), regexp.MustCompile(`-->`), false},
	{regexp.MustCompile(`^ {0,3}<\?`), regexp.MustCompile(`\?>`), false},
	{regexp.MustCompile(`^ {0,3}<![A-Za-z]`), regexp.MustCompile(`>`), false},
	{regexp.MustCompile(`^ {0,3}<!\[CDATA\[`), regexp.MustCompile(`\]\]>`), false},
	{regexp.MustCompile(`^ {0,3}</?(?:` +
		`address|article|aside|base|basefont|blockquote|body|caption|center|` +
		`col|colgroup|dd|details|dialog|dir|div|dl|dt|fieldset|figcaption|` +
		`figure|footer|form|frame|frameset|h1|h2|h3|h4|h5|h6|head|header|hr|` +
		`html|iframe|legend|li|link|main|menu|menuitem|nav|noframes|ol|` +
		`optgroup|option|p|param|search|section|summary|table|tbody|td|` +
		`tfoot|th|thead|title|tr|track|ul` +
		`)(?:[ \t]|/?>|$)`), nil, true},
}

// htmlTagLine is kind 7: a line holding one complete open or closing tag
// (CommonMark's tag grammar) and nothing else but spaces or tabs. The tag
// name is captured so htmlBlockEnd can turn away the kind 1 names.
var htmlTagLine = regexp.MustCompile(`^ {0,3}(?:` +
	`<([A-Za-z][A-Za-z0-9-]*)` + // open tag name
	`(?:[ \t]+[A-Za-z_:][A-Za-z0-9_.:-]*` + // attribute name
	`(?:[ \t]*=[ \t]*(?:[^ \t\n"'=<>\x60]+|'[^'\n]*'|"[^"\n]*"))?)*` + // value
	`[ \t]*/?>` +
	`|</([A-Za-z][A-Za-z0-9-]*)[ \t]*>` + // closing tag
	`)[ \t]*$`)

// writeHTML writes an HTML response.
func writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(body))
}
