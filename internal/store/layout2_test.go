// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestConcurrentFirstOpensOfALegacyStore: processes starting together
// (serve and a receive) on a store still in the 2.0.0 layout all open it.
func TestConcurrentFirstOpensOfALegacyStore(t *testing.T) {
	for round := 0; round < 10; round++ {
		root := t.TempDir()
		legacyTree(t, root)
		// Modes the permission pass repairs, so the openers race through
		// it as well as the move.
		for _, p := range []string{"", "blobs", "sources", "sites"} {
			if err := os.Chmod(filepath.Join(root, p), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		const n = 6
		var wg sync.WaitGroup
		errs := make([]error, n)
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				open := Open
				if i%2 == 1 {
					open = OpenShared
				}
				s, err := open(root)
				if err == nil {
					s.Close()
				}
				errs[i] = err
			}(i)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: open %d: %v", round, i, err)
			}
		}
		assertMarkers(t, root)
		if b, err := os.ReadFile(filepath.Join(root, "data", "blobs", "page")); err != nil || string(b) != "bytes" {
			t.Fatalf("round %d: moved blob = %q, %v", round, b, err)
		}
	}
}

// TestLayoutMoveIsDurableBeforeItsMarker: a moved directory's new parent is
// synced before the marker that commits the move is installed, and a sync
// failure stops the open with no marker in place.
func TestLayoutMoveIsDurableBeforeItsMarker(t *testing.T) {
	root := t.TempDir()
	legacyTree(t, root)
	data := filepath.Join(root, "data")
	layoutSync = func(dir string) error {
		if dir == data {
			return errors.New("injected sync failure")
		}
		return syncDir(dir)
	}
	t.Cleanup(func() { layoutSync = syncDir })
	if s, err := Open(root); err == nil {
		s.Close()
		t.Fatal("Open succeeded although the moved directories could not be synced")
	}
	for _, name := range legacyContentDirs {
		if fi, err := os.Lstat(filepath.Join(root, name)); err == nil && fi.Mode().IsRegular() {
			t.Errorf("%s marker installed before its move was durable", name)
		}
	}
	layoutSync = syncDir
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	assertMarkers(t, root)
	if b, err := os.ReadFile(filepath.Join(root, "data", "sites", "demo", "index.html")); err != nil || string(b) != "<p>site" {
		t.Fatalf("moved site = %q, %v", b, err)
	}
}

// TestMarkerSyncFailureStopsTheOpen: a marker whose rename cannot be made
// durable fails the open rather than being assumed.
func TestMarkerSyncFailureStopsTheOpen(t *testing.T) {
	root := t.TempDir()
	layoutSync = func(dir string) error {
		if dir == root {
			return errors.New("injected sync failure")
		}
		return syncDir(dir)
	}
	t.Cleanup(func() { layoutSync = syncDir })
	if s, err := Open(root); err == nil {
		s.Close()
		t.Fatal("Open succeeded although the store root could not be synced")
	}
}

// TestOpenRefusesAMissingMigratedDirectory: a marker with no directory
// behind it means content went missing; the open stops instead of serving
// an empty store.
func TestOpenRefusesAMissingMigratedDirectory(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := os.Remove(filepath.Join(root, "data", "blobs")); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(root); err == nil || !strings.Contains(err.Error(), "missing") {
		if err == nil {
			s.Close()
		}
		t.Fatalf("Open with data/blobs gone: %v", err)
	}
}

// TestOpenRepairsPermissionsBeforeMoving: a legacy store restored with its
// root unwritable by the owner is repaired, then moved, as 2.0.0 would have
// repaired it.
func TestOpenRepairsPermissionsBeforeMoving(t *testing.T) {
	skipIfRoot(t)
	root := t.TempDir()
	legacyTree(t, root)
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o700) })
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	assertMarkers(t, root)
}

// TestServeLockIsExclusive: a second server (or a maintenance command) on a
// store a server holds is refused at once.
func TestServeLockIsExclusive(t *testing.T) {
	root := t.TempDir()
	unlock, err := LockServe(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockServe(root); !errors.Is(err, ErrStoreServed) {
		t.Fatalf("second LockServe = %v, want ErrStoreServed", err)
	}
	unlock()
	unlock2, err := LockServe(root)
	if err != nil {
		t.Fatalf("LockServe after release: %v", err)
	}
	unlock2()
}
