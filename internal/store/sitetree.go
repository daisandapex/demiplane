// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/daisandapex/demiplane/v3/internal/logsafe"
)

// sitetree.go walks, measures and removes file trees by descriptor: each
// directory is opened (os.Root) relative to its parent's descriptor and
// every lstat, readdir and unlink names one entry relative to an open
// directory. No path handed to the kernel is longer than one name, so how
// the store root is spelled (relative, or a long absolute path) and how deep
// a tree goes do not decide whether it can be measured or removed. The
// publish-time bound (maxSiteRelPath) counts the root as spelled when the
// site was published; a store reopened through a longer spelling of the same
// root would otherwise hold entries past PATH_MAX that it could neither
// count nor remove (audit/04-site-path-past-path-max-bricks-store.md).

// openParent opens dir's parent directory as a root and returns it with
// dir's last element.
func openParent(dir string) (*os.Root, string, error) {
	parent, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return nil, "", err
	}
	return parent, filepath.Base(dir), nil
}

// readNames lists the names in the directory d.
func readNames(d *os.Root) ([]string, error) {
	f, err := d.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}

// treeStats sums the regular files under dir and counts its entries (files,
// directories and anything else, dir itself excluded), skipping temp
// entries. A missing dir, or an entry removed during the walk, counts as
// empty. On an error it returns what it measured so far.
func treeStats(dir string) (size, entries int64, err error) {
	parent, name, err := openParent(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	defer parent.Close()
	return treeStatsIn(parent, name)
}

// treeStatsIn is treeStats for the entry name of the open directory parent.
func treeStatsIn(parent *os.Root, name string) (size, entries int64, err error) {
	fi, err := parent.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return 0, 0, nil
	case err != nil:
		return 0, 0, err
	case fi.Mode().IsRegular():
		return fi.Size(), 0, nil
	case !fi.IsDir():
		return 0, 0, nil
	}
	d, err := parent.OpenRoot(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	defer d.Close()
	return dirStats(d)
}

// dirStats measures the contents of the open directory d (treeStats).
func dirStats(d *os.Root) (size, entries int64, err error) {
	names, err := readNames(d)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	for _, name := range names {
		if strings.HasPrefix(name, tempPrefix) {
			continue
		}
		fi, err := d.Lstat(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return size, entries, err
		}
		entries++
		if fi.Mode().IsRegular() {
			size += fi.Size()
			continue
		}
		if !fi.IsDir() {
			continue
		}
		sub, err := d.OpenRoot(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return size, entries, err
		}
		n, c, err := dirStats(sub)
		sub.Close()
		size += n
		entries += c
		if err != nil {
			return size, entries, err
		}
	}
	return size, entries, nil
}

// removeTreeCounted removes dir and everything under it, deepest first, one
// entry at a time, and returns what this call removed (treeRemoval). A
// directory it cannot read is left with its unseen contents, and the error is
// returned; an entry already gone counts for nothing (whoever removed it
// accounts for it). A missing dir is not an error.
func (s *Store) removeTreeCounted(dir string) (treeRemoval, error) {
	var r treeRemoval
	parent, name, err := openParent(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return r, nil
		}
		return r, err
	}
	defer parent.Close()
	var errs []error
	s.removeIn(parent, name, dir, &r, &errs)
	return r, errors.Join(errs...)
}

// removeIn removes the entry name of the open directory d, its contents
// first, adding what it removed to r and its failures to errs. path is the
// entry's full name, for the ledger and messages only; it is never opened.
func (s *Store) removeIn(d *os.Root, name, path string, r *treeRemoval, errs *[]error) {
	fi, err := d.Lstat(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			*errs = append(*errs, err)
		}
		return
	}
	if fi.IsDir() {
		sub, err := d.OpenRoot(name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return
		case err != nil:
			*errs = append(*errs, err) // its contents stay
		default:
			names, err := readNames(sub)
			if err != nil {
				*errs = append(*errs, err)
			}
			for _, n := range names {
				s.removeIn(sub, n, filepath.Join(path, n), r, errs)
			}
			sub.Close()
		}
	}
	size, _, removed, retired, err := s.unlinkIn(d, name, path)
	if err != nil {
		*errs = append(*errs, err)
	}
	if !removed {
		return
	}
	if retired {
		r.retired += size
	} else {
		r.bytes += size
	}
	r.entries++
}

// siteCharge is what a site tree under sites/ that could not be measured in
// full is charged: the bytes of the regular files measured and the entries
// counted, its top directory included.
type siteCharge struct{ size, entries int64 }

// logQuarantine logs, the first time, that the live site tree name could not
// be measured. What was measured stays counted (siteStats records it); the
// store opens and recounts without the rest rather than failing, and the
// site is served as before. A later measurement that succeeds lifts it.
func (s *Store) logQuarantine(name string, err error) {
	s.quota.mu.Lock()
	_, seen := s.quota.partial[name]
	s.quota.mu.Unlock()
	if !seen {
		log.Printf("store: could not measure site %q, quarantined: its usage is counted only in part: %s", name, logsafe.Err(err))
	}
}

// quarantinedSites reports the live sites charged only in part (tests).
func (s *Store) quarantinedSites() []string {
	s.quota.mu.Lock()
	defer s.quota.mu.Unlock()
	var names []string
	for n := range s.quota.partial {
		if !strings.HasPrefix(n, tempPrefix) {
			names = append(names, n)
		}
	}
	return names
}

// movePartial moves the partial charge recorded for the tree name under
// sites/, if any, to the name it was renamed to. Call it inside the
// transition that renames the tree.
func (s *Store) movePartial(from, to string) {
	s.quota.mu.Lock()
	defer s.quota.mu.Unlock()
	if c, ok := s.quota.partial[from]; ok {
		delete(s.quota.partial, from)
		s.quota.partial[to] = c
	}
}

// removalChange is the accounting for r, what removeTreeCounted removed of
// a tree under sites/ that was charged under the name key, with err its
// error. A tree measured in full releases exactly what was removed. One
// charged only in part (quarantined, or a leftover tree that could not be
// measured) releases no more than its charge: the rest of what was removed
// was never counted, and releasing it would take it out of other trees'
// charges. Survivors keep the charge not yet released, under survivor, the
// name they now sit under: what is left of a partial charge, or, for a tree
// charged in full, a record that they are (quotaState.whole), so a recount
// that reads them only in part does not lower usage for data still on disk.
// Call it inside the removal's transition.
func (s *Store) removalChange(key, survivor string, r treeRemoval, err error) change {
	q := &s.quota
	q.mu.Lock()
	c, ok := q.partial[key]
	delete(q.partial, key)
	delete(q.whole, key)
	rel := c
	if ok && err != nil {
		rel = siteCharge{size: min(r.bytes+r.retired, c.size), entries: min(r.entries, c.entries)}
		if q.partial == nil {
			q.partial = make(map[string]siteCharge)
		}
		q.partial[survivor] = siteCharge{size: c.size - rel.size, entries: c.entries - rel.entries}
	}
	if !ok && err != nil && strings.HasPrefix(survivor, tempPrefix) {
		if q.whole == nil {
			q.whole = make(map[string]struct{})
		}
		q.whole[survivor] = struct{}{}
	}
	q.mu.Unlock()
	if !ok {
		return r.change()
	}
	// A retired file's bytes already left the live count (unlinkFile), so
	// they come out of what is still to release, and anything retired past
	// the charge is given back.
	removed := rel.size + rel.entries*metaAllowance - r.retired
	return change{removed: max(removed, 0), restored: max(-removed, 0), removedEntries: rel.entries}
}
