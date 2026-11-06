// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// TestNoEmojiGlyphs: the lock is an inline SVG, so it renders without an
// emoji font, on gallery cards and the landing table alike.
func TestNoEmojiGlyphs(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, Config{})
	card := s.galleryCard("http://x", store.Artifact{Slug: "gated", ContentType: "text/html", PasswordHash: "abc"})
	if strings.Contains(card, "🔒") || !strings.Contains(card, `<svg class="lock"`) {
		t.Errorf("gallery lock is not the SVG icon: %s", card)
	}
	if strings.Contains(lockIcon, "🔒") || !strings.Contains(lockIcon, `aria-label="password protected"`) {
		t.Errorf("lock icon is not a labelled SVG: %s", lockIcon)
	}

	ts := newTestServer(t, "")
	_, body := get(t, ts.URL+"/docs/readme")
	for _, r := range string(body) {
		if r >= 0x1F000 {
			t.Errorf("/docs/readme renders emoji %q, a missing-glyph box without an emoji font", r)
			break
		}
	}
}
