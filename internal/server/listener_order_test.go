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
	"time"

	"github.com/daisandapex/demiplane/internal/limit"
)

// connGet sends a GET for path on c and reads one response.
func connGet(t *testing.T, c net.Conn, br *bufio.Reader, path string, hdr ...string) (*http.Response, error) {
	t.Helper()
	req := "GET " + path + " HTTP/1.1\r\nHost: " + c.RemoteAddr().String() + "\r\n"
	for _, h := range hdr {
		req += h + "\r\n"
	}
	if _, err := fmt.Fprint(c, req+"\r\n"); err != nil {
		return nil, err
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(br, nil)
	if err == nil {
		resp.Body.Close()
	}
	return resp, err
}

// TestListenerOrderRefusalsBeforeAdmission: path canonicalization and the
// service-worker refusal answer before admission, so they still answer when
// every admission slot is taken, while a request that would be routed gets
// the 503.
func TestListenerOrderRefusalsBeforeAdmission(t *testing.T) {
	s, _ := limitServer(t, Config{Requests: RequestLimits{MaxRequests: 1}})
	held, release := make(chan struct{}), make(chan struct{})
	h := s.wrapListener(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			close(held)
			<-release
		}
	}))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(release) })
	go func() {
		if resp, err := http.Get(ts.URL + "/hold"); err == nil {
			resp.Body.Close()
		}
	}()
	<-held

	for _, tc := range []struct {
		name, path string
		hdr        []string
		want       int
	}{
		{"routed request", "/ok", nil, http.StatusServiceUnavailable},
		{"non-canonical path", "/a/../ok", nil, http.StatusTemporaryRedirect},
		{"encoded separator", "/a%2Fb", nil, http.StatusBadRequest},
		{"service worker on a non-canonical path", "/a/../sw.js", []string{"Service-Worker: script"}, http.StatusOK},
		{"service worker on an ambiguous path", "/a%2Fsw.js", []string{"Service-Worker: script"}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := net.Dial("tcp", ts.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			resp, err := connGet(t, c, bufio.NewReader(c), tc.path, tc.hdr...)
			if err != nil {
				t.Fatalf("no response: %v", err)
			}
			if resp.StatusCode != tc.want {
				t.Errorf("status %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// TestListenerOrderRefusalHasBodyDeadline: a request refused before routing
// (here a non-canonical path) without its body being read is still under the
// body deadline. net/http drains an unread body before it answers, so without
// the deadline a client that stalls mid-body would hold the refusal and the
// connection open.
func TestListenerOrderRefusalHasBodyDeadline(t *testing.T) {
	s, _ := limitServer(t, Config{Requests: RequestLimits{BodyTimeout: 200 * time.Millisecond}})
	ts := httptest.NewServer(s.wrapListener(http.NotFoundHandler()))
	t.Cleanup(ts.Close)
	for _, p := range []string{"/a/../b", "/a%2Fb"} {
		c := rawPost(t, ts.Listener.Addr().String(), p, 100_000, func(c net.Conn) {
			_, _ = c.Write([]byte("only a little")) // then stall
		})
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		line, err := bufio.NewReader(c).ReadString('\n')
		c.Close()
		if err != nil {
			t.Fatalf("%s with a stalled body: no answer within 3s: %v", p, err)
		}
		if !strings.Contains(line, "307") && !strings.Contains(line, "400") {
			t.Fatalf("%s: answered %q", p, strings.TrimSpace(line))
		}
	}
}

// TestContentWriteGuardUsesContentOrigin: the content listener judges a write
// against the content origin as a full origin (scheme, host, port), both in
// the Origin fallback and in the opaque-origin Referer check a sandboxed
// page's form relies on.
func TestContentWriteGuardUsesContentOrigin(t *testing.T) {
	s, _ := limitServer(t, Config{
		BaseURL:        "https://ctl.example",
		ContentBaseURL: "https://content.example",
		ContentPort:    "8081",
	})
	h := s.guardContentWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for _, tc := range []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"content origin, no fetch metadata", map[string]string{"Origin": "https://content.example"}, http.StatusOK},
		{"control origin", map[string]string{"Origin": "https://ctl.example"}, http.StatusForbidden},
		{"content host over plain http", map[string]string{"Origin": "http://content.example"}, http.StatusForbidden},
		{"sandboxed page, no Referer", map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site"}, http.StatusOK},
		{"sandboxed page on the content origin", map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site",
			"Referer": "https://content.example/quiz"}, http.StatusOK},
		{"opaque origin, Referer on another scheme", map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site",
			"Referer": "http://content.example/quiz"}, http.StatusForbidden},
		{"opaque origin, Referer on another site", map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site",
			"Referer": "https://evil.example/"}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://content.example/answer/quiz", strings.NewReader("a=1"))
			for k, v := range tc.hdr {
				r.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// TestTrustedProxyClientIdentity: behind a --trusted-proxy, per-client
// admission charges the forwarded client, not the proxy; without one the
// forwarded header is ignored.
func TestTrustedProxyClientIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		proxies string
		// status of a second request from another forwarded client while the
		// first holds the only per-client slot
		otherClient int
	}{
		{"trusted proxy", "127.0.0.1", http.StatusOK},
		{"no trusted proxy", "", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := limit.ParseProxies(tc.proxies)
			if err != nil {
				t.Fatal(err)
			}
			s, _ := limitServer(t, Config{TrustedProxies: p, Requests: RequestLimits{MaxRequestsPerClient: 1}})
			held, release := make(chan struct{}), make(chan struct{})
			ts := httptest.NewServer(s.wrapListener(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/hold" {
					close(held)
					<-release
				}
			})))
			t.Cleanup(ts.Close)
			t.Cleanup(func() { close(release) })
			get := func(path, xff string) int {
				req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
				req.Header.Set("X-Forwarded-For", xff)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				return resp.StatusCode
			}
			go func() {
				req, _ := http.NewRequest(http.MethodGet, ts.URL+"/hold", nil)
				req.Header.Set("X-Forwarded-For", "203.0.113.1")
				if resp, err := http.DefaultClient.Do(req); err == nil {
					resp.Body.Close()
				}
			}()
			<-held
			if got := get("/ok", "203.0.113.2"); got != tc.otherClient {
				t.Errorf("another forwarded client: %d, want %d", got, tc.otherClient)
			}
			if tc.proxies != "" {
				if got := get("/ok", "203.0.113.1"); got != http.StatusServiceUnavailable {
					t.Errorf("same forwarded client: %d, want 503", got)
				}
			}
		})
	}
}
