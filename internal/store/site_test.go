// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanSiteRelPath(t *testing.T) {
	ok := map[string]string{
		"index.html":       "index.html",
		"css/style.css":    "css/style.css",
		"a/b/c/deep.js":    "a/b/c/deep.js",
		"./index.html":     "index.html",
		"a/./b.txt":        "a/b.txt",
		"Main-Page_1.html": "Main-Page_1.html",
	}
	for in, want := range ok {
		got, err := cleanSiteRelPath(in)
		if err != nil {
			t.Errorf("cleanSiteRelPath(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("cleanSiteRelPath(%q)=%q, want %q", in, got, want)
		}
	}

	bad := []string{
		"", ".", "..",
		"../escape",
		"a/../../escape",
		"/abs.html",
		"foo/../../bar",
		".git/config",    // leading-dot segment
		"a/.hidden",      // leading-dot nested
		"bad seg/x.html", // space is not URL-safe
	}
	for _, in := range bad {
		if got, err := cleanSiteRelPath(in); err == nil {
			t.Errorf("cleanSiteRelPath(%q) = %q, want error", in, got)
		}
	}
}

func TestSiteWriterRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	w, err := st.NewSiteWriter("demo")
	if err != nil {
		t.Fatalf("NewSiteWriter: %v", err)
	}
	files := map[string]string{
		"index.html":    "<h1>hi</h1>",
		"css/style.css": "body{}",
	}
	for name, body := range files {
		if err := w.AddFile(name, strings.NewReader(body)); err != nil {
			t.Fatalf("AddFile %q: %v", name, err)
		}
	}
	n, err := w.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if n != 2 {
		t.Errorf("Commit count=%d, want 2", n)
	}
	if !st.SiteExists("demo") {
		t.Errorf("SiteExists(demo)=false after commit")
	}

	for name, body := range files {
		f, ct, _, err := st.OpenSiteFile("demo", name)
		if err != nil {
			t.Fatalf("OpenSiteFile %q: %v", name, err)
		}
		got, _ := io.ReadAll(f)
		f.Close()
		if string(got) != body {
			t.Errorf("%q body=%q, want %q", name, got, body)
		}
		if ct == "" {
			t.Errorf("%q empty content-type", name)
		}
	}
	if !strings.HasPrefix(mustCT(t, st, "css/style.css"), "text/css") {
		t.Errorf("css content-type = %q", mustCT(t, st, "css/style.css"))
	}
}

func mustCT(t *testing.T, st *Store, rel string) string {
	t.Helper()
	f, ct, _, err := st.OpenSiteFile("demo", rel)
	if err != nil {
		t.Fatalf("OpenSiteFile %q: %v", rel, err)
	}
	f.Close()
	return ct
}

func TestSiteWriterAddFileRejectsTraversal(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	w, err := st.NewSiteWriter("demo")
	if err != nil {
		t.Fatalf("NewSiteWriter: %v", err)
	}
	defer w.Abort()
	for _, name := range []string{"../escape", "/abs", "a/../../x"} {
		if err := w.AddFile(name, strings.NewReader("x")); err == nil {
			t.Errorf("AddFile(%q) succeeded, want traversal rejection", name)
		}
	}
}

func TestSiteWriterEmptyCommitRejected(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	w, _ := st.NewSiteWriter("demo")
	if _, err := w.Commit(); err == nil {
		t.Fatalf("empty Commit succeeded, want error")
	}
	if st.SiteExists("demo") {
		t.Errorf("empty commit created a site dir")
	}
}

func TestSiteWriterAbortLeavesNoSite(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	w, _ := st.NewSiteWriter("demo")
	_ = w.AddFile("index.html", strings.NewReader("x"))
	if err := w.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if st.SiteExists("demo") {
		t.Errorf("aborted writer left a site")
	}
	// The scratch dir must be gone too.
	entries, _ := os.ReadDir(st.siteRoot())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("scratch dir survived Abort: %s", e.Name())
		}
	}
}

func TestSiteWriterRepublishAtomicReplace(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()

	w1, _ := st.NewSiteWriter("demo")
	_ = w1.AddFile("index.html", strings.NewReader("v1"))
	_ = w1.AddFile("stale.txt", strings.NewReader("old"))
	if _, err := w1.Commit(); err != nil {
		t.Fatalf("v1 commit: %v", err)
	}

	w2, _ := st.NewSiteWriter("demo")
	_ = w2.AddFile("index.html", strings.NewReader("v2"))
	if _, err := w2.Commit(); err != nil {
		t.Fatalf("v2 commit: %v", err)
	}

	f, _, _, err := st.OpenSiteFile("demo", "index.html")
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if string(got) != "v2" {
		t.Errorf("index=%q, want v2", got)
	}
	if _, _, _, err := st.OpenSiteFile("demo", "stale.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("stale.txt survived republish: err=%v", err)
	}
}

func TestOpenSiteFileMissing(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	if _, _, _, err := st.OpenSiteFile("demo", "index.html"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing site file err=%v, want ErrNotFound", err)
	}
}

// TestOpenSiteFileRejectsSymlink proves the serve path never follows a symlink
// planted in the store out of band (defense-in-depth; AddFile never writes one).
func TestOpenSiteFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	defer st.Close()

	w, _ := st.NewSiteWriter("demo")
	_ = w.AddFile("index.html", strings.NewReader("ok"))
	if _, err := w.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Plant a symlink inside the committed site pointing outside the tree.
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	link := filepath.Join(st.siteRoot(), "demo", "leak.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, _, _, err := st.OpenSiteFile("demo", "leak.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("OpenSiteFile followed a symlink: err=%v, want ErrNotFound", err)
	}
}

func TestDeleteSite(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	if err := st.DeleteSite("demo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteSite(missing)=%v, want ErrNotFound", err)
	}
	w, _ := st.NewSiteWriter("demo")
	_ = w.AddFile("index.html", strings.NewReader("x"))
	_, _ = w.Commit()
	if err := st.DeleteSite("demo"); err != nil {
		t.Fatalf("DeleteSite: %v", err)
	}
	if st.SiteExists("demo") {
		t.Errorf("site survived DeleteSite")
	}
}

func TestNewSiteWriterReservedName(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	if _, err := st.NewSiteWriter("docs"); err == nil {
		t.Errorf("NewSiteWriter(docs) succeeded, want reserved-name rejection")
	}
}
