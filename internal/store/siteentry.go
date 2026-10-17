// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daisandapex/demiplane/internal/logsafe"
)

// siteentry.go bounds how many filesystem entries the published sites hold.
//
// A site's files and directories cost an inode and a directory entry each,
// whatever their size, so archives of empty files under fresh paths and fresh
// site names would otherwise cost nothing. Every site file and directory,
// each site's own directory included, is a site entry. An entry is charged
// before it is created, while the archive is staged, so one archive cannot
// overshoot; it moves to the live count when the site commits and leaves it
// when the site is replaced or deleted. Two limits apply:
//
//   - With a byte quota (SetQuota), each entry also counts metaAllowance
//     bytes on top of its file's size.
//   - With or without one, the store holds at most the site entry limit
//     (DefaultSiteEntryLimit, SetSiteEntryLimit), counting staged trees.
//
// The live count is measured when the store opens and again by SetQuota and
// RecountUsage. Only the server writes sites, so no other process changes it.
//
// A staging tree (".tmp-" under sites/) is charged in flight while its writer
// in this process owns it. Any other one, left by a crash or by a cleanup
// that failed, and a replaced site's tree that could not be removed, is
// abandoned: measured and kept charged like a live site, and removed by
// Open and Reconcile once it is older than orphanGrace. Every removal
// releases exactly what it removed.

// DefaultSiteEntryLimit is the default cap on site files and directories
// across all sites, staged trees included.
const DefaultSiteEntryLimit = 100_000

// ErrSiteEntries means a site publish would take the store past its site
// entry limit. It is an ErrQuota (a capacity condition; 507 over HTTP).
var ErrSiteEntries = fmt.Errorf("%w: the sites hold the maximum number of files and directories", ErrQuota)

// SetSiteEntryLimit sets the cap on site files and directories across all
// sites; n <= 0 restores DefaultSiteEntryLimit. Call it at startup.
func (s *Store) SetSiteEntryLimit(n int64) {
	if n <= 0 {
		n = DefaultSiteEntryLimit
	}
	s.quota.mu.Lock()
	s.quota.entryLimit = n
	s.quota.mu.Unlock()
}

// measureSiteEntries counts the live site entries (Open).
func (s *Store) measureSiteEntries() error {
	_, n, err := s.siteStats()
	if err != nil {
		return fmt.Errorf("count site entries: %w", err)
	}
	s.quota.mu.Lock()
	s.quota.entries = n
	s.quota.mu.Unlock()
	return nil
}

// chargeSiteEntries reserves n staged site entries, failing with
// ErrSiteEntries when live plus staged entries would pass the limit.
func (s *Store) chargeSiteEntries(n int64) error {
	q := &s.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	limit := q.entryLimit
	if limit <= 0 {
		limit = DefaultSiteEntryLimit
	}
	if q.entries+q.entriesInflight+n > limit {
		return ErrSiteEntries
	}
	q.entriesInflight += n
	return nil
}

// releaseSiteEntries returns n staged site entries that were not committed.
func (s *Store) releaseSiteEntries(n int64) {
	if n <= 0 {
		return
	}
	q := &s.quota
	q.mu.Lock()
	q.entriesInflight = max(q.entriesInflight-n, 0)
	q.mu.Unlock()
}

// siteStats measures the sites: the bytes of their regular files and their
// entries, abandoned staging trees included and the trees of writers in this
// process excluded (those are charged in flight).
func (s *Store) siteStats() (size, entries int64, err error) {
	root := s.siteRoot()
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), tempPrefix) && s.stagingLive(e.Name()) {
			continue
		}
		p := filepath.Join(root, e.Name())
		if _, err := os.Lstat(p); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, 0, err
		}
		n, c, err := treeStats(p)
		if err != nil {
			return 0, 0, err
		}
		size += n
		entries += c + 1 // the top entry itself
	}
	return size, entries, nil
}

// registerStaging and unregisterStaging mark a staging tree under sites/ as
// owned by a writer in this process (by its base name), or not.
func (s *Store) registerStaging(name string) {
	s.quota.mu.Lock()
	if s.quota.staging == nil {
		s.quota.staging = make(map[string]struct{})
	}
	s.quota.staging[name] = struct{}{}
	s.quota.mu.Unlock()
}

func (s *Store) unregisterStaging(name string) {
	s.quota.mu.Lock()
	delete(s.quota.staging, name)
	s.quota.mu.Unlock()
}

func (s *Store) stagingLive(name string) bool {
	s.quota.mu.Lock()
	defer s.quota.mu.Unlock()
	_, ok := s.quota.staging[name]
	return ok
}

// reclaimStagingTrees removes the abandoned staging trees under sites/ older
// than orphanGrace as of now, never one a writer in this process owns, and
// releases exactly what it removed. It returns how many entries it removed.
func (s *Store) reclaimStagingTrees(now time.Time) (int, error) {
	root := s.siteRoot()
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("scan site staging trees: %w", err)
	}
	cutoff := now.Add(-orphanGrace)
	removed := 0
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), tempPrefix) || s.stagingLive(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		s.beginTransition()
		r, err := s.removeTreeCounted(filepath.Join(root, e.Name()))
		s.endChange(r.change())
		removed += int(r.entries)
		if err != nil {
			log.Printf("store: could not remove an abandoned site staging tree, will retry: %s", logsafe.Err(err))
		}
	}
	return removed, nil
}
