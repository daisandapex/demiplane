// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// raceIterations is how many times each concurrency test replays its
// interleaving. The windows being closed are a few syscalls wide, so a single
// attempt proves little; this many hit them reliably on the old code.
const raceIterations = 400

// checkConsistent fails if slug has a metadata row but its bytes cannot be
// opened: the only acceptable outcomes are a readable artifact or ErrNotFound.
func checkConsistent(t *testing.T, s *Store, slug string, i int) {
	t.Helper()
	_, f, err := s.Get(slug)
	if errors.Is(err, ErrNotFound) {
		return
	}
	if err != nil {
		t.Fatalf("iteration %d: row without readable bytes: %v", i, err)
	}
	f.Close()
}

// TestConcurrentPublishAndDelete: a publish and a delete of the same slug,
// racing, must leave either a complete artifact or nothing.
func TestConcurrentPublishAndDelete(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < raceIterations; i++ {
		mustPut(t, s, PutOptions{Slug: "x"}, "old")
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := s.Put(PutOptions{Slug: "x"}, strings.NewReader("new")); err != nil {
				t.Errorf("Put: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := s.Delete("x"); err != nil && !errors.Is(err, ErrNotFound) {
				t.Errorf("Delete: %v", err)
			}
		}()
		wg.Wait()
		checkConsistent(t, s, "x", i)
	}
}

// TestConcurrentRepublishAndReap: re-publishing an expired named slug while the
// sweeper reaps it must leave either the new artifact or nothing, never a row
// whose bytes the sweeper removed.
func TestConcurrentRepublishAndReap(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < raceIterations; i++ {
		mustPut(t, s, PutOptions{Slug: "x", TTL: time.Nanosecond}, "old")
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := s.Put(PutOptions{Slug: "x"}, strings.NewReader("new")); err != nil {
				t.Errorf("Put: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := s.SweepExpired(time.Now().Add(time.Second)); err != nil {
				t.Errorf("SweepExpired: %v", err)
			}
		}()
		wg.Wait()
		checkConsistent(t, s, "x", i)
		_ = s.Delete("x")
	}
}

// TestConcurrentReplaceBodyAndDelete: a rebake racing a delete must not leave
// bytes behind for a deleted slug, and must not resurrect it.
func TestConcurrentReplaceBodyAndDelete(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < raceIterations; i++ {
		mustPut(t, s, PutOptions{Slug: "x", Source: []byte("src")}, "old")
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := s.ReplaceBody("x", strings.NewReader("rebaked"))
			if err != nil && !errors.Is(err, ErrNotFound) {
				t.Errorf("ReplaceBody: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := s.Delete("x"); err != nil && !errors.Is(err, ErrNotFound) {
				t.Errorf("Delete: %v", err)
			}
		}()
		wg.Wait()
		if _, _, err := s.Get("x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("iteration %d: deleted slug still resolves: %v", i, err)
		}
		if n := blobCount(t, s); n != 0 {
			t.Fatalf("iteration %d: %d blob file(s) left after delete", i, n)
		}
	}
}

// TestGetNeverServesNewBytesUnderOldMetadata: while a slug is republished
// alternately with and without a view password, a reader must never get the
// password-protected bytes together with metadata that has no password.
func TestGetNeverServesNewBytesUnderOldMetadata(t *testing.T) {
	s := newTestStore(t)
	mustPut(t, s, PutOptions{Slug: "g"}, "PUBLIC")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < raceIterations; i++ {
			if _, err := s.Put(PutOptions{Slug: "g", Password: "pw"}, strings.NewReader("SECRET")); err != nil {
				t.Errorf("Put gated: %v", err)
			}
			if _, err := s.Put(PutOptions{Slug: "g"}, strings.NewReader("PUBLIC")); err != nil {
				t.Errorf("Put open: %v", err)
			}
		}
		close(stop)
	}()
	reads, leaks := 0, 0
	for done := false; !done; {
		select {
		case <-stop:
			done = true
		default:
		}
		art, f, err := s.Get("g")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		b, _ := io.ReadAll(f)
		f.Close()
		reads++
		if !art.HasPassword() && string(b) == "SECRET" {
			leaks++
		}
	}
	wg.Wait()
	if leaks > 0 {
		t.Fatalf("%d of %d reads returned password-protected bytes with no password on the metadata", leaks, reads)
	}
}

func blobCount(t *testing.T, s *Store) int {
	t.Helper()
	n := 0
	for _, dir := range []string{s.blobDir, s.sourceDir} {
		entries, err := readDirNames(dir)
		if err != nil {
			t.Fatal(err)
		}
		n += len(entries)
	}
	return n
}

