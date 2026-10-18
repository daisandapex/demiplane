// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyTree lays out content directories as 2.0.0 did, at the store root.
func legacyTree(t *testing.T, root string) {
	t.Helper()
	for p, body := range map[string]string{
		"blobs/page":            "bytes",
		"sources/page":          "# source",
		"sites/demo/index.html": "<p>site",
	} {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertMarkers(t *testing.T, root string) {
	t.Helper()
	for _, name := range legacyContentDirs {
		fi, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != markerMode {
			t.Errorf("%s: %v %v; want a regular file with mode %o", name, fi.Mode(), err, markerMode)
			continue
		}
		b, _ := os.ReadFile(filepath.Join(root, name))
		if !strings.Contains(string(b), "upgraded") {
			t.Errorf("%s marker reads %q", name, b)
		}
		// An older binary's open creates <root>/blobs (and sources) as a
		// directory first; that must fail.
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err == nil {
			t.Errorf("MkdirAll(%s) succeeded over the marker", name)
		}
	}
}

// TestOpenMovesALegacyLayout: the content directories move under data/ with
// what they hold, and a marker takes each old name.
func TestOpenMovesALegacyLayout(t *testing.T) {
	root := t.TempDir()
	legacyTree(t, root)
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	for p, want := range map[string]string{
		"data/blobs/page":            "bytes",
		"data/sources/page":          "# source",
		"data/sites/demo/index.html": "<p>site",
	} {
		if b, err := os.ReadFile(filepath.Join(root, p)); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", p, b, err, want)
		}
	}
	assertMarkers(t, root)
	// Opening again changes nothing.
	if s, err = Open(root); err != nil {
		t.Fatal(err)
	}
	s.Close()
	assertMarkers(t, root)
}

// TestOpenFinishesAnInterruptedMove: a crash after a directory moved but
// before its marker was written is finished at the next open; an empty
// legacy directory an older binary recreated meanwhile is removed, and one
// holding files stops the open.
func TestOpenFinishesAnInterruptedMove(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data", "blobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "blobs", "page"), []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	// blobs moved, no marker yet; sources was recreated, empty, by an older binary.
	if err := os.MkdirAll(filepath.Join(root, "data", "sources"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sources"), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	assertMarkers(t, root)
	if b, err := os.ReadFile(filepath.Join(root, "data", "blobs", "page")); err != nil || string(b) != "bytes" {
		t.Errorf("moved blob = %q, %v", b, err)
	}

	// A recreated legacy directory with files in it needs an operator.
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data", "sites"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sites", "new"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("Open with both sites directories, the old one in use: %v", err)
	}
}
