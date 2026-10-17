// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"

	"github.com/daisandapex/demiplane/internal/store"
)

// coreRouteNames are the top-level names the HTTP planes route in every build.
// Listed by hand, not derived from the route registry, so a name that stops
// being reserved fails here.
var coreRouteNames = []string{
	"publish", "list", "docs", "help", "help.json", "llms.txt",
	"gallery", "connect", "rerender", "_events",
	"favicon.svg", "favicon.ico", "apple-touch-icon.png",
}

// TestRouteNamesReservedWithoutMountingHandlers runs in a process that never
// builds an HTTP handler, which is what `demiplane receive` is. Every routed
// name must already be refused as an artifact slug, or SSH ingest could create
// an artifact that a route shadows.
func TestRouteNamesReservedWithoutMountingHandlers(t *testing.T) {
	for _, name := range append(append([]string{}, coreRouteNames...), moduleRouteNames...) {
		if err := store.ValidateNamedSlug(name); err == nil {
			t.Errorf("slug %q is accepted, but it is a route name", name)
		}
	}
}
