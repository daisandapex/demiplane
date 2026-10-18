// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/daisandapex/demiplane/internal/logsafe"
)

// siteswap.go makes replacing a site crash-safe.
//
// Commit replaces a site with two renames: the live tree to a backup name
// (".tmp-old-…"), then the staging tree to the site's name. (A first
// publish has only the second, and its record names no backup.) Between them the
// backup is the only committed copy, and it carries a ".tmp-" name, which
// the scavenger treats as disposable. So before the first rename Commit
// records the swap durably in meta.db (site, a fresh id, backup, staging),
// and it deletes that record, by id, only once both renames are on disk
// (the sites directory is fsynced). A swap is recovered under the site's
// lock, rereading the record and acting only on the one it read:
//
//   - the site exists: the swap finished, or never moved anything; the
//     record is dropped and any backup or staging tree is left to the
//     scavenger as usual;
//   - the site is missing and its backup exists: the backup is moved back,
//     restoring the last committed site (the replace never reported
//     success);
//   - the site and its backup are missing and the staging tree exists: it
//     is complete (Commit only runs on a fully staged tree), so it is moved
//     into place;
//   - nothing exists: the record is dropped.
//
// A path that cannot be inspected (any error but absence) keeps the record
// for the next try. Open and Reconcile recover every outstanding swap before
// they scavenge, and Commit and DeleteSite settle the site's own swap first,
// so a later publish or delete never leaves an older copy recoverable. The
// scavenger never removes a tree an outstanding record names.
//
// Only the serving process recovers swaps, scavenges site trees and writes
// sites, so the per-site lock (siteLockKey) orders every one of those steps.
// Another process sharing the store (`demiplane receive`) opens it with
// OpenShared, which leaves sites/ alone.

// OpenShared opens the store for a process that shares it with a running
// server (`demiplane receive`). It defers deletions to the server
// (DeferDeletions) and leaves sites/ to it: no interrupted site replace is
// recovered and no site staging tree is scavenged, at open or by Reconcile,
// since only the server can tell which ones are in progress.
func OpenShared(dir string) (*Store, error) {
	return open(dir, true)
}

const siteSwapsSchema = `
CREATE TABLE IF NOT EXISTS site_swaps (
    site    TEXT PRIMARY KEY,
    id      TEXT NOT NULL DEFAULT '',
    backup  TEXT NOT NULL,
    staging TEXT NOT NULL
);`

// migrateSiteSwaps creates the swap table, adding the id column to one made
// before it had one.
func (s *Store) migrateSiteSwaps() error {
	if _, err := s.db.Exec(siteSwapsSchema); err != nil {
		return fmt.Errorf("migrate site swaps: %w", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('site_swaps') WHERE name = 'id'`).Scan(&n); err != nil {
		return fmt.Errorf("migrate site swaps: %w", err)
	}
	if n == 0 {
		if _, err := s.db.Exec(`ALTER TABLE site_swaps ADD COLUMN id TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("migrate site swaps: %w", err)
		}
	}
	return nil
}

// siteSwap is an outstanding swap record.
type siteSwap struct{ site, id, backup, staging string }

// recordSwap durably records that site is about to be swapped: its live
// tree to backup and the staging tree into its place (base names under
// sites/). The caller holds the site's lock and has settled any earlier
// swap of it (settleSwapLocked), so none is outstanding.
func (s *Store) recordSwap(sw siteSwap) error {
	if _, err := s.db.Exec(
		`INSERT INTO site_swaps (site, id, backup, staging) VALUES (?, ?, ?, ?)`,
		sw.site, sw.id, sw.backup, sw.staging); err != nil {
		return fmt.Errorf("record site swap: %w", err)
	}
	return nil
}

