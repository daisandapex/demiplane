// SPDX-FileCopyrightText: 2026 Benjamin Connelly
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http"

	demiplane "github.com/daisandapex/demiplane"
)

// favicon.go serves the brand icons a browser fetches by convention —
// /favicon.svg, /favicon.ico, /apple-touch-icon.png — from bytes embedded at the
// module root (demiplane.BrandFS). The bytes are the committed assets/brand/
// files verbatim, so the served icon can never drift from the source art.
//
// Origin placement (ADR 0003): these mount on the CONTROL seam only, so they are
// present on ControlHandler and the combined same-origin Handler. Registering on
// BOTH seams would double-register the same literal pattern on the combined mux
// (ServeMux panics on a duplicate pattern), and the content origin does not need
// a served favicon route: rendered artifacts carry a self-contained data: URI
// icon in their <head> (demiplane.FaviconDataURI), which works cross-origin and
// offline. The three names are reserved slugs (via the control seam's
// mountCoreControlRoutes) so no artifact can shadow them on the combined origin.
func init() {
	reg := func(mux *http.ServeMux, _ *Server) {
		mux.HandleFunc("GET /favicon.svg", serveBrandAsset("assets/brand/favicon.svg", "image/svg+xml"))
		mux.HandleFunc("GET /favicon.ico", serveBrandAsset("assets/brand/favicon.ico", "image/x-icon"))
		mux.HandleFunc("GET /apple-touch-icon.png", serveBrandAsset("assets/brand/apple-touch-icon.png", "image/png"))
	}
	registerCoreControlRoute([]string{"favicon.svg", "favicon.ico", "apple-touch-icon.png"}, reg)
}

// serveBrandAsset returns a handler that writes an embedded brand icon with a
// fixed content type. The bytes are read once at mount time; a missing embed is a
// build-time fault surfaced as 500 rather than a silent empty body. Icons are
// immutable per build, so they carry a day-long cacheable, immutable Cache-Control.
func serveBrandAsset(path, contentType string) http.HandlerFunc {
	body, err := demiplane.BrandFS.ReadFile(path)
	if err != nil || len(body) == 0 {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "icon unavailable", http.StatusInternalServerError)
		}
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
		w.Write(body)
	}
}

// faviconChromeLinks is the <head> icon block for the control-plane chrome (the
// landing, /docs, /gallery, /help, /connect, and the themed 404). It leads with
// the self-contained SVG data: URI (crisp at any size, theme-adaptive, no request)
// and adds the served raster fallbacks — /favicon.ico for clients that ignore
// rel=icon, and the apple-touch PNG for an iOS home-screen tile. These served
// paths resolve on the control origin, where favicon.go mounts them.
var faviconChromeLinks = `<link rel="icon" type="image/svg+xml" href="` + demiplane.FaviconDataURI + `">
<link rel="alternate icon" href="/favicon.ico" sizes="16x16 32x32 48x48">
<link rel="apple-touch-icon" href="/apple-touch-icon.png">
`
