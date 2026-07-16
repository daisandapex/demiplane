// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeControl records the last request it saw and replies with a canned body.
type fakeControl struct {
	*httptest.Server
	lastMethod   string
	lastPath     string
	lastQuery    string
	lastAuth     string
	lastPassword string
	lastBody     string
	status       int
	reply        string
	contentType  string
}

func newFakeControl(t *testing.T) *fakeControl {
	t.Helper()
	f := &fakeControl{status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.lastMethod = r.Method
		f.lastPath = r.URL.EscapedPath()
		f.lastQuery = r.URL.RawQuery
		f.lastAuth = r.Header.Get("Authorization")
		f.lastPassword = r.Header.Get(passwordHeader)
		f.lastBody = string(b)
		if f.contentType != "" {
			w.Header().Set("Content-Type", f.contentType)
		}
		w.WriteHeader(f.status)
		io.WriteString(w, f.reply)
	}))
	t.Cleanup(f.Close)
	return f
}

func TestPublish_QueryAndHeaders(t *testing.T) {
	f := newFakeControl(t)
	f.status = http.StatusCreated
	f.reply = `{"url":"http://content/abc","slug":"abc","size":3}`

	c := NewClient(f.URL, "", "sekret")
	res, err := c.Publish(context.Background(), PublishParams{
		Content:  []byte("hi!"),
		Slug:     "notes",
		TTL:      "24h",
		Render:   "md",
		Filename: "n.md",
		Password: "pw",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.URL != "http://content/abc" {
		t.Errorf("url = %q", res.URL)
	}
	if f.lastMethod != http.MethodPost || f.lastPath != "/publish" {
		t.Errorf("got %s %s", f.lastMethod, f.lastPath)
	}
	if f.lastBody != "hi!" {
		t.Errorf("body = %q", f.lastBody)
	}
	if f.lastAuth != "Bearer sekret" {
		t.Errorf("auth = %q", f.lastAuth)
	}
	if f.lastPassword != "pw" {
		t.Errorf("password header = %q", f.lastPassword)
	}
	q := f.lastQuery
	for _, want := range []string{"slug=notes", "ttl=24h", "render=md", "filename=n.md"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q missing %q", q, want)
		}
	}
	// The password must NEVER be in the query string (log-leak surface).
	if strings.Contains(strings.ToLower(q), "password") || strings.Contains(q, "pw") {
		t.Errorf("password leaked into query: %q", q)
	}
}

func TestPublish_PrivateFlag(t *testing.T) {
	f := newFakeControl(t)
	f.status = http.StatusCreated
	f.reply = `{"url":"http://content/x","slug":"x"}`
	c := NewClient(f.URL, "", "")
	if _, err := c.Publish(context.Background(), PublishParams{Content: []byte("x"), Private: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.lastQuery, "private=true") {
		t.Errorf("query = %q", f.lastQuery)
	}
	// No token configured ⇒ no Authorization header.
	if f.lastAuth != "" {
		t.Errorf("unexpected auth header %q", f.lastAuth)
	}
}

func TestList_And_Delete(t *testing.T) {
	f := newFakeControl(t)
	f.reply = `{"artifacts":[],"count":0}`
	c := NewClient(f.URL, "", "tok")

	body, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"count":0`) {
		t.Errorf("list body = %q", body)
	}
	if f.lastPath != "/list" || f.lastAuth != "Bearer tok" {
		t.Errorf("list req: %s auth=%q", f.lastPath, f.lastAuth)
	}

	f.status = http.StatusNoContent
	f.reply = ""
	if err := c.Delete(context.Background(), "my/slug"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != http.MethodDelete {
		t.Errorf("method = %s", f.lastMethod)
	}
	// Slug is path-escaped so it cannot inject extra segments.
	if f.lastPath != "/my%2Fslug" {
		t.Errorf("delete path = %q (want escaped)", f.lastPath)
	}
}

func TestHTTPErrorMapping(t *testing.T) {
	cases := []struct {
		status int
	}{{http.StatusUnauthorized}, {http.StatusRequestEntityTooLarge}, {http.StatusBadRequest}}
	for _, tc := range cases {
		f := newFakeControl(t)
		f.status = tc.status
		f.reply = "nope"
		c := NewClient(f.URL, "", "")
		_, err := c.Publish(context.Background(), PublishParams{Content: []byte("x")})
		if err == nil {
			t.Fatalf("status %d: expected error", tc.status)
		}
		he, ok := err.(*httpError)
		if !ok {
			t.Fatalf("status %d: got %T, want *httpError", tc.status, err)
		}
		if he.Status != tc.status {
			t.Errorf("mapped status = %d, want %d", he.Status, tc.status)
		}
	}
}

func TestGetBySlug_Textual(t *testing.T) {
	f := newFakeControl(t)
	f.contentType = "text/html; charset=utf-8"
	f.reply = "<h1>hi</h1>"
	c := NewClient(f.URL, f.URL, "")
	res, err := c.GetBySlug(context.Background(), "pg")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Body) != "<h1>hi</h1>" || !isTextual(res.ContentType) {
		t.Errorf("get = %q ct=%q", res.Body, res.ContentType)
	}
	if f.lastPath != "/pg" {
		t.Errorf("path = %q", f.lastPath)
	}
}

func TestGetByURL_HostGuard(t *testing.T) {
	f := newFakeControl(t)
	f.reply = "ok"
	c := NewClient(f.URL, f.URL, "")

	// A URL on a foreign host is rejected before any request goes out (SSRF guard).
	if _, err := c.GetByURL(context.Background(), "http://169.254.169.254/latest/meta-data"); err == nil {
		t.Fatal("expected foreign-host rejection")
	}
	// A URL on the configured host is allowed.
	if _, err := c.GetByURL(context.Background(), f.URL+"/pg"); err != nil {
		t.Fatalf("same-host get: %v", err)
	}
}

// TestGetByURL_PortPivotRejected proves the guard is bound to host:port, not
// hostname alone: a target on the SAME loopback host but a DIFFERENT port than
// either configured origin (a co-located service — admin panel, socket-proxy) is
// rejected before any request leaves the process (demiplane-u2h).
func TestGetByURL_PortPivotRejected(t *testing.T) {
	f := newFakeControl(t)
	f.reply = "ok"
	c := NewClient(f.URL, f.URL, "")

	u, err := url.Parse(f.URL)
	if err != nil {
		t.Fatalf("parse fake url: %v", err)
	}
	// Pick a port that is definitely not the fake server's port.
	pivotPort := "1"
	if u.Port() == pivotPort {
		pivotPort = "2"
	}
	pivot := u.Scheme + "://" + u.Hostname() + ":" + pivotPort + "/secret"
	if _, err := c.GetByURL(context.Background(), pivot); err == nil {
		t.Fatalf("expected same-host/different-port rejection for %q", pivot)
	}

	// The content origin's own port stays reachable even when it differs from the
	// control origin — the split content listener must not be collateral damage.
	cc := NewClient("http://127.0.0.1:9/control", f.URL, "")
	if _, err := cc.GetByURL(context.Background(), f.URL+"/pg"); err != nil {
		t.Fatalf("content-origin get rejected: %v", err)
	}
}

func TestValidateTTL(t *testing.T) {
	good := []string{"", "30m", "2h", "7d", "1.5d", "90s"}
	bad := []string{"nope", "-1h", "0d", "5x", "-3d"}
	for _, s := range good {
		if err := validateTTL(s); err != nil {
			t.Errorf("validateTTL(%q) = %v, want nil", s, err)
		}
	}
	for _, s := range bad {
		if err := validateTTL(s); err == nil {
			t.Errorf("validateTTL(%q) = nil, want error", s)
		}
	}
}

func TestPublishResultDecode(t *testing.T) {
	var r PublishResult
	if err := json.Unmarshal([]byte(`{"url":"u","slug":"s","size":9,"private":true,"password":true,"expires_at":"2026-01-01T00:00:00Z"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.URL != "u" || r.Size != 9 || !r.Private || !r.Password {
		t.Errorf("decoded = %+v", r)
	}
}
