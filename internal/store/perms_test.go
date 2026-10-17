// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenTightensExistingStore: a store left with group/world bits (an older
// release, a restored backup, a hand-made directory) is brought to owner-only
// at Open, including the SQLite file and its WAL and shared-memory companions.
// Paths missing owner bits (sources 0500, -shm 0400) are repaired to the same
// exact modes.
func TestOpenTightensExistingStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, PutOptions{Slug: "a"}, "x")
	sw, err := s.NewSiteWriter("site")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.AddFile("index.html", strings.NewReader("<p>")); err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Commit(); err != nil {
		t.Fatal(err)
	}
	s.Close()

	loose := map[string]os.FileMode{
		dir:                               0o775,
		filepath.Join(dir, "blobs"):       0o755,
		filepath.Join(dir, "sources"):     0o500,
		filepath.Join(dir, "sites"):       0o755,
		filepath.Join(dir, "meta.db"):     0o644,
		filepath.Join(dir, "meta.db-wal"): 0o644,
		filepath.Join(dir, "meta.db-shm"): 0o400,
	}
	for p, m := range loose {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			// WAL companions are removed on a clean close; recreate them so the
			// tightening of an existing -wal/-shm is exercised.
			if err := os.WriteFile(p, nil, m); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chmod(p, m); err != nil {
			t.Fatal(err)
		}
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for p, m := range loose {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if m.Perm()&0o100 != 0 {
			want = 0o700
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s perms = %04o, want %04o", filepath.Base(p), got, want)
		}
	}
}

// TestOpenCreatesPrivateDB: a fresh store's metadata database is owner-only
// from the first write, so its WAL and shared-memory files (which SQLite
// creates with the database file's mode) are too.
func TestOpenCreatesPrivateDB(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mustPut(t, s, PutOptions{Slug: "a"}, "x")
	for _, name := range []string{"meta.db", "meta.db-wal", "meta.db-shm"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s perms = %04o, want 0600", name, got)
		}
	}
}

// TestPermRepairLogEscapesControls: the permission-repair log line names the
// store path, which may contain any byte; a newline or ESC in it must not
// forge a log line or reach a terminal raw.
func TestPermRepairLogEscapesControls(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store\nFORGED line \x1b[31m")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	log.SetOutput(prev)
	got := buf.String()
	if !strings.Contains(got, "set mode of") {
		t.Fatalf("no permission repair was logged:\n%q", got)
	}
	if strings.ContainsRune(got, 0x1b) || strings.Contains(got, "\nFORGED") {
		t.Errorf("control characters reached the log raw:\n%q", got)
	}
}
