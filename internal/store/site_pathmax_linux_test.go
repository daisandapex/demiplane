// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// deepPath returns a valid site-relative path (segments of at most 128
// bytes) that is n or n-1 bytes long.
func deepPath(n int) string {
	var segs []string
	for n > 0 {
		l := min(n, 128)
		segs = append(segs, strings.Repeat("a", l))
		n -= l + 1 // the segment and the separator after it
	}
	return strings.Join(segs, "/")
}

// TestSiteWithLongPathsCanBeRemoved: a site entry may be 32 segments of 128
// bytes, so its length is bounded by PATH_MAX alone. Replacing or deleting
// the site renames the tree to a longer name than its staging directory
// (".tmp-old-<site>-<n>", ".tmp-del-<16 hex>"), and the store removes and
// measures trees by full path, so an entry that fits only under the staging
// name would leave a tree that can be neither removed nor counted, and the
// store would refuse to open. An entry is refused at publish unless it fits
// under the longest of those names; the longest one accepted survives a
// replace and a delete, and the store opens again.
// See audit/04-site-path-past-path-max-bricks-store.md.
func TestSiteWithLongPathsCanBeRemoved(t *testing.T) {
	const pathMax = 4095 // Linux PATH_MAX without the terminating NUL
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// The longest staging name for site "s" is ".tmp-s-" plus 10 digits.
	n := pathMax - len(s.siteRoot()) - len("/.tmp-s-4294967295/")
	publishOne := func(rel string) error {
		t.Helper()
		sw, err := s.NewSiteWriter("s")
		if err != nil {
			t.Fatalf("NewSiteWriter: %v", err)
		}
		if err := sw.AddFile(rel, strings.NewReader("x")); err != nil {
			sw.Abort()
			return err
		}
		if _, err := sw.Commit(); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		return nil
	}
	err = publishOne(deepPath(n))
	var inErr *InputError
	if err == nil || !errors.As(err, &inErr) {
		t.Fatalf("AddFile(%d-byte path that fits only under the staging name) = %v, want an InputError", len(deepPath(n)), err)
	}

	// The longest path accepted.
	rel := ""
	for m := n - 1; m > 0; m-- {
		err := publishOne(deepPath(m))
		if err == nil {
			rel = deepPath(m)
			break
		}
		if !errors.As(err, &inErr) {
			t.Fatalf("AddFile(%d-byte path): %v", m, err)
		}
	}
	if rel == "" {
		t.Fatal("no path was accepted")
	}
	if err := publishOne(rel); err != nil { // a replace: the live tree becomes .tmp-old-…
		t.Fatalf("replace with a %d-byte path: %v", len(rel), err)
	}
	if err := s.DeleteSite("s"); err != nil {
		t.Errorf("DeleteSite: %v", err)
	}
	left, _ := filepath.Glob(filepath.Join(s.siteRoot(), ".tmp-*"))
	if len(left) != 0 {
		t.Errorf("the deleted site's tree is still on disk: %s", filepath.Base(left[0]))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(dir)
	if err != nil {
		t.Fatalf("the store no longer opens: %v", shortenErr(err))
	}
	again.Close()
}

// TestOpenToleratesUnmeasurableLeftoverTree: a store that already holds a
// leftover tree it can neither measure nor remove (one a release without
// the publish-time bound left behind, with a file past PATH_MAX once
// renamed) still opens, fresh tree or old. The tree is logged and skipped.
func TestOpenToleratesUnmeasurableLeftoverTree(t *testing.T) {
	const pathMax = 4095
	for _, age := range []time.Duration{0, 48 * time.Hour} {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		root := s.siteRoot()
		s.Close()
		// Build the tree under a short name, then rename it to a tombstone
		// name, which pushes its deepest file past PATH_MAX.
		rel := deepPath(pathMax - len(root) - len("/t/"))
		full := filepath.Join(root, "t", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		tomb := filepath.Join(root, ".tmp-del-0123456789abcdef")
		if err := os.Rename(filepath.Join(root, "t"), tomb); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		if err := os.Chtimes(tomb, old, old); err != nil {
			t.Fatal(err)
		}

		again, err := Open(dir)
		if err != nil {
			t.Fatalf("leftover tree %v old: the store no longer opens: %v", age, shortenErr(err))
		}
		again.Close()
	}
}

// shortenErr keeps a failure line readable: the error quotes a path of
// about 4 KiB.
func shortenErr(err error) string {
	msg := err.Error()
	if len(msg) > 240 {
		msg = msg[:120] + " … " + msg[len(msg)-100:]
	}
	return msg
}
