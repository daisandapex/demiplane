// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// capture records everything the CLI sent so tests can assert the wire shape.
type capture struct {
	method   string
	path     string
	query    url.Values
	auth     string
	password string
	accept   string
	body     string
}

// newPublishServer returns an httptest server that records the request and
// replies with a 201 JSON body mirroring the real handler, plus the capture it
// fills in.
func newPublishServer(t *testing.T) (*httptest.Server, *capture) {
	t.Helper()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.query = r.URL.Query()
		cap.auth = r.Header.Get("Authorization")
		cap.password = r.Header.Get(passwordHeader)
		cap.accept = r.Header.Get("Accept")
		cap.body = string(body)

		slug := r.URL.Query().Get("slug")
		if slug == "" {
			slug = "auto-slug"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"url":  "http://content.example/" + slug,
			"slug": slug,
		})
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func TestPublishRawRoundTrip(t *testing.T) {
	srv, cap := newPublishServer(t)
	p := &publisher{client: srv.Client(), base: srv.URL, filename: "index.html"}
	res, err := p.publish(context.Background(), strings.NewReader("<h1>hi</h1>"))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.URL != "http://content.example/auto-slug" || res.Slug != "auto-slug" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if cap.method != http.MethodPost || cap.path != "/publish" {
		t.Fatalf("method/path = %s %s", cap.method, cap.path)
	}
	if cap.body != "<h1>hi</h1>" {
		t.Fatalf("body = %q", cap.body)
	}
	if got := cap.query.Get("filename"); got != "index.html" {
		t.Fatalf("filename query = %q", got)
	}
	if cap.accept != "application/json" {
		t.Fatalf("accept = %q", cap.accept)
	}
}

func TestPublishQueryParams(t *testing.T) {
	tests := []struct {
		name   string
		p      publisher
		want   map[string]string
		absent []string
	}{
		{
			name:   "named slug",
			p:      publisher{slug: "notes", ttl: "2h"},
			want:   map[string]string{"slug": "notes", "ttl": "2h"},
			absent: []string{"private", "render"},
		},
		{
			name:   "render markdown",
			p:      publisher{render: "md", filename: "doc.md"},
			want:   map[string]string{"render": "md", "filename": "doc.md"},
			absent: []string{"slug", "private"},
		},
		{
			name:   "private",
			p:      publisher{private: true},
			want:   map[string]string{"private": "true"},
			absent: []string{"slug"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, cap := newPublishServer(t)
			p := tt.p
			p.client = srv.Client()
			p.base = srv.URL
			if _, err := p.publish(context.Background(), strings.NewReader("x")); err != nil {
				t.Fatalf("publish: %v", err)
			}
			for k, v := range tt.want {
				if got := cap.query.Get(k); got != v {
					t.Errorf("query %q = %q, want %q", k, got, v)
				}
			}
			for _, k := range tt.absent {
				if cap.query.Has(k) {
					t.Errorf("query %q should be absent, got %q", k, cap.query.Get(k))
				}
			}
		})
	}
}

