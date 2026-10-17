// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daisandapex/demiplane/internal/logsafe"
)

// generation.go makes each write of an artifact's bytes a new file and the
// metadata row the single commit point.
//
// Every Put and every body replacement writes its bytes (and a Put its retained
// source) to a file named <slug>~<generation>, where the generation is fresh
// random hex, and then records that generation in the row in the same statement
// or transaction that records the rest of the metadata. A reader resolves the
// row first and opens the file the row names, so the bytes it serves are always
// the bytes that row describes: new bytes can never be served under the previous
// publish's password, privacy or expiry. The previous generation's files are
// removed only once the row no longer names them; a reader that already opened
// one keeps reading it.
//
// Rows written before generations existed carry an empty generation, which maps
// to the original <slug> file name, so an existing store needs no migration.
//
// Writers to one slug (Put, ReplaceBody, Rebake, Delete, expiry reaping) are also
// serialized in-process by a per-slug lock. The row transaction keeps the
// row/file pairing correct across processes (the SSH receive path opens the same
// store); the lock additionally makes a rebake's read-render-write atomic
// against a publish in this process.

// genSep separates slug and generation in a file name. It is outside both the
// named-slug alphabet and the capability-slug alphabet, so a generation file can
// never collide with another slug's legacy file.
const genSep = "~"

// noSourceGen is the source generation of an artifact with no retained source.
const noSourceGen = "-"

// errGenConflict reports that the row no longer names the generation a
// conditional body replacement expected.
var errGenConflict = errors.New("artifact changed during the write")

func newGen() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate blob name: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// blobFile is the path of slug's bytes at generation gen ("" = legacy name).
func (s *Store) blobFile(slug, gen string) string {
	if gen == "" {
		return filepath.Join(s.blobDir, slug)
	}
	return filepath.Join(s.blobDir, slug+genSep+gen)
}

// sourceFile is the path of slug's retained source at generation gen.
func (s *Store) sourceFile(slug, gen string) string {
	if gen == "" {
		return filepath.Join(s.sourceDir, slug)
	}
	return filepath.Join(s.sourceDir, slug+genSep+gen)
}

// removeGenFiles removes the blob at blobGen and the source at sourceGen. A file
// that is already gone is not an error.
func (s *Store) removeGenFiles(slug, blobGen, sourceGen string) error {
	var errs []error
	if err := os.Remove(s.blobFile(slug, blobGen)); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("remove blob: %w", err))
	}
	if sourceGen != noSourceGen {
		if err := os.Remove(s.sourceFile(slug, sourceGen)); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("remove source: %w", err))
		}
	}
	return errors.Join(errs...)
}

// dropReplacedGen removes a generation the row has stopped naming. The write
// that replaced it recorded it as a pending deletion in its own transaction,
// so a failure here is retried by the sweeper.
func (s *Store) dropReplacedGen(slug, blobGen, sourceGen string) {
	s.finishDeletion(slug, blobGen, sourceGen)
}

// Pending deletions.
//
// Every operation that stops a row naming a generation (Delete, expiry
// reaping, an overwriting Put, a rebake) records that generation in
// pending_deletions in the SAME transaction, then unlinks the files after the
// commit and clears the record. If the unlink fails (a read-only disk, a
// permissions mistake, an I/O error) the record survives, so the files are not
// orphaned with nothing left pointing at them: the sweeper retries every
// record until its files are gone. Reconcile additionally sweeps up generation
// files no row or record names (a crash between the file write and the row
// commit, or a store written before pending deletions existed).

const pendingDeletionsSchema = `
CREATE TABLE IF NOT EXISTS pending_deletions (
    slug       TEXT NOT NULL,
    blob_gen   TEXT NOT NULL,
    source_gen TEXT NOT NULL,
    queued_at  INTEGER NOT NULL,
    PRIMARY KEY (slug, blob_gen, source_gen)
);`

// orphanGrace is how old an unreferenced file must be before Reconcile
// removes it. A publish writes its files before committing the row that names
// them, so a young unreferenced file may be a write in flight (possibly in
// another process, such as SSH receive).
var orphanGrace = time.Hour

// queueDeletion records slug's blob and source generations for removal. tx is
// the transaction that stops the row naming them.
func queueDeletion(tx *sql.Tx, slug, blobGen, sourceGen string) error {
	_, err := tx.Exec(
		`INSERT OR IGNORE INTO pending_deletions (slug, blob_gen, source_gen, queued_at) VALUES (?, ?, ?, ?)`,
		slug, blobGen, sourceGen, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("record pending deletion: %w", err)
	}
	return nil
}

// finishDeletion unlinks a recorded generation and clears its record. A
// failure is logged (redacted: the paths carry the slug) and left for the
// sweeper to retry. The caller holds slug's lock.
func (s *Store) finishDeletion(slug, blobGen, sourceGen string) {
	if err := s.removeGenFiles(slug, blobGen, sourceGen); err != nil {
		log.Printf("store: could not remove artifact files for %s, will retry: %s",
			logsafe.Slug(slug), logsafe.Err(err, slug))
		return
	}
	if _, err := s.db.Exec(
		`DELETE FROM pending_deletions WHERE slug = ? AND blob_gen = ? AND source_gen = ?`,
		slug, blobGen, sourceGen); err != nil {
		// The files are gone; a stale record only costs a no-op retry.
		log.Printf("store: could not clear a pending deletion: %s", logsafe.Err(err, slug))
	}
}

