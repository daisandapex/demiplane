// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func swapRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM site_swaps`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// failInstall makes every rename onto the site's own name fail (the install
// and its rollback) until the returned function restores renames.
func failInstall(t *testing.T, dir, site string) func() {
	t.Helper()
	final := filepath.Join(dir, "data", "sites", site)
	siteRename = func(from, to string) error {
		if to == final {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
		}
		return os.Rename(from, to)
	}
	restore := func() { siteRename = os.Rename }
	t.Cleanup(restore)
	return restore
}

// TestSharedOpenLeavesSwapsAlone: a store opened by another process
// (receive) during a site replace neither restores the moved-aside site nor
// drops the swap's record.
func TestSharedOpenLeavesSwapsAlone(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if err := publishSiteBody(a, "site", "index.html", "old"); err != nil {
		t.Fatal(err)
	}
	shared := func() {
		b, err := OpenShared(dir)
		if err != nil {
			t.Fatal(err)
		}
		b.Close()
	}
	afterSwapRecord = func() {
		shared()
		if n := swapRows(t, a); n != 1 {
			t.Errorf("another process's open dropped the active swap record (%d left)", n)
		}
	}
	afterSiteBackup = shared
	t.Cleanup(func() { afterSwapRecord, afterSiteBackup = nil, nil })
	if err := publishSiteBody(a, "site", "index.html", "new"); err != nil {
		t.Fatalf("replace with another process opening the store meanwhile: %v", err)
	}
	if got := readSite(t, a, "site", "index.html"); got != "new" {
		t.Fatalf("site = %q, want %q", got, "new")
	}
}

// TestRecoveryIgnoresStaleSwap: recovery working from a snapshot taken
// before a newer swap of the same site replaced the record never clears the
// newer record, so its backup is never scavenged.
func TestRecoveryIgnoresStaleSwap(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := publishSiteBody(s, "site", "index.html", "committed"); err != nil {
		t.Fatal(err)
	}
	// A stale record: a finished swap whose trees are gone.
	if _, err := s.db.Exec(`INSERT INTO site_swaps (site, backup, staging) VALUES ('site', '.tmp-old-gone', '.tmp-gone')`); err != nil {
		t.Fatal(err)
	}
	var once bool
	beforeSwapLock = func() {
		if once {
			return
		}
		once = true
		// A newer replace that fails to install and to roll back.
		if _, err := s.db.Exec(`DELETE FROM site_swaps`); err != nil {
			t.Fatal(err)
		}
		restore := failInstall(t, dir, "site")
		if err := publishSiteBody(s, "site", "index.html", "new"); err == nil {
			t.Error("replace with failing renames reported success")
		}
		restore()
		ageBackups(t, dir)
	}
	t.Cleanup(func() { beforeSwapLock = nil })
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if got := readSite(t, s, "site", "index.html"); got != "committed" {
		t.Fatalf("site after recovery = %q, want %q", got, "committed")
	}
}

// ageBackups backdates every ".tmp-old-" tree past the orphan grace.
func ageBackups(t *testing.T, dir string) {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(dir, "data", "sites"))
	old := time.Now().Add(-2 * orphanGrace)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), tempPrefix+"old-") {
			os.Chtimes(filepath.Join(dir, "data", "sites", e.Name()), old, old)
		}
	}
}

// TestNoResurrectionAfterFailedRollback: after a replace whose install and
// rollback both failed, a later publish or delete of the site settles the
// interrupted swap first, so recovery never brings back an obsolete copy.
func TestNoResurrectionAfterFailedRollback(t *testing.T) {
	for _, then := range []string{"publish-then-delete", "delete"} {
		t.Run(then, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			if err := s.SetQuota(1 << 20); err != nil {
				t.Fatal(err)
			}
			if err := publishSiteBody(s, "site", "index.html", "v1"); err != nil {
				t.Fatal(err)
			}
			restore := failInstall(t, dir, "site")
			if err := publishSiteBody(s, "site", "index.html", "v2"); err == nil {
				t.Fatal("replace with failing renames reported success")
			}
			restore()
			if then == "publish-then-delete" {
				if err := publishSiteBody(s, "site", "index.html", "v3"); err != nil {
					t.Fatal(err)
				}
				if got := readSite(t, s, "site", "index.html"); got != "v3" {
					t.Fatalf("site = %q, want v3", got)
				}
			}
			if err := s.DeleteSite("site"); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
				t.Fatal(err)
			}
			if s.SiteExists("site") {
				t.Fatalf("a deleted site came back after Reconcile: %q", readSite(t, s, "site", "index.html"))
			}
			if n := swapRows(t, s); n != 0 {
				t.Fatalf("%d swap records left", n)
			}
			assertAccounted(t, s)
			if n := siteEntries(t, s); n != 0 {
				t.Fatalf("%d site entries left", n)
			}
		})
	}
}

// TestRecoveryKeepsRecordWhenInspectionFails: a swap whose trees cannot be
// inspected (a permission or I/O error, not absence) keeps its record.
func TestRecoveryKeepsRecordWhenInspectionFails(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := publishSiteBody(s, "site", "index.html", "v1"); err != nil {
		t.Fatal(err)
	}
	restore := failInstall(t, dir, "site")
	if err := publishSiteBody(s, "site", "index.html", "v2"); err == nil {
		t.Fatal("replace with failing renames reported success")
	}
	restore()
	sites := filepath.Join(dir, "data", "sites")
	if err := os.Chmod(sites, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sites, 0o700) })
	s.recoverSiteSwaps()
	if err := os.Chmod(sites, 0o700); err != nil {
		t.Fatal(err)
	}
	if n := swapRows(t, s); n != 1 {
		t.Fatalf("recovery that could not inspect the trees left %d records, want 1", n)
	}
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if got := readSite(t, s, "site", "index.html"); got != "v1" {
		t.Fatalf("site after recovery = %q, want v1", got)
	}
}
