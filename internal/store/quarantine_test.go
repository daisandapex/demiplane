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

// quarantinedPair publishes site "a" (a.txt in an unreadable directory d,
// plus x.txt) and site "b" (b.txt), all 100 bytes, then recounts so "a" is
// quarantined: charged only for the part of it the recount could read. It
// returns the store, the unreadable directory, and what "b" alone is charged.
func quarantinedPair(t *testing.T) (*Store, string, int64) {
	t.Helper()
	skipIfRoot(t)
	s := newQuotaStore(t, 1<<20)
	body := strings.Repeat("x", 100)
	for site, files := range map[string][]string{"a": {"d/a.txt", "x.txt"}, "b": {"b.txt"}} {
		sw, err := s.NewSiteWriter(site)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := sw.AddFile(f, strings.NewReader(body)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := sw.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(s.siteRoot(), "a", "d")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	if err := s.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	if q := s.quarantinedSites(); len(q) != 1 || q[0] != "a" {
		t.Fatalf("quarantined sites = %q, want [a]", q)
	}
	return s, locked, 2*metaAllowance + 100 // b's directory and b.txt
}

// TestRemovingAQuarantinedSiteKeepsOtherSitesCharged: a quarantined site is
// charged only in part, so removing it, once it can be read again, must
// release no more than that part. Releasing all of it would take the
// difference out of the other sites' charges and let the store grow past its
// quota and entry limit until the next recount.
func TestRemovingAQuarantinedSiteKeepsOtherSitesCharged(t *testing.T) {
	t.Run("delete", func(t *testing.T) {
		s, locked, wantB := quarantinedPair(t)
		if err := os.Chmod(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteSite("a"); err != nil {
			t.Fatal(err)
		}
		if used, _ := s.Usage(); used != wantB {
			t.Errorf("usage after deleting the quarantined site = %d, want %d (site b's charge)", used, wantB)
		}
		if n := liveEntries(s); n != 2 {
			t.Errorf("site entries after deleting the quarantined site = %d, want 2", n)
		}
		if q := s.quarantinedSites(); len(q) != 0 {
			t.Errorf("quarantined sites after the delete = %q, want none", q)
		}
		assertAccounted(t, s)
	})

	t.Run("replace", func(t *testing.T) {
		s, locked, wantB := quarantinedPair(t)
		if err := os.Chmod(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		sw, err := s.NewSiteWriter("a")
		if err != nil {
			t.Fatal(err)
		}
		if err := sw.AddFile("new.txt", strings.NewReader(strings.Repeat("y", 50))); err != nil {
			t.Fatal(err)
		}
		if _, err := sw.Commit(); err != nil {
			t.Fatal(err)
		}
		want := wantB + 2*metaAllowance + 50 // the new a and new.txt
		if used, _ := s.Usage(); used != want {
			t.Errorf("usage after replacing the quarantined site = %d, want %d", used, want)
		}
		if n := liveEntries(s); n != 4 {
			t.Errorf("site entries after replacing the quarantined site = %d, want 4", n)
		}
		if q := s.quarantinedSites(); len(q) != 0 {
			t.Errorf("quarantined sites after the replace = %q, want none", q)
		}
		assertAccounted(t, s)
	})

	// Deleted while still unreadable: the delete removes what it can and the
	// rest of the quarantined charge stays with the tombstone, which the
	// scavenger removes once it can, releasing only that rest.
	t.Run("delete then reclaim", func(t *testing.T) {
		s, _, wantB := quarantinedPair(t)
		if err := s.DeleteSite("a"); err != nil {
			t.Fatal(err)
		}
		left, err := filepath.Glob(filepath.Join(s.siteRoot(), tempPrefix+"del-*", "d"))
		if err != nil || len(left) != 1 {
			t.Fatalf("tombstoned survivors = %v, %v; want one", left, err)
		}
		if err := os.Chmod(left[0], 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
			t.Fatal(err)
		}
		if used, _ := s.Usage(); used != wantB {
			t.Errorf("usage after the scavenger removed the rest = %d, want %d (site b's charge)", used, wantB)
		}
		if n := liveEntries(s); n != 2 {
			t.Errorf("site entries after the scavenger removed the rest = %d, want 2", n)
		}
		assertAccounted(t, s)
	})
}
