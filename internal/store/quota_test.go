// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newQuotaStore opens a fresh store with the given quota.
func newQuotaStore(t *testing.T, limit int64) *Store {
	t.Helper()
	s := newTestStore(t)
	if err := s.SetQuota(limit); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	return s
}

// diskBytes sums the sizes of every regular file under the store's content
// directories, temp files included: the bytes actually occupying the disk.
func diskBytes(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	for _, dir := range []string{"blobs", sourceDirName, "sites"} {
		err := filepath.WalkDir(filepath.Join(s.root, dir), func(p string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				fi, err := d.Info()
				if err != nil {
					return err
				}
				n += fi.Size()
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	return n
}

// tempFiles lists leftover temp/scratch entries in the content directories.
func tempFiles(t *testing.T, s *Store) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{"blobs", sourceDirName, "sites"} {
		ents, err := os.ReadDir(filepath.Join(s.root, dir))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".tmp-") {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	return out
}

// assertAccounted checks that the quota counter matches the bytes on disk and
// that nothing temporary was left behind.
func assertAccounted(t *testing.T, s *Store) {
	t.Helper()
	used, _ := s.Usage()
	if disk := diskBytes(t, s); used != disk {
		t.Errorf("usage = %d, bytes on disk = %d", used, disk)
	}
	if left := tempFiles(t, s); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func payload(n int) []byte { return bytes.Repeat([]byte("q"), n) }

func TestQuotaRejectsPutOverLimit(t *testing.T) {
	s := newQuotaStore(t, 1000)
	if _, err := s.Put(PutOptions{Slug: "a"}, bytes.NewReader(payload(600))); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	_, err := s.Put(PutOptions{Slug: "b"}, bytes.NewReader(payload(600)))
	if !errors.Is(err, ErrQuota) {
		t.Fatalf("second Put err = %v, want ErrQuota", err)
	}
	var ie *InputError
	if errors.As(err, &ie) {
		t.Errorf("quota error is an InputError; the client did nothing wrong")
	}
	if _, _, err := s.Get("b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rejected artifact exists: %v", err)
	}
	if used, limit := s.Usage(); used != 600 || limit != 1000 {
		t.Errorf("Usage = %d/%d, want 600/1000", used, limit)
	}
	assertAccounted(t, s)
}

func TestQuotaExactFit(t *testing.T) {
	s := newQuotaStore(t, 1000)
	if _, err := s.Put(PutOptions{Slug: "a"}, bytes.NewReader(payload(1000))); err != nil {
		t.Fatalf("Put of exactly the quota: %v", err)
	}
	if _, err := s.Put(PutOptions{Slug: "b"}, bytes.NewReader(payload(1))); !errors.Is(err, ErrQuota) {
		t.Fatalf("Put past a full quota: %v, want ErrQuota", err)
	}
	assertAccounted(t, s)
}

func TestQuotaZeroIsUnlimited(t *testing.T) {
	s := newQuotaStore(t, 0)
	if _, err := s.Put(PutOptions{}, bytes.NewReader(payload(1<<20))); err != nil {
		t.Fatalf("Put with no quota: %v", err)
	}
}

func TestQuotaConcurrentPutsNeverExceed(t *testing.T) {
	const limit, size, writers = 10_000, 1_000, 40
	s := newQuotaStore(t, limit)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, rejected := 0, 0
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Put(PutOptions{Slug: fmt.Sprintf("c%d", i)}, bytes.NewReader(payload(size)))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrQuota):
				rejected++
			default:
				t.Errorf("Put %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if ok > limit/size {
		t.Errorf("%d publishes succeeded, at most %d fit", ok, limit/size)
	}
	if ok+rejected != writers {
		t.Errorf("ok %d + rejected %d != %d", ok, rejected, writers)
	}
	if disk := diskBytes(t, s); disk > limit {
		t.Errorf("bytes on disk = %d, quota %d", disk, limit)
	}
	assertAccounted(t, s)
}

// failingReader yields n bytes and then an error.
type failingReader struct{ n int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("connection reset")
	}
	k := min(len(p), f.n)
	for i := range p[:k] {
		p[i] = 'x'
	}
	f.n -= k
	return k, nil
}

func TestQuotaReleasesFailedUpload(t *testing.T) {
	s := newQuotaStore(t, 1000)
	if _, err := s.Put(PutOptions{Slug: "a"}, &failingReader{n: 700}); err == nil {
		t.Fatal("Put of a failing body succeeded")
	}
	if used, _ := s.Usage(); used != 0 {
		t.Errorf("usage after a failed upload = %d, want 0", used)
	}
	if _, err := s.Put(PutOptions{Slug: "b"}, bytes.NewReader(payload(900))); err != nil {
		t.Fatalf("Put that fits after a failed upload: %v", err)
	}
	assertAccounted(t, s)
}

func TestQuotaOverwriteReleasesReplacedBytes(t *testing.T) {
	// An overwrite writes the new bytes before the old ones go, so it needs
	// room for both copies while it runs; afterwards only one is counted.
	s := newQuotaStore(t, 1300)
	for i := 0; i < 5; i++ {
		if _, err := s.Put(PutOptions{Slug: "same"}, bytes.NewReader(payload(600))); err != nil {
			t.Fatalf("overwrite %d: %v", i, err)
		}
	}
	if used, _ := s.Usage(); used != 600 {
		t.Errorf("usage after overwrites = %d, want 600", used)
	}
	if _, err := s.Put(PutOptions{Slug: "other"}, bytes.NewReader(payload(700))); err != nil {
		t.Fatalf("Put into the space the overwrites freed: %v", err)
	}
	if _, err := s.Put(PutOptions{Slug: "same"}, bytes.NewReader(payload(600))); !errors.Is(err, ErrQuota) {
		t.Fatalf("overwrite with no room for a second copy: %v, want ErrQuota", err)
	}
	if _, body := readBack(t, s, "same"); len(body) != 600 {
		t.Errorf("artifact after a refused overwrite has %d bytes, want 600", len(body))
	}
	assertAccounted(t, s)
}

func TestQuotaDeleteReleasesBytes(t *testing.T) {
	s := newQuotaStore(t, 1000)
	if _, err := s.Put(PutOptions{Slug: "a", Source: payload(100)}, bytes.NewReader(payload(600))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if used, _ := s.Usage(); used != 700 {
		t.Errorf("usage with source = %d, want 700", used)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if used, _ := s.Usage(); used != 0 {
		t.Errorf("usage after delete = %d, want 0", used)
	}
	if _, err := s.Put(PutOptions{Slug: "b"}, bytes.NewReader(payload(1000))); err != nil {
		t.Fatalf("Put after delete: %v", err)
	}
	assertAccounted(t, s)
}

func TestQuotaExpiryReleasesBytes(t *testing.T) {
	s := newQuotaStore(t, 1000)
	if _, err := s.Put(PutOptions{Slug: "a", TTL: time.Minute}, bytes.NewReader(payload(600))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if n, err := s.SweepExpired(time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("SweepExpired = %d, %v", n, err)
	}
	if used, _ := s.Usage(); used != 0 {
		t.Errorf("usage after expiry = %d, want 0", used)
	}
	assertAccounted(t, s)
}

func TestQuotaCountsSource(t *testing.T) {
	s := newQuotaStore(t, 1000)
	_, err := s.Put(PutOptions{Slug: "a", Source: payload(500)}, bytes.NewReader(payload(600)))
	if !errors.Is(err, ErrQuota) {
		t.Fatalf("Put whose bytes plus source exceed the quota: %v, want ErrQuota", err)
	}
	if _, _, err := s.Get("a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rejected artifact exists: %v", err)
	}
	assertAccounted(t, s)
}

func TestQuotaRebakeReleasesOldBody(t *testing.T) {
	s := newQuotaStore(t, 1000)
	if _, err := s.Put(PutOptions{Slug: "a", Source: payload(100)}, bytes.NewReader(payload(400))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Rebake("a", func(SourceRecord) ([]byte, error) { return payload(400), nil }); err != nil {
			t.Fatalf("rebake %d: %v", i, err)
		}
	}
	if used, _ := s.Usage(); used != 500 {
		t.Errorf("usage after rebakes = %d, want 500", used)
	}
	// A rebake writes its new bytes before the old ones go, so a body that
	// does not fit beside the current one is refused and the page is kept.
	if _, err := s.Rebake("a", func(SourceRecord) ([]byte, error) { return payload(600), nil }); !errors.Is(err, ErrQuota) {
		t.Fatalf("oversized rebake: %v, want ErrQuota", err)
	}
	if _, body := readBack(t, s, "a"); len(body) != 400 {
		t.Errorf("page after a refused rebake has %d bytes, want 400", len(body))
	}
	assertAccounted(t, s)
}

func TestQuotaCountsExistingStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Put(PutOptions{Slug: "a", Source: payload(100)}, bytes.NewReader(payload(500))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sw, err := s.NewSiteWriter("site")
	if err != nil {
		t.Fatalf("NewSiteWriter: %v", err)
	}
	if err := sw.AddFile("index.html", bytes.NewReader(payload(200))); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if _, err := sw.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	s.Close()

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if err := s2.SetQuota(1000); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	if used, _ := s2.Usage(); used != 800 {
		t.Errorf("usage of an existing store = %d, want 800", used)
	}
	if _, err := s2.Put(PutOptions{Slug: "b"}, bytes.NewReader(payload(201))); !errors.Is(err, ErrQuota) {
		t.Fatalf("Put past the existing usage: %v, want ErrQuota", err)
	}
	if _, err := s2.Put(PutOptions{Slug: "b"}, bytes.NewReader(payload(200))); err != nil {
		t.Fatalf("Put that fits: %v", err)
	}
}

func TestQuotaSiteWriterChargesAndReleases(t *testing.T) {
	s := newQuotaStore(t, 1000)

	// Abort releases the staged files.
	sw, err := s.NewSiteWriter("site")
	if err != nil {
		t.Fatalf("NewSiteWriter: %v", err)
	}
	if err := sw.AddFile("a.html", bytes.NewReader(payload(600))); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := sw.AddFile("b.html", bytes.NewReader(payload(600))); !errors.Is(err, ErrQuota) {
		t.Fatalf("AddFile past the quota: %v, want ErrQuota", err)
	}
	sw.Abort()
	if used, _ := s.Usage(); used != 0 {
		t.Errorf("usage after Abort = %d, want 0", used)
	}

	// A committed site stays charged; re-publishing frees the replaced tree.
	for i := 0; i < 3; i++ {
		sw, err := s.NewSiteWriter("site")
		if err != nil {
			t.Fatalf("NewSiteWriter: %v", err)
		}
		if err := sw.AddFile("index.html", bytes.NewReader(payload(400))); err != nil {
			t.Fatalf("AddFile %d: %v", i, err)
		}
		if _, err := sw.Commit(); err != nil {
			t.Fatalf("Commit %d: %v", i, err)
		}
	}
	if used, _ := s.Usage(); used != 400 {
		t.Errorf("usage after site re-publishes = %d, want 400", used)
	}
	if err := s.DeleteSite("site"); err != nil {
		t.Fatalf("DeleteSite: %v", err)
	}
	if used, _ := s.Usage(); used != 0 {
		t.Errorf("usage after DeleteSite = %d, want 0", used)
	}
	assertAccounted(t, s)
}

func TestQuotaScratchCharged(t *testing.T) {
	s := newQuotaStore(t, 1000)
	sc, err := s.NewScratch()
	if err != nil {
		t.Fatalf("NewScratch: %v", err)
	}
	if !strings.HasPrefix(filepath.Dir(sc.f.Name()), s.root) {
		t.Errorf("scratch file %s is outside the store", sc.f.Name())
	}
	if _, err := io.Copy(sc, bytes.NewReader(payload(700))); err != nil {
		t.Fatalf("write scratch: %v", err)
	}
	if used, _ := s.Usage(); used != 700 {
		t.Errorf("usage with a scratch file = %d, want 700", used)
	}
	if _, err := s.Put(PutOptions{Slug: "a"}, bytes.NewReader(payload(400))); !errors.Is(err, ErrQuota) {
		t.Fatalf("Put beside a scratch file: %v, want ErrQuota", err)
	}
	if _, err := io.Copy(sc, bytes.NewReader(payload(400))); !errors.Is(err, ErrQuota) {
		t.Fatalf("scratch past the quota: %v, want ErrQuota", err)
	}
	if err := sc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if used, _ := s.Usage(); used != 0 {
		t.Errorf("usage after the scratch closed = %d, want 0", used)
	}
	assertAccounted(t, s)
}

func TestQuotaRecountPicksUpOutOfProcessWrites(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if err := s.SetQuota(1000); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	// A second handle stands in for the separate receive process.
	other, err := Open(dir)
	if err != nil {
		t.Fatalf("Open other: %v", err)
	}
	defer other.Close()
	if _, err := other.Put(PutOptions{Slug: "a"}, bytes.NewReader(payload(700))); err != nil {
		t.Fatalf("other Put: %v", err)
	}
	if err := s.RecountUsage(); err != nil {
		t.Fatalf("RecountUsage: %v", err)
	}
	if used, _ := s.Usage(); used != 700 {
		t.Errorf("usage after recount = %d, want 700", used)
	}
	if _, err := s.Put(PutOptions{Slug: "b"}, bytes.NewReader(payload(400))); !errors.Is(err, ErrQuota) {
		t.Fatalf("Put after recount: %v, want ErrQuota", err)
	}
}

// TestQuotaRecountMissesNoConcurrentCommit commits a write after the recount
// walked the disk and before it updated the counter: the write's bytes must
// not be lost.
func TestQuotaRecountMissesNoConcurrentCommit(t *testing.T) {
	s := newQuotaStore(t, 10_000)
	if _, err := s.Put(PutOptions{Slug: "a"}, bytes.NewReader(payload(500))); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	var once sync.Once
	afterUsageWalk = func() {
		once.Do(func() {
			if _, err := s.Put(PutOptions{Slug: "b"}, bytes.NewReader(payload(300))); err != nil {
				t.Errorf("Put b during the recount: %v", err)
			}
		})
	}
	defer func() { afterUsageWalk = nil }()
	if err := s.RecountUsage(); err != nil {
		t.Fatalf("RecountUsage: %v", err)
	}
	afterUsageWalk = nil
	assertAccounted(t, s)
}

// TestQuotaRecountDoesNotDoubleCount runs a recount after a file became live
// on disk and before its write was accounted: the bytes must be counted once.
func TestQuotaRecountDoesNotDoubleCount(t *testing.T) {
	s := newQuotaStore(t, 10_000)
	var once sync.Once
	afterLiveRename = func() {
		once.Do(func() {
			if err := s.RecountUsage(); err != nil {
				t.Errorf("RecountUsage during a commit: %v", err)
			}
		})
	}
	defer func() { afterLiveRename = nil }()
	if _, err := s.Put(PutOptions{Slug: "a"}, bytes.NewReader(payload(500))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	afterLiveRename = nil
	assertAccounted(t, s)
}

// TestQuotaRecountSkipsInFlight checks that a recount does not count a
// scratch file that is already charged as in-flight.
func TestQuotaRecountSkipsInFlight(t *testing.T) {
	s := newQuotaStore(t, 10_000)
	sc, err := s.NewScratch()
	if err != nil {
		t.Fatalf("NewScratch: %v", err)
	}
	defer sc.Close()
	if _, err := io.Copy(sc, bytes.NewReader(payload(700))); err != nil {
		t.Fatalf("write scratch: %v", err)
	}
	if err := s.RecountUsage(); err != nil {
		t.Fatalf("RecountUsage: %v", err)
	}
	if used, _ := s.Usage(); used != 700 {
		t.Errorf("usage with an in-flight scratch after a recount = %d, want 700", used)
	}
}
