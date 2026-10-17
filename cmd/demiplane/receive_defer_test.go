// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/store"
)

// receiveOnce runs `demiplane receive` with body on stdin.
func receiveOnce(t *testing.T, dir, slug, body string) {
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
	if err := runReceive([]string{"--store", dir, "--slug", slug}); err != nil {
		t.Fatalf("receive: %v", err)
	}
	r.Close()
}

func blobFiles(t *testing.T, dir, slug string) int {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), slug) {
			n++
		}
	}
	return n
}

// TestReceiveLeavesReplacedGenerationToServer: a replace through `receive`
// leaves the old generation on disk for the server's sweep to remove.
func TestReceiveLeavesReplacedGenerationToServer(t *testing.T) {
	dir := t.TempDir()
	receiveOnce(t, dir, "notes", "one")
	receiveOnce(t, dir, "notes", "two")
	if n := blobFiles(t, dir, "notes"); n != 2 {
		t.Fatalf("%d blob files after a replace through receive, want 2 (the old one is the server's to remove)", n)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.SweepExpired(time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := blobFiles(t, dir, "notes"); n != 1 {
		t.Fatalf("%d blob files after the server's sweep, want 1", n)
	}
}
