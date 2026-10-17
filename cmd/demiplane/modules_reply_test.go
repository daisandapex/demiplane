// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build reply

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/modules/reply"
)

// TestReplyHookConfigKeys pins the -tags reply config wiring: the reply_hook_*
// keys parse (they are registered iff the module is compiled in) and a bad
// webhook URL is a hard startup error, per the config fail-loud contract.
func TestReplyHookConfigKeys(t *testing.T) {
	t.Cleanup(func() { _ = reply.ConfigureHook("", "", false) })

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

// TestReplyHookLogOutputKey: the output-logging opt-in parses as on/off, and
// anything else is a hard startup error.
func TestReplyHookLogOutputKey(t *testing.T) {
	t.Cleanup(func() { _ = reply.ConfigureHook("", "", false) })
	for _, v := range []string{"on", "off", "true", "no"} {
		if err := applyModuleConfig(map[string]string{"reply_hook_exec": "true", "reply_hook_log_output": v}); err != nil {
			t.Errorf("reply_hook_log_output = %s rejected: %v", v, err)
		}
	}
	if err := applyModuleConfig(map[string]string{"reply_hook_log_output": "loud"}); err == nil {
		t.Error("reply_hook_log_output = loud must be a hard startup error")
	}
	p := filepath.Join(t.TempDir(), "config")
	writeFile(t, p, "reply_hook_log_output = on\n")
	if _, err := loadConfig(p); err != nil {
		t.Errorf("reply_hook_log_output should be a recognized key: %v", err)
	}
}

// TestReplyHookURLErrorRedacted: a rejected webhook URL is reported by origin
// only, since the value is a credential.
func TestReplyHookURLErrorRedacted(t *testing.T) {
	t.Cleanup(func() { _ = reply.ConfigureHook("", "", false) })
	err := applyModuleConfig(map[string]string{"reply_hook_url": "ftp://u:pw@host/secretpath?token=q"})
	if err == nil {
		t.Fatal("ftp webhook URL accepted")
	}
	for _, leak := range []string{"pw", "secretpath", "token=q"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("startup error leaked %q: %v", leak, err)
		}
	}
}
