// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"html"
	"net/http"

	demiplane "github.com/daisandapex/demiplane/v3"
	"github.com/daisandapex/demiplane/v3/internal/render"
)

// notfound.go gives the CONTENT origin (where every published/bookmarked view
// URL points, ADR 0003) a themed 404 instead of Go's plaintext "404 page not
// found". A reader who follows a stale or expired link is demiplane's first-time
// visitor; a dead-end in Times New Roman reads as "the tool is broken." Expired
// and never-existed are indistinguishable at this layer (store.Get collapses a
// past-TTL artifact into ErrNotFound so existence is never leaked), so the copy
// covers both honestly: "doesn't exist or has expired."
//
// It is wired in two places on the ContentHandler mux: handleGet routes its
// ErrNotFound here, and GET /{$} (the bare content root, which has no landing on
// this plane) falls through here too.

// serveContentNotFound writes the themed 404 page with a 404 status.
func (s *Server) serveContentNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(s.notFoundHTML()))
}

// notFoundHTML builds the self-contained themed 404 document. It uses the
// operator pages' stylesheet, light/dark contract and toggle, under a site bar
// that holds only the name (linking to the index) and the toggle. The "back to the index" link
// targets the operator-advertised base URL when configured (the control plane /
// landing lives on a different origin than this content plane), else the origin
// root as a best-effort fallback.
func (s *Server) notFoundHTML() string {
	return s.statusPage("Not found", `<p class="nf-code">404 · Not Found</p>
<h1 class="nf-h">This document doesn't exist or has expired</h1>
<p class="nf-p">The link may be mistyped, or the artifact reached its expiry and was
removed. Nothing here is stored forever by design.</p>
`)
}

// statusPage wraps inner (trusted HTML: a heading and a line or two) in the
// self-contained themed status document shared by the 404 and the error pages
// (errorpage.go), with title in <head> and the link back to the index.
func (s *Server) statusPage(title, inner string) string {
	index := s.baseURL
	if index == "" {
		index = "/"
	}
	tag := `<html lang="en">`
	if s.renderTheme != "" {
		tag = `<html lang="en" data-theme="` + html.EscapeString(s.renderTheme) + `">`
	}
	head, foot, tools := "", "", ""
	if s.chromeToggle() {
		head = render.ThemeInitScript() + "\n"
		foot = render.FrameScripts()
		tools = render.ToggleButton(s.renderTheme)
	}
	return `<!DOCTYPE html>
` + tag + `
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + html.EscapeString(title) + ` · demiplane</title>
<link rel="icon" type="image/svg+xml" href="` + demiplane.FaviconDataURI + `">
<style>` + s.chromeStyle() + `</style>
` + head + `</head>
<body>
<header class="sitehead"><div class="sitebar"><a class="brand" href="` + html.EscapeString(index) + `">demiplane</a>` + tools + `</div></header>
<div class="frame">
<main class="doc">
` + inner + `<p><a class="nf-back" href="` + html.EscapeString(index) + `">Back to the index &rarr;</a></p>
</main>
</div>
` + foot + `</body>
</html>
`
}
