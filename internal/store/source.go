// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// Shape: source bytes are a sidecar blob under <root>/sources/<slug>, alongside
// the baked artifact in <root>/blobs/<slug>, and the render parameters that
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
}

// sourcePath is the sidecar path for slug. Only ever called with a slug that
// already has a metadata row, which means it passed slug validation on the way
// in — a caller-supplied path segment never reaches the filesystem unresolved.
func (s *Store) sourcePath(slug string) string {
	return filepath.Join(s.sourceDir, slug)
}

// writeSource stores body as slug's retained source, atomically (temp + rename)
// so a crash mid-write cannot leave a truncated source behind a valid artifact.
func (s *Store) writeSource(slug string, body []byte) error {
	tmp, err := os.CreateTemp(s.sourceDir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp source: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write source: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close source: %w", err)
	}
	if err := os.Rename(tmpName, s.sourcePath(slug)); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("commit source: %w", err)
	}
	return nil
}

// removeSource deletes slug's retained source if one exists. A missing sidecar
// is success: most artifacts never had one.
func (s *Store) removeSource(slug string) error {
	if err := os.Remove(s.sourcePath(slug)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove source: %w", err)
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
		created int64
		spec    string
	)
	err := s.db.QueryRow(
		`SELECT created_at, render_spec FROM artifacts WHERE slug = ?`, slug,
	).Scan(&created, &spec)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceRecord{}, ErrNotFound
	}
	if err != nil {
		return SourceRecord{}, fmt.Errorf("lookup source metadata: %w", err)
	}

	body, err := os.ReadFile(s.sourcePath(slug))
	if err != nil {
		if os.IsNotExist(err) {
			return SourceRecord{}, ErrNoSource
		}
		return SourceRecord{}, fmt.Errorf("read source: %w", err)
	}
	return SourceRecord{
		Slug:      slug,
		Body:      body,
		Spec:      spec,
		CreatedAt: time.Unix(created, 0).UTC(),
	}, nil
}

// Slugs lists owner's non-expired artifacts, oldest publish first. Unlike List
// it includes PRIVATE artifacts and returns names only: it exists for
// maintenance sweeps over everything stored (the bulk rebake), where excluding
// private pages would quietly leave them on an old rendering forever, and where
// nothing is being shown to a reader.
func (s *Store) Slugs(owner string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT slug FROM artifacts
		  WHERE owner = ? AND (expires_at = 0 OR expires_at > ?)
		  ORDER BY created_at ASC, slug ASC`,
		owner, time.Now().Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("list slugs: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, fmt.Errorf("scan slug: %w", err)
		}
		out = append(out, slug)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate slugs: %w", err)
	}
	return out, nil
}

// ReplaceBody swaps an existing artifact's bytes in place and returns its
// updated metadata. Only the size changes: slug, filename, content-type, owner,
// privacy, password, expiry, series, and publish time all survive.
//
// This is deliberately NOT Put with a named slug. Put is a publish — it resets
// the password, the TTL, and created_at from the incoming request, which is
// right for a publish and wrong for a rebake: re-rendering a page must not
// silently unlock a password-gated artifact, extend an expiring one, or move it
// to the top of the gallery. A rebake changes the rendering, nothing else.
func (s *Store) ReplaceBody(slug string, r io.Reader) (Artifact, error) {
	art, f, err := s.Get(slug)
	if err != nil {
		return Artifact{}, err
	}
	// Only the existence check was wanted; the bytes are about to be replaced.
	f.Close()

	_, size, err := s.writeBlob(slug, r)
	if err != nil {
		return Artifact{}, err
	}
	if _, err := s.db.Exec(`UPDATE artifacts SET size = ? WHERE slug = ?`, size, slug); err != nil {
		return Artifact{}, fmt.Errorf("update size: %w", err)
	}
	art.Size = size
	// Same notification a publish fires, so a ?live tab watching this slug picks
	// the new rendering up without a manual reload.
	s.notify(slug)
	return art, nil
}
