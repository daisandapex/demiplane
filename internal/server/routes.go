// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http"

	"github.com/daisandapex/demiplane/internal/store"
)

// This file is the first-party (core, non-module) route seam. It lets a feature
// that ships in the DEFAULT build add HTTP routes by owning a single file that
// appends a registration from its init() — without editing server.go's handler
// wiring. It mirrors the module.RouteModule mechanism (internal/module) but for
// core features that must not be gated behind a build tag.
//
// Two planes, matching the origin-isolation split (ADR 0003):
//   - coreControlRoutes mount on the control plane (ControlHandler + the combined
//     Handler), alongside /publish, /list, DELETE, /, /docs, /llms.txt, /help.
//   - coreContentRoutes mount on the content origin (ContentHandler + the combined
//     Handler), alongside the GET /{slug} artifact catch-all.
//
// A feature file registers with registerCoreControlRoute / registerCoreContentRoute
// from its init(); registerControlRoutes, ContentHandler, and Handler mount the
// collected routes. Reserved top-level segments are registered as reserved slugs
// so an artifact can never shadow a core route (same invariant core holds for its
// own literal paths and for module routes).

// coreRoute is one feature-contributed route group.
type coreRoute struct {
	// reserved lists top-level path segments this route owns (e.g. "connect").
	// They are registered as reserved slugs at mount time. Pass nil for routes
	// on dynamic path shapes that cannot shadow a flat slug (e.g. the SSE
	// GET /{slug}/_events sub-path or GET /{site}/{path...} multi-segment paths).
	reserved []string
	// register mounts the feature's handlers on mux. It receives *Server for the
	// store, chrome helpers, and config.
	register func(mux *http.ServeMux, s *Server)
}

// coreControlRoutes and coreContentRoutes are populated once, at package init,
// by feature files calling the register* helpers below. They are read (never
// mutated) when a handler mux is built, so no locking is needed.
var (
	coreControlRoutes []coreRoute
	coreContentRoutes []coreRoute
)

// registerCoreControlRoute is the seam a CONTROL-PLANE feature calls from its
// init() (e.g. connect.go, gallery.go). reserved names it owns are reserved as
// slugs at mount time.
func registerCoreControlRoute(reserved []string, register func(mux *http.ServeMux, s *Server)) {
	coreControlRoutes = append(coreControlRoutes, coreRoute{reserved: reserved, register: register})
}

// registerCoreContentRoute is the seam a CONTENT-ORIGIN feature calls from its
// init() (e.g. live.go, site.go).
func registerCoreContentRoute(reserved []string, register func(mux *http.ServeMux, s *Server)) {
	coreContentRoutes = append(coreContentRoutes, coreRoute{reserved: reserved, register: register})
}

// mountCoreControlRoutes mounts every registered control route onto mux and
// reserves the slugs they own. Called by registerControlRoutes (shared by the
// split ControlHandler and the combined Handler).
func (s *Server) mountCoreControlRoutes(mux *http.ServeMux) {
	for _, rt := range coreControlRoutes {
		store.AddReservedSlugs(rt.reserved...)
		rt.register(mux, s)
	}
}

// mountCoreContentRoutes mounts every registered content route onto mux and
// reserves the slugs they own. Called by ContentHandler and the combined Handler.
func (s *Server) mountCoreContentRoutes(mux *http.ServeMux) {
	for _, rt := range coreContentRoutes {
		store.AddReservedSlugs(rt.reserved...)
		rt.register(mux, s)
	}
}

// liveView is the ?live live-preview hook installed by live.go (feature T2.1).
// When non-nil, handleGet calls it first; it returns true if it fully served the
// request (a ?live-wrapped view) and false to fall through to normal artifact
// serving. Nil (default build without the wiring) means every GET serves the
// stored bytes verbatim — the byte-identical promise for non-live views.
var liveView func(s *Server, w http.ResponseWriter, r *http.Request) bool

// publishSite is the multi-file / directory publish hook installed by site.go
// (feature T2.2). When non-nil, handlePublish delegates POST /publish?site=<name>
// to it. Nil means the feature is not wired in and ?site= returns 501.
var publishSite func(s *Server, w http.ResponseWriter, r *http.Request)
