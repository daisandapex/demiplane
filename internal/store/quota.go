// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// quota.go bounds the store's aggregate size on disk.
//
// The per-request caps (--max-upload, the archive decompression budget) bound
// one write; they do not stop many legal writes from filling the disk before
// TTL sweeping reclaims anything. With a quota set, every byte the store is
// about to write is charged first, under one lock, and the write fails with
// ErrQuota the moment committed bytes plus in-flight bytes would pass the
// limit. Temporary files (upload temps, a site's staging tree, the site
// archive scratch copy) are charged as they grow, so concurrent uploads cannot
// each pass a check and then together overshoot. A failed write releases
// exactly what it charged; removing a file (overwrite, rebake, delete, expiry,
// site replacement) releases the size it had on disk.
//
// What is counted: the apparent size of regular files under blobs/, sources/
// and sites/, plus a fixed allowance (metaAllowance) per artifact row, which
// stands in for the metadata database, and per site file or directory, which
// stands in for its inode and directory entry. Not counted: the database's
// actual size, module data (replies, TLS material), filesystem block
// overhead. Abandoned temporary files and staging trees are counted; active
// temporary files owned by this process are charged in flight instead.
//
// Other processes: `demiplane receive` does not enforce a storage quota; each
// run is bounded only by --max-upload. The server learns of its writes when
// it recounts (RecountUsage, run on each sweep), then refuses further growth
// if over quota. Between recounts, out-of-process writes and their in-flight
// bytes can take the store past the server's quota.

// ErrQuota means a write would take the store past its configured aggregate
// storage quota. It is a capacity condition, not a malformed request, so it is
// deliberately not an *InputError; the HTTP layer maps it to 507.
var ErrQuota = errors.New("storage quota exceeded")

// tempPrefix marks temporary files and directories. Active entries owned by
// this process are charged in flight; other entries are counted as live data.
const tempPrefix = ".tmp-"

// quotaState is the store's byte accounting. A zero limit disables it.
type quotaState struct {
	mu        sync.Mutex
	limit     int64
	committed int64 // bytes of live files on disk
	inflight  int64 // bytes charged to writes not yet committed
	retired   int64 // bytes of removed files a reader still holds open (readers.go)
	// active counts filesystem transitions in progress (a file becoming live
	// or being removed, plus its accounting), and gen changes whenever one
	// starts or ends. A recount applies its walk only if no transition
	// overlapped it, so a commit is never lost or counted twice.
	active int
	gen    uint64
	// Site files and directories (siteentry.go), live and staged. Kept with
	// or without a byte quota: they are capped by entryLimit.
	entries, entriesInflight int64
	entryLimit               int64
	// staging holds the names of the site staging trees under sites/ that a
	// writer in this process owns (siteentry.go). They are charged in flight;
	// any other ".tmp-" tree there is abandoned, counted as live and reclaimed.
	staging map[string]struct{}
	// temps holds the paths of the temp files in blobs/ and sources/ that a
	// write or scratch file in this process owns (tempfile.go). They are
	// charged in flight; any other ".tmp-" file there is abandoned, counted
	// as live and reclaimed.
	temps map[string]struct{}
	// counted is what committed includes of the blob and source files, the
	// artifact rows and the pending deletion records (ledger.go).
	counted ledger
}

// SetQuota sets the aggregate storage quota in bytes and measures the bytes
// already stored. limit <= 0 disables the quota. Call it once at startup,
// before the store serves writes.
func (s *Store) SetQuota(limit int64) error {
	if limit <= 0 {
		s.quota.mu.Lock()
		s.quota.limit, s.quota.committed, s.quota.inflight, s.quota.retired = 0, 0, 0, 0
		s.quota.counted = ledger{}
		s.quota.mu.Unlock()
		return nil
	}
	used, entries, l, err := s.measureUsage()
	if err != nil {
		return err
	}
	s.quota.mu.Lock()
	s.quota.limit, s.quota.committed, s.quota.inflight = limit, used, 0
	s.quota.entries, s.quota.counted = entries, l
	s.quota.mu.Unlock()
	return nil
}

// Usage returns the bytes counted against the quota (committed plus in-flight)
// and the quota itself. Both are 0 when no quota is set.
func (s *Store) Usage() (used, limit int64) {
	s.quota.mu.Lock()
	defer s.quota.mu.Unlock()
	return s.quota.committed + s.quota.inflight + s.quota.retired, s.quota.limit
}

// recountAttempts and recountBackoff bound how long a recount waits for a
// quiet moment before it gives up and keeps the current counts.
const (
	recountAttempts = 5
	recountBackoff  = 10 * time.Millisecond
)

