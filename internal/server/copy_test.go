// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"regexp"
	"strings"
	"testing"
)

// TestChromeCopyHasNoEmDash: demiplane's own UI copy refuses em dashes
// (DESIGN.md); code samples are exempt.
func TestChromeCopyHasNoEmDash(t *testing.T) {
	ts := newTestServer(t, "")
	strip := regexp.MustCompile(`(?s)<(pre|code|script|style)\b.*?</(pre|code|script|style)>`)
	for _, p := range []string{"/", "/docs", "/help", "/connect", "/gallery"} {
		_, body := get(t, ts.URL+p)
		page := strip.ReplaceAllString(string(body), "")
		for _, d := range []string{"—", "&mdash;", "&#8212;"} {
			if i := strings.Index(page, d); i >= 0 {
				lo := max(0, i-60)
				t.Errorf("%s: em dash in UI copy: …%s…", p, page[lo:min(len(page), i+60)])
			}
		}
	}
}
