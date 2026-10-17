// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"context"
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
	var rows int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM artifacts`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	entries := siteEntries(t, s)
	if want := diskBytes(t, s) + (rows+entries)*metaAllowance; used != want {
		t.Errorf("usage = %d, bytes on disk + %d row and %d site-entry allowances = %d", used, rows, entries, want)
	}
	if left := tempFiles(t, s); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func payload(n int) []byte { return bytes.Repeat([]byte("q"), n) }

func TestQuotaRejectsPutOverLimit(t *testing.T) {
	s := newQuotaStore(t, 1000+metaAllowance)
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
	if used, limit := s.Usage(); used != 600+metaAllowance || limit != 1000+metaAllowance {
		t.Errorf("Usage = %d/%d, want 600/1000 plus one row allowance", used, limit)
	}
	assertAccounted(t, s)
}

func TestQuotaExactFit(t *testing.T) {
	s := newQuotaStore(t, 1000+metaAllowance)
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
	const size, writers = 1_000, 40
	const perPut = size + metaAllowance // the bytes plus the row allowance
	const limit = 10 * perPut
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
	if ok > limit/perPut {
		t.Errorf("%d publishes succeeded, at most %d fit", ok, limit/perPut)
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
	s := newQuotaStore(t, 1000+metaAllowance)
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
	s := newQuotaStore(t, 1300+2*metaAllowance)
	for i := 0; i < 5; i++ {
		if _, err := s.Put(PutOptions{Slug: "same"}, bytes.NewReader(payload(600))); err != nil {
			t.Fatalf("overwrite %d: %v", i, err)
		}
	}
	if used, _ := s.Usage(); used != 600+metaAllowance {
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
	s := newQuotaStore(t, 1000+metaAllowance)
	if _, err := s.Put(PutOptions{Slug: "a", Source: payload(100)}, bytes.NewReader(payload(600))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if used, _ := s.Usage(); used != 700+metaAllowance {
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
	s := newQuotaStore(t, 1000+metaAllowance)
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
	s := newQuotaStore(t, 1000+metaAllowance)
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
	s := newQuotaStore(t, 1000+metaAllowance)
	if _, err := s.Put(PutOptions{Slug: "a", Source: payload(100)}, bytes.NewReader(payload(400))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Rebake("a", func(SourceRecord) ([]byte, error) { return payload(400), nil }); err != nil {
			t.Fatalf("rebake %d: %v", i, err)
		}
	}
	if used, _ := s.Usage(); used != 500+metaAllowance {
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
	// Allowances: the rows a and b, the site's directory and its file.
	if err := s2.SetQuota(1000 + 4*metaAllowance); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	if used, _ := s2.Usage(); used != 800+3*metaAllowance {
		t.Errorf("usage of an existing store = %d, want 800 plus one row and two site-entry allowances", used)
	}
	if _, err := s2.Put(PutOptions{Slug: "b"}, bytes.NewReader(payload(201))); !errors.Is(err, ErrQuota) {
		t.Fatalf("Put past the existing usage: %v, want ErrQuota", err)
	}
	if _, err := s2.Put(PutOptions{Slug: "b"}, bytes.NewReader(payload(200))); err != nil {
		t.Fatalf("Put that fits: %v", err)
	}
}

func TestQuotaSiteWriterChargesAndReleases(t *testing.T) {
	// Room for a staged re-publish beside the live site: two trees of a
	// directory and one 400-byte file.
	s := newQuotaStore(t, 1000+4*metaAllowance)

	// Abort releases the staged files.
	sw, err := s.NewSiteWriter("site")
	if err != nil {
		t.Fatalf("NewSiteWriter: %v", err)
	}
	if err := sw.AddFile("a.html", bytes.NewReader(payload(600))); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := sw.AddFile("b.html", bytes.NewReader(payload(metaAllowance+500))); !errors.Is(err, ErrQuota) {
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
	if used, _ := s.Usage(); used != 400+2*metaAllowance {
		t.Errorf("usage after site re-publishes = %d, want %d", used, 400+2*metaAllowance)
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
	if err := s.SetQuota(1000 + metaAllowance); err != nil {
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
	if used, _ := s.Usage(); used != 700+metaAllowance {
		t.Errorf("usage after recount = %d, want 700 plus one row allowance", used)
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

// TestReconcileReleasesQuota: files Reconcile removes (an orphan with no
// intent, and the file of a stale write intent) were counted when usage was
// measured, so removing them releases their bytes at once, not only at the
// next recount.
func TestReconcileReleasesQuota(t *testing.T) {
	s := newTestStore(t)
	old := time.Now().Add(-2 * orphanGrace)
	orphan := filepath.Join(s.blobDir, "gone"+genSep+"00112233aabbccdd")
	abandoned := filepath.Join(s.blobDir, "ghost"+genSep+"8899aabbccddeeff")
	for _, p := range []string{orphan, abandoned} {
		if err := os.WriteFile(p, payload(700), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO pending_writes (slug, gen, started_at) VALUES ('ghost', '8899aabbccddeeff', ?)`,
		time.Now().Add(-2*staleWriteAge).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetQuota(10_000); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != 1400 {
		t.Fatalf("usage before reconcile = %d, want 1400", used)
	}
	n, err := s.Reconcile(time.Now())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n != 2 {
		t.Fatalf("Reconcile removed %d files, want 2", n)
	}
	assertAccounted(t, s)
}

