// SPDX-FileCopyrightText: 2026 Benjamin Connelly
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"strings"
	"testing"

	demiplane "github.com/daisandapex/demiplane"
)

// TestRenderedDocHasSelfContainedFavicon: every rendered artifact must carry a
// favicon in its <head>, and it must be the self-contained data: URI (not a
// served /favicon path) so a published page keeps its tab icon when saved and
// opened offline or served from the isolated content origin.
func TestRenderedDocHasSelfContainedFavicon(t *testing.T) {
	out := string(Markdown([]byte("# Title\n\nbody"), Options{}))

	if !strings.Contains(out, `rel="icon"`) {
		t.Fatalf("rendered doc has no favicon link:\n%s", out)
	}
	if !strings.Contains(out, `href="data:image/svg+xml;base64,`) {
		t.Errorf("favicon is not a self-contained data: URI (a served path would break offline)")
	}
	if !strings.Contains(out, demiplane.FaviconDataURI) {
		t.Errorf("favicon link does not match demiplane.FaviconDataURI (icon drifted from source)")
	}
}