// RecountUsage re-measures the committed bytes on disk, picking up writes made
// by other processes (the SSH receive path). In-flight charges are kept, and
// temp files (already charged as in-flight) are not walked. The new figure is
// applied only if no in-process file transition started, ran or ended during
// the walk; otherwise the walk is retried, and after recountAttempts busy
// walks the counts are left as they are until the next recount. No-op without
// a quota.
func (s *Store) RecountUsage() error {
	q := &s.quota
	for attempt := 0; attempt < recountAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(recountBackoff)
		}
		q.mu.Lock()
		on, busy, start := q.limit > 0, q.active > 0, q.gen
		q.mu.Unlock()
		if !on {
			return nil
		}
		if busy {
			continue
		}
		used, entries, l, err := s.measureUsage()
		if err != nil {
			return err
		}
		if afterUsageWalk != nil {
			afterUsageWalk()
		}
		q.mu.Lock()
		applied := q.limit > 0 && q.active == 0 && q.gen == start
		if applied {
			q.committed, q.entries, q.counted = used, entries, l
		}
		q.mu.Unlock()
		if applied {
			return nil
		}
	}
	return nil
}

// beginTransition marks the start of a change to the live files (a rename into
// place or a removal). Every call is paired with endTransition, which applies
// the accounting for the change.
func (s *Store) beginTransition() {
	q := &s.quota
	q.mu.Lock()
	q.active++
	q.gen++
	q.mu.Unlock()
}

// endTransition ends a transition: settled bytes move from in-flight to
// committed (a write became live) and removed bytes leave committed (a live
// file was deleted). The floor at zero covers removing a file another process
// wrote after the last measurement.
func (s *Store) endTransition(settled, removed int64) {
	s.endChange(change{settled: settled, removed: removed})
}

// endSiteTransition is endTransition for a site tree, which also settles or
// removes site entries (siteentry.go).
func (s *Store) endSiteTransition(settled, removed, settledEntries, removedEntries int64) {
	s.endChange(change{settled: settled, removed: removed, settledEntries: settledEntries, removedEntries: removedEntries})
}

// change is the accounting a transition applies when it ends. Entries are
// kept with or without a quota; bytes only with one. Retired files are not
// part of it: unlinkFile moves their bytes itself (readers.go).
type change struct {
	settled, removed               int64
	settledEntries, removedEntries int64
}

func (s *Store) endChange(c change) {
	settled, removed, settledEntries, removedEntries := c.settled, c.removed, c.settledEntries, c.removedEntries
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	q.active--
	q.gen++
	if settledEntries > 0 {
		q.entriesInflight = max(q.entriesInflight-settledEntries, 0)
		q.entries += settledEntries
	}
	if removedEntries > 0 {
		q.entries = max(q.entries-removedEntries, 0)
	}
	if q.limit <= 0 {
		return
	}
	if settled > 0 {
		q.inflight = max(q.inflight-settled, 0)
		q.committed += settled
	}
	if removed > 0 {
		q.committed = max(q.committed-removed, 0)
	}
}

// retire moves a removed file a reader holds from the live count, where
// counted of it was (ledger.go), to the retired one, all size bytes of it:
// they stay on disk until the reader closes. The caller holds s.open.mu
// (readers.go).
func (s *Store) retire(counted, size int64) {
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.limit <= 0 {
		return
	}
	q.committed = max(q.committed-counted, 0)
	q.retired += size
}

// releaseRetired releases n retired bytes: the last reader of a removed file
// closed it. The caller holds s.open.mu (readers.go).
func (s *Store) releaseRetired(n int64) {
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.limit <= 0 {
		return
	}
	q.retired = max(q.retired-n, 0)
}

// commitRename renames a fully written temp file or directory into place and
// moves its charged bytes to committed, as one transition.
func (s *Store) commitRename(from, to string, charged int64) error {
	s.beginTransition()
	if err := os.Rename(from, to); err != nil {
		s.endTransition(0, 0)
		return err
	}
	s.unregisterTemp(from)
	s.countFile(to, charged)
	if afterLiveRename != nil {
		afterLiveRename()
	}
	s.endTransition(charged, 0)
	return nil
}

// Test hooks: afterUsageWalk runs between a recount's walk and its update;
// afterLiveRename runs between a file becoming live and its accounting.
var afterUsageWalk, afterLiveRename func()

