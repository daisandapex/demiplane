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

// denyWrites makes dir unwritable (so unlink inside it fails) until the
// returned function restores it. Cleanup restores it regardless.
func denyWrites(t *testing.T, dir string) (restore func()) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection needs a non-root user")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	restore = func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	return restore
}

// genFiles lists the generation files (not temp files) in dir that belong to
// slug.
func genFiles(t *testing.T, dir, slug string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if e.Name() == slug || strings.HasPrefix(e.Name(), slug+genSep) {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestDeleteUnlinkFailureIsRetried: explicit deletion whose unlink fails must
// leave a durable record, so restoring the filesystem and running the
// periodic sweep removes both the bytes and the retained source.
func TestDeleteUnlinkFailureIsRetried(t *testing.T) {
	s := newTestStore(t)
	art := mustPutSource(t, s, "")
	slug := art.Slug

	restoreBlobs := denyWrites(t, s.blobDir)
	restoreSources := denyWrites(t, s.sourceDir)
	if err := s.Delete(slug); err != nil {
		t.Fatalf("Delete with failing unlink: %v (the row is gone; cleanup is deferred, not failed)", err)
	}
	if _, _, err := s.Get(slug); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted artifact still served: %v", err)
	}
	if len(genFiles(t, s.blobDir, slug)) == 0 || len(genFiles(t, s.sourceDir, slug)) == 0 {
		t.Fatal("injection did not take: files were removed despite unwritable dirs")
	}

	restoreBlobs()
	restoreSources()
	if _, err := s.SweepExpired(time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := genFiles(t, s.blobDir, slug); len(got) != 0 {
		t.Errorf("blob survived recovery: %v", got)
	}
	if got := genFiles(t, s.sourceDir, slug); len(got) != 0 {
		t.Errorf("retained source survived recovery: %v", got)
	}
	if n := countPending(t, s); n != 0 {
		t.Errorf("%d pending deletion(s) left after a successful retry", n)
	}
}

// TestExpiryUnlinkFailureIsRetried is the same contract for TTL reaping.
func TestExpiryUnlinkFailureIsRetried(t *testing.T) {
	s := newTestStore(t)
	art := mustPutSource(t, s, "")
	slug := art.Slug
	if _, err := s.db.Exec(`UPDATE artifacts SET expires_at = 1 WHERE slug = ?`, slug); err != nil {
		t.Fatal(err)
	}

	restoreBlobs := denyWrites(t, s.blobDir)
	restoreSources := denyWrites(t, s.sourceDir)
	n, err := s.SweepExpired(time.Now())
	if err != nil {
		t.Fatalf("sweep with failing unlink: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	if len(genFiles(t, s.blobDir, slug)) == 0 {
		t.Fatal("injection did not take: blob removed despite an unwritable dir")
	}

	restoreBlobs()
	restoreSources()
	if _, err := s.SweepExpired(time.Now()); err != nil {
		t.Fatalf("recovery sweep: %v", err)
	}
	if got := genFiles(t, s.blobDir, slug); len(got) != 0 {
		t.Errorf("expired blob survived recovery: %v", got)
	}
	if got := genFiles(t, s.sourceDir, slug); len(got) != 0 {
		t.Errorf("expired source survived recovery: %v", got)
	}
}

// TestReplacedGenerationUnlinkFailureIsRetried covers an overwrite: the
// previous generation's files must also be recorded before the row stops
// naming them.
func TestReplacedGenerationUnlinkFailureIsRetried(t *testing.T) {
	s := newTestStore(t)
	mustPut(t, s, PutOptions{Slug: "page", Source: []byte("# v1")}, "<p>v1</p>")
	if os.Geteuid() == 0 {
		t.Skip("failure injection needs a non-root user")
	}
	first := genFiles(t, s.blobDir, "page")
	if len(first) != 1 {
		t.Fatalf("want one generation, have %v", first)
	}
	// Replace the old generation file with a non-empty directory: its unlink
	// fails (the injected failure) while the blob directory stays writable,
	// so the overwrite itself still succeeds.
	old := filepath.Join(s.blobDir, first[0])
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(old, "pin"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, PutOptions{Slug: "page"}, "<p>v2</p>")
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("injection did not take: old generation removed: %v", err)
	}
	if countPending(t, s) == 0 {
		t.Fatal("failed removal of a replaced generation left no pending record")
	}
	if err := os.RemoveAll(filepath.Join(old, "pin")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SweepExpired(time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("replaced generation survived recovery: %v", err)
	}
	if _, b := readBack(t, s, "page"); string(b) != "<p>v2</p>" {
		t.Errorf("current bytes = %q, want v2", b)
	}
}

// TestReconcileRemovesOrphans: files no row names (from a crash between the
// file write and the row commit, or from before pending deletions existed)
// are removed once older than the grace period; fresh and referenced files
// are kept.
func TestReconcileRemovesOrphans(t *testing.T) {
	s := newTestStore(t)
	live := mustPutSource(t, s, "live")
	old := time.Now().Add(-2 * orphanGrace)

	write := func(dir, name string, mtime time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	staleBlob := write(s.blobDir, "gone"+genSep+"00112233aabbccdd", old)
	staleLegacy := write(s.blobDir, "legacy-gone", old)
	staleSource := write(s.sourceDir, "gone"+genSep+"00112233aabbccdd", old)
	staleTmp := write(s.blobDir, ".tmp-123", old)
	freshBlob := write(s.blobDir, "inflight"+genSep+"8899aabbccddeeff", time.Now())
	freshTmp := write(s.blobDir, ".tmp-456", time.Now())
	// The live artifact's own files, aged past the grace, must survive.
	for _, dir := range []string{s.blobDir, s.sourceDir} {
		for _, name := range genFiles(t, dir, live.Slug) {
			p := filepath.Join(dir, name)
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}

	if _, err := s.Reconcile(time.Now()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, p := range []string{staleBlob, staleLegacy, staleSource, staleTmp} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("orphan %s survived reconcile", filepath.Base(p))
		}
	}
	for _, p := range []string{freshBlob, freshTmp} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("in-grace file %s was removed: %v", filepath.Base(p), err)
		}
	}
	if _, b := readBack(t, s, live.Slug); string(b) != "<p>live</p>" {
		t.Errorf("live artifact damaged by reconcile: %q", b)
	}
	if _, err := s.Source(live.Slug); err != nil {
		t.Errorf("live source damaged by reconcile: %v", err)
	}
}

func mustPutSource(t *testing.T, s *Store, slug string) Artifact {
	t.Helper()
	body := "<p>" + slug + "</p>"
	if slug == "" {
		body = "<p>private</p>"
		return mustPut(t, s, PutOptions{Private: true, Source: []byte("# private")}, body)
	}
	return mustPut(t, s, PutOptions{Slug: slug, Source: []byte("# " + slug)}, body)
}

func countPending(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pending_deletions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
