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
// and sites/. Not counted: the metadata database, module data (replies, TLS
// material), filesystem block overhead, and temp files abandoned by a crash
// (their ".tmp-" names are skipped when usage is measured).
//
// Other processes: `demiplane receive` opens the same store in its own process.
// Each process charges its own writes against usage measured when it set its
// quota, so neither sees the other's in-flight bytes, and a running server
// learns of committed out-of-process writes only when it recounts
// (RecountUsage, run on each sweep). Between recounts the store can exceed its
// quota by the bytes other processes committed since the last recount plus
// their in-flight writes.

// ErrQuota means a write would take the store past its configured aggregate
// storage quota. It is a capacity condition, not a malformed request, so it is
// deliberately not an *InputError; the HTTP layer maps it to 507.
var ErrQuota = errors.New("storage quota exceeded")

// tempPrefix marks in-flight files and directories. Measuring usage skips
// them: an in-flight entry is charged as in-flight, and an abandoned one is
// not live data.
const tempPrefix = ".tmp-"

// quotaState is the store's byte accounting. A zero limit disables it.
type quotaState struct {
	mu        sync.Mutex
	limit     int64
	committed int64 // bytes of live files on disk
	inflight  int64 // bytes charged to writes not yet committed
}

// SetQuota sets the aggregate storage quota in bytes and measures the bytes
// already stored. limit <= 0 disables the quota. Call it once at startup,
// before the store serves writes.
func (s *Store) SetQuota(limit int64) error {
	if limit <= 0 {
		s.quota.mu.Lock()
		s.quota.limit, s.quota.committed, s.quota.inflight = 0, 0, 0
		s.quota.mu.Unlock()
		return nil
	}
	used, err := s.measureUsage()
	if err != nil {
		return err
	}
	s.quota.mu.Lock()
	s.quota.limit, s.quota.committed, s.quota.inflight = limit, used, 0
	s.quota.mu.Unlock()
	return nil
}

// Usage returns the bytes counted against the quota (committed plus in-flight)
// and the quota itself. Both are 0 when no quota is set.
func (s *Store) Usage() (used, limit int64) {
	s.quota.mu.Lock()
	defer s.quota.mu.Unlock()
	return s.quota.committed + s.quota.inflight, s.quota.limit
}

// RecountUsage re-measures the committed bytes on disk, picking up writes made
// by other processes (the SSH receive path). In-flight charges are kept. A
// write that commits while the walk runs can be counted once too often or too
// rarely until the next recount. No-op without a quota.
func (s *Store) RecountUsage() error {
	s.quota.mu.Lock()
	on := s.quota.limit > 0
	s.quota.mu.Unlock()
	if !on {
		return nil
	}
	used, err := s.measureUsage()
	if err != nil {
		return err
	}
	s.quota.mu.Lock()
	if s.quota.limit > 0 {
		s.quota.committed = used
	}
	s.quota.mu.Unlock()
	return nil
}

// measureUsage sums the live regular files under the content directories.
func (s *Store) measureUsage() (int64, error) {
	var total int64
	for _, dir := range []string{s.blobDir, s.sourceDir, s.siteRoot()} {
		n, err := treeSize(dir)
		if err != nil {
			return 0, fmt.Errorf("measure store usage: %w", err)
		}
		total += n
	}
	return total, nil
}

// treeSize sums the regular files under dir, skipping temp entries. A missing
// dir, or an entry removed during the walk, counts as empty.
func treeSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
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
	return n, err
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
	if q.committed+q.inflight+n > q.limit {
		return ErrQuota
	}
	q.inflight += n
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

// settle moves n bytes from in-flight to committed, for a write whose file
// became live.
func (s *Store) settle(n int64) {
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
	q.committed += n
}

// uncommit releases n committed bytes, for a live file that was removed. The
// floor at zero covers removing a file another process wrote after the last
// measurement.
func (s *Store) uncommit(n int64) {
	if n <= 0 {
		return
	}
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.limit <= 0 {
		return
	}
	q.committed = max(q.committed-n, 0)
}

// removeLive removes a live file and releases its size. A file already gone is
// not an error (and releases nothing).
func (s *Store) removeLive(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Mode().IsRegular() {
		s.uncommit(fi.Size())
	}
	return nil
}

// removeLiveTree removes a live directory tree and releases the size of the
// files it held. On a partial failure only what is gone is released.
func (s *Store) removeLiveTree(dir string) error {
	before, err := treeSize(dir)
	if err != nil {
		return err
	}
	rerr := os.RemoveAll(dir)
	after := int64(0)
	if rerr != nil {
		after, _ = treeSize(dir)
	}
	s.uncommit(before - after)
	return rerr
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
	f, err := os.CreateTemp(s.blobDir, tempPrefix+"arc-*")
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

// Close closes and removes the scratch file and releases its bytes. Safe to
// call more than once.
func (sc *Scratch) Close() error {
	if sc.f == nil {
		return nil
	}
	err := sc.f.Close()
	if rerr := os.Remove(sc.f.Name()); rerr != nil && !os.IsNotExist(rerr) && err == nil {
		err = rerr
	}
	sc.s.release(sc.cw.charged)
	sc.cw.charged = 0
	sc.f = nil
	return err
}