func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// slugFiles lists the files in dir that hold any generation of slug.
func slugFiles(t *testing.T, dir, slug string) []string {
	t.Helper()
	names, err := readDirNames(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, n := range names {
		if n == slug || strings.HasPrefix(n, slug+genSep) {
			out = append(out, n)
		}
	}
	return out
}

// TestLegacyFileNamesStillServe: rows written before generations existed name
// their files by slug alone. They must still read, and the first write after
// the upgrade must remove the old-named files.
func TestLegacyFileNamesStillServe(t *testing.T) {
	s := newTestStore(t)
	art := mustPut(t, s, PutOptions{Slug: "old", Source: []byte("# src")}, "<p>legacy</p>")
	if err := os.Rename(s.blobFile("old", art.Version), s.blobFile("old", "")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.sourceFile("old", art.Version), s.sourceFile("old", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE artifacts SET blob_gen = '', source_gen = '', modified_at = 0 WHERE slug = 'old'`); err != nil {
		t.Fatal(err)
	}

	got, b := readBack(t, s, "old")
	if string(b) != "<p>legacy</p>" || got.Version != "" || !got.LastModified().Equal(got.CreatedAt) {
		t.Fatalf("legacy read: body %q version %q", b, got.Version)
	}
	if rec, err := s.Source("old"); err != nil || string(rec.Body) != "# src" {
		t.Fatalf("legacy source: %v", err)
	}

	mustPut(t, s, PutOptions{Slug: "old"}, "<p>new</p>")
	if _, b := readBack(t, s, "old"); string(b) != "<p>new</p>" {
		t.Fatalf("after republish: %q", b)
	}
	for _, p := range []string{s.blobFile("old", ""), s.sourceFile("old", "")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("legacy file %s survived the republish: %v", filepath.Base(p), err)
		}
	}
}

// TestRebakeRetriesAfterOutOfProcessPublish: a publish through a second handle
// on the same store (as the SSH receive process has) lands between a rebake's
// source read and its write. The rebake must not overwrite it with a rendering
// of the older source; it starts over from the new one.
func TestRebakeRetriesAfterOutOfProcessPublish(t *testing.T) {
	s := newTestStore(t)
	other, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	mustPut(t, s, PutOptions{Slug: "doc", Source: []byte("v1")}, "render(v1)")

	calls := 0
	art, err := s.Rebake("doc", func(rec SourceRecord) ([]byte, error) {
		calls++
		if calls == 1 {
			if _, err := other.Put(PutOptions{Slug: "doc", Source: []byte("v2")}, strings.NewReader("render(v2)")); err != nil {
				t.Fatalf("out-of-process Put: %v", err)
			}
		}
		return []byte("rebaked(" + string(rec.Body) + ")"), nil
	})
	if err != nil {
		t.Fatalf("Rebake: %v", err)
	}
	if calls != 2 {
		t.Errorf("render called %d times, want 2 (one conflict, one retry)", calls)
	}
	if _, b := readBack(t, s, "doc"); string(b) != "rebaked(v2)" {
		t.Fatalf("body after rebake = %q, want a rendering of the newer source", b)
	}
	if left := slugFiles(t, s.blobDir, "doc"); len(left) != 1 || art.Version == "" {
		t.Errorf("blob files after rebake = %v, want exactly the current one", left)
	}
}

// TestModifiedAtAdvancesWithinOneSecond: every write moves the modified time
// forward, even several writes inside one second, so an HTTP Last-Modified
// validator always changes with the bytes.
func TestModifiedAtAdvancesWithinOneSecond(t *testing.T) {
	s := newTestStore(t)
	var last time.Time
	for i, write := range []func() Artifact{
		func() Artifact { return mustPut(t, s, PutOptions{Slug: "m"}, "a") },
		func() Artifact {
			a, err := s.ReplaceBody("m", strings.NewReader("b"))
			if err != nil {
				t.Fatal(err)
			}
			return a
		},
		func() Artifact { return mustPut(t, s, PutOptions{Slug: "m"}, "c") },
		func() Artifact {
			a, err := s.ReplaceBody("m", strings.NewReader("d"))
			if err != nil {
				t.Fatal(err)
			}
			return a
		},
	} {
		a := write()
		stored, _ := readBack(t, s, "m")
		if !stored.ModifiedAt.Equal(a.ModifiedAt) {
			t.Fatalf("write %d: returned modified %v, stored %v", i, a.ModifiedAt, stored.ModifiedAt)
		}
		if i > 0 && !stored.LastModified().After(last) {
			t.Fatalf("write %d: modified time %v did not advance past %v", i, stored.LastModified(), last)
		}
		last = stored.LastModified()
	}
}

// TestSourceRereadsAfterConcurrentWrite: a publish from another process that
// commits between Source's row read and its file read removes the generation
// the row named. Source must return the new source, not ErrNoSource.
func TestSourceRereadsAfterConcurrentWrite(t *testing.T) {
	s := newTestStore(t)
	other, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	mustPut(t, s, PutOptions{Slug: "doc", Source: []byte("v1")}, "r1")

	fired := false
	beforeSourceRead = func() {
		if fired {
			return
		}
		fired = true
		if _, err := other.Put(PutOptions{Slug: "doc", Source: []byte("v2")}, strings.NewReader("r2")); err != nil {
			t.Errorf("out-of-process Put: %v", err)
		}
	}
	defer func() { beforeSourceRead = nil }()

	rec, err := s.Source("doc")
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if string(rec.Body) != "v2" {
		t.Fatalf("Source body = %q, want the newer source", rec.Body)
	}
}
