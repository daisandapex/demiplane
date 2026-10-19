// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

func hostPolicyServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, cfg)
}

func serveHost(h http.Handler, method, target, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestAllowedHostsPolicy: with an accepted-host list configured, a request
// naming any other Host is refused (421) on both planes before any handler
// runs, so a forged Host can never shape an advertised URL. The base URL
// hosts are always accepted.
func TestAllowedHostsPolicy(t *testing.T) {
	srv := hostPolicyServer(t, Config{
		BaseURL:        "https://demi.example",
		ContentBaseURL: "https://content.example",
		ContentPort:    "8081",
		AllowedHosts:   []string{"mesh.lan", "10.0.0.5:8080", "[fd00::1]"},
	})
	cases := []struct {
		host string
		ok   bool
	}{
		{"mesh.lan", true},
		{"mesh.lan:8080", true},
		{"MESH.lan", true},
		{"10.0.0.5:8080", true},
		{"10.0.0.5:9090", false},
		{"10.0.0.5", false},
		{"[fd00::1]:8080", true},
		{"demi.example", true},
		{"demi.example:8081", true}, // the content origin derived from --base-url
		{"content.example:443", true},
		{"content.example", true},
		{"evil.example", false},
		{"mesh.lan.evil.example", false},
		{"", false},
	}
	for name, h := range map[string]http.Handler{"control": srv.ControlHandler(), "content": srv.ContentHandler(), "combined": srv.Handler()} {
		for _, c := range cases {
			rec := serveHost(h, http.MethodGet, "/help.json", c.host)
			refused := rec.Code == http.StatusMisdirectedRequest
			if refused == c.ok {
				t.Errorf("%s: Host %q: status %d, want accepted=%v", name, c.host, rec.Code, c.ok)
			}
		}
	}
}

// TestNoHostPolicyAcceptsAnyHost: without a list the behavior is unchanged.
func TestNoHostPolicyAcceptsAnyHost(t *testing.T) {
	h := hostPolicyServer(t, Config{}).ControlHandler()
	if rec := serveHost(h, http.MethodGet, "/help.json", "anything.example"); rec.Code != http.StatusOK {
		t.Errorf("no policy: status %d, want 200", rec.Code)
	}
}

// TestContentURLFollowsBaseURL: with --base-url set and no --content-base-url,
// the advertised content origin is derived from the base URL, not the request
// Host, so the Host header no longer reaches any advertised URL.
func TestContentURLFollowsBaseURL(t *testing.T) {
	h := hostPolicyServer(t, Config{BaseURL: "https://demi.example", ContentPort: "8081"}).ControlHandler()
	req := httptest.NewRequest(http.MethodPost, "/publish?slug=page", strings.NewReader("<p>x</p>"))
	req.Host = "evil.example:8080"
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish = %d: %s", rec.Code, rec.Body)
	}
	var out struct{ URL string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.URL != "https://demi.example:8081/page" {
		t.Errorf("content URL = %q, want https://demi.example:8081/page", out.URL)
	}
}

func TestParseHostList(t *testing.T) {
	got, err := ParseHostList(" mesh.lan, 10.0.0.5:8080 ,[fd00::1],")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "mesh.lan|10.0.0.5:8080|[fd00::1]" {
		t.Errorf("ParseHostList = %q", got)
	}
	if got, err := ParseHostList(""); err != nil || got != nil {
		t.Errorf("empty list = %q, %v; want nil, nil", got, err)
	}
	for _, bad := range []string{"http://mesh.lan", "mesh.lan/x", "user@mesh.lan", "mesh lan", "mesh.lan:port", "*.example"} {
		if _, err := ParseHostList(bad); err == nil {
			t.Errorf("ParseHostList(%q) accepted an invalid host", bad)
		}
	}
}

// TestAllowedHostsPortFollowsScheme: a bare Host means the scheme's default
// port, so an explicit host:443 rule does not accept plain HTTP, nor host:80
// TLS.
func TestAllowedHostsPortFollowsScheme(t *testing.T) {
	rules := buildHostRules([]string{"mesh.lan:443", "lan.box:80"})
	cases := []struct {
		host string
		tls  bool
		ok   bool
	}{
		{"mesh.lan", true, true},
		{"mesh.lan", false, false},
		{"mesh.lan:443", false, true},
		{"lan.box", false, true},
		{"lan.box", true, false},
	}
	for _, c := range cases {
		if got := hostAllowed(rules, c.host, c.tls); got != c.ok {
			t.Errorf("Host %q tls=%v: allowed=%v, want %v", c.host, c.tls, got, c.ok)
		}
	}
}

// TestAllowedHostsServeDerivedContentURL: the content URL a publish
// advertises (derived from --base-url) must itself pass the host policy.
func TestAllowedHostsServeDerivedContentURL(t *testing.T) {
	srv := hostPolicyServer(t, Config{BaseURL: "https://demi.example", ContentPort: "8081", AllowedHosts: []string{"mesh.lan"}})
	req := httptest.NewRequest(http.MethodPost, "/publish?slug=page", strings.NewReader("<p>x</p>"))
	req.Host = "demi.example"
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	srv.ControlHandler().ServeHTTP(rec, req)
	var out struct{ URL string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body)
	}
	u, _ := url.Parse(out.URL)
	get := httptest.NewRequest(http.MethodGet, u.Path, nil)
	get.Host = u.Host
	rec = httptest.NewRecorder()
	srv.ContentHandler().ServeHTTP(rec, get)
	if rec.Code != http.StatusOK {
		t.Errorf("following the advertised %s = %d, want 200", out.URL, rec.Code)
	}
}
