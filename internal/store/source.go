// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// source.go keeps the ORIGINAL document a rendered artifact was baked from, so a
// renderer or theme change can rebake it later.
//
// Why this exists: ?render=md turns markdown into HTML at publish time and, until
// now, the store kept only the HTML. The markdown lived wherever the publisher
// happened to have it — a scratch file, a pipe, an agent's context — so upgrading
// the renderer meant hunting down every source by hand. During the 2026-08-30
// render overhaul five pages had no surviving source and their original text is
// gone for good. That is data loss, not a missing feature, and the fix is for the
// store to keep the input it was given.
//
// Shape: source bytes are a sidecar file under <root>/sources/, alongside the
// baked artifact under <root>/blobs/ (both named by generation, see
// generation.go), and the render parameters that
// produced the page are an opaque string in the artifacts row (render_spec). The
// store deliberately does not parse that string — it is the render caller's
// business (internal/server owns the schema), which keeps the store ignorant of
// rendering exactly as it is today.
//
// Lifecycle invariant: source follows the artifact. A re-publish that carries no
// source (plain HTML overwriting a rendered page at the same slug) DROPS the
// stale source rather than leaving a sidecar describing bytes that no longer
// exist; delete and TTL reaping remove it too.
//
// Legacy artifacts published before this existed simply have no sidecar. That is
// a normal, expected state — never an error. Callers get ErrNoSource and are
// expected to skip.

// sourceDirName is the sidecar directory under the store root.
const sourceDirName = "sources"

// beforeSourceRead, when set by a test, runs between Source's row read and its
// file read, to place a concurrent write in that window.
var beforeSourceRead func()

// ErrNoSource means the artifact exists but no source was retained for it —
// either it was never a rendered publish, or it predates source retention.
// Callers treat this as "nothing to do", not as a failure.
var ErrNoSource = errors.New("no source retained for artifact")

// SourceRecord is a retained document plus what is needed to rebake it.
type SourceRecord struct {
	Slug      string
	Body      []byte    // the original document bytes (markdown)
	Spec      string    // opaque render parameters captured at publish
	CreatedAt time.Time // the artifact's publish time, for a stable colophon
	// PublishID is the publish the source belongs to (Artifact.PublishID); a
	// rebake keeps it.
	PublishID string
	// Version is the artifact's bytes version when the source was read, so a
	// rebake can refuse to overwrite bytes that changed meanwhile.
	Version string
}

// writeSource stores body at path (a file in the source directory) atomically
// (temp + rename) so a crash mid-write cannot leave a truncated source behind a
// valid artifact. Only called with a path built from a slug that passed
// validation, so a caller-supplied path segment never reaches the filesystem.
func (s *Store) writeSource(path string, body []byte) error {
	tmp, err := s.createTemp(s.sourceDir, tempPattern(path))
	if err != nil {
		return fmt.Errorf("create temp source: %w", err)
	}
	tmpName := tmp.Name()
	cw := &chargedWriter{s: s, w: tmp}
	if _, err := cw.Write(body); err != nil {
		tmp.Close()
		s.discardTemp(tmpName, cw.charged)
		return fmt.Errorf("write source: %w", err)
	}
	if err := tmp.Close(); err != nil {
		s.discardTemp(tmpName, cw.charged)
		return fmt.Errorf("close source: %w", err)
	}
	if err := s.commitRename(tmpName, path, cw.charged); err != nil {
		s.discardTemp(tmpName, cw.charged)
		return fmt.Errorf("commit source: %w", err)
	}
	return nil
}

