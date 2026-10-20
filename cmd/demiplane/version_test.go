// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	installed := &debug.BuildInfo{Main: debug.Module{Version: "v3.1.0"}}
	devel := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
	for _, c := range []struct {
		name, in string
		bi       *debug.BuildInfo
		want     string
	}{
		{"go install reports the module version", baselineVersion, installed, "v3.1.0"},
		{"an ldflags stamp wins", "v3.1.0-pre+abc1234", installed, "v3.1.0-pre+abc1234"},
		{"a checkout build keeps the baseline", baselineVersion, devel, baselineVersion},
		{"no build info keeps the baseline", baselineVersion, nil, baselineVersion},
	} {
		if got := resolveVersion(c.in, c.bi); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
