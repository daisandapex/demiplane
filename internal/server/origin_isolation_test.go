// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/store"
)

// newSplitServers builds the production topology (ADR 0003): a control listener
// (/publish, /list, DELETE) and a SEPARATE content listener (GET /{slug} only),
// both backed by one store. The content origin is pre-bound so the server can
// advertise artifact URLs on it. Returns both test servers.
func newSplitServers(t *testing.T, cfg Config) (control, content *httptest.Server) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// Pre-bind the content listener so its origin is known before constructing
	// the server, which must mint artifact URLs on that origin.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	cfg.ContentBaseURL = "http://" + ln.Addr().String()

	srv := New(st, cfg)
	content = &httptest.Server{Listener: ln, Config: &http.Server{Handler: srv.ContentHandler()}}
	content.Start()
	t.Cleanup(content.Close)
	control = httptest.NewServer(srv.ControlHandler())
	t.Cleanup(control.Close)
	return control, content
}

// TestContentOriginHasNoControlPlane is the core origin-isolation regression:
// hosted JS executing on the content origin must not be able to reach the
// control API (/publish, /list, DELETE) — they are not mounted there at all, so
// even a same-origin fetch from a malicious artifact gets nothing. This is what
// makes a published page's script harmless: its own origin has no API to abuse.
func TestContentOriginHasNoControlPlane(t *testing.T) {
	_, content := newSplitServers(t, Config{})

	mustNotServe := func(method, path string) {
		t.Helper()
		req, _ := http.NewRequest(method, content.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		// The control routes don't exist on the content origin. /publish and
		// /list resolve to the GET /{slug} catch-all (artifact lookup → 404);
		// the key property is they NEVER succeed (2xx).
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			t.Errorf("%s %s on content origin returned %d — control plane is reachable from hosted JS",
				method, path, resp.StatusCode)
		}
	}
	mustNotServe(http.MethodGet, "/list")
	mustNotServe(http.MethodPost, "/publish")
	mustNotServe(http.MethodDelete, "/some-slug")
}

// TestArtifactURLOnSeparateContentOrigin verifies /publish advertises the
// artifact on the content origin, not the control origin the request hit — so a
// link handed to a browser loads the page on the isolated origin.
func TestArtifactURLOnSeparateContentOrigin(t *testing.T) {
	control, content := newSplitServers(t, Config{})

	resp, err := http.Post(control.URL+"/publish", "text/html",
		strings.NewReader("<!DOCTYPE html><html><body>page</body></html>"))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	gotURL := strings.TrimSpace(string(b))

	if !strings.HasPrefix(gotURL, content.URL+"/") {
		t.Fatalf("artifact URL = %q, want content origin %q", gotURL, content.URL)
	}
	// And it actually resolves on the content origin.
	gr, gb := get(t, gotURL)
	if gr.StatusCode != http.StatusOK {
		t.Fatalf("GET artifact on content origin = %d, body=%q", gr.StatusCode, gb)
	}
}

// TestCrossOriginWriteGuard covers the second half of the fix (ADR 0003, P2):
// the same-origin policy blocks hosted JS from READING control responses, but a
// "simple" cross-origin POST still executes server-side unless the control plane
// rejects it. A worm artifact's fetch("/publish") from the content origin must
// be refused; legitimate same-origin and non-browser (curl) requests pass.
func TestCrossOriginWriteGuard(t *testing.T) {
	control, _ := newSplitServers(t, Config{})

	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"non-browser client (no fetch metadata)", nil, http.StatusCreated},
		{"same-origin browser write", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusCreated},
		{"top-level navigation", map[string]string{"Sec-Fetch-Site": "none"}, http.StatusCreated},
		{"same-site (other port = content origin)", map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"cross-site", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"foreign Origin, no Sec-Fetch", map[string]string{"Origin": "http://evil.example:9999"}, http.StatusForbidden},
		{"own Origin fallback", nil, http.StatusCreated}, // Origin == control host, set below
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, control.URL+"/publish",
				strings.NewReader("<h1>worm</h1>"))
			req.Header.Set("Content-Type", "text/html")
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			if tc.name == "own Origin fallback" {
				u, _ := url.Parse(control.URL)
				req.Header.Set("Origin", "http://"+u.Host)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST /publish: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// TestFilenameCannotRelabelNonHTMLToHTML is the served-response view of the
// interim defense-in-depth fix (ADR 0003, D5): an SVG/XML body coerced with
// ?filename=page.html must NOT come back as executable text/html. Combined with
// origin isolation this is belt-and-suspenders, but it independently closes the
// content-type-confusion vector (pentest XSS-VULN-02).
func TestFilenameCannotRelabelNonHTMLToHTML(t *testing.T) {
	control, _ := newSplitServers(t, Config{})

	svg := `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(document.domain)</script></svg>`
	resp, err := http.Post(control.URL+"/publish?filename=page.html", "image/svg+xml",
		strings.NewReader(svg))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	gotURL := strings.TrimSpace(string(b))

	ar, _ := get(t, gotURL)
	ct := ar.Header.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(ct), "text/html") {
		t.Fatalf("SVG body coerced to %q — content-type confusion not closed", ct)
	}
	// Whatever inert type it lands on, the active-document types must still carry
	// the no-script CSP (text/plain is non-executable as a page, which is also
	// safe). The point: the script never runs as same-origin HTML.
	if store.IsScriptableNonHTML(ct) &&
		!strings.Contains(ar.Header.Get("Content-Security-Policy"), "script-src 'none'") {
		t.Errorf("scriptable type %q served without script-src 'none' CSP (got %q)",
			ct, ar.Header.Get("Content-Security-Policy"))
	}
}

// TestUnsafeSameOriginStillCombined documents the escape hatch: the combined
// Handler serves both planes on one origin (the legacy footgun). The artifact
// URL is advertised on the request origin, and GET /{slug} resolves there.
func TestUnsafeSameOriginStillCombined(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler()) // ContentPort empty ⇒ same-origin
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/publish", "text/html",
		strings.NewReader("<!DOCTYPE html><html><body>x</body></html>"))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	gotURL := strings.TrimSpace(string(b))
	if !strings.HasPrefix(gotURL, ts.URL+"/") {
		t.Fatalf("same-origin artifact URL = %q, want prefix %q", gotURL, ts.URL)
	}
	if gr, _ := get(t, gotURL); gr.StatusCode != http.StatusOK {
		t.Fatalf("GET artifact (combined) = %d", gr.StatusCode)
	}
}
