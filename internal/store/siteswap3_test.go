// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func failSitesSync(t *testing.T) func() {
	t.Helper()
	sitesSync = func(string) error { return errors.New("injected sync failure") }
	restore := func() { sitesSync = syncDir }
	t.Cleanup(restore)
	return restore
}

// failSitesSyncAfterRecord fails the sites directory syncs from the next
// swap record on: the staged tree's own sync, before it, still succeeds.
func failSitesSyncAfterRecord(t *testing.T) func() {
	t.Helper()
	afterSwapRecord = func() {
		sitesSync = func(string) error { return errors.New("injected sync failure") }
	}
	restore := func() { sitesSync, afterSwapRecord = syncDir, nil }
	t.Cleanup(restore)
	return restore
}

// TestRecoveryClearsOnlyADurableRestore: a restore whose rename landed but
// could not be synced keeps its record on every retry until a sync succeeds.
func TestRecoveryClearsOnlyADurableRestore(t *testing.T) {
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
	failSitesSync(t)
	for i := 0; i < 2; i++ {
		if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
			t.Fatal(err)
		}
		if n := swapRows(t, s); n != 1 {
			t.Fatalf("attempt %d: %d swap records with the restore not durable, want 1", i, n)
		}
	}
	sitesSync = syncDir
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if n := swapRows(t, s); n != 0 {
		t.Fatalf("%d swap records after a durable restore", n)
	}
	if got := readSite(t, s, "site", "index.html"); got != "v1" {
		t.Fatalf("site = %q, want v1", got)
	}
}

// TestRollbackClearsOnlyWhenDurable: a failed replace whose rollback rename
// worked but could not be synced keeps its record for recovery.
func TestRollbackClearsOnlyWhenDurable(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := publishSiteBody(s, "site", "index.html", "v1"); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(dir, "data", "sites", "site")
	// Only the install fails; the rollback (from the backup) works.
	siteRename = func(from, to string) error {
		if to == final && !strings.Contains(filepath.Base(from), "old-") {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { siteRename = os.Rename })
	failSitesSyncAfterRecord(t)
	if err := publishSiteBody(s, "site", "index.html", "v2"); err == nil {
		t.Fatal("replace with a failing install reported success")
	}
	siteRename = os.Rename
	if n := swapRows(t, s); n != 1 {
		t.Fatalf("%d swap records after a rollback that was not durable, want 1", n)
	}
	sitesSync = syncDir
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if n := swapRows(t, s); n != 0 {
		t.Fatalf("%d swap records after recovery", n)
	}
	if got := readSite(t, s, "site", "index.html"); got != "v1" {
		t.Fatalf("site = %q, want v1", got)
	}
}
