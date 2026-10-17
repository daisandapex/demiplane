// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
)

// TestExitMessageRedactsPaths: the fatal line of a command (receive's private
// publish in particular) must not carry the capability slug that sits in a
// wrapped rename or open path.
func TestExitMessageRedactsPaths(t *testing.T) {
	const capSlug = "Zq3xW9pLm2Rt8vKs4bNc7yHd"
	err := fmt.Errorf("commit blob: %w", &os.LinkError{
		Op: "rename", Old: "/srv/store/blobs/.tmp-1", New: "/srv/store/blobs/" + capSlug + "~00aa", Err: syscall.EXDEV,
	})
	got := exitMessage(err)
	if strings.Contains(got, capSlug) || strings.Contains(got, "/srv/store") {
		t.Errorf("fatal line leaked a path: %s", got)
	}
	if !strings.Contains(got, "commit blob") {
		t.Errorf("fatal line lost its cause: %s", got)
	}
}
