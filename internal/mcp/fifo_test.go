// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestPublishPathGuard_FIFOAtOpen: the path checks see a regular file, but the
// file the publish root opens is a FIFO. Opening it must not block waiting for
// a writer; the descriptor check refuses it. The startup root is renamed away
// and a fresh directory put at its path, so the checks and the open see
// different files without a timing race.
func TestPublishPathGuard_FIFOAtOpen(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	c, f := newGuardClient(t, "")
	setPublishRoot(t, c, root)

	old := filepath.Join(parent, "old")
	if err := os.Rename(root, old); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(old, "page.html")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "page.html"), []byte("decoy"), 0o600); err != nil {
		t.Fatal(err)
	}

	type result struct {
		rerr *rpcError
		body string
	}
	done := make(chan result, 1)
	go func() {
		rerr, body := publishPathCall(t, c, f, filepath.Join(root, "page.html"))
		done <- result{rerr, body}
	}()
	select {
	case r := <-done:
		if r.rerr == nil || !strings.Contains(r.rerr.Message, "regular file") {
			t.Fatalf("want a special-file refusal, got %+v (body %q)", r.rerr, r.body)
		}
	case <-time.After(3 * time.Second):
		// Unblock the stuck open so the goroutine can finish.
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		<-done
		t.Fatal("publish blocked opening a FIFO")
	}
}
