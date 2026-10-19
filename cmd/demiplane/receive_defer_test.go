// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/store"
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
	ents, err := os.ReadDir(filepath.Join(dir, "data", "blobs"))
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

// TestReceiveLeavesSitesAlone: receive never recovers an interrupted site
// replace or scavenges a site tree; those are the server's.
func TestReceiveLeavesSitesAlone(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	// A replace interrupted with the site moved aside, as the server would
	// have recorded it.
	backup := filepath.Join(dir, "data", "sites", ".tmp-old-site-1")
	if err := os.MkdirAll(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "index.html"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(backup, old, old); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO site_swaps (site, id, backup, staging) VALUES ('site', 'x', '.tmp-old-site-1', '.tmp-site-1')`); err != nil {
		t.Fatal(err)
	}
	receiveOnce(t, dir, "notes", "one")
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("receive touched the moved-aside site: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "sites", "site")); !os.IsNotExist(err) {
		t.Fatalf("receive restored the site itself: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM site_swaps`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("swap records after receive = %d, %v; want 1", n, err)
	}
}