// Source returns the retained source for slug. It returns ErrNotFound when no
// such artifact exists and ErrNoSource when the artifact exists but carries no
// retained source (the legacy case).
//
// The metadata row is read FIRST and the sidecar is opened only for a slug that
// has one, so an attacker-supplied path can never be joined onto the source
// directory: a row exists only for a slug that passed validation at publish.
func (s *Store) Source(slug string) (SourceRecord, error) {
	var (
		created   int64
		spec      string
		blobGen   string
		sourceGen string
		publishID string
	)
	err := s.db.QueryRow(
		`SELECT created_at, render_spec, blob_gen, source_gen, publish_id FROM artifacts WHERE slug = ?`, slug,
	).Scan(&created, &spec, &blobGen, &sourceGen, &publishID)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceRecord{}, ErrNotFound
	}
	if err != nil {
		return SourceRecord{}, fmt.Errorf("lookup source metadata: %w", err)
	}
	var body []byte
	for attempt := 1; ; attempt++ {
		if sourceGen == noSourceGen {
			return SourceRecord{}, ErrNoSource
		}
		if beforeSourceRead != nil {
			beforeSourceRead()
		}
		body, err = os.ReadFile(s.sourceFile(slug, sourceGen))
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return SourceRecord{}, fmt.Errorf("read source: %w", err)
		}
		// Either there is no retained source (a legacy artifact), or another
		// process committed a write after the row read and removed the
		// generation that row named. Only a changed row means the latter.
		prev := sourceGen
		err = s.db.QueryRow(
			`SELECT created_at, render_spec, blob_gen, source_gen, publish_id FROM artifacts WHERE slug = ?`, slug,
		).Scan(&created, &spec, &blobGen, &sourceGen, &publishID)
		if errors.Is(err, sql.ErrNoRows) {
			return SourceRecord{}, ErrNotFound
		}
		if err != nil {
			return SourceRecord{}, fmt.Errorf("lookup source metadata: %w", err)
		}
		if sourceGen == prev || attempt == getAttempts {
			return SourceRecord{}, ErrNoSource
		}
	}
	return SourceRecord{
		Slug:      slug,
		Body:      body,
		Spec:      spec,
		CreatedAt: time.Unix(created, 0).UTC(),
		Version:   blobGen,
		PublishID: publishID,
	}, nil
}

const qSlugPage = `SELECT slug, expires_at FROM artifacts
  WHERE owner = ? AND slug > ?
  ORDER BY slug ASC LIMIT ?`

