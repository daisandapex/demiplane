// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build audit

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAuditConfigPathIsNeverRelative: with neither XDG_CONFIG_HOME nor HOME
// (a service started with a bare environment), configPath falls back to
// "demiplane/config", which loadConfig then resolves against the working
// directory. Whoever can write there chooses the server's configuration,
// including reply_hook_exec in a -tags reply build, a command the server
// runs. audit/07-config-path-relative-to-cwd.md.
func TestAuditConfigPathIsNeverRelative(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.MkdirAll(filepath.Join(cwd, "demiplane"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "demiplane", "config"), []byte("footer = off\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := configPath()
	if p != "" && !filepath.IsAbs(p) {
		t.Errorf("configPath() = %q with no HOME: relative to the working directory", p)
	}
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig(%q): %v", p, err)
	}
	if len(cfg) != 0 {
		t.Errorf("a config file in the working directory was loaded: %v", cfg)
	}
}

// TestAuditBlankTokenEnvIsRefused: an empty token file is a startup error,
// but DEMIPLANE_TOKEN set to an empty or blank value (a failed command
// substitution, a missing secret) starts the server with no authentication.
// audit/08-blank-token-env-starts-open.md.
func TestAuditBlankTokenEnvIsRefused(t *testing.T) {
	for _, v := range []string{"", "  \n"} {
		t.Setenv("DEMIPLANE_TOKEN", v)
		tok, err := resolveToken("")
		if err == nil {
			t.Errorf("resolveToken with DEMIPLANE_TOKEN=%q = %q, nil: want an error, as for an empty token file", v, tok)
		}
	}
}
