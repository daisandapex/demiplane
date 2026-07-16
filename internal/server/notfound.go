// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"html"
	"net/http"

	"github.com/daisandapex/demiplane/internal/theme"
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
// configured render theme (the default house style when unset) as a single fixed
// palette (no toggle: a 404 needs no chrome script). The "back to the index" link
// targets the operator-advertised base URL when configured (the control plane /
// landing lives on a different origin than this content plane), else the origin
// root as a best-effort fallback.
func (s *Server) notFoundHTML() string {
	css := theme.CSS(s.renderTheme)
	initial := s.renderTheme
	if initial == "" {
		initial = theme.Default
	}
	index := s.baseURL
	if index == "" {
		index = "/"
	}
	return `<!DOCTYPE html>
<html lang="en" data-theme="` + html.EscapeString(initial) + `">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Not found · demiplane</title>
<style>` + css + `
main.wrap{max-width:40rem;padding-top:4rem}
.nf-kicker{font-family:var(--sans);font-size:.72rem;font-weight:700;letter-spacing:.2em;
  text-transform:uppercase;color:var(--accent)}
.nf-h{font-family:var(--serif);font-size:2rem;font-weight:600;color:var(--ink);
  line-height:1.2;letter-spacing:-.012em;margin:.4rem 0 .6rem}
.nf-p{color:var(--muted);margin:0 0 1.4em}
.nf-back{font-weight:600}
</style>
</head>
<body>
<main class="wrap">
<span class="nf-kicker">demiplane</span>
<h1 class="nf-h">This document doesn't exist or has expired</h1>
<p class="nf-p">The link may be mistyped, or the artifact reached its expiry and was
removed. Nothing here is stored forever by design.</p>
<p><a class="nf-back" href="` + html.EscapeString(index) + `">Back to the index &rarr;</a></p>
</main>
</body>
</html>
`
}
