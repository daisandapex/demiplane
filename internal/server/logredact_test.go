// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/logsafe"
	"github.com/daisandapex/demiplane/v3/internal/store"
)

// syncBuffer is a log sink safe for the concurrent writes handler goroutines
// make.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLog redirects the standard logger for the rest of the test.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

// lockDir sets dir's mode for the rest of the test and restores 0700 after,
// so t.TempDir cleanup can still remove it.
func lockDir(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection needs a non-root user")
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
}

// assertNoLeak fails if the log carries the capability slug, any store path,
// or nothing at all (a check that cannot fail proves nothing: the injected
// failure must actually have been logged).
func assertNoLeak(t *testing.T, logs, slug, root string) {
	t.Helper()
	if !strings.Contains(logs, "permission denied") {
		t.Fatalf("injected failure was not logged; log:\n%s", logs)
	}
	if strings.Contains(logs, slug) {
		t.Errorf("log leaked the capability slug:\n%s", logs)
	}
	if strings.Contains(logs, root) {
		t.Errorf("log leaked a store path:\n%s", logs)
	}
}

type redactFixture struct {
	ts   *httptest.Server
	st   *store.Store
	root string
}

func newRedactFixture(t *testing.T) redactFixture {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)
	return redactFixture{ts: ts, st: st, root: root}
}

// publishPrivate publishes a private page and returns its capability slug.
func (f redactFixture) publishPrivate(t *testing.T, query string) string {
	t.Helper()
	u := publish(t, f.ts, "?private=true"+query, "# secret\n\nbody\n")
	return u[strings.LastIndex(u, "/")+1:]
}

func TestLogsRedactGetFailure(t *testing.T) {
	f := newRedactFixture(t)
	slug := f.publishPrivate(t, "")
	logs := captureLog(t)
	lockDir(t, filepath.Join(f.root, "data", "blobs"), 0o000)

	resp, _ := get(t, f.ts.URL+"/"+slug)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("GET with unreadable blobs = %d, want 500", resp.StatusCode)
	}
	assertNoLeak(t, logs.String(), slug, f.root)
}

func TestLogsRedactDeleteFailure(t *testing.T) {
	f := newRedactFixture(t)
	slug := f.publishPrivate(t, "")
	logs := captureLog(t)
	lockDir(t, filepath.Join(f.root, "data", "blobs"), 0o500)

	do(t, http.MethodDelete, f.ts.URL+"/"+slug, "", "")
	assertNoLeak(t, logs.String(), slug, f.root)
}

func TestLogsRedactRerenderFailure(t *testing.T) {
	f := newRedactFixture(t)
	slug := f.publishPrivate(t, "&render=md")
	logs := captureLog(t)
	lockDir(t, filepath.Join(f.root, "data", "sources"), 0o000)

	if code, _ := do(t, http.MethodPost, f.ts.URL+"/rerender/"+slug, "", ""); code != http.StatusInternalServerError {
		t.Fatalf("rerender with unreadable sources = %d, want 500", code)
	}
	assertNoLeak(t, logs.String(), slug, f.root)
}

func TestLogsRedactRerenderAllFailure(t *testing.T) {
	f := newRedactFixture(t)
	slug := f.publishPrivate(t, "&render=md")
	logs := captureLog(t)
	lockDir(t, filepath.Join(f.root, "data", "sources"), 0o000)

	do(t, http.MethodPost, f.ts.URL+"/rerender", "", "")
	assertNoLeak(t, logs.String(), slug, f.root)
}

// TestLogsRedactSweepFailure covers the expiry sweeper. Its caller (the serve
// loop) logs the returned error through logsafe.Err without knowing any slug,
// so the error itself must carry the slug only inside a path Err removes.
func TestLogsRedactSweepFailure(t *testing.T) {
	f := newRedactFixture(t)
	slug := f.publishPrivate(t, "&ttl=1s")
	logs := captureLog(t)
	lockDir(t, filepath.Join(f.root, "data", "blobs"), 0o500)
	time.Sleep(1100 * time.Millisecond)

	_, err := f.st.SweepExpired(time.Now())
	if err != nil {
		log.Printf("ttl sweep error: %s", logsafe.Err(err))
	}
	assertNoLeak(t, logs.String(), slug, f.root)
}
