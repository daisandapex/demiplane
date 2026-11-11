// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureStderr redirects os.Stderr for the duration of fn and returns what
// was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	outCh := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		outCh <- string(b)
	}()
	fn()
	_ = w.Close()
	return <-outCh
}

// watchIdleSignal sets the watchIdle hook for the test and returns a wait
// function: it reports whether watchLoop became idle (baseline recorded, or a
// change handled) within five seconds.
func watchIdleSignal(t *testing.T) func() bool {
	t.Helper()
	idle := make(chan struct{}, 16)
	watchIdle = func() { idle <- struct{}{} }
	t.Cleanup(func() { watchIdle = nil })
	return func() bool {
		select {
		case <-idle:
			return true
		case <-time.After(5 * time.Second):
			return false
		}
	}
}

// sequenceControlPlane answers the n-th publish with urls[n] (the last one
// repeats) and reports each request on the returned channel.
func sequenceControlPlane(t *testing.T, urls ...string) (string, <-chan int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	seen := make(chan int, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		i := n
		n++
		mu.Unlock()
		u := urls[min(i, len(urls)-1)]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"url": u, "slug": "s"})
		select {
		case seen <- i:
		default:
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, seen
}

// TestWatchRefusesHostileRepublishURL: the --watch republish path prints the
// server's URL too, so a hostile second response must be vetted like the
// first. Nothing of it may reach the terminal raw, and the clipboard and
// browser see only the first, good URL.
func TestWatchRefusesHostileRepublishURL(t *testing.T) {
	const good = "https://content.example/s"
	const hostile = "https://evil.example/\x1b]0;pwned\x07\x1b[2K\r\nforged line"
	src := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(src, []byte("<h1>one</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := stubOpeners(t)
	base, seen := sequenceControlPlane(t, good, hostile)
	p := &publisher{client: noRedirectClient(), base: base, filename: "page.html"}
	waitIdle := watchIdleSignal(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			done := make(chan error, 1)
			go func() { done <- runWatch(ctx, p, src, true) }()
			<-seen // first publish answered
			if !waitIdle() {
				t.Error("watchLoop never recorded its baseline")
			}
			if err := os.WriteFile(src, []byte("<h1>two</h1>"), 0o644); err != nil {
				t.Error(err)
			}
			later := time.Now().Add(time.Hour)
			_ = os.Chtimes(src, later, later)
			select {
			case <-seen:
			case <-time.After(5 * time.Second):
				t.Error("no republish reached the control plane")
			}
			if !waitIdle() {
				t.Error("the republish callback never returned")
			}
			cancel()
			if err := <-done; err != context.Canceled {
				t.Errorf("runWatch = %v, want context.Canceled", err)
			}
		})
	})

	if stdout != good+"\n" {
		t.Errorf("stdout = %q, want only the first URL", stdout)
	}
	_, after, ok := strings.Cut(stderr, "(Ctrl-C to stop)\n")
	if !ok {
		t.Fatalf("stderr has no watching line: %q", stderr)
	}
	if after == "" {
		t.Fatalf("nothing reported for the hostile republish; stderr = %q", stderr)
	}
	body := strings.TrimSuffix(after, "\n")
	for i := 0; i < len(body); i++ {
		if c := body[i]; c < 0x20 || c == 0x7f {
			t.Fatalf("raw byte %#x reached stderr after the second response: %q", c, after)
		}
	}
	if !strings.Contains(after, "refusing") {
		t.Errorf("stderr does not report the refusal: %q", after)
	}
	time.Sleep(50 * time.Millisecond) // openBrowser does not wait
	b, _ := os.ReadFile(calls)
	if got := string(b); strings.Count(got, "\n") != 2 || !strings.Contains(got, "stub-open "+good) || strings.Contains(got, "evil") {
		t.Errorf("clipboard/browser calls = %q, want one of each for the first URL", got)
	}
}
