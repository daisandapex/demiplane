// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"path/filepath"
	"strings"
	"testing"
)

// routeNames are the top-level routes no artifact slug may take.
var routeNames = map[string]bool{"publish": true, "list": true, "docs": true, "help": true, "help.json": true, "llms.txt": true}

// slugByte reports whether c may appear in a slug: an ASCII letter or
// digit, '.', '_' or '-'.
func slugByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
}

// FuzzPathToSlug checks the SSH --untar entry name flattener with a plain
// character scan and path/filepath, not with the validator it calls. An
// accepted slug is 1 to 128 bytes of letters, digits, '.', '_' and '-',
// starts with a letter or digit, is not a route name, and is one path
// element that stays inside any directory it is joined to. The reject
// side: a name holding a NUL or any byte a slug cannot carry other than a
// separator ('/' or '\\'), or one with no letter or digit at all, is
// refused, and so is one whose flattened form would pass 128 bytes.
func FuzzPathToSlug(f *testing.F) {
	for _, s := range []string{
		"index.html", "a/b/c.html", "../../etc/passwd", "/abs/x", `a\..\b`, "./a",
		"a/./b", "a//b", "", ".", "..", "a/..", "publish", "docs/x", ".hidden/x",
		strings.Repeat("a/", 70) + "x", "a\x00b", "a b", "a/../../b", strings.Repeat("a", 129),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		slug, err := pathToSlug(name)
		alnum := false
		for i := 0; i < len(name); i++ {
			c := name[i]
			if !slugByte(c) && c != '/' && c != '\\' {
				if err == nil {
					t.Fatalf("pathToSlug(%q) accepted byte %q as %q", name, c, slug)
				}
				return
			}
			alnum = alnum || slugByte(c) && c != '.' && c != '_' && c != '-'
		}
		if !alnum && err == nil {
			t.Fatalf("pathToSlug(%q) accepted a name with no letter or digit as %q", name, slug)
		}
		if err != nil {
			return
		}
		if len(slug) == 0 || len(slug) > 128 {
			t.Fatalf("pathToSlug(%q) = %q, %d bytes", name, slug, len(slug))
		}
		for i := 0; i < len(slug); i++ {
			if !slugByte(slug[i]) {
				t.Fatalf("pathToSlug(%q) = %q carries %q", name, slug, slug[i])
			}
		}
		if c := slug[0]; c == '.' || c == '_' || c == '-' {
			t.Fatalf("pathToSlug(%q) = %q starts with %q", name, slug, c)
		}
		if routeNames[slug] {
			t.Fatalf("pathToSlug(%q) = route name %q", name, slug)
		}
		root := "/srv/store/blobs"
		j := filepath.Join(root, slug)
		if filepath.Dir(j) != root || filepath.Base(j) != slug || !filepath.IsLocal(slug) {
			t.Fatalf("slug %q from %q is not one child of the root", slug, name)
		}
	})
}
