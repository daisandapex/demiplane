// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import "fmt"

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
	_, n, err := treeStats(s.siteRoot())
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
