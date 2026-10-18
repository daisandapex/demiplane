// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/store"
)

// receiveErr runs `demiplane receive --slug` with body on stdin and returns
// its error.
func receiveErr(t *testing.T, dir, slug, body string) error {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		w.WriteString(body)
		w.Close()
	}()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = r, devnull
	defer func() { os.Stdin, os.Stdout = stdin, stdout }()
	defer r.Close()
	return runReceive([]string{"--store", dir, "--slug", slug})
}

// TestReceiveEnforcesTheServersQuota: receive, run without --store-quota,
// enforces the quota the server recorded in the store.
func TestReceiveEnforcesTheServersQuota(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveQuotaSetting(8 << 10); err != nil {
		t.Fatal(err)
	}
	st.Close()
	err = receiveErr(t, dir, "big", strings.Repeat("x", 16<<10))
	if !errors.Is(err, store.ErrQuota) {
		t.Fatalf("receive past the server's quota: %v, want ErrQuota", err)
	}
	if err := receiveErr(t, dir, "small", "fits"); err != nil {
		t.Fatalf("receive within the quota: %v", err)
	}
}

// TestDeleteSiteCommand: delete-site removes a site, its interrupted
// replace's copy included, and refuses while a server holds the store.
func TestDeleteSiteCommand(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sw, err := st.NewSiteWriter("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.AddFile("index.html", strings.NewReader("<p>site")); err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Commit(); err != nil {
		t.Fatal(err)
	}
	st.Close()

	unlock, err := store.LockServe(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := runDeleteSite([]string{"--store", dir, "demo"}); err == nil || !strings.Contains(err.Error(), "stop the server") {
		t.Fatalf("delete-site with a server running: %v", err)
	}
	unlock()
	if err := runDeleteSite([]string{"--store", dir, "demo"}); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.SiteExists("demo") {
		t.Fatal("site still exists after delete-site")
	}
}

// TestReceiveGivesUpOnAStalledStdin: a receive whose stdin stops sending
// gives up (releasing the receive lock) instead of holding it forever.
func TestReceiveGivesUpOnAStalledStdin(t *testing.T) {
	dir := t.TempDir()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.WriteString("partial")
	fired := make(chan struct{}, 1)
	prevStall, prevExit := receiveStall, stallExit
	receiveStall = 200 * time.Millisecond
	stallExit = func() {
		select {
		case fired <- struct{}{}:
		default:
		}
		w.Close() // in the binary this exits the process
	}
	defer func() { receiveStall, stallExit = prevStall, prevExit }()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = r, devnull
	defer func() { os.Stdin, os.Stdout = stdin, stdout }()
	done := make(chan struct{})
	go func() {
		runReceive([]string{"--store", dir, "--slug", "stalled"})
		close(done)
	}()
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled stdin was never given up on")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("receive did not return after giving up")
	}
}
