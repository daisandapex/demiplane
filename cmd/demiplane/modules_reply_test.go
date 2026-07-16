// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build reply

package main

import (
	"path/filepath"
	"testing"

	"github.com/daisandapex/demiplane/internal/modules/reply"
)

// TestReplyHookConfigKeys pins the -tags reply config wiring: the reply_hook_*
// keys parse (they are registered iff the module is compiled in) and a bad
// webhook URL is a hard startup error, per the config fail-loud contract.
func TestReplyHookConfigKeys(t *testing.T) {
	t.Cleanup(func() { _ = reply.ConfigureHook("", "") })

	p := filepath.Join(t.TempDir(), "config")
	writeFile(t, p, "reply_hook_exec = /usr/local/bin/professor-hook\nreply_hook_url = http://127.0.0.1:9999/hook\n")
	m, err := loadConfig(p)
	if err != nil {
		t.Fatalf("reply_hook_* keys should parse in a -tags reply build: %v", err)
	}
	if err := applyModuleConfig(m); err != nil {
		t.Fatalf("valid hook config rejected: %v", err)
	}

	if err := applyModuleConfig(map[string]string{"reply_hook_url": "not-a-url"}); err == nil {
		t.Error("a malformed reply_hook_url must be a hard startup error")
	}
}
