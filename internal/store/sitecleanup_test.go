// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// skipIfRoot skips a test that relies on directory permissions refusing a
// removal, which root ignores.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-based removal failures do not apply to root")
	}
}

func liveEntries(s *Store) int64 {
	s.quota.mu.Lock()
	defer s.quota.mu.Unlock()
	return s.quota.entries + s.quota.entriesInflight
}

// TestSiteAbortKeepsSurvivorsCharged: a staging tree Abort cannot fully remove
// stays charged, a recount keeps counting it, and Reconcile reclaims it once
// it can be removed.
func TestSiteAbortKeepsSurvivorsCharged(t *testing.T) {
	skipIfRoot(t)
	s := newQuotaStore(t, 1<<20)
	sw, err := s.NewSiteWriter("site")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"d/a.txt", "d/b.txt"} {
		if err := sw.AddFile(f, strings.NewReader(strings.Repeat("x", 100))); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(sw.tmpDir, "d")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	if err := sw.Abort(); err == nil {
		t.Fatal("Abort of an unremovable tree reported success")
	}
	// Survivors: the scratch dir, d, a.txt and b.txt.
	want := int64(4*metaAllowance + 200)
	if used, _ := s.Usage(); used != want {
		t.Fatalf("usage after a partial Abort = %d, want %d (survivors stay charged)", used, want)
	}
	if err := s.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != want {
		t.Fatalf("usage after a recount = %d, want %d", used, want)
	}
	if n := liveEntries(s); n != 4 {
		t.Fatalf("site entries after a partial Abort = %d, want 4", n)
	}
	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if n := siteEntries(t, s); n != 0 {
		t.Fatalf("Reconcile left %d site entries", n)
	}
	if used, _ := s.Usage(); used != 0 {
		t.Fatalf("usage after Reconcile = %d, want 0", used)
	}
	if n := liveEntries(s); n != 0 {
		t.Fatalf("site entry count after Reconcile = %d, want 0", n)
	}
}

// TestDeleteSiteUnreadableSubdirKeepsCharge: a delete that cannot read part
// of the tree releases only what it removed.
func TestDeleteSiteUnreadableSubdirKeepsCharge(t *testing.T) {
	skipIfRoot(t)
	s := newQuotaStore(t, 1<<20)
	sw, err := s.NewSiteWriter("site")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"d/a.txt", "x.txt"} {
		if err := sw.AddFile(f, strings.NewReader(strings.Repeat("x", 100))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sw.Commit(); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(s.siteRoot(), "site", "d")
	if err := os.Chmod(locked, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	if err := s.DeleteSite("site"); err == nil {
		t.Fatal("DeleteSite of an unreadable tree reported success")
	}
	// Survivors: the site dir, d and a.txt; x.txt went.
	want := int64(3*metaAllowance + 100)
	if used, _ := s.Usage(); used != want {
		t.Fatalf("usage after a partial delete = %d, want %d", used, want)
	}
	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSite("site"); err != nil {
		t.Fatalf("retried delete: %v", err)
	}
	if used, _ := s.Usage(); used != 0 {
		t.Fatalf("usage after the retried delete = %d, want 0", used)
	}
	assertAccounted(t, s)
}

// TestAbandonedStagingTreesReclaimed: staging trees a crash left under sites/
// are counted while they exist and removed once past the grace, at Open and
// by Reconcile.
func TestAbandonedStagingTreesReclaimed(t *testing.T) {
	dir := t.TempDir()
	plant := func(name string, age time.Duration) {
		t.Helper()
		p := filepath.Join(dir, "data", "sites", name, "a")
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "f.txt"), []byte(strings.Repeat("y", 50)), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		for _, q := range []string{filepath.Join(p, "f.txt"), p, filepath.Dir(p)} {
			if err := os.Chtimes(q, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	plant(".tmp-old-1", 2*orphanGrace)
	plant(".tmp-young-1", 0)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := os.Stat(filepath.Join(dir, "data", "sites", ".tmp-old-1")); !os.IsNotExist(err) {
		t.Fatalf("an abandoned staging tree past the grace survived Open: %v", err)
	}
	if err := s.SetQuota(1 << 20); err != nil {
		t.Fatal(err)
	}
	// The young tree: its dir, a and f.txt.
	if used, _ := s.Usage(); used != 3*metaAllowance+50 {
		t.Fatalf("usage with a young abandoned tree = %d, want %d", used, 3*metaAllowance+50)
	}
	if n := liveEntries(s); n != 3 {
		t.Fatalf("site entries with a young abandoned tree = %d, want 3", n)
	}
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != 0 {
		t.Fatalf("usage after Reconcile = %d, want 0", used)
	}
	if n := siteEntries(t, s); n != 0 {
		t.Fatalf("Reconcile left %d site entries", n)
	}
	// A live writer's tree is never reclaimed, however old.
	sw, err := s.NewSiteWriter("live")
	if err != nil {
		t.Fatal(err)
	}
	defer sw.Abort()
	if err := sw.AddFile("i.html", strings.NewReader("hi")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reconcile(time.Now().Add(100 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Commit(); err != nil {
		t.Fatalf("commit after a Reconcile: %v", err)
	}
	assertAccounted(t, s)
}

// TestRestageReservesTempEntry: restaging a path reserves an entry for its
// temp file before creating it and returns one after the rename.
func TestRestageReservesTempEntry(t *testing.T) {
	s := newTestStore(t)
	s.SetSiteEntryLimit(2)
	sw, err := s.NewSiteWriter("site")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.AddFile("x.txt", strings.NewReader("1")); err != nil {
		t.Fatal(err)
	}
	if err := sw.AddFile("x.txt", strings.NewReader("2")); !errors.Is(err, ErrSiteEntries) {
		t.Fatalf("restage with no entry left for its temp file: %v, want ErrSiteEntries", err)
	}
	sw.Abort()
	s.SetSiteEntryLimit(3)
	sw, err = s.NewSiteWriter("site")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := sw.AddFile("x.txt", strings.NewReader("abc")); err != nil {
			t.Fatalf("restage %d: %v", i, err)
		}
	}
	if _, err := sw.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := liveEntries(s); n != 2 {
		t.Fatalf("site entries after restaging = %d, want 2", n)
	}
}

// TestConcurrentDeleteSiteReleasesOnce: two deletes of one site release its
// charge once, and the loser finds nothing.
func TestConcurrentDeleteSiteReleasesOnce(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	for _, name := range []string{"a", "b"} {
		if err := publishSite(s, name, "d/x.txt"); err != nil {
			t.Fatal(err)
		}
	}
	keep, _ := s.Usage()
	keep /= 2
	var (
		once   sync.Once
		second = make(chan error, 1)
	)
	beforeSiteRemove = func() {
		once.Do(func() {
			go func() { second <- s.DeleteSite("a") }()
			select {
			case err := <-second:
				second <- err
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
	t.Cleanup(func() { beforeSiteRemove = nil })
	if err := s.DeleteSite("a"); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := <-second; !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v, want ErrNotFound", err)
	}
	if used, _ := s.Usage(); used != keep {
		t.Fatalf("usage after two deletes of one site = %d, want %d", used, keep)
	}
	assertAccounted(t, s)
}
