// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// failTempRemoval makes every temp file removal fail until the test ends.
func failTempRemoval(t *testing.T) {
	t.Helper()
	removeTempFile = func(string) error { return &os.PathError{Op: "remove", Err: os.ErrPermission} }
	t.Cleanup(func() { removeTempFile = os.Remove })
}

// leftoverTemps lists the ".tmp-" files in blobs/ and sources/.
func leftoverTemps(t *testing.T, s *Store) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{s.blobDir, s.sourceDir} {
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), tempPrefix) {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	return out
}

// TestFailedTempRemovalStaysCharged: a scratch file or upload temp file that
// cannot be removed stays charged, across a recount, until Reconcile removes
// it.
func TestFailedTempRemovalStaysCharged(t *testing.T) {
	s := newQuotaStore(t, 1<<20)

	sc, err := s.NewScratch()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(sc, bytes.NewReader(payload(5000))); err != nil {
		t.Fatal(err)
	}
	failTempRemoval(t)
	if err := sc.Close(); err == nil {
		t.Fatal("Close reported success with the scratch file still on disk")
	}
	// An upload that fails after 3000 bytes, whose temp file also stays.
	if _, err := s.Put(PutOptions{Slug: "a"}, &failingReader{n: 3000}); err == nil {
		t.Fatal("Put of a failing body succeeded")
	}
	want := int64(5000 + 3000)
	if n := len(leftoverTemps(t, s)); n != 2 {
		t.Fatalf("%d temp files left, want 2", n)
	}
	if used, _ := s.Usage(); used != want {
		t.Fatalf("usage with two unremovable temp files = %d, want %d", used, want)
	}
	if err := s.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != want {
		t.Fatalf("usage after a recount = %d, want %d", used, want)
	}
	// A live temp file of this process is never reclaimed or counted twice.
	removeTempFile = os.Remove
	live, err := s.NewScratch()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(live, bytes.NewReader(payload(700))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != 700 {
		t.Fatalf("usage after Reconcile with a live scratch file = %d, want 700", used)
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	if left := leftoverTemps(t, s); len(left) != 0 {
		t.Fatalf("temp files left after Reconcile: %v", left)
	}
	if used, _ := s.Usage(); used != 0 {
		t.Fatalf("usage at the end = %d, want 0", used)
	}
	assertAccounted(t, s)
}

// TestFailedTempRemovalCountedAtOpen: an abandoned temp file is counted by a
// store that opens later, until it is reclaimed.
func TestFailedTempRemovalCountedAtOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	failTempRemoval(t)
	if _, err := s.Put(PutOptions{Slug: "a", Source: payload(10)}, &failingReader{n: 2000}); err == nil {
		t.Fatal("Put of a failing body succeeded")
	}
	s.Close()
	removeTempFile = os.Remove
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetQuota(1 << 20); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != 2000 {
		t.Fatalf("usage of a store with an abandoned temp file = %d, want 2000", used)
	}
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != 0 {
		t.Fatalf("usage after Reconcile = %d, want 0", used)
	}
}
