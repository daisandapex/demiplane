// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build audit && linux

package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// auditDeepPath returns a valid site-relative path (segments of at most 128
// bytes) that is n or n-1 bytes long.
func auditDeepPath(n int) string {
	var segs []string
	for n > 0 {
		l := min(n, 128)
		segs = append(segs, strings.Repeat("a", l))
		n -= l + 1 // the segment and the separator after it
	}
	return strings.Join(segs, "/")
}

// TestAuditSiteWithLongPathsCanBeRemoved: a site entry may be 32 segments of
// 128 bytes, and AddFile only needs the path to fit under the staging
// directory ".tmp-<site>-<n>". Replacing or deleting the site renames the
// tree to a longer name (".tmp-old-<site>-<n>", ".tmp-del-<16 hex>"), which
// pushes a file near the limit past PATH_MAX. The store removes and measures
// trees by full path, so that file can then be neither removed nor counted:
// the leftover tree stays for good, and the store refuses to open, for serve
// after its next restart and for every `demiplane receive`.
// audit/04-site-path-past-path-max-bricks-store.md.
func TestAuditSiteWithLongPathsCanBeRemoved(t *testing.T) {
	const pathMax = 4095 // Linux PATH_MAX without the terminating NUL
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// The longest staging name for site "s" is ".tmp-s-" plus 10 digits.
	rel := auditDeepPath(pathMax - len(s.siteRoot()) - len("/.tmp-s-4294967295/"))
	sw, err := s.NewSiteWriter("s")
	if err != nil {
		t.Fatalf("NewSiteWriter: %v", err)
	}
	if err := sw.AddFile(rel, strings.NewReader("x")); err != nil {
		sw.Abort()
		t.Fatalf("AddFile(%d-byte path): %v", len(rel), err)
	}
	if _, err := sw.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
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
		t.Fatalf("the store no longer opens: %v", shortenAuditErr(err))
	}
	again.Close()
}

// shortenAuditErr keeps a failure line readable: the error quotes a path of
// about 4 KiB.
func shortenAuditErr(err error) string {
	msg := err.Error()
	if len(msg) > 240 {
		msg = msg[:120] + " … " + msg[len(msg)-100:]
	}
	return msg
}