// dbBytes is the size of the metadata database and its WAL.
func dbBytes(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	for _, name := range []string{"meta.db", "meta.db-wal"} {
		if fi, err := os.Stat(filepath.Join(s.root, name)); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// TestQuotaBoundsMetadata: artifact metadata counts against the quota. A
// filename hint over the cap is refused before anything is written, and a
// flood of empty artifacts stops at the quota instead of growing the
// metadata database without bound.
func TestQuotaBoundsMetadata(t *testing.T) {
	const limit = 1 << 20
	s := newQuotaStore(t, limit)
	long := strings.Repeat("f", 64<<10) + ".txt"
	refused := 0
	for i := 0; i < 100; i++ {
		_, err := s.Put(PutOptions{Slug: fmt.Sprintf("long%d", i), Filename: long}, strings.NewReader(""))
		var ie *InputError
		if errors.As(err, &ie) {
			refused++
		} else if err != nil {
			t.Fatalf("long filename %d: %v", i, err)
		}
	}
	used, _ := s.Usage()
	t.Logf("after 100 empty artifacts with 64 KiB filenames: Usage %d, meta.db+wal %d bytes", used, dbBytes(t, s))
	if refused != 100 {
		t.Fatalf("%d of 100 filename hints over %d bytes refused, want all", refused, maxFilenameBytes)
	}
	if _, err := s.Put(PutOptions{Slug: "edge", Filename: strings.Repeat("e", maxFilenameBytes-4) + ".txt"}, strings.NewReader("")); err != nil {
		t.Fatalf("filename at the cap refused: %v", err)
	}
	if err := s.Delete("edge"); err != nil {
		t.Fatal(err)
	}

	ok := 0
	var err error
	for i := 0; i < 2*limit/metaAllowance; i++ {
		if _, err = s.Put(PutOptions{Slug: fmt.Sprintf("e%d", i)}, strings.NewReader("")); err != nil {
			break
		}
		ok++
	}
	if !errors.Is(err, ErrQuota) {
		t.Fatalf("empty artifacts never hit the quota (%d stored, last err %v)", ok, err)
	}
	if ok != limit/metaAllowance {
		t.Fatalf("%d empty artifacts fit a %d-byte quota, want %d", ok, limit, limit/metaAllowance)
	}
	// An overwrite needs no new allowance; a delete releases one.
	if _, err := s.Put(PutOptions{Slug: "e0"}, strings.NewReader("")); err != nil {
		t.Fatalf("overwrite at a full quota: %v", err)
	}
	if err := s.Delete("e1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(PutOptions{Slug: "fresh"}, strings.NewReader("")); err != nil {
		t.Fatalf("new artifact after a delete freed room: %v", err)
	}
	assertAccounted(t, s)
	if err := s.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	assertAccounted(t, s)
}

// siteEntries counts every file and directory under sites/, temp entries
// included: what the site trees occupy on disk.
func siteEntries(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	root := s.siteRoot()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if p != root {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk sites: %v", err)
	}
	return n
}

// publishSite stages files (path -> body) as site and commits it.
func publishSite(s *Store, site string, files ...string) error {
	sw, err := s.NewSiteWriter(site)
	if err != nil {
		return err
	}
	defer sw.Abort()
	for _, f := range files {
		if err := sw.AddFile(f, strings.NewReader("")); err != nil {
			return err
		}
	}
	_, err = sw.Commit()
	return err
}

// TestQuotaChargesSiteEntries: every site file and directory counts one
// allowance on top of its bytes, charged while it is staged, so one archive
// cannot overshoot; replacing or deleting a site releases its entries, and a
// recount or a restart measures them again.
func TestQuotaChargesSiteEntries(t *testing.T) {
	const limit = 16 * metaAllowance
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetQuota(limit); err != nil {
		t.Fatal(err)
	}
	// 1 + 17 entries: refused while staging, nothing left behind.
	var deep []string
	for i := 0; i < 17; i++ {
		deep = append(deep, fmt.Sprintf("f%d", i))
	}
	if err := publishSite(s, "big", deep...); !errors.Is(err, ErrQuota) {
		t.Fatalf("site of 18 entries under a 16-entry quota: %v, want ErrQuota", err)
	}
	if used, _ := s.Usage(); used != 0 {
		t.Fatalf("usage after a refused site = %d, want 0", used)
	}
	if n := siteEntries(t, s); n != 0 {
		t.Fatalf("refused site left %d entries", n)
	}
	// Site dir + a + b + c.txt: four entries each, so four sites fill it.
	for i := 0; i < 4; i++ {
		if err := publishSite(s, fmt.Sprintf("s%d", i), "a/b/c.txt"); err != nil {
			t.Fatalf("site %d: %v", i, err)
		}
	}
	if err := publishSite(s, "s4", "x.txt"); !errors.Is(err, ErrQuota) {
		t.Fatalf("site past the quota: %v, want ErrQuota", err)
	}
	assertAccounted(t, s)
	if err := s.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != limit {
		t.Fatalf("usage after a recount = %d, want %d", used, limit)
	}
	s.Close()
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SetQuota(limit); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != limit {
		t.Fatalf("usage after a restart = %d, want %d", used, limit)
	}
	// A replacement needs room for both trees while it stages, so free some
	// first; afterwards only the new tree counts.
	if err := s.DeleteSite("s3"); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != 12*metaAllowance {
		t.Fatalf("usage after a delete = %d, want %d", used, 12*metaAllowance)
	}
	if err := publishSite(s, "s0", "x.txt"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if used, _ := s.Usage(); used != 10*metaAllowance {
		t.Fatalf("usage after a replace = %d, want %d", used, 10*metaAllowance)
	}
	// Staging a path twice does not charge its entries twice.
	sw, err := s.NewSiteWriter("twice")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := sw.AddFile("d/x.txt", strings.NewReader("abc")); err != nil {
			t.Fatalf("stage %d: %v", i, err)
		}
	}
	if _, err := sw.Commit(); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != 13*metaAllowance+3 {
		t.Fatalf("usage after restaging one path = %d, want %d", used, 13*metaAllowance+3)
	}
	assertAccounted(t, s)
}

// TestSiteEntryLimit: the store-wide site entry cap holds with no quota,
// across a restart, and frees with a delete.
func TestSiteEntryLimit(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetSiteEntryLimit(5)
	if err := publishSite(s, "a", "d/x.txt"); err != nil {
		t.Fatal(err)
	}
	err = publishSite(s, "b", "d/x.txt")
	if !errors.Is(err, ErrSiteEntries) || !errors.Is(err, ErrQuota) {
		t.Fatalf("site past the entry limit: %v, want ErrSiteEntries (an ErrQuota)", err)
	}
	if n := siteEntries(t, s); n != 3 {
		t.Fatalf("%d site entries on disk, want 3", n)
	}
	s.Close()
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.SetSiteEntryLimit(5)
	if err := publishSite(s, "b", "d/x.txt"); !errors.Is(err, ErrSiteEntries) {
		t.Fatalf("after a restart: %v, want ErrSiteEntries", err)
	}
	if err := s.DeleteSite("a"); err != nil {
		t.Fatal(err)
	}
	if err := publishSite(s, "b", "d/x.txt"); err != nil {
		t.Fatalf("after a delete: %v", err)
	}
}

func TestSeriesSlugs(t *testing.T) {
	s := newTestStore(t)
	for _, slug := range []string{"b-2", "b-1", "b-3", "other"} {
		series := "b"
		if slug == "other" {
			series = "x"
		}
		if _, err := s.Put(PutOptions{Slug: slug, Series: series}, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put(PutOptions{Private: true, Series: "b"}, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	got, err := s.SeriesSlugs(context.Background(), DefaultOwner, "b", "b-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "b-1,b-3" {
		t.Errorf("SeriesSlugs = %v, want [b-1 b-3] (sorted, without the named slug, others or private)", got)
	}
	if got, _ := s.SeriesSlugs(context.Background(), DefaultOwner, "b", "", 2); len(got) != 2 {
		t.Errorf("limit 2 returned %d", len(got))
	}
}
