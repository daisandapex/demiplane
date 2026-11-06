// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package theme

import (
	"strings"
	"testing"
)

// TestBadgeInkIsAToken: the badge label colour is a token like every other
// colour, defined in each palette.
func TestBadgeInkIsAToken(t *testing.T) {
	for name, root := range rootByName {
		if !strings.Contains(root, "--on-badge:") {
			t.Errorf("%s palette has no --on-badge token", name)
		}
	}
}
