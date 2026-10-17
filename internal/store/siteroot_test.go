// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// siteWithFiles commits a site with the given files and returns the store and
// its root directory.
func siteWithFiles(t *testing.T, site string, files map[string]string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	w, err := st.NewSiteWriter(site)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := w.AddFile(name, strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	return st, dir
}

// outsideDir makes a directory outside the site tree holding secret.txt and
// index.html, both with secret content.
func outsideDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	for _, n := range []string{"secret.txt", "index.html"} {
		if err := os.WriteFile(filepath.Join(d, n), []byte("TOP SECRET"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
}

// mustNotServe asserts OpenSiteFile refuses the path and, if it ever returned
// a file, that the file is not the secret.
func mustNotServe(t *testing.T, st *Store, site, rel string) {
	t.Helper()
	f, _, _, err := st.OpenSiteFile(site, rel)
	if err == nil {
		b, _ := io.ReadAll(f)
		f.Close()
		t.Fatalf("OpenSiteFile(%q, %q) served %q, want ErrNotFound", site, rel, b)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("OpenSiteFile(%q, %q) err = %v, want ErrNotFound", site, rel, err)
	}
}

func TestOpenSiteFileRejectsIntermediateSymlink(t *testing.T) {
	st, _ := siteWithFiles(t, "demo", map[string]string{"index.html": "ok"})
	symlink(t, outsideDir(t), filepath.Join(st.siteRoot(), "demo", "assets"))
	mustNotServe(t, st, "demo", "assets/secret.txt")
}

func TestOpenSiteFileRejectsSymlinkedSiteRoot(t *testing.T) {
	st, _ := siteWithFiles(t, "demo", map[string]string{"index.html": "ok"})
	symlink(t, outsideDir(t), filepath.Join(st.siteRoot(), "evil"))
	mustNotServe(t, st, "evil", "index.html")
	mustNotServe(t, st, "evil", "secret.txt")
}

// TestOpenSiteFileRejectsInTreeSymlinks: the policy is no symlinks at all,
// not merely none that escape; archive ingest never creates one, so any link
// in the tree was planted out of band.
func TestOpenSiteFileRejectsInTreeSymlinks(t *testing.T) {
	st, _ := siteWithFiles(t, "demo", map[string]string{"index.html": "ok", "css/style.css": "body{}"})
	base := filepath.Join(st.siteRoot(), "demo")
	symlink(t, filepath.Join(base, "css"), filepath.Join(base, "alias"))
	symlink(t, filepath.Join(base, "index.html"), filepath.Join(base, "home.html"))
	mustNotServe(t, st, "demo", "alias/style.css")
	mustNotServe(t, st, "demo", "home.html")
	// Regular files keep working.
	f, _, _, err := st.OpenSiteFile("demo", "css/style.css")
	if err != nil {
		t.Fatalf("regular nested file refused: %v", err)
	}
	f.Close()
}

// TestOpenSiteFileSwapRace swaps a served file and a symlink to a secret as
// fast as possible while serving it. A check-then-open implementation reads
// the secret whenever the swap lands between its check and its open.
func TestOpenSiteFileSwapRace(t *testing.T) {
	st, _ := siteWithFiles(t, "demo", map[string]string{"index.html": "ok", "page.html": "public"})
	secret := filepath.Join(outsideDir(t), "secret.txt")
	base := filepath.Join(st.siteRoot(), "demo")
	page := filepath.Join(base, "page.html")
	regular := filepath.Join(base, "page.regular")
	link := filepath.Join(base, "page.link")
	if err := os.Link(page, regular); err != nil {
		t.Fatal(err)
	}
	symlink(t, secret, link)

	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			_ = os.Rename(link, page)
			_ = os.Symlink(secret, link)
			_ = os.Rename(regular, page)
			_ = os.Link(page, regular)
		}
	}()
	defer func() { stop.Store(true); wg.Wait() }()

	for i := 0; i < 5000; i++ {
		f, _, _, err := st.OpenSiteFile("demo", "page.html")
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(f)
		f.Close()
		if string(b) == "TOP SECRET" {
			t.Fatalf("iteration %d: served the symlink target outside the site", i)
		}
	}
}