// SlugPage returns, in slug order, up to limit of owner's unexpired slugs
// after the slug after ("" = from the first). Unlike List it includes PRIVATE
// artifacts and returns names only: it exists for maintenance sweeps over
// everything stored (the bulk rebake), where excluding private pages would
// quietly leave them on an old rendering forever, and where nothing is shown
// to a reader. It examines at most limit index entries, skipping expired ones,
// so a page may be short; next is the slug to continue after, or "" when the
// walk is done.
func (s *Store) SlugPage(ctx context.Context, owner, after string, limit int) (slugs []string, next string, err error) {
	if limit < 1 {
		limit = 1
	}
	rows, err := s.db.QueryContext(ctx, qSlugPage, owner, after, limit)
	if err != nil {
		return nil, "", fmt.Errorf("list slugs: %w", err)
	}
	defer rows.Close()
	now := time.Now().Unix()
	examined := 0
	last := ""
	for rows.Next() {
		var slug string
		var expires int64
		if err := rows.Scan(&slug, &expires); err != nil {
			return nil, "", fmt.Errorf("scan slug: %w", err)
		}
		examined++
		last = slug
		if !expired(expires, now) {
			slugs = append(slugs, slug)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate slugs: %w", err)
	}
	if examined == limit {
		next = last
	}
	return slugs, next, nil
}

// ReplaceBody swaps an existing artifact's bytes in place and returns its
// updated metadata. Only the bytes, size, version and modified time change:
// slug, filename, content-type, owner, privacy, password, expiry, series, and
// publish time all survive.
//
// This is deliberately NOT Put with a named slug. Put is a publish — it resets
// the password, the TTL, and created_at from the incoming request, which is
// right for a publish and wrong for a rebake: re-rendering a page must not
// silently unlock a password-gated artifact, extend an expiring one, or move it
// to the top of the gallery. A rebake changes the rendering, nothing else.
func (s *Store) ReplaceBody(slug string, r io.Reader) (Artifact, error) {
	unlock := s.locks.lock(slug)
	defer unlock()
	return s.replaceBodyLocked(slug, nil, r)
}

// rebakeAttempts bounds how often Rebake starts over after another process
// changed the artifact between its source read and its write.
const rebakeAttempts = 3

// ErrConcurrentWrite means a rebake kept losing to concurrent writes of the
// same artifact and gave up; retrying later is safe.
var ErrConcurrentWrite = errors.New("artifact kept changing during the rebake")

// Rebake re-renders slug from its retained source and swaps the result in as
// ReplaceBody does. It holds slug's write lock from the source read to the body
// write, so a publish cannot land in between and be overwritten by a rendering
// of the older source. The write is also conditional on the bytes version the
// source was read with, which covers a publish from another process; on a
// mismatch the rebake starts over from the new source. It returns ErrNotFound or
// ErrNoSource as Source does, and whatever error render returns.
func (s *Store) Rebake(slug string, render func(SourceRecord) ([]byte, error)) (Artifact, error) {
	unlock := s.locks.lock(slug)
	defer unlock()
	for i := 0; i < rebakeAttempts; i++ {
		rec, err := s.Source(slug)
		if err != nil {
			return Artifact{}, err
		}
		body, err := render(rec)
		if err != nil {
			return Artifact{}, err
		}
		art, err := s.replaceBodyLocked(slug, &rec.Version, bytes.NewReader(body))
		if errors.Is(err, errGenConflict) {
			continue
		}
		return art, err
	}
	return Artifact{}, ErrConcurrentWrite
}

// swapBody points slug's row at blob generation gen if it still names cur,
// recording cur as a pending deletion in the same transaction. It returns
// sql.ErrNoRows when the row is gone or names another generation.
func (s *Store) swapBody(slug, gen, cur string, size int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := endWrite(tx, slug, gen); err != nil {
		return 0, err
	}
	var modified int64
	if err := tx.QueryRow(
		`UPDATE artifacts SET size = ?, blob_gen = ?, modified_at = ?
		  WHERE slug = ? AND blob_gen = ? RETURNING modified_at`,
		size, gen, time.Now().Unix(), slug, cur,
	).Scan(&modified); err != nil {
		return 0, err
	}
	queued, err := queueDeletion(tx, slug, cur, noSourceGen, true)
	if err != nil {
		return 0, err
	}
	// The pending deletion takes its allowance (queueDeletion).
	var charged int64
	if queued {
		if err := s.chargeQueued(); err != nil {
			return 0, err
		}
		charged = metaAllowance
	}
	s.beginTransition()
	if err := tx.Commit(); err != nil {
		s.endTransition(0, 0)
		s.release(charged)
		return 0, err
	}
	if queued {
		s.countPending(pendingKey{slug, cur, noSourceGen})
	}
	s.endTransition(charged, 0)
	return modified, nil
}

// replaceBodyLocked writes r as slug's bytes at a fresh generation and points
// the row at it. With expect set, the row is updated only if it still names
// that version (errGenConflict otherwise). The caller holds slug's lock.
func (s *Store) replaceBodyLocked(slug string, expect *string, r io.Reader) (Artifact, error) {
	art, err := s.lookup(slug)
	if err != nil {
		return Artifact{}, err
	}
	if !art.ExpiresAt.IsZero() && !art.ExpiresAt.After(time.Now()) {
		return Artifact{}, ErrNotFound
	}
	cur := art.Version
	if expect != nil && *expect != cur {
		return Artifact{}, errGenConflict
	}
	gen, err := newGen()
	if err != nil {
		return Artifact{}, err
	}
	if err := s.beginWrite(slug, gen); err != nil {
		return Artifact{}, err
	}
	_, size, err := s.writeBlob(s.blobFile(slug, gen), r)
	if err != nil {
		s.abortWrite(slug, gen, noSourceGen)
		return Artifact{}, err
	}
	// The modified time is the wall clock, never ahead of it. A rebake in the
	// same second as the publish keeps the same Last-Modified; the new version
	// (ETag) is what marks the change.
	modified, err := s.swapBody(slug, gen, cur, size)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.abortWrite(slug, gen, noSourceGen)
		return Artifact{}, fmt.Errorf("update body: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		// Deleted or republished by another process since the lookup.
		s.abortWrite(slug, gen, noSourceGen)
		if _, lerr := s.lookup(slug); errors.Is(lerr, ErrNotFound) {
			return Artifact{}, ErrNotFound
		}
		return Artifact{}, errGenConflict
	}
	s.dropReplacedGen(slug, cur, noSourceGen)
	art.Size = size
	art.Version = gen
	art.ModifiedAt = time.Unix(modified, 0).UTC()
	// Same notification a publish fires, so a ?live tab watching this slug picks
	// the new rendering up without a manual reload.
	s.notify(slug)
	return art, nil
}
