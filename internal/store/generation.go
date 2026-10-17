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

// removeGenFiles removes the blob at blobGen and the source at sourceGen,
// releasing their bytes from the store quota. A file that is already gone is
// not an error.
func (s *Store) removeGenFiles(slug, blobGen, sourceGen string) error {
	var errs []error
	if err := s.removeLive(s.blobFile(slug, blobGen)); err != nil {
		errs = append(errs, fmt.Errorf("remove blob: %w", err))
	}
	if sourceGen != noSourceGen {
		if err := s.removeLive(s.sourceFile(slug, sourceGen)); err != nil {
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

// Write intents.
//
// A write (Put, a body replacement) records (slug, generation) in
// pending_writes BEFORE it creates any file of that generation, and deletes
// the record inside the transaction that commits the row naming it. So every
// unreferenced generation file a current binary creates has an intent until
// the write commits or aborts. Reconcile never removes a file whose intent is
// live; it reclaims an intent only once it is older than staleWriteAge, by
// deleting the record first, and a writer whose intent is gone at commit time
// fails instead of committing a row that names reclaimed files. Both sides
// work through the shared database, so this holds across processes (serve
// and SSH receive) as well as within one.

const pendingWritesSchema = `
CREATE TABLE IF NOT EXISTS pending_writes (
    slug       TEXT NOT NULL,
    gen        TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    PRIMARY KEY (slug, gen)
);`

// orphanGrace is how old an unreferenced file with no write intent must be
// before Reconcile removes it. Such a file is a crash leftover from before
// write intents existed, or from a binary that predates them, so the grace
// only protects a write in flight from an older binary sharing the store.
var orphanGrace = time.Hour

// staleWriteAge is how old a write intent must be before Reconcile treats the
// write as abandoned (its process crashed) and reclaims its files.
var staleWriteAge = 24 * time.Hour

// beforeMetaCommit, when set by a test, runs in Put after the files are
// written and before the row commit, to pause a writer in that window.
var beforeMetaCommit func(slug string)

// errWriteReclaimed means Reconcile reclaimed this write as abandoned before
// it committed; its files may be gone, so the write must not commit.
var errWriteReclaimed = errors.New("write was reclaimed as abandoned before it committed")

// Every temp file name in blobs/ and sources/ starts with tempPrefix
// (quota.go).

// tempPattern is the os.CreateTemp pattern for a temp file that will be
// renamed to final: it embeds final's name ("<slug>~<gen>"), so Reconcile can
// match a temp file to its write intent and spare it while the write is live.
func tempPattern(final string) string {
	return tempPrefix + filepath.Base(final) + ".*"
}

// tempTarget returns the "<slug>~<gen>" name a temp file was created for, or
// "" for a temp file from before temp names carried it.
func tempTarget(name string) string {
	rest := strings.TrimPrefix(name, tempPrefix)
	i := strings.LastIndex(rest, ".")
	if i < 0 || !strings.Contains(rest[:i], genSep) {
		return ""
	}
	return rest[:i]
}

// splitGenName splits "<slug>~<gen>" (or a legacy "<slug>") into its parts.
func splitGenName(name string) (slug, gen string) {
	if i := strings.LastIndex(name, genSep); i >= 0 {
		return name[:i], name[i+len(genSep):]
	}
	return name, ""
}

// beginWrite records the intent to write slug's generation gen.
func (s *Store) beginWrite(slug, gen string) error {
	if _, err := s.db.Exec(
		`INSERT INTO pending_writes (slug, gen, started_at) VALUES (?, ?, ?)`,
		slug, gen, time.Now().Unix()); err != nil {
		return fmt.Errorf("record write intent: %w", err)
	}
	return nil
}

// endWrite retires the write intent inside tx, the transaction that commits
// the row naming gen. It fails with errWriteReclaimed if the intent is gone.
func endWrite(tx *sql.Tx, slug, gen string) error {
	res, err := tx.Exec(`DELETE FROM pending_writes WHERE slug = ? AND gen = ?`, slug, gen)
	if err != nil {
		return fmt.Errorf("retire write intent: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errWriteReclaimed
	}
	return nil
}

// abortWrite removes the files of a write that will not commit and then its
// intent. If a file cannot be removed the intent stays, and Reconcile
// reclaims it once it is stale.
func (s *Store) abortWrite(slug, gen, sourceGen string) {
	if err := s.removeGenFiles(slug, gen, sourceGen); err != nil {
		log.Printf("store: could not remove the files of a failed write for %s: %s",
			logsafe.Slug(slug), logsafe.Err(err, slug))
		return
	}
	if _, err := s.db.Exec(`DELETE FROM pending_writes WHERE slug = ? AND gen = ?`, slug, gen); err != nil {
		log.Printf("store: could not clear a write intent: %s", logsafe.Err(err, slug))
	}
}

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

// Reconcile retries pending deletions and removes orphaned files under blobs/
// and sources/: generation files no row names whose write intent is stale (or
// that have none and are older than orphanGrace), and temp files older than
// orphanGrace that a crashed write left behind. It returns how many files it
// removed. Intended for startup and a slow periodic cadence; the sweeper
// retries pending deletions on every pass already.
func (s *Store) Reconcile(now time.Time) (int, error) {
	if err := s.retryPendingDeletions(); err != nil {
		return 0, err
	}
	removed, err := s.reclaimStaleWrites(now)
	if err != nil {
		return removed, err
	}
	cutoff := now.Add(-orphanGrace)
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
			if err != nil {
				continue
			}
			name := e.Name()
			p := filepath.Join(dir.path, name)
			if strings.HasPrefix(name, tempPrefix) {
				// A temp file of a live write (its intent still recorded) is
				// a write in flight however old it is; any other temp file is
				// a crash leftover once past the grace.
				if t := tempTarget(name); t != "" {
					if slug, gen := splitGenName(t); s.writeLive(slug, gen) {
						continue
					}
				}
				if info.ModTime().Before(cutoff) {
					if err := os.Remove(p); err == nil {
						removed++
					}
				}
				continue
			}
			slug, gen := splitGenName(name)
			if s.removeOrphan(p, slug, gen, dir.source, now) {
				removed++
			}
		}
	}
	return removed, nil
}

// writeLive reports whether a write intent for slug's generation gen exists.
// On a lookup error it answers true, keeping the file.
func (s *Store) writeLive(slug, gen string) bool {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM pending_writes WHERE slug = ? AND gen = ?`, slug, gen).Scan(&one)
	return !errors.Is(err, sql.ErrNoRows)
}

// reclaimStaleWrites reclaims every write intent older than staleWriteAge,
// whether or not its files still exist: the intent is deleted first
// (conditionally, so a writer that commits or aborts meanwhile wins), after
// which that writer can no longer commit, and then its files are removed
// unless a row names them. It returns how many files it removed.
func (s *Store) reclaimStaleWrites(now time.Time) (int, error) {
	type intent struct {
		slug, gen string
		started   int64
	}
	rows, err := s.db.Query(`SELECT slug, gen, started_at FROM pending_writes WHERE started_at <= ?`,
		now.Add(-staleWriteAge).Unix())
	if err != nil {
		return 0, fmt.Errorf("scan write intents: %w", err)
	}
	var stale []intent
	for rows.Next() {
		var in intent
		if err := rows.Scan(&in.slug, &in.gen, &in.started); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan write intent: %w", err)
		}
		stale = append(stale, in)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate write intents: %w", err)
	}
	rows.Close()

	removed := 0
	for _, in := range stale {
		func() {
			unlock := s.locks.lock(in.slug)
			defer unlock()
			res, err := s.db.Exec(`DELETE FROM pending_writes WHERE slug = ? AND gen = ? AND started_at = ?`,
				in.slug, in.gen, in.started)
			if err != nil {
				return
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				return
			}
			for _, f := range []struct {
				path    string
				blobGen string
				srcGen  string
			}{
				{s.blobFile(in.slug, in.gen), in.gen, noSourceGen},
				{s.sourceFile(in.slug, in.gen), noSourceGen, in.gen},
			} {
				referenced, err := s.genReferenced(in.slug, f.blobGen, f.srcGen)
				if err != nil || referenced {
					continue
				}
				if gone, err := s.removeLiveFile(f.path); err != nil {
					log.Printf("store: could not remove an abandoned write's file: %s", logsafe.Err(err, in.slug))
				} else if gone {
					removed++
				}
			}
		}()
	}
	return removed, nil
}

// removeOrphan removes path (slug's file at generation gen) if it is an
// orphan. The checks run in an order that is safe against a concurrent writer
// in any process: first the write intent (a live intent means the write is in
// flight; a stale one is reclaimed by deleting it, after which that writer can
// no longer commit), then the row reference. A writer can only make gen
// referenced while its intent exists, so once the intent is absent the
// reference check cannot be overtaken. A failure is logged and left for the
// next pass.
func (s *Store) removeOrphan(path, slug, gen string, source bool, now time.Time) bool {
	unlock := s.locks.lock(slug)
	defer unlock()
	var started int64
	err := s.db.QueryRow(`SELECT started_at FROM pending_writes WHERE slug = ? AND gen = ?`, slug, gen).Scan(&started)
	switch {
	case err == nil:
		if now.Sub(time.Unix(started, 0)) < staleWriteAge {
			return false // a write in flight
		}
		res, err := s.db.Exec(`DELETE FROM pending_writes WHERE slug = ? AND gen = ? AND started_at = ?`, slug, gen, started)
		if err != nil {
			return false
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return false // the writer committed or aborted meanwhile
		}
	case errors.Is(err, sql.ErrNoRows):
		if fi, err := os.Lstat(path); err != nil || !fi.ModTime().Before(now.Add(-orphanGrace)) {
			return false
		}
	default:
		return false
	}
	blobGen, sourceGen := gen, noSourceGen
	if source {
		blobGen, sourceGen = noSourceGen, gen
	}
	referenced, err := s.genReferenced(slug, blobGen, sourceGen)
	if err != nil || referenced {
		return false
	}
	if _, err := s.removeLiveFile(path); err != nil {
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
