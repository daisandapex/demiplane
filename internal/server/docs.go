// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"fmt"
	"html"
	"log"
	"net/http"
	"strings"

	demiplane "github.com/daisandapex/demiplane"
	"github.com/daisandapex/demiplane/internal/render"
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
		log.Printf("docs read %q failed: %v", page.File, err)
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
	b.WriteString(render.Body(src))

	// The API reference is the target of the top-nav "API" link (/docs/api), so
	// mark that entry active there; every other curated doc keeps "Docs" active.
	active := "/docs"
	if page.Slug == "api" {
		active = "/docs/api"
	}
	writeHTML(w, s.pageHTML(page.Title+" · demiplane", topNav(active), b.String()))
}

// writeHTML writes an HTML response.
func writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(body))
}
