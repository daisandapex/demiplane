// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestContentPlaneThemedNotFound: an unknown or expired slug and the bare content
// root must return a themed 404 (demiplane chrome + an honest message + a way
// back), not Go's plaintext "404 page not found". The reading plane must never
// dead-end a reader who followed a stale link.
func TestContentPlaneThemedNotFound(t *testing.T) {
	_, content := newSplitServers(t, Config{BaseURL: "http://idx.example:8891"})

	for _, path := range []string{"/does-not-exist", "/"} {
		resp, body := get(t, content.URL+path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s content-type = %q, want text/html", path, ct)
		}
		page := string(body)
		if strings.Contains(page, "404 page not found") {
			t.Errorf("GET %s served Go's plaintext 404:\n%s", path, page)
		}
		if !strings.Contains(page, "doesn't exist or has expired") {
			t.Errorf("GET %s missing the themed 404 copy:\n%s", path, page)
		}
		// Themed: carries the house tokens and a way back to the index.
		if !strings.Contains(page, "var(--accent)") {
			t.Errorf("GET %s 404 is not themed (no design tokens):\n%s", path, page)
		}
		if !strings.Contains(page, `href="http://idx.example:8891"`) {
			t.Errorf("GET %s 404 missing the index link:\n%s", path, page)
		}
	}

	// A real artifact on the content plane still serves (the catch-all 404 does
	// not shadow GET /{slug}).
	control, content2 := newSplitServers(t, Config{})
	url := publish(t, control, "?slug=lives", "<html>ok</html>")
	if !strings.HasPrefix(url, content2.URL+"/") {
		t.Fatalf("published URL %q not on content origin %q", url, content2.URL)
	}
	if resp, body := get(t, url); resp.StatusCode != http.StatusOK || string(body) != "<html>ok</html>" {
		t.Errorf("valid slug: status=%d body=%q", resp.StatusCode, body)
	}
}

// TestLandingArtifactLinksUseContentOrigin guards the P0 fix: the landing's
// "Published" table must link artifacts on the CONTENT origin, not the control
// origin (which has no GET /{slug} and returned 405). One canonical URL per
// artifact, on the content plane.
func TestLandingArtifactLinksUseContentOrigin(t *testing.T) {
	control, content := newSplitServers(t, Config{Browse: true})
	publish(t, control, "?slug=public-doc", "<html>a</html>")

	resp, body := get(t, control.URL+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("landing status = %d", resp.StatusCode)
	}
	page := string(body)
	wantHref := `href="` + content.URL + `/public-doc"`
	if !strings.Contains(page, wantHref) {
		t.Errorf("landing table missing content-origin link %q:\n%s", wantHref, page)
	}
	badHref := `href="` + control.URL + `/public-doc"`
	if strings.Contains(page, badHref) {
		t.Errorf("landing table still links the control origin (the 405 bug): %q", badHref)
	}
	// Real table structure: a <thead> so header styling applies, and a link on to
	// the full gallery.
	if !strings.Contains(page, "<thead>") || !strings.Contains(page, "<tbody>") {
		t.Errorf("landing table missing thead/tbody:\n%s", page)
	}
	if !strings.Contains(page, `href="/gallery"`) {
		t.Errorf("landing table missing the link to /gallery:\n%s", page)
	}
}
