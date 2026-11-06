// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"regexp"
	"strings"
	"testing"
)

// TestTopNavWrapsOnPhones: the chrome nav may not clip items on a narrow
// screen: the row wraps, and the bar grows to hold it.
func TestTopNavWrapsOnPhones(t *testing.T) {
	nav := regexp.MustCompile(`header\.top nav\{([^}]*)\}`).FindStringSubmatch(chromeCSS)
	if nav == nil || !strings.Contains(nav[1], "flex-wrap:wrap") {
		t.Errorf("top nav does not wrap: %v", nav)
	}
	bar := regexp.MustCompile(`header\.top \.wrap\{([^}]*)\}`).FindStringSubmatch(chromeCSS)
	if bar == nil || regexp.MustCompile(`(^|;)height:`).MatchString(bar[1]) {
		t.Errorf("top bar has a fixed height, so a wrapped nav would overflow it: %v", bar)
	}
}