// retryPendingDeletions retries every recorded deletion. A generation that a
// row names again is never removed (generations are random, so this is a
// guard, not an expected case); its record is dropped.
func (s *Store) retryPendingDeletions() error {
	type pending struct{ slug, blobGen, sourceGen string }
	rows, err := s.db.Query(`SELECT slug, blob_gen, source_gen FROM pending_deletions`)
	if err != nil {
		return fmt.Errorf("scan pending deletions: %w", err)
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.slug, &p.blobGen, &p.sourceGen); err != nil {
			rows.Close()
			return fmt.Errorf("scan pending deletion: %w", err)
		}
		todo = append(todo, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate pending deletions: %w", err)
	}
	rows.Close()

	for _, p := range todo {
		func() {
			unlock := s.locks.lock(p.slug)
			defer unlock()
			referenced, err := s.genReferenced(p.slug, p.blobGen, p.sourceGen)
			if err != nil {
				log.Printf("store: pending deletion check failed: %s", logsafe.Err(err, p.slug))
				return
			}
			if referenced {
				_, _ = s.db.Exec(
					`DELETE FROM pending_deletions WHERE slug = ? AND blob_gen = ? AND source_gen = ?`,
					p.slug, p.blobGen, p.sourceGen)
				return
			}
			s.finishDeletion(p.slug, p.blobGen, p.sourceGen)
		}()
	}
	return nil
}

// genReferenced reports whether slug's current row names blobGen as its bytes
// or sourceGen as its retained source.
func (s *Store) genReferenced(slug, blobGen, sourceGen string) (bool, error) {
	var rowBlob, rowSource string
	err := s.db.QueryRow(`SELECT blob_gen, source_gen FROM artifacts WHERE slug = ?`, slug).Scan(&rowBlob, &rowSource)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return rowBlob == blobGen || (sourceGen != noSourceGen && rowSource == sourceGen), nil
}

// Reconcile retries pending deletions and removes files under blobs/ and
// sources/ that no row names and that are older than the grace period,
// including temp files a crashed write left behind. It returns how many
// orphaned files it removed. Intended for startup and a slow periodic cadence;
// the sweeper retries pending deletions on every pass already.
func (s *Store) Reconcile(now time.Time) (int, error) {
	if err := s.retryPendingDeletions(); err != nil {
		return 0, err
	}
	cutoff := now.Add(-orphanGrace)
	removed := 0
	for _, dir := range []struct {
		path   string
		source bool
	}{{s.blobDir, false}, {s.sourceDir, true}} {
		ents, err := os.ReadDir(dir.path)
		if err != nil {
			return removed, fmt.Errorf("scan for orphans: %w", err)
		}
		for _, e := range ents {
			if !e.Type().IsRegular() {
				continue
			}
			info, err := e.Info()
			if err != nil || !info.ModTime().Before(cutoff) {
				continue
			}
			name := e.Name()
			p := filepath.Join(dir.path, name)
			if strings.HasPrefix(name, ".tmp-") {
				if err := os.Remove(p); err == nil {
					removed++
				}
				continue
			}
			slug, gen := name, ""
			if i := strings.LastIndex(name, genSep); i >= 0 {
				slug, gen = name[:i], name[i+len(genSep):]
			}
			if s.removeOrphan(p, slug, gen, dir.source) {
				removed++
			}
		}
	}
	return removed, nil
}

// removeOrphan removes path (slug's file at generation gen) if, under slug's
// lock, no row names it. A failure is logged and left for the next pass.
func (s *Store) removeOrphan(path, slug, gen string, source bool) bool {
	unlock := s.locks.lock(slug)
	defer unlock()
	blobGen, sourceGen := gen, noSourceGen
	if source {
		blobGen, sourceGen = noSourceGen, gen
	}
	referenced, err := s.genReferenced(slug, blobGen, sourceGen)
	if err != nil || referenced {
		return false
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("store: could not remove an orphaned artifact file: %s", logsafe.Err(err, slug))
		return false
	}
	return true
}

// slugLocks is a set of per-slug mutexes, created on demand and dropped when
// no writer holds or waits for them.
type slugLocks struct {
	mu sync.Mutex
	m  map[string]*slugLock
}

type slugLock struct {
	mu   sync.Mutex
	refs int
}

// lock blocks until the caller is the only writer of slug and returns the
// function that releases it.
func (l *slugLocks) lock(slug string) (unlock func()) {
	l.mu.Lock()
	if l.m == nil {
		l.m = make(map[string]*slugLock)
	}
	sl := l.m[slug]
	if sl == nil {
		sl = &slugLock{}
		l.m[slug] = sl
	}
	sl.refs++
	l.mu.Unlock()

	sl.mu.Lock()
	return func() {
		sl.mu.Unlock()
		l.mu.Lock()
		sl.refs--
		if sl.refs == 0 {
			delete(l.m, slug)
		}
		l.mu.Unlock()
	}
}
