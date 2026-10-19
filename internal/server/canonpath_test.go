// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// rawGet sends target verbatim as the request-target (no client-side
// normalization, unlike net/http's client) and returns the response.
func rawGet(t *testing.T, ts *httptest.Server, target string) *http.Response {
	t.Helper()
	c, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target, ts.Listener.Addr())
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	resp.Body.Close()
	return resp
}

// TestCanonicalPathRedirects: a non-canonical path is redirected to its
// canonical form with the escaping it arrived with, never re-escaped
// ("%61" must not become "%2561"), and never to a network-path reference.
// Every handler (control, content, combined) gets the same treatment.
func TestCanonicalPathRedirects(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := New(st, Config{ContentPort: "1"})
	handlers := map[string]http.Handler{
		"control":  srv.ControlHandler(),
		"content":  srv.ContentHandler(),
		"combined": srv.Handler(),
	}
	cases := []struct{ target, loc string }{
		{"/site//%61", "/site/%61"},
		{"//%72eport", "/%72eport"},
		{"/a/./b", "/a/b"},
		{"/a/../b", "/b"},
		{"/x/..", "/"},
		{"/a//b?q=1", "/a/b?q=1"},
		{"//evil.example", "/evil.example"},
		{"///evil.example/x", "/evil.example/x"},
		{"/site//index.html/", "/site/index.html/"},
	}
	for name, h := range handlers {
		ts := httptest.NewServer(h)
		t.Cleanup(ts.Close)
		for _, c := range cases {
			resp := rawGet(t, ts, c.target)
			loc := resp.Header.Get("Location")
			if resp.StatusCode != http.StatusTemporaryRedirect || loc != c.loc {
				t.Errorf("%s: GET %s = %d Location %q, want 307 %q", name, c.target, resp.StatusCode, loc, c.loc)
			}
			if strings.Contains(loc, "%25") {
				t.Errorf("%s: GET %s: Location %q re-escaped a percent", name, c.target, loc)
			}
			if strings.HasPrefix(loc, "//") || strings.HasPrefix(loc, `/\`) {
				t.Errorf("%s: GET %s: Location %q is a network-path reference", name, c.target, loc)
			}
		}
	}
}

// TestAmbiguousPathRejected: encoded dot segments, encoded or literal
// separators inside a segment, and control characters have no canonical
// meaning for a slug or a site path, so they are refused before routing
// instead of reaching a handler as "../b" or "a/".
func TestAmbiguousPathRejected(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := New(st, Config{ContentPort: "1"})
	for name, h := range map[string]http.Handler{"control": srv.ControlHandler(), "content": srv.ContentHandler(), "combined": srv.Handler()} {
		ts := httptest.NewServer(h)
		t.Cleanup(ts.Close)
		for _, target := range []string{
			"/a/%2e%2e/b", "/a/%2E/b", "/%2e%2e", "/a%2F/b", "/%2F%2Fevil", "/%2f", "/%5Cevil",
			`/\evil`, "/a%00b", "/a%0d%0aSet-Cookie:x", "/a%zz",
		} {
			resp := rawGet(t, ts, target)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s: GET %s = %d Location %q, want 400", name, target, resp.StatusCode, resp.Header.Get("Location"))
			}
		}
	}
}

// TestCanonicalPathsPassThrough: ordinary paths, including legitimate
// percent-escapes, reach the handlers untouched.
func TestCanonicalPathsPassThrough(t *testing.T) {
	ts := newTestServer(t, "")
	url := publish(t, ts, "?slug=hello", "<p>hi</p>")
	for _, target := range []string{"/hello", "/%68ello", "/hello?x=%2F"} {
		resp := rawGet(t, ts, target)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (%s)", target, resp.StatusCode, url)
		}
	}
	if resp := rawGet(t, ts, "/"); resp.StatusCode != http.StatusOK {
		t.Errorf("GET / = %d, want 200", resp.StatusCode)
	}
}
