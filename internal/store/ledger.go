// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import "path/filepath"

// ledger.go keeps the quota from releasing what it never counted.
//
// Another process (`demiplane receive`) writes to the same store, and this
// process learns of its writes only when it measures usage (SetQuota,
// RecountUsage). Until then, a generation file, an artifact row or a pending
// deletion record that process created is on disk but not in this process's
// count; if this process removed it and released its size or allowance, the
// count would drop below the content it does hold and admit writes past the
// quota. So each process keeps a ledger of what its count includes: the
// blob and source files with the size counted for each, the artifact rows,
// and the pending deletion records. A measure that is applied replaces the
// ledger with what it measured; this process's own commits add to it; and a
// removal releases only what the ledger holds, removing it from the ledger.
// Anything else is released by the next recount instead, which no longer
// finds it. Site files are written by this process alone, so they need none.
// The ledger is kept only with a quota, and costs one entry per artifact
// file, row and outstanding pending deletion.

// pendingKey identifies a pending deletion record.
type pendingKey struct{ slug, blobGen, sourceGen string }

type ledger struct {
	files   map[string]int64 // blob and source file path -> counted size
	rows    map[string]struct{}
	pending map[pendingKey]struct{}
}

func newLedger() ledger {
	return ledger{
		files:   make(map[string]int64),
		rows:    make(map[string]struct{}),
		pending: make(map[pendingKey]struct{}),
	}
}

// ledgered reports whether path is a blob or source file, which the ledger
// tracks (site files are always counted).
func (s *Store) ledgered(path string) bool {
	d := filepath.Dir(path)
	return d == s.blobDir || d == s.sourceDir
}

// countFile records a file this process committed (or left behind) with the
// size it charged.
func (s *Store) countFile(path string, size int64) {
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.limit > 0 && q.counted.files != nil {
		q.counted.files[path] = size
	}
}

// uncountFile returns how much of a removed file of the given size this
// process counted, and drops it from the ledger: the ledger's size for a
// blob or source file (0 if it was never counted), the size for anything
// else.
func (s *Store) uncountFile(path string, size int64) int64 {
	if !s.ledgered(path) {
		return size
	}
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	n, ok := q.counted.files[path]
	if !ok {
		return 0
	}
	delete(q.counted.files, path)
	return n
}

func (s *Store) countRow(slug string) {
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.limit > 0 && q.counted.rows != nil {
		q.counted.rows[slug] = struct{}{}
	}
}

// uncountRow reports whether this process counted slug's row, and drops it.
func (s *Store) uncountRow(slug string) bool {
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.counted.rows[slug]
	delete(q.counted.rows, slug)
	return ok
}

func (s *Store) countPending(k pendingKey) {
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.limit > 0 && q.counted.pending != nil {
		q.counted.pending[k] = struct{}{}
	}
}

// uncountPending reports whether this process counted the record, and drops
// it.
func (s *Store) uncountPending(k pendingKey) bool {
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.counted.pending[k]
	delete(q.counted.pending, k)
	return ok
}