func TestPublishTokenAndPasswordHeaders(t *testing.T) {
	srv, cap := newPublishServer(t)
	p := &publisher{client: srv.Client(), base: srv.URL, token: "s3cr3t-token", password: "hunter2"}
	if _, err := p.publish(context.Background(), strings.NewReader("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if cap.auth != "Bearer s3cr3t-token" {
		t.Fatalf("authorization = %q", cap.auth)
	}
	if cap.password != "hunter2" {
		t.Fatalf("password header = %q", cap.password)
	}
	// The password must never appear in the URL query (logged surface).
	if cap.query.Has("password") {
		t.Fatalf("password leaked into query: %v", cap.query)
	}
}

// TestPublishDoesNotFollowRedirect proves the CLI client refuses a redirect
// rather than replaying the X-Demiplane-Password header to the redirect target.
// Go strips Authorization on a cross-host redirect but NOT a custom header, so
// following one would leak the view password to an attacker-controlled host.
func TestPublishDoesNotFollowRedirect(t *testing.T) {
	var attackerSawPassword bool
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(passwordHeader) != "" {
			attackerSawPassword = true
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"url":"http://evil/x","slug":"x"}`)
	}))
	defer attacker.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/publish", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	p := &publisher{client: noRedirectClient(), base: redirector.URL, password: "hunter2"}
	// The publish should FAIL (the 307 is treated as the final, non-201 response),
	// and the password must never have reached the attacker origin.
	if _, err := p.publish(context.Background(), strings.NewReader("x")); err == nil {
		t.Fatal("expected publish to fail on the redirect, not follow it")
	}
	if attackerSawPassword {
		t.Fatal("X-Demiplane-Password header was replayed to the redirect target")
	}
}

// TestPublishTokenNeverPrinted drives the full runPublish path and asserts the
// bearer token never reaches stdout.
func TestPublishTokenNeverPrinted(t *testing.T) {
	srv, _ := newPublishServer(t)

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("TOP-SECRET-TOKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(src, []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runPublish([]string{"--url", srv.URL, "--token-file", tokenFile, src}); err != nil {
			t.Fatalf("runPublish: %v", err)
		}
	})
	if strings.Contains(out, "TOP-SECRET-TOKEN") {
		t.Fatalf("token leaked to stdout: %q", out)
	}
	if !strings.Contains(out, "http://content.example/") {
		t.Fatalf("expected URL in stdout, got %q", out)
	}
}

func TestPublishErrorMapping(t *testing.T) {
	tests := []struct {
		code int
		body string
		want string
	}{
		{http.StatusUnauthorized, "unauthorized\n", "401"},
		{http.StatusRequestEntityTooLarge, "upload exceeds the configured size limit\n", "413"},
		{http.StatusBadRequest, "private artifacts cannot use a named slug\n", "private artifacts cannot use a named slug"},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, strings.TrimSpace(tt.body), tt.code)
			}))
			defer srv.Close()
			p := &publisher{client: srv.Client(), base: srv.URL}
			_, err := p.publish(context.Background(), strings.NewReader("x"))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q missing %q", err.Error(), tt.want)
			}
			// A server error body must never masquerade as success text.
			if strings.Contains(err.Error(), "content.example") {
				t.Fatalf("unexpected success URL in error: %q", err)
			}
		})
	}
}

func TestPublishStdin(t *testing.T) {
	srv, cap := newPublishServer(t)
	p := &publisher{client: srv.Client(), base: srv.URL}
	if _, err := p.publish(context.Background(), strings.NewReader("from-stdin")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if cap.body != "from-stdin" {
		t.Fatalf("body = %q", cap.body)
	}
}

func TestResolveBaseURL(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", defaultControlURL, false},
		{"http://host:8891/", "http://host:8891", false},
		{"https://demiplane.example", "https://demiplane.example", false},
		{"ftp://host", "", true},
		{"://nonsense", "", true},
		{"http://", "", true},
	}
	for _, tt := range tests {
		got, err := resolveBaseURL(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("resolveBaseURL(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("resolveBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestValidateRender(t *testing.T) {
	for _, ok := range []string{"", "md", "MD", "markdown"} {
		if err := validateRender(ok); err != nil {
			t.Errorf("validateRender(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"html", "rst", "x"} {
		if err := validateRender(bad); err == nil {
			t.Errorf("validateRender(%q) = nil, want error", bad)
		}
	}
}

func TestRunPublishPrivateSlugConflict(t *testing.T) {
	err := runPublish([]string{"--url", "http://127.0.0.1:9", "--private", "--slug", "x", "/dev/null"})
	if err == nil || !strings.Contains(err.Error(), "--private cannot be combined with --slug") {
		t.Fatalf("expected private+slug conflict, got %v", err)
	}
}

func TestRunWatchRejectsStdinAndPrivate(t *testing.T) {
	// stdin (no path)
	if err := runWatch(context.Background(), &publisher{}, "", false); err == nil ||
		!strings.Contains(err.Error(), "stdin") {
		t.Fatalf("watch stdin: got %v", err)
	}
	// private
	if err := runWatch(context.Background(), &publisher{private: true}, "f", false); err == nil ||
		!strings.Contains(err.Error(), "--private") {
		t.Fatalf("watch private: got %v", err)
	}
}

func TestWatchLoopRepublishesOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.html")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	var count atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = watchLoop(ctx, path, 5*time.Millisecond, func() { count.Add(1) })
		close(done)
	}()

	// Keep advancing mtime until onChange fires. Bumping repeatedly avoids the
	// startup race where the loop captures its baseline mtime AFTER a single
	// Chtimes, which would then look unchanged.
	base := time.Now()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for i := 1; count.Load() == 0; i++ {
		select {
		case <-deadline:
			t.Fatal("watchLoop did not fire onChange after mtime change")
		case <-tick.C:
			ft := base.Add(time.Duration(i) * time.Second)
			if err := os.Chtimes(path, ft, ft); err != nil {
				t.Fatal(err)
			}
		}
	}
	cancel()
	<-done
}

func TestCopyClipboardNoToolIsNoop(t *testing.T) {
	// With PATH emptied, LookPath finds nothing; copyClipboard must not panic or
	// block, proving clipboard support is best-effort.
	t.Setenv("PATH", "")
	copyClipboard("http://example/x") // must simply return
}

// captureStdout redirects os.Stdout for the duration of fn and returns what was
// written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	outCh := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		outCh <- string(b)
	}()

	fn()
	_ = w.Close()
	return <-outCh
}