// measureUsage sums the live regular files under the content directories
// and the allowances of the artifact rows and site entries, and returns the
// site entry count with it.
// The ledger it returns holds what it counted (ledger.go).
func (s *Store) measureUsage() (used, siteEntries int64, l ledger, err error) {
	fail := func(err error) (int64, int64, ledger, error) {
		return 0, 0, ledger{}, fmt.Errorf("measure store usage: %w", err)
	}
	l = newLedger()
	// Artifact rows and pending deletion records each hold an allowance.
	if err := s.scanUsageRows(`SELECT slug FROM artifacts`, func(scan func(...any) error) error {
		var slug string
		if err := scan(&slug); err != nil {
			return err
		}
		l.rows[slug] = struct{}{}
		return nil
	}); err != nil {
		return fail(err)
	}
	if err := s.scanUsageRows(`SELECT slug, blob_gen, source_gen FROM pending_deletions`, func(scan func(...any) error) error {
		var k pendingKey
		if err := scan(&k.slug, &k.blobGen, &k.sourceGen); err != nil {
			return err
		}
		l.pending[k] = struct{}{}
		return nil
	}); err != nil {
		return fail(err)
	}
	var total int64
	for _, dir := range []string{s.blobDir, s.sourceDir} {
		n, err := s.contentStats(dir, l.files)
		if err != nil {
			return fail(err)
		}
		total += n
	}
	n, entries, err := s.siteStats()
	if err != nil {
		return fail(err)
	}
	total += n
	return total + (int64(len(l.rows)+len(l.pending))+entries)*metaAllowance, entries, l, nil
}

// usageRows is the part of *sql.Rows a usage scan uses.
type usageRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

// wrapUsageRows, when set, wraps the rows of each usage scan; a test hook.
var wrapUsageRows func(usageRows) usageRows

// scanUsageRows runs query and calls each for every row. A scan or iteration
// error fails the whole scan: Next stops at a read failure without saying so,
// and Close does not report it, so Err is checked before the rows count.
func (s *Store) scanUsageRows(query string, each func(scan func(...any) error) error) error {
	r, err := s.db.Query(query)
	if err != nil {
		return err
	}
	var rows usageRows = r
	if wrapUsageRows != nil {
		rows = wrapUsageRows(rows)
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows.Scan); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return rows.Close()
}

// validateMeta bounds the user-supplied metadata a publish stores in its row.
func validateMeta(opts PutOptions) error {
	if len(opts.Filename) > maxFilenameBytes {
		return inputErrorf("filename is longer than %d bytes", maxFilenameBytes)
	}
	if opts.Series != "" {
		if err := ValidateSeries(opts.Series); err != nil {
			return err
		}
	}
	if len(opts.RenderSpec) > maxRenderSpecBytes {
		return inputErrorf("render parameters are longer than %d bytes", maxRenderSpecBytes)
	}
	return nil
}

// treeStats sums the regular files under dir and counts its entries (files,
// directories and anything else, dir itself excluded), skipping temp
// entries. A missing dir, or an entry removed during the walk, counts as
// empty.
func treeStats(dir string) (size, entries int64, err error) {
	var n, count int64
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if p != dir && strings.HasPrefix(d.Name(), tempPrefix) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if p != dir {
			count++
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		n += fi.Size()
		return nil
	})
	return n, count, err
}

// charge reserves n in-flight bytes, failing with ErrQuota when they do not fit.
func (s *Store) charge(n int64) error {
	if n <= 0 {
		return nil
	}
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.limit <= 0 {
		return nil
	}
	if q.committed+q.inflight+q.retired+n > q.limit {
		return ErrQuota
	}
	q.inflight += n
	return nil
}

// chargeQueued charges a new pending deletion's allowance (queueDeletion). A
// store that removes the generation itself clears the record as soon as the
// write commits, so it takes the allowance without a quota check: an
// overwrite at a full quota still goes through. A deferring store keeps the
// record until the server sweeps, so its allowance is checked like any
// other byte.
func (s *Store) chargeQueued() error {
	if s.deferDeletions {
		return s.charge(metaAllowance)
	}
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.limit > 0 {
		q.inflight += metaAllowance
	}
	return nil
}

// release returns n in-flight bytes, for a write that failed.
func (s *Store) release(n int64) {
	if n <= 0 {
		return
	}
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.limit <= 0 {
		return
	}
	q.inflight = max(q.inflight-n, 0)
}

// removeLive removes a live file and releases its size. A file already gone is
// not an error (and releases nothing).
func (s *Store) removeLive(path string) error {
	_, err := s.removeLiveFile(path)
	return err
}

// removeLiveFile is removeLive that also reports whether this call removed the
// file (false when it was already gone).
func (s *Store) removeLiveFile(path string) (bool, error) {
	s.beginTransition()
	_, counted, removed, retired, err := s.unlinkFile(path)
	var c change
	if !retired {
		c.removed = counted // a retired file's bytes moved already
	}
	s.endChange(c)
	return removed, err
}

// removeLiveTree removes a live site tree and releases exactly what it
// removed: each file's size and each entry's allowance, dir itself included.
// Whatever survives, measured or not, stays charged.
func (s *Store) removeLiveTree(dir string) error {
	s.beginTransition()
	r, err := s.removeTreeCounted(dir)
	s.endChange(r.change())
	return err
}

