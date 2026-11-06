// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"strings"
	"testing"
)

// TestChromeAccessibility: chrome pages open with a skip link to main, and
// the site nav is labelled.
func TestChromeAccessibility(t *testing.T) {
	ts := newTestServer(t, "")
	for _, p := range []string{"/", "/docs", "/help", "/connect", "/gallery"} {
		_, body := get(t, ts.URL+p)
		page := string(body)
		body0 := strings.Index(page, "<body>")
		skip := strings.Index(page, `<a class="skip" href="#dp-main">Skip to content</a>`)
		if skip < 0 || skip < body0 || skip > strings.Index(page, "<header") {
			t.Errorf("%s: no skip link before the header", p)
		}
		if !strings.Contains(page, `<main id="dp-main" class="doc">`) {
			t.Errorf("%s: main is not the skip target", p)
		}
		if !strings.Contains(page, `<nav aria-label="Site">`) {
			t.Errorf("%s: site nav has no label", p)
		}
	}
}
