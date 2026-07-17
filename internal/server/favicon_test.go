// SPDX-FileCopyrightText: 2026 Benjamin Connelly
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/store"
)

// TestBrandIconsServed verifies the three convention-named icon routes serve
// their embedded bytes with the right content type on the control plane (and the
// combined Handler that newTestServer builds).
func TestBrandIconsServed(t *testing.T) {
	ts := newTestServer(t, "")

	cases := []struct {
		path        string
		contentType string
		magic       []byte // leading bytes that identify the format
	}{
		{"/favicon.svg", "image/svg+xml", []byte("<svg")},
		{"/favicon.ico", "image/x-icon", []byte{0x00, 0x00, 0x01, 0x00}},
		{"/apple-touch-icon.png", "image/png", []byte{0x89, 'P', 'N', 'G'}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, body := get(t, ts.URL+tc.path)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", tc.path, resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); ct != tc.contentType {
				t.Errorf("Content-Type = %q, want %q", ct, tc.contentType)
			}
			if len(body) == 0 {
				t.Fatalf("empty body for %s", tc.path)
			}
			if !bytes.HasPrefix(body, tc.magic) {
				t.Errorf("%s body does not start with expected magic %v (got %v)",
					tc.path, tc.magic, body[:min(len(body), len(tc.magic))])
			}
			if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
				t.Errorf("Cache-Control = %q, want immutable", cc)
			}
		})
	}
}

// TestBrandIconSlugsReserved verifies an artifact cannot be published under one
// of the icon route names — otherwise, on the combined origin, the literal route
// would shadow the artifact (and confuse a reader who bookmarked it).
func TestBrandIconSlugsReserved(t *testing.T) {
	ts := newTestServer(t, "")
	for _, slug := range []string{"favicon.svg", "favicon.ico", "apple-touch-icon.png"} {
		resp, body := post(t, ts.URL+"/publish?slug="+slug, "x")
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("publish ?slug=%s = %d, want 400 (reserved); body=%q",
				slug, resp.StatusCode, body)
		}
		// The store's own validator must agree, independent of the HTTP layer.
		if err := store.ValidateNamedSlug(slug); err == nil {
			t.Errorf("ValidateNamedSlug(%q) = nil, want reserved error", slug)
		}
	}
}

// TestContentOriginHasNoFaviconRoute documents the origin decision (ADR 0003):
// icon routes live on the control plane only. On the isolated content origin the
// path falls through to the artifact catch-all and 404s — rendered artifacts
// carry their own self-contained data: URI icon instead, so no served route is
// needed there.
func TestContentOriginHasNoFaviconRoute(t *testing.T) {
	_, content := newSplitServers(t, Config{})
	resp, _ := get(t, content.URL+"/favicon.svg")
	if resp.StatusCode == http.StatusOK {
		t.Errorf("content origin served /favicon.svg (200) — expected fall-through 404")
	}
}

func post(t *testing.T, url, body string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/octet-stream", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}
