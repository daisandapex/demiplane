// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

// TestThemedErrorEdges: the swapped page drops headers that described the
// handler's body, says Vary: Accept, honours q=0, and leaves a non-error
// plaintext response alone.
func TestThemedErrorEdges(t *testing.T) {
	control, content := newSplitServers(t, Config{AuthToken: "tok"})

	req, _ := http.NewRequest("POST", control.URL+"/publish?filename=a.bin", strings.NewReader("0123456789"))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	url := strings.TrimSpace(string(b))
	if !strings.HasPrefix(url, content.URL) {
		t.Fatalf("publish: %d %q", resp.StatusCode, b)
	}
	req, _ = http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Range", "bytes=999-")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("range error: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		t.Errorf("themed page kept the artifact's Content-Disposition %q", cd)
	}

	for _, accept := range []string{"text/html", ""} {
		req, _ = http.NewRequest("GET", control.URL+"/docs/nope", nil)
		req.Header.Set("Accept", accept)
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if !strings.Contains(resp.Header.Get("Vary"), "Accept") {
			t.Errorf("Accept %q: error response has no Vary: Accept", accept)
		}
	}

	resp, page := browse(t, "GET", control.URL+"/llms.txt")
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || strings.Contains(page, "nf-h") {
		t.Errorf("a 200 plaintext page was altered: %q", resp.Header.Get("Content-Type"))
	}

	for accept, want := range map[string]bool{
		"text/html": true, "text/html;q=0.5": true, "application/json, text/html;q=0": false,
		"application/json": false, "": false, "*/*": false,
	} {
		if got := acceptsHTML(accept); got != want {
			t.Errorf("acceptsHTML(%q) = %v, want %v", accept, got, want)
		}
	}
}

// failFlusher is a writer whose flush fails.
type failFlusher struct{ *httptest.ResponseRecorder }

var errFlush = errors.New("flush failed")

func (failFlusher) FlushError() error { return errFlush }

// TestErrorWriterSemantics: headers changed after the error was written do
// not reach the themed page, and a failed flush still reports its error.
func TestErrorWriterSemantics(t *testing.T) {
	s := &Server{}
	h := s.themedErrors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
		w.Header().Set("X-Late", "1")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept", "text/html")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || rec.Header().Get("X-Late") != "" {
		t.Errorf("late header leaked or status lost: %d %v", rec.Code, rec.Header())
	}

	var got error
	h = s.themedErrors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = http.NewResponseController(w).Flush()
	}))
	h.ServeHTTP(failFlusher{httptest.NewRecorder()}, httptest.NewRequest("GET", "/", nil))
	if !errors.Is(got, errFlush) {
		t.Errorf("flush error = %v, want %v", got, errFlush)
	}
}
