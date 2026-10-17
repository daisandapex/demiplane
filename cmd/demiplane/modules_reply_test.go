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
	t.Cleanup(func() { _ = reply.ConfigureHook("", "", reply.DefaultHookOptions()) })

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

	p2 := filepath.Join(t.TempDir(), "config")
	writeFile(t, p2, "reply_hook_exec = true\nreply_hook_timeout = 30s\nreply_hook_workers = 4\nreply_hook_queue = 0\n")
	m2, err := loadConfig(p2)
	if err != nil {
		t.Fatalf("reply_hook pool keys should parse: %v", err)
	}
	if err := applyModuleConfig(m2); err != nil {
		t.Fatalf("valid hook pool config rejected: %v", err)
	}
	for _, bad := range []map[string]string{
		{"reply_hook_timeout": "soon"},
		{"reply_hook_timeout": "-1s"},
		{"reply_hook_workers": "0"},
		{"reply_hook_workers": "many"},
		{"reply_hook_queue": "-1"},
	} {
		if err := applyModuleConfig(bad); err == nil {
			t.Errorf("invalid hook pool config %v accepted", bad)
		}
	}
}
