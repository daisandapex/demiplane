// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// FuzzPathToSlug checks the SSH --untar entry name flattener: whatever the
// tar entry is called, the slug it yields is one valid named slug that
// stays inside any directory it is joined to.
func FuzzPathToSlug(f *testing.F) {
	for _, s := range []string{
		"index.html", "a/b/c.html", "../../etc/passwd", "/abs/x", `a\..\b`, "./a",
		"a/./b", "a//b", "", ".", "..", "a/..", "publish", "docs/x", ".hidden/x",
		strings.Repeat("a/", 70) + "x",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		slug, err := pathToSlug(name)
		if err != nil {
			return
		}
		if store.ValidateNamedSlug(slug) != nil {
			t.Fatalf("pathToSlug(%q) = %q, not a valid slug", name, slug)
		}
		root := "/srv/store/blobs"
		j := filepath.Join(root, slug)
		if filepath.Dir(j) != root {
			t.Fatalf("slug %q from %q is not a child of the root", slug, name)
		}
	})
}
