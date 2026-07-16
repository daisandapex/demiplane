// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build reply

package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/store"

	// Compile the reply module into this test binary so Handler() mounts its
	// routes alongside the real core routes. This is the regression guard for the
	// mux-conflict class of bug: a wildcard-first module pattern (/{slug}/reply)
	// is ambiguous with core's literal-first routes (GET /docs/{page}) and panics
	// at registration. Run with: go test -tags reply ./internal/server/
	_ "github.com/daisandapex/demiplane/internal/modules/reply"
)

// TestHandlerMountsReplyModule verifies the full handler (core + reply module)
// builds without a ServeMux pattern conflict, and that the asymmetric auth wiring
// holds end to end against the real Server.Handler.
func TestHandlerMountsReplyModule(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Put(store.PutOptions{Slug: "doc"}, strings.NewReader("<html>x</html>")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Handler() panics on a mux conflict — building it at all is half the test.
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)

	// Core /docs route still resolves (the route that conflicted with the naive
	// /{slug}/reply shape).
	if resp, err := http.Get(ts.URL + "/docs"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /docs: err=%v status=%v", err, resp)
	}

	// Submit is mesh-only (open); list is bearer-gated (open here, no token set).
	resp, err := http.Post(ts.URL+"/reply/doc", "application/json", strings.NewReader(`{"kind":"approve"}`))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /reply/doc: err=%v status=%v", err, resp)
	}
	if resp, err := http.Get(ts.URL + "/replies"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /replies: err=%v status=%v", err, resp)
	}

	// A slug named "replies" is reserved (the module route owns it).
	resp, err = http.Post(ts.URL+"/publish?slug=replies", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("publish replies: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("publish slug=replies = %d, want 400 (reserved)", resp.StatusCode)
	}
}

// TestContentPlaneSameOriginReplySubmit verifies the reply module's content-
// origin route (POST /answer/{slug}) mounts alongside the GET /{slug} artifact
// catch-all without a mux conflict, and that a plain same-origin form post
// records an answer and lands on a server-rendered confirmation — the honest
// reply path that replaces the cross-origin, JS-timer form from the incident.
func TestContentPlaneSameOriginReplySubmit(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Put(store.PutOptions{Slug: "lesson"}, strings.NewReader("<html>x</html>")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// ContentHandler() panics on a mux conflict — building it is half the test.
	ts := httptest.NewServer(New(st, Config{}).ContentHandler())
	t.Cleanup(ts.Close)

	// GET /{slug} still serves the artifact body.
	if resp, err := http.Get(ts.URL + "/lesson"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /lesson: err=%v status=%v", err, resp)
	}

	// Same-origin answer submit: the baked box posts here (/answer/<slug>).
	resp, err := http.Post(ts.URL+"/answer/lesson", "application/x-www-form-urlencoded",
		strings.NewReader("body=my+worked+answer"))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /answer/lesson: err=%v status=%v", err, resp)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	if body := string(buf[:n]); !strings.Contains(body, "✓ Recorded") {
		t.Errorf("expected server-rendered confirmation, got:\n%s", body)
	}
}

// TestForwardFlowEndToEnd drives the whole classroom loop against the real
// combined handler: publish lesson A with ?reply&next=B, answer it BEFORE B
// exists (honest waiting state), then publish B and watch the wait endpoint
// carry the student onto it — the auto-advance demiplane-tb7 ships.
func TestForwardFlowEndToEnd(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	read := func(resp *http.Response) string {
		t.Helper()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		resp.Body.Close()
		return string(b)
	}

	// Publish lesson A pointing at the (not yet existing) lesson B.
	resp, err := http.Post(ts.URL+"/publish?render=md&slug=fw-a&reply=question&next=fw-b",
		"text/markdown", strings.NewReader("# Lesson A\n\nAnswer me."))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish A: err=%v status=%v", err, resp)
	}
	resp.Body.Close()
	resp, err = http.Get(ts.URL + "/fw-a")
	if err != nil {
		t.Fatal(err)
	}
	if page := read(resp); !strings.Contains(page, `name="next" value="fw-b"`) {
		t.Fatalf("lesson A missing the baked next pointer:\n%s", page)
	}

	// Answer A while B does not exist: honest confirmation + forward chrome.
	resp, err = http.Post(ts.URL+"/answer/fw-a", "application/x-www-form-urlencoded",
		strings.NewReader("body=because&next=fw-b"))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("answer A: err=%v status=%v", err, resp)
	}
	conf := read(resp)
	if !strings.Contains(conf, "✓ Recorded") || !strings.Contains(conf, "/answer/fw-a/next?to=fw-b") {
		t.Fatalf("confirmation missing recorded badge or forward link:\n%s", conf)
	}

	// The wait endpoint holds honestly while B is unpublished.
	resp, err = client.Get(ts.URL + "/answer/fw-a/next?to=fw-b")
	if err != nil {
		t.Fatal(err)
	}
	if wait := read(resp); resp.StatusCode != http.StatusOK || !strings.Contains(wait, "being prepared") {
		t.Fatalf("wait before publish = %d, want honest holding page:\n%s", resp.StatusCode, wait)
	}

	// Publish B → the same wait URL now moves the student forward.
	resp, err = http.Post(ts.URL+"/publish?render=md&slug=fw-b",
		"text/markdown", strings.NewReader("# Lesson B"))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish B: err=%v status=%v", err, resp)
	}
	resp.Body.Close()
	resp, err = client.Get(ts.URL + "/answer/fw-a/next?to=fw-b")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/fw-b" {
		t.Fatalf("wait after publish = %d loc=%q, want 302 /fw-b",
			resp.StatusCode, resp.Header.Get("Location"))
	}
}
