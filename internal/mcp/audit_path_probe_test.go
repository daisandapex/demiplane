// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build audit

package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAuditPublishPathOutsideRootAnswersUniformly: openPublishPath calls
// os.Lstat on the model-chosen path and reports its result before it checks
// the publish root. A path outside the root therefore gets one of three
// answers (the lstat error, "must be a regular file", or "outside the publish
// root"), which tells a model that was told to probe whether any path the
// process can stat exists and whether it is a file. Outside the root, every
// path must be refused the same way.
// audit/09-mcp-publish-path-probes-outside-root.md.
func TestAuditPublishPathOutsideRootAnswersUniformly(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	file := filepath.Join(outside, "exists.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, f := newGuardClient(t, "")
	setPublishRoot(t, c, root)

	answers := map[string]string{}
	for name, p := range map[string]string{
		"a file":      file,
		"a directory": outside,
		"nothing":     filepath.Join(outside, "missing.txt"),
	} {
		rerr, _ := publishPathCall(t, c, f, p)
		if rerr == nil {
			t.Fatalf("%s outside the publish root was accepted", name)
		}
		answers[name] = rerr.Message
	}
	if answers["a file"] != answers["a directory"] || answers["a file"] != answers["nothing"] {
		t.Errorf("refusals for paths outside the publish root differ by what is there:\n  a file:      %q\n  a directory: %q\n  nothing:     %q",
			answers["a file"], answers["a directory"], answers["nothing"])
	}
}
