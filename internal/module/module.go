// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package module is demiplane's compile-time extension seam. It defines the
// narrow capability surface (Host) that core exposes to optional modules, the
// module interfaces themselves, and a Caddy-style global registry that each
// module populates from its init(). The binary's module set is chosen by which
// module packages are imported (gated with build tags in cmd/demiplane), so the
// default core stays tiny: opt-in capabilities are not compiled in at all.
//
// See docs/adr/0001-module-extension-pattern.md for the rationale and
// docs/MODULES.md for the developer guide.
package module

import (
	"net/http"

	"github.com/daisandapex/demiplane/internal/store"
)

// Host is the capability surface core hands to a module at startup. It is an
// interface (not the concrete *server.Server) so a module can reach only these
// five methods — the entire blast radius of any module is reviewing its use of
// this surface. Implemented by *server.Server.
type Host interface {
	// Store is the shared artifact content store.
	Store() *store.Store
	// RequireAuth wraps a handler with core's bearer-token publish-auth layer, so
	// a module's privileged routes share one consistent auth posture.
	RequireAuth(http.HandlerFunc) http.HandlerFunc
	// RequestBase returns the advertised base URL for the request (no trailing
	// slash), honouring --base-url or deriving scheme+host from the request.
	RequestBase(*http.Request) string
	// BaseURL returns the statically configured --base-url ("" if unset).
	BaseURL() string
	// ModuleDataDir returns (creating if needed) a storage directory private to
	// the named module, under the store root. Modules that need their own
	// persistence (e.g. the reply module's SQLite db) live here, isolated from
	// the artifact blobs and from each other.
	ModuleDataDir(name string) (string, error)
}

// Module is the base every module implements. Name must be a short, stable,
// filesystem-safe identifier (it doubles as the ModuleDataDir key).
type Module interface {
	Name() string
}

// RouteModule adds HTTP routes to the server. The reply endpoint is the
// canonical example.
type RouteModule interface {
	Module
	// Reserved returns top-level path segments this module owns (e.g. "replies").
	// Core registers them as reserved slugs so an artifact can never shadow a
	// module route — the same invariant core holds for "publish"/"list"/"docs".
	// Return nil if the module mounts only sub-paths of an existing segment.
	Reserved() []string
	// Routes mounts the module's handlers on the CONTROL plane mux. Use
	// host.RequireAuth for any privileged route so auth stays consistent with core.
	Routes(mux *http.ServeMux, host Host)
}

// ContentRouteModule is an OPTIONAL capability a RouteModule may also implement
// to mount handlers on the CONTENT origin (where artifact bodies are served),
// not just the control plane. The reply module implements it so a rendered
// page's inline reply box can POST *same-origin* to the content plane — a plain
// form post with no cross-origin/mixed-content failure mode — while its
// privileged agent read API stays isolated on the control plane (ADR 0003).
type ContentRouteModule interface {
	RouteModule
	// ContentReserved returns top-level segments the module owns on the content
	// origin (registered as reserved slugs). Return nil when it mounts only
	// method-specific dynamic routes that cannot shadow a flat GET /{slug}.
	ContentReserved() []string
	// ContentRoutes mounts the module's content-origin handlers on mux. Called
	// once per content handler build, on the same module instance as Routes.
	ContentRoutes(mux *http.ServeMux, host Host)
}

// registry is the package-global set of compiled-in modules, populated by
// Register from each module's init(). Mutated only during package
// initialisation (single-threaded), read once at server startup.
var registry []Module

// Register adds a module to the registry. Call it from a module package's
// init(); the module is then included in the binary iff that package is imported
// (see cmd/demiplane). Duplicate names are the caller's responsibility — core
// mounts whatever is registered.
func Register(m Module) { registry = append(registry, m) }

// All returns the registered modules in registration order. Core calls this once
// at startup to collect the route modules it mounts.
func All() []Module { return registry }

// RouteModules returns the registered modules that add HTTP routes.
func RouteModules() []RouteModule {
	var out []RouteModule
	for _, m := range registry {
		if rm, ok := m.(RouteModule); ok {
			out = append(out, rm)
		}
	}
	return out
}

// ContentRouteModules returns the registered modules that also add content-origin
// routes. Core calls this once at startup to collect the modules ContentHandler
// (and the combined same-origin Handler) must mount.
func ContentRouteModules() []ContentRouteModule {
	var out []ContentRouteModule
	for _, m := range registry {
		if rm, ok := m.(ContentRouteModule); ok {
			out = append(out, rm)
		}
	}
	return out
}
