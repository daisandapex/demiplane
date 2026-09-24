// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenTightensExistingStore: a store left with group/world bits (an older
// release, a restored backup, a hand-made directory) is brought to owner-only
// at Open, including the SQLite file and its WAL and shared-memory companions.
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
		filepath.Join(dir, "sources"):     0o755,
		filepath.Join(dir, "sites"):       0o755,
		filepath.Join(dir, "meta.db"):     0o644,
		filepath.Join(dir, "meta.db-wal"): 0o644,
		filepath.Join(dir, "meta.db-shm"): 0o644,
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
