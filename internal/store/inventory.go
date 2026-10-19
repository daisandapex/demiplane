// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"os"
	"time"
)

// OwnerArtifact pairs inventory metadata with a continuation ID that contains no slug.
type OwnerArtifact struct {
	Artifact
	RowID int64
}

// OwnerArtifacts returns a page of unexpired artifacts, including private ones,
// in rowid order. It uses existing metadata without changing the store format.
func (s *Store) OwnerArtifacts(ctx context.Context, owner string, after int64, limit int) ([]OwnerArtifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT slug, filename, content_type, size, created_at, owner, private, password_hash, expires_at, series, render_spec, replies, rowid
 FROM artifacts WHERE owner = ? AND rowid > ? AND (expires_at = 0 OR expires_at > ?)
 ORDER BY rowid LIMIT ?`, owner, after, time.Now().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OwnerArtifact
	for rows.Next() {
		var a OwnerArtifact
		a.Artifact, err = scanArtifact(rows, &a.RowID)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListSites lists published directory names in lexical order. Staging trees,
// backups, tombstones and symlinks are not published sites. Site file contents
// are never traversed. Site count is bounded by the existing site entry limit.
func (s *Store) ListSites() ([]string, error) {
	entries, err := os.ReadDir(s.siteRoot())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && ValidateNamedSlug(e.Name()) == nil {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