// clearSwap deletes exactly the swap record sw, never a newer one.
func (s *Store) clearSwap(sw siteSwap) error {
	if clearSwapErr != nil {
		if err := clearSwapErr(); err != nil {
			return fmt.Errorf("clear site swap: %w", err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM site_swaps WHERE site = ? AND id = ?`, sw.site, sw.id); err != nil {
		return fmt.Errorf("clear site swap: %w", err)
	}
	return nil
}

// currentSwap reads site's outstanding swap record, if any.
func (s *Store) currentSwap(site string) (siteSwap, bool, error) {
	sw := siteSwap{site: site}
	err := s.db.QueryRow(`SELECT id, backup, staging FROM site_swaps WHERE site = ?`, site).
		Scan(&sw.id, &sw.backup, &sw.staging)
	if errors.Is(err, sql.ErrNoRows) {
		return siteSwap{}, false, nil
	}
	if err != nil {
		return siteSwap{}, false, fmt.Errorf("read site swap: %w", err)
	}
	return sw, true, nil
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

// afterStagingScan runs after the scavenger has listed the staging trees
// and read the swap records; a test hook.
var afterStagingScan func()

// swapNamed reports whether an outstanding swap names the tree name.
func (s *Store) swapNamed(name string) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM site_swaps WHERE backup = ? OR staging = ?`, name, name).Scan(&n); err != nil {
		return false, fmt.Errorf("read site swaps: %w", err)
	}
	return n > 0, nil
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
		if b != "" {
			names[b] = true
		}
		names[st] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read site swaps: %w", err)
	}
	return names, nil
}

// beforeSwapLock is a test hook run by recovery after it has read the
// outstanding swaps and before it locks each site.
var beforeSwapLock func()

// sitesSync makes the renames in the sites directory durable; a test hook.
// A swap record is cleared only once its outcome is durable, so a power
// loss cannot undo a restore whose record is gone.
var sitesSync = syncDir

// clearSwapErr, when set, makes clearSwap fail with its error; a test hook.
var clearSwapErr func() error

// recoverSiteSwaps settles every outstanding site swap (see above). A swap
// it cannot settle yet is logged and kept for the next try. A shared store
// (OpenShared) leaves them to the server.
func (s *Store) recoverSiteSwaps() error {
	if s.shared {
		return nil
	}
	rows, err := s.db.Query(`SELECT site FROM site_swaps`)
	if err != nil {
		return fmt.Errorf("read site swaps: %w", err)
	}
	var sites []string
	for rows.Next() {
		var site string
		if err := rows.Scan(&site); err != nil {
			rows.Close()
			return fmt.Errorf("read site swaps: %w", err)
		}
		sites = append(sites, site)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read site swaps: %w", err)
	}
	rows.Close()
	if beforeSwapLock != nil {
		beforeSwapLock()
	}
	for _, site := range sites {
		func() {
			unlock := s.locks.lock(siteLockKey(site))
			defer unlock()
			if err := s.settleSwapLocked(site); err != nil {
				log.Printf("store: could not recover an interrupted site replace, will retry: %s", logsafe.Err(err))
			}
		}()
	}
	return nil
}

// settleSwapLocked settles site's outstanding swap record, as it reads it
// now, if there is one. The caller holds the site's lock, which Commit holds
// across its swap, so none is in progress.
func (s *Store) settleSwapLocked(site string) error {
	sw, ok, err := s.currentSwap(site)
	if err != nil || !ok {
		return err
	}
	root := s.siteRoot()
	final := filepath.Join(root, site)
	present := func(name string) (bool, error) {
		_, err := os.Lstat(filepath.Join(root, name))
		if err == nil {
			return true, nil
		}
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect site swap: %w", err)
	}
	have, err := present(site)
	if err != nil {
		return err
	}
	if have {
		// The site is in place, but the rename that put it there may not be
		// durable yet (its sync failed earlier).
		if err := sitesSync(root); err != nil {
			return fmt.Errorf("sync sites: %w", err)
		}
		return s.clearSwap(sw)
	}
	var from string
	if sw.backup != "" {
		if have, err = present(sw.backup); err != nil {
			return err
		} else if have {
			from = sw.backup
		}
	}
	if from == "" {
		if have, err = present(sw.staging); err != nil {
			return err
		} else if have {
			// A recorded staging tree is never a writer's (Commit holds
			// the site lock across its swap), so one registered now is
			// being claimed by the scavenger, which will spare it; settle
			// later rather than drop the record.
			if s.stagingLive(sw.staging) {
				return fmt.Errorf("site swap: its staged tree is being inspected; will retry")
			}
			from = sw.staging
		}
	}
	if from == "" {
		log.Printf("store: an interrupted site replace left nothing to restore")
		return s.clearSwap(sw)
	}
	// Either tree is already counted as live (an unowned staging-named tree
	// is measured like a site), so the rename changes no totals; it is one
	// transition so a recount does not walk it halfway.
	s.beginTransition()
	err = siteRename(filepath.Join(root, from), final)
	s.endTransition(0, 0)
	if err != nil {
		return fmt.Errorf("restore site: %w", err)
	}
	if err := sitesSync(root); err != nil {
		return fmt.Errorf("restore site: %w", err)
	}
	return s.clearSwap(sw)
}

// retireSwapLocked retires site's outstanding swap for a delete: the record
// goes first, durably, so nothing can restore the predecessor any more, and
// then its trees are removed. It reports whether there was one. The caller
// holds the site's lock.
func (s *Store) retireSwapLocked(site string) (bool, error) {
	sw, ok, err := s.currentSwap(site)
	if err != nil || !ok {
		return false, err
	}
	if err := s.clearSwap(sw); err != nil {
		return false, err
	}
	for _, name := range []string{sw.backup, sw.staging} {
		if name == "" || s.stagingLive(name) {
			continue
		}
		if err := s.removeLiveTree(filepath.Join(s.siteRoot(), name)); err != nil {
			// Unrecorded now, so the scavenger finishes the job.
			log.Printf("store: could not remove a retired site tree, will retry: %s", logsafe.Err(err))
		}
	}
	return true, nil
}
