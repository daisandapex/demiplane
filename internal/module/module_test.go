// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package module

import (
	"net/http"
	"testing"
)

// fakeRoute implements RouteModule; a plain fakeModule implements only Module.
// Together they exercise the registry's interface-classification logic.
type fakeRoute struct{}

func (fakeRoute) Name() string                { return "fake-route" }
func (fakeRoute) Reserved() []string          { return []string{"fake"} }
func (fakeRoute) Routes(*http.ServeMux, Host) {}

type fakeModule struct{}

func (fakeModule) Name() string { return "fake-plain" }

func TestRegistryClassification(t *testing.T) {
	before := len(All())
	Register(fakeRoute{})
	Register(fakeModule{})

	if got := len(All()); got != before+2 {
		t.Fatalf("All() len = %d, want %d", got, before+2)
	}

	var foundRoute bool
	for _, rm := range RouteModules() {
		if rm.Name() == "fake-route" {
			foundRoute = true
		}
	}
	if !foundRoute {
		t.Error("fake-route not classified as a RouteModule")
	}

	// A plain Module must NOT be classified as a RouteModule.
	for _, rm := range RouteModules() {
		if rm.Name() == "fake-plain" {
			t.Error("fake-plain (plain Module) wrongly classified as a RouteModule")
		}
	}
}
