// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"regexp"
	"testing"
)

// TestChromeColoursAreTokens: every colour in the chrome sheet is a token.
func TestChromeColoursAreTokens(t *testing.T) {
	if m := regexp.MustCompile(`oklch\([^)]*\)|#[0-9a-fA-F]{3,8}\b`).FindString(chromeCSS); m != "" {
		t.Errorf("chrome sheet hard-codes a colour: %s", m)
	}
}
