// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// browse sends a request the way a browser navigating to url would.
func browse(t *testing.T, method, url string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// TestErrorsAreThemedForBrowsers: a browser that hits an error gets the house
// page with the status, the server's reason and a way back, not Go's
// plaintext. A client that does not ask for HTML still gets the plaintext.
func TestErrorsAreThemedForBrowsers(t *testing.T) {
	control, content := newSplitServers(t, Config{AuthToken: "tok"})
	cases := []struct {
		name, method, url string
		code              int
	}{
		{"control 404", "GET", control.URL + "/docs/nope", http.StatusNotFound},
		{"control 405", "PUT", control.URL + "/docs", http.StatusMethodNotAllowed},
		{"control 401", "GET", control.URL + "/list", http.StatusUnauthorized},
		{"site 404", "GET", content.URL + "/nosite/", http.StatusNotFound},
	}
	for _, c := range cases {
		resp, page := browse(t, c.method, c.url)
		if resp.StatusCode != c.code {
			t.Errorf("%s: status %d, want %d", c.name, resp.StatusCode, c.code)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: content-type %q, want text/html:\n%s", c.name, ct, page)
			continue
		}
		if !strings.Contains(page, "var(--accent)") || !strings.Contains(page, `class="nf-h"`) {
			t.Errorf("%s: error page is not themed:\n%s", c.name, page)
		}
		if !strings.Contains(page, http.StatusText(c.code)) {
			t.Errorf("%s: page does not name the status:\n%s", c.name, page)
		}
	}

	// Not a browser: the plaintext answer is unchanged.
	resp, body := get(t, control.URL+"/docs/nope")
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") ||
		strings.TrimSpace(string(body)) != "404 page not found" {
		t.Errorf("non-browser 404 changed: %q %q", ct, body)
	}
}

// TestThemedErrorEscapesReason: the server's reason is shown as text.
func TestThemedErrorEscapesReason(t *testing.T) {
	page := (&Server{}).errorPageHTML(http.StatusBadRequest, "<b>bad</b>")
	if strings.Contains(page, "<b>bad</b>") || !strings.Contains(page, "&lt;b&gt;bad&lt;/b&gt;") {
		t.Errorf("reason not escaped:\n%s", page)
	}
}
