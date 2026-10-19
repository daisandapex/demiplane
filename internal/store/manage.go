// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

var ErrPinned = errors.New("artifact is pinned; unpin before setting a ttl")
var ErrChanged = errors.New("revision changed or artifact pinned")

// SetDefaultTTL persists the serve policy for all writers, including SSH receive.
func (s *Store) SetDefaultTTL(d time.Duration) error {
	if d < 0 || (d != 0 && (d < time.Minute || d > 365*24*time.Hour)) {
		return &InputError{Msg: "default ttl must be 0 or between 1m and 365d"}
	}
	_, err := s.db.Exec(`INSERT INTO management_policy(id,default_ttl) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET default_ttl=excluded.default_ttl`, int64(d))
	return err
}

// Patch updates metadata under a database write lock, also shared with other
// processes. Expired rows are never revived. A pin's zero expiry is understood
// by older sweepers even though those writers cannot enforce pin policy.
func (s *Store) Patch(slug string, ttl *time.Duration, pin *bool) (Artifact, error) {
	if err := ValidateNamedSlug(slug); err != nil {
		return Artifact{}, err
	}
	if ttl != nil && (*ttl < 0 || (*ttl != 0 && (*ttl < time.Minute || *ttl > 365*24*time.Hour))) {
		return Artifact{}, &InputError{Msg: "ttl must be 0 or between 1m and 365d"}
	}
	unlock := s.locks.lock(slug)
	defer unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Artifact{}, err
	}
	defer tx.Rollback()
	// Open configures immediate transactions, so Begin already owns the writer lock.
	var pinned bool
	var expires int64
	err = tx.QueryRow(`SELECT pinned, expires_at FROM artifacts WHERE slug=? AND (expires_at=0 OR expires_at>?)`, slug, time.Now().Unix()).Scan(&pinned, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, ErrNotFound
	}
	if err != nil {
		return Artifact{}, err
	}
	if pin != nil {
		pinned = *pin
	}
	if pinned && ttl != nil {
		return Artifact{}, ErrPinned
	}
	if ttl != nil {
		expires = 0
		if *ttl > 0 {
			expires = time.Now().Add(*ttl).Unix()
		}
	}
	if pinned {
		expires = 0
	}
	if ttl != nil || pin != nil {
		if _, err = tx.Exec(`UPDATE artifacts SET pinned=?, expires_at=?, writer_tag=lower(hex(randomblob(16))) WHERE slug=?`, pinned, expires, slug); err != nil {
			return Artifact{}, err
		}
	}
	var revision string
	a, err := scanArtifact(tx.QueryRow(`SELECT slug,filename,content_type,size,created_at,owner,private,password_hash,expires_at,series,render_spec,replies,pinned,writer_tag FROM artifacts WHERE slug=?`, slug), &pinned, &revision)
	// Read the revision explicitly into the returned record (scanArtifact's extra
	// targets cannot point into a value that it has not returned yet).
	if err != nil {
		return Artifact{}, err
	}
	a.Pinned = pinned
	a.Revision = revision
	if err = tx.Commit(); err != nil {
		return Artifact{}, err
	}
	return a, nil
}

// DeleteRevision compares the owner revision and pin in the DELETE statement.
// The durable deletion queue removes exactly that row's body and source.
func (s *Store) DeleteRevision(slug, revision string, includePinned bool) error {
	unlock := s.locks.lock(slug)
	defer unlock()
	bg, sg, err := s.deleteRow(slug, `DELETE FROM artifacts WHERE slug=? AND writer_tag=? AND (pinned=0 OR ?) RETURNING blob_gen,source_gen`, slug, revision, includePinned)
	if errors.Is(err, sql.ErrNoRows) {
		exists, e := s.exists(slug)
		if e != nil {
			return e
		}
		if !exists {
			return ErrNotFound
		}
		return ErrChanged
	}
	if err != nil {
		return err
	}
	s.finishDeletion(slug, bg, sg)
	s.removed(slug)
	return nil
}

type SiteInventory struct {
	Revision  string
	Size      int64
	CreatedAt time.Time
}

// SiteMetadata and conditional deletion hold the same lock as SiteWriter.Commit.
// Only the serving process writes sites (the serve lock excludes other writers).
func (s *Store) SiteMetadata(site string) (SiteInventory, error) {
	if err := ValidateNamedSlug(site); err != nil {
		return SiteInventory{}, err
	}
	unlock := s.locks.lock(siteLockKey(site))
	defer unlock()
	return s.siteMetadataLocked(site)
}
func (s *Store) siteMetadataLocked(site string) (SiteInventory, error) {
	var out SiteInventory
	dir := filepath.Join(s.siteRoot(), site)
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if !fi.IsDir() {
		return out, ErrNotFound
	}
	// Tree identity changes on every supported publish, even same-size same-time
	// replacements. Hashing hides host filesystem details from the wire protocol.
	out.Revision = fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%v:%d", siteFileIdentity(fi), fi.ModTime().UnixNano()))))
	out.CreatedAt = fi.ModTime().UTC()
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type().IsRegular() {
			info, e := d.Info()
			if e != nil {
				return e
			}
			out.Size += info.Size()
		}
		return nil
	})
	return out, err
}
func (s *Store) DeleteSiteRevision(site, revision string) error {
	if err := ValidateNamedSlug(site); err != nil {
		return err
	}
	unlock := s.locks.lock(siteLockKey(site))
	defer unlock()
	meta, err := s.siteMetadataLocked(site)
	if err != nil {
		return err
	}
	if meta.Revision != revision {
		return ErrChanged
	}
	return s.deleteSiteLocked(site)
}
