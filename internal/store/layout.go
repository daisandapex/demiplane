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

// migrateLayout moves the legacy content directories under contentDirName
// and puts a marker at each old name. It is idempotent and resumes a move a
// crash interrupted: a directory is renamed (atomic) before its marker is
// written (temp file, then rename), so after a crash either the old
// directory is still there or the new one is and only the marker is missing.
func migrateLayout(root string) error {
	data := filepath.Join(root, contentDirName)
	if err := os.MkdirAll(data, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", data, err)
	}
	for _, name := range legacyContentDirs {
		old, cur := filepath.Join(root, name), filepath.Join(data, name)
		fi, err := os.Lstat(old)
		switch {
		case err == nil && fi.Mode().IsRegular():
			// Already a marker.
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
			if err := writeMarker(root, name); err != nil {
				return err
			}
		case err == nil:
			return fmt.Errorf("store layout: %s is neither a directory nor a demiplane marker", old)
		case errors.Is(err, fs.ErrNotExist):
			if err := writeMarker(root, name); err != nil {
				return err
			}
		default:
			return fmt.Errorf("inspect %s: %w", old, err)
		}
		if err := os.MkdirAll(cur, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", cur, err)
		}
	}
	return nil
}

// writeMarker puts the marker for a legacy content directory at
// <root>/<name>, atomically.
func writeMarker(root, name string) error {
	final := filepath.Join(root, name)
	tmp := filepath.Join(root, "."+name+".marker-tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, markerMode)
	if err != nil {
		return fmt.Errorf("write store marker: %w", err)
	}
	if _, err := fmt.Fprintf(f, markerNote, name, contentDirName); err == nil {
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
	if d, err := os.Open(root); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
