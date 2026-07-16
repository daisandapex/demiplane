// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func fetch(t *testing.T, st *store.Store, slug string) string {
	t.Helper()
	_, f, err := st.Get(slug)
	if err != nil {
		t.Fatalf("Get(%q): %v", slug, err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return string(b)
}

func TestReceiveSingleFile(t *testing.T) {
	st := newStore(t)
	var out bytes.Buffer
	body := "<!DOCTYPE html><html>ssh pipe</html>"

	err := Receive(st, ReceiveOptions{
		PutOptions: store.PutOptions{Slug: "viassh", Filename: "page.html"},
		BaseURL:    "https://demi.example",
	}, strings.NewReader(body), &out)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "https://demi.example/viassh" {
		t.Errorf("printed URL = %q", got)
	}
	if got := fetch(t, st, "viassh"); got != body {
		t.Errorf("stored body = %q, want %q", got, body)
	}
}

func TestReceiveRelativeURLWhenNoBase(t *testing.T) {
	st := newStore(t)
	var out bytes.Buffer
	if err := Receive(st, ReceiveOptions{}, strings.NewReader("x"), &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); !strings.HasPrefix(got, "/") {
		t.Errorf("relative URL = %q, want leading slash", got)
	}
}

func TestReceivePrivateNamedRejected(t *testing.T) {
	st := newStore(t)
	err := Receive(st, ReceiveOptions{
		PutOptions: store.PutOptions{Slug: "named", Private: true},
	}, strings.NewReader("x"), io.Discard)
	if err == nil {
		t.Fatal("expected error for private + named slug over SSH")
	}
}

func TestReceiveMaxUpload(t *testing.T) {
	st := newStore(t)
	// Over the cap → ErrTooLarge, nothing stored.
	err := Receive(st, ReceiveOptions{MaxUpload: 8}, strings.NewReader(strings.Repeat("x", 64)), io.Discard)
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize receive = %v, want ErrTooLarge", err)
	}
	if arts, _ := st.List(store.DefaultOwner); len(arts) != 0 {
		t.Errorf("oversize upload was stored: %d artifacts", len(arts))
	}
	// Exactly the cap succeeds.
	var out bytes.Buffer
	if err := Receive(st, ReceiveOptions{MaxUpload: 8}, strings.NewReader("12345678"), &out); err != nil {
		t.Errorf("exact-cap receive: %v", err)
	}
	// Zero = unlimited.
	if err := Receive(st, ReceiveOptions{MaxUpload: 0}, strings.NewReader(strings.Repeat("y", 1000)), io.Discard); err != nil {
		t.Errorf("unlimited receive: %v", err)
	}
}

func TestReceiveTarDirectorySync(t *testing.T) {
	st := newStore(t)
	files := map[string]string{
		"index.html":     "<html>home</html>",
		"css/style.css":  "body{color:red}",
		"data/info.json": `{"ok":true}`,
	}
	tarball := makeTar(t, files)

	var out bytes.Buffer
	if err := Receive(st, ReceiveOptions{Untar: true, BaseURL: "https://h"}, bytes.NewReader(tarball), &out); err != nil {
		t.Fatalf("Receive untar: %v", err)
	}
	lines := strings.Fields(out.String())
	if len(lines) != 3 {
		t.Fatalf("printed %d URLs, want 3: %q", len(lines), out.String())
	}
	// Nested paths flatten to hyphenated slugs and round-trip.
	if got := fetch(t, st, "index.html"); got != files["index.html"] {
		t.Errorf("index.html = %q", got)
	}
	if got := fetch(t, st, "css-style.css"); got != files["css/style.css"] {
		t.Errorf("css-style.css = %q", got)
	}
	if got := fetch(t, st, "data-info.json"); got != files["data/info.json"] {
		t.Errorf("data-info.json = %q", got)
	}

	// Re-sync overwrites in place (named slugs), not appends.
	tar2 := makeTar(t, map[string]string{"index.html": "<html>updated</html>"})
	if err := Receive(st, ReceiveOptions{Untar: true}, bytes.NewReader(tar2), io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := fetch(t, st, "index.html"); got != "<html>updated</html>" {
		t.Errorf("re-sync did not overwrite: %q", got)
	}
}

func TestReceiveTarRejectsPrivate(t *testing.T) {
	st := newStore(t)
	tarball := makeTar(t, map[string]string{"a.html": "x"})
	err := Receive(st, ReceiveOptions{Untar: true, PutOptions: store.PutOptions{Private: true}}, bytes.NewReader(tarball), io.Discard)
	if err == nil {
		t.Fatal("expected error: private directory sync")
	}
}

func TestReceiveTarEmpty(t *testing.T) {
	st := newStore(t)
	tarball := makeTar(t, nil)
	if err := Receive(st, ReceiveOptions{Untar: true}, bytes.NewReader(tarball), io.Discard); err == nil {
		t.Error("expected error for tar with no regular files")
	}
}

// TestReceiveTarRejectsDecompressBomb proves the SSH untar path is bounded by
// the shared total-decompressed-bytes budget: a single regular tar entry whose
// content exceeds the budget trips store.ErrDecompressBudget and stores nothing.
// A raw --max-upload cap on stdin does NOT cover this (a sparse tar declares a
// tiny stored payload yet expands to attacker-chosen zero-fill through
// tar.Reader), which is exactly what this budget closes.
func TestReceiveTarRejectsDecompressBomb(t *testing.T) {
	st := newStore(t)

	saved := untarDecompressBudget
	untarDecompressBudget = 2 << 20 // 2 MiB
	defer func() { untarDecompressBudget = saved }()

	// One entry, budget + 1 MiB of zero-fill — past the total budget.
	oversize := int(untarDecompressBudget) + (1 << 20)
	tarball := makeTar(t, map[string]string{"big.bin": strings.Repeat("\x00", oversize)})

	err := Receive(st, ReceiveOptions{Untar: true}, bytes.NewReader(tarball), io.Discard)
	if !errors.Is(err, store.ErrDecompressBudget) {
		t.Fatalf("tar-bomb receive = %v, want store.ErrDecompressBudget", err)
	}
	// Nothing committed — the oversize entry must not have been stored.
	if arts, _ := st.List(store.DefaultOwner); len(arts) != 0 {
		t.Errorf("tar-bomb stored %d artifacts, want 0", len(arts))
	}

	// An entry comfortably under the budget still publishes.
	small := makeTar(t, map[string]string{"ok.txt": strings.Repeat("A", 4096)})
	if err := Receive(st, ReceiveOptions{Untar: true}, bytes.NewReader(small), io.Discard); err != nil {
		t.Errorf("under-budget receive: %v", err)
	}
}

func TestPathToSlug(t *testing.T) {
	cases := map[string]string{
		"index.html":       "index.html",
		"css/style.css":    "css-style.css",
		"./a/b/c.txt":      "a-b-c.txt",
		"deep/nested/x.js": "deep-nested-x.js",
		// Traversal is neutralized by path.Clean (resolved against root), not an error.
		"../escape":        "escape",
		"../../etc/passwd": "etc-passwd",
	}
	for in, want := range cases {
		got, err := pathToSlug(in)
		if err != nil {
			t.Errorf("pathToSlug(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("pathToSlug(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", ".", "/", "../"} {
		if _, err := pathToSlug(bad); err == nil {
			t.Errorf("pathToSlug(%q) = nil error, want error", bad)
		}
	}
}

func makeTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
