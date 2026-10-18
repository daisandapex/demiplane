// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/daisandapex/demiplane/internal/logsafe"
)

// siteswap.go makes replacing a site crash-safe.
//
// Commit replaces a site with two renames: the live tree to a backup name
// (".tmp-old-…"), then the staging tree to the site's name. Between them the
// backup is the only committed copy, and it carries a ".tmp-" name, which
// the scavenger treats as disposable. So before the first rename Commit
// records the swap durably in meta.db (site, backup, staging), and it
// deletes the record only once both renames are on disk (the sites
// directory is fsynced). Open and Reconcile recover every outstanding swap
// before they scavenge:
//
//   - the site exists: the swap finished, or never moved anything; the
//     record is dropped and any backup or staging tree is left to the
//     scavenger as usual;
//   - the site is missing and its backup exists: the backup is moved back,
//     restoring the last committed site (the replace never reported
//     success);
//   - the site and its backup are missing and the staging tree exists: it
//     is complete (Commit only runs on a fully staged tree), so it is moved
//     into place.
//
// The scavenger never removes a tree an outstanding record names, so a
// record that cannot be recovered yet (a rename that keeps failing) keeps
// its trees until it can.

const siteSwapsSchema = `
CREATE TABLE IF NOT EXISTS site_swaps (
    site    TEXT PRIMARY KEY,
    backup  TEXT NOT NULL,
    staging TEXT NOT NULL
);`

// recordSwap durably records that site is about to be swapped: its live
// tree to backup and the staging tree into its place (base names under
// sites/).
func (s *Store) recordSwap(site, backup, staging string) error {
	if _, err := s.db.Exec(
		`INSERT OR REPLACE INTO site_swaps (site, backup, staging) VALUES (?, ?, ?)`,
		site, backup, staging); err != nil {
		return fmt.Errorf("record site swap: %w", err)
	}
	return nil
}

func (s *Store) clearSwap(site string) error {
	if _, err := s.db.Exec(`DELETE FROM site_swaps WHERE site = ?`, site); err != nil {
		return fmt.Errorf("clear site swap: %w", err)
	}
	return nil
}

// syncDir fsyncs a directory, making the renames inside it durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// swapNames returns the backup and staging names of the outstanding swaps,
// which the scavenger must leave alone.
func (s *Store) swapNames() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT backup, staging FROM site_swaps`)
	if err != nil {
		return nil, fmt.Errorf("read site swaps: %w", err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var b, st string
		if err := rows.Scan(&b, &st); err != nil {
			return nil, fmt.Errorf("read site swaps: %w", err)
		}
		names[b], names[st] = true, true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read site swaps: %w", err)
	}
	return names, nil
}

// recoverSiteSwaps finishes or rolls back every outstanding site swap (see
// above). A swap it cannot recover yet is logged and kept for the next try.
func (s *Store) recoverSiteSwaps() error {
	type swap struct{ site, backup, staging string }
	rows, err := s.db.Query(`SELECT site, backup, staging FROM site_swaps`)
	if err != nil {
		return fmt.Errorf("read site swaps: %w", err)
	}
	var todo []swap
	for rows.Next() {
		var sw swap
		if err := rows.Scan(&sw.site, &sw.backup, &sw.staging); err != nil {
			rows.Close()
			return fmt.Errorf("read site swaps: %w", err)
		}
		todo = append(todo, sw)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read site swaps: %w", err)
	}
	rows.Close()
	for _, sw := range todo {
		if err := s.recoverSiteSwap(sw.site, sw.backup, sw.staging); err != nil {
			log.Printf("store: could not recover an interrupted site replace, will retry: %s", logsafe.Err(err))
		}
	}
	return nil
}

func (s *Store) recoverSiteSwap(site, backup, staging string) error {
	// Commit holds the same lock across its swap, so none is in progress.
	unlock := s.locks.lock(siteLockKey(site))
	defer unlock()
	root := s.siteRoot()
	final := filepath.Join(root, site)
	exists := func(p string) bool {
		_, err := os.Lstat(p)
		return err == nil
	}
	var from string
	switch {
	case exists(final):
		return s.clearSwap(site)
	case exists(filepath.Join(root, backup)):
		from = backup
	case exists(filepath.Join(root, staging)) && !s.stagingLive(staging):
		from = staging
	default:
		log.Printf("store: an interrupted site replace left nothing to restore")
		return s.clearSwap(site)
	}
	// Either tree is already counted as live (an unowned staging-named tree
	// is measured like a site), so the rename changes no totals; it is one
	// transition so a recount does not walk it halfway.
	s.beginTransition()
	err := siteRename(filepath.Join(root, from), final)
	s.endTransition(0, 0)
	if err != nil {
		return fmt.Errorf("restore site: %w", err)
	}
	if err := syncDir(root); err != nil {
		return fmt.Errorf("restore site: %w", err)
	}
	return s.clearSwap(site)
}
