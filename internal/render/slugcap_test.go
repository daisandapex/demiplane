// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"strings"
	"testing"
)

// TestHeadingIDLengthCapped: an id built from heading text stops at
// maxSlugLen, so a long heading does not make every reference to it long.
func TestHeadingIDLengthCapped(t *testing.T) {
	if got := slugify(strings.Repeat("ab ", 200)); len(got) > maxSlugLen || strings.HasSuffix(got, "-") {
		t.Errorf("slugify of a long heading = %d bytes %q, want at most %d and no trailing hyphen", len(got), got, maxSlugLen)
	}
	out := string(Markdown([]byte("## "+strings.Repeat("x", 5000)+"\n\n## "+strings.Repeat("x", 6000)+"\n"), Options{}))
	for _, id := range []string{`id="` + strings.Repeat("x", maxSlugLen) + `"`, `id="` + strings.Repeat("x", maxSlugLen) + `-1"`} {
		if !strings.Contains(out, id) {
			t.Errorf("missing capped id %.40s...: two long headings must get distinct capped ids", id)
		}
	}
}