// treeRemoval is what removeTreeCounted removed: the bytes freed, the bytes
// of removed files a reader still holds (retired, moved by unlinkFile), and
// the entries.
type treeRemoval struct{ bytes, retired, entries int64 }

// change is the accounting for a removal of live entries: their allowances
// and freed bytes leave the live count.
func (r treeRemoval) change() change {
	return change{removed: r.bytes + r.entries*metaAllowance, removedEntries: r.entries}
}

// removeTreeCounted removes dir and everything under it, deepest first, one
// entry at a time, and returns what this call removed (treeRemoval). A
// directory it cannot read is left with its unseen contents, and the error is
// returned; an entry already gone counts for nothing (whoever removed it
// accounts for it). A missing dir is not an error.
func (s *Store) removeTreeCounted(dir string) (treeRemoval, error) {
	var (
		list []string
		errs []error
		r    treeRemoval
	)
	werr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			errs = append(errs, err)
			if d != nil && d.IsDir() {
				return filepath.SkipDir // listed already; its contents stay
			}
			return nil
		}
		list = append(list, p)
		return nil
	})
	if werr != nil {
		errs = append(errs, werr)
	}
	for i := len(list) - 1; i >= 0; i-- {
		_, size, removed, retired, err := s.unlinkFile(list[i])
		if err != nil {
			errs = append(errs, err)
		}
		if !removed {
			continue
		}
		if retired {
			r.retired += size
		} else {
			r.bytes += size
		}
		r.entries++
	}
	return r, errors.Join(errs...)
}

// chargedWriter charges each write against the quota before passing it on. It
// records how many bytes it charged and wrote, so a failed write can release
// exactly that and a committed one can settle it.
type chargedWriter struct {
	s       *Store
	w       io.Writer
	charged int64
}

func (c *chargedWriter) Write(p []byte) (int, error) {
	if err := c.s.charge(int64(len(p))); err != nil {
		return 0, err
	}
	n, err := c.w.Write(p)
	if n < len(p) {
		c.s.release(int64(len(p) - n))
	}
	c.charged += int64(n)
	return n, err
}

// Scratch is a temporary file inside the store whose bytes count against the
// quota until it is closed. Close removes it and releases its bytes.
type Scratch struct {
	s  *Store
	f  *os.File
	cw *chargedWriter
}

// NewScratch creates an owner-only scratch file under the store root (in the
// blob directory, so it shares the store's filesystem and permissions).
func (s *Store) NewScratch() (*Scratch, error) {
	f, err := s.createTemp(s.blobDir, tempPrefix+"arc-*")
	if err != nil {
		return nil, fmt.Errorf("create scratch file: %w", err)
	}
	return &Scratch{s: s, f: f, cw: &chargedWriter{s: s, w: f}}, nil
}

// Write appends to the scratch file, charging the quota first.
func (sc *Scratch) Write(p []byte) (int, error) { return sc.cw.Write(p) }

// Read reads from the scratch file's current offset.
func (sc *Scratch) Read(p []byte) (int, error) { return sc.f.Read(p) }

// ReadAt reads at an absolute offset.
func (sc *Scratch) ReadAt(p []byte, off int64) (int, error) { return sc.f.ReadAt(p, off) }

// Seek moves the read offset.
func (sc *Scratch) Seek(off int64, whence int) (int64, error) { return sc.f.Seek(off, whence) }

// Close closes and removes the scratch file and releases its bytes. A file
// that cannot be removed stays charged and is left for Reconcile
// (discardTemp). Safe to call more than once.
func (sc *Scratch) Close() error {
	if sc.f == nil {
		return nil
	}
	err := sc.f.Close()
	if rerr := sc.s.discardTemp(sc.f.Name(), sc.cw.charged); rerr != nil && err == nil {
		err = rerr
	}
	sc.cw.charged = 0
	sc.f = nil
	return err
}

// MetaAllowance is what one artifact row counts against the quota, standing
// in for its share of the metadata database (and WAL), which is not walked.
// It is charged when a row is created and released when it is deleted or
// reaped; overwriting a row changes nothing. The per-row fields are bounded
// (maxFilenameBytes, maxRenderSpecBytes, the slug and series patterns), so
// this covers a row with room to spare.
const MetaAllowance = 4 << 10

const metaAllowance = MetaAllowance

// maxFilenameBytes caps a stored filename hint, and maxRenderSpecBytes the
// opaque render spec, so one row cannot carry an arbitrary amount of
// metadata.
const (
	maxFilenameBytes   = MaxFilenameBytes
	maxRenderSpecBytes = 2 << 10
)

// MaxFilenameBytes is the longest filename hint a publish may carry.
const MaxFilenameBytes = 255
