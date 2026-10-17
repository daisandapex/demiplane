// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build reply

package main

import (
	"path/filepath"
	"testing"
	"time"

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

// TestReplyLimitConfigKeys pins the reply_* admission and storage keys.
func TestReplyLimitConfigKeys(t *testing.T) {
	t.Cleanup(func() { _ = reply.ConfigureLimits(reply.DefaultLimits()) })
	p := filepath.Join(t.TempDir(), "config")
	writeFile(t, p, "reply_max_per_artifact = 50\nreply_max_total = 500\nreply_max_bytes = 1048576\n"+
		"reply_retention = 30d\nreply_rate_client = 5\nreply_rate_target = 0\nreply_rate_global = 100\n")
	m, err := loadConfig(p)
	if err != nil {
		t.Fatalf("reply limit keys should parse: %v", err)
	}
	l, err := replyLimits(m)
	if err != nil {
		t.Fatal(err)
	}
	if l.PerArtifact != 50 || l.Total != 500 || l.TotalBytes != 1<<20 || l.Retention != 30*24*time.Hour {
		t.Fatalf("limits = %+v", l)
	}
	if l.RateClient.String() != "5/min burst 5" || !l.RateTarget.Unlimited() || l.RateGlobal.String() != "100/min burst 100" {
		t.Fatalf("rates = %v %v %v", l.RateClient, l.RateTarget, l.RateGlobal)
	}
	if err := applyModuleConfig(m); err != nil {
		t.Fatalf("valid limits rejected: %v", err)
	}
	if l, _ := replyLimits(map[string]string{"reply_retention": "0"}); l.Retention != 0 {
		t.Fatal("reply_retention = 0 should keep replies forever")
	}
	for _, bad := range []map[string]string{
		{"reply_max_per_artifact": "0"},
		{"reply_max_total": "lots"},
		{"reply_max_bytes": "-5"},
		{"reply_max_bytes": "100"}, // below one reply
		{"reply_retention": "soon"},
		{"reply_rate_client": "-1"},
	} {
		if err := applyModuleConfig(bad); err == nil {
			t.Errorf("invalid reply limit config %v accepted", bad)
		}
	}
}
