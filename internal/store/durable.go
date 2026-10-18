// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Content is made durable before anything that names it commits: a
// generation's files are synced as they are written and their directories
// before the row that points at them, and a staged site tree, files and
// directories, before it is swapped in, so a power loss after a commit
// cannot leave the row or the site naming bytes that never reached the disk
// while the predecessor is already gone.
var (
	// fileSync syncs a written file; a test hook.
	fileSync = (*os.File).Sync
	// contentSync syncs a content directory (blobs, sources, a staged
	// site's directories); a test hook.
	contentSync = syncDir
)

// syncGeneration makes the names of a generation's files durable: the blob
// directory, and the source directory when the generation retains a source.
func (s *Store) syncGeneration(withSource bool) error {
	if err := contentSync(s.blobDir); err != nil {
		return fmt.Errorf("sync blobs: %w", err)
	}
	if withSource {
		if err := contentSync(s.sourceDir); err != nil {
			return fmt.Errorf("sync sources: %w", err)
		}
	}
	return nil
}

// syncTree syncs every directory of the tree at root, deepest first, so
// each one's entries are durable before its parent's entry for it.
func syncTree(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := contentSync(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}
