// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"strings"
	"testing"
)

// TestNotFoundHasNoWordmark: the themed 404 opens with its message, not the
// tool's name.
func TestNotFoundHasNoWordmark(t *testing.T) {
	_, content := newSplitServers(t, Config{})
	_, body := get(t, content.URL+"/does-not-exist")
	if strings.Contains(string(body), "nf-kicker") || strings.Contains(string(body), ">demiplane</span>") {
		t.Errorf("themed 404 still carries the wordmark kicker:\n%s", body)
	}
}
