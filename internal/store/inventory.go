// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"os"
	"time"
)

// OwnerArtifacts returns a page of unexpired artifacts, including private ones,
// in slug order. It uses existing metadata without changing the store format.
func (s *Store) OwnerArtifacts(ctx context.Context, owner, after string, limit int) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT slug, filename, content_type, size, created_at, owner, private, password_hash, expires_at, series, render_spec, replies
 FROM artifacts WHERE owner = ? AND slug > ? AND (expires_at = 0 OR expires_at > ?)
 ORDER BY slug LIMIT ?`, owner, after, time.Now().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanArtifacts(rows)
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
