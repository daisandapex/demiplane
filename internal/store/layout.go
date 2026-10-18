// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// contentDirName holds the store's content directories (blobs, sources,
// sites). Up to 2.0.0 they sat at the store root; the first open by a later
// binary moves them here and leaves a marker file at each old name.
//
// The markers are the fence against older binaries sharing the store: 2.0.0
// opens a store by creating <root>/blobs as a directory before it touches
// anything else, and a regular file there makes that fail, so an older serve
// or receive stops at open with nothing written. (Writes that reached the
// database would be refused anyway, by writerTriggers, but an older binary
// also writes blob files and site trees in place, which no trigger sees.)
const contentDirName = "data"

// legacyContentDirs are the content directories as 2.0.0 named them at the
// store root, now marker files.
var legacyContentDirs = []string{"blobs", sourceDirName, "sites"}

// markerMode is the marker files' mode. It is the mode the 2.0.0-era open
// gives these names (it sets the root's content directories to 0700), so an
// older binary's permission pass leaves the markers untouched before failing.
const markerMode = 0o700

const markerNote = `This demiplane store was upgraded: its %[1]s directory moved to %[2]s/%[1]s.
A demiplane binary from 2.0.0 or earlier cannot open the store any more.
Upgrade every program that writes to it, or restore a backup taken before the
upgrade to go back (see docs/deployment.md, "Upgrading").
`

// layoutSync makes the renames inside a directory durable; a test hook.
var layoutSync = syncDir

// migrateLayout moves the legacy content directories under contentDirName
// and puts a marker at each old name. It is idempotent and resumes a move a
// crash interrupted. Each step is durable before the next: the directory is
// renamed and both its old and new parents synced, and only then is the
// marker installed (a uniquely named temp file, renamed, the root synced).
// So after a crash either the old directory is still there, or the new one
// is and only the marker may be missing; a marker never stands without its
// directory, and one that does means content went missing, which stops the
// open. The caller holds the store's open lock (openlock.go), so concurrent
// first opens do not race through it.
func migrateLayout(root string) error {
	data := filepath.Join(root, contentDirName)
	if err := os.MkdirAll(data, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", data, err)
	}
	if err := layoutSync(root); err != nil {
		return fmt.Errorf("store layout: sync %s: %w", root, err)
	}
	for _, name := range legacyContentDirs {
		old, cur := filepath.Join(root, name), filepath.Join(data, name)
		fi, err := os.Lstat(old)
		switch {
		case err == nil && fi.Mode().IsRegular():
			// Already a marker: its directory must be there.
			if _, err := os.Lstat(cur); errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("store layout: %s is missing (the store was upgraded and %s is its marker); restore it from a backup, or create it empty to start without that content", cur, old)
			} else if err != nil {
				return fmt.Errorf("inspect %s: %w", cur, err)
			}
			continue
		case err == nil && fi.IsDir():
			if _, err := os.Lstat(cur); err == nil {
				// Both exist: an older binary recreated the old directory
				// after an interrupted move. An empty one goes; one with
				// content needs an operator to merge it.
				if err := os.Remove(old); err != nil {
					return fmt.Errorf("store layout: both %s and %s exist and %s is not empty; move what it holds into %s and remove it: %w", old, cur, old, cur, err)
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("inspect %s: %w", cur, err)
			} else if err := os.Rename(old, cur); err != nil {
				return fmt.Errorf("move %s to %s: %w", old, cur, err)
			}
		case err == nil:
			return fmt.Errorf("store layout: %s is neither a directory nor a demiplane marker", old)
		case errors.Is(err, fs.ErrNotExist):
			// A new store, or a move whose marker a crash cut off.
		default:
			return fmt.Errorf("inspect %s: %w", old, err)
		}
		if err := os.MkdirAll(cur, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", cur, err)
		}
		for _, dir := range []string{data, root} {
			if err := layoutSync(dir); err != nil {
				return fmt.Errorf("store layout: sync %s: %w", dir, err)
			}
		}
		if err := writeMarker(root, name); err != nil {
			return err
		}
	}
	return nil
}

// writeMarker puts the marker for a legacy content directory at
// <root>/<name>, atomically and durably.
func writeMarker(root, name string) error {
	final := filepath.Join(root, name)
	f, err := os.CreateTemp(root, "."+name+".marker-*")
	if err != nil {
		return fmt.Errorf("write store marker %s: %w", final, err)
	}
	tmp := f.Name()
	_, err = fmt.Fprintf(f, markerNote, name, contentDirName)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, markerMode) // exact, whatever the umask
	}
	if err == nil {
		err = os.Rename(tmp, final)
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write store marker %s: %w", final, err)
	}
	if err := layoutSync(root); err != nil {
		return fmt.Errorf("write store marker %s: sync: %w", final, err)
	}
	return nil
}
