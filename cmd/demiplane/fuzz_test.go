// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzLoadConfig checks the config file reader: it returns only known keys,
// each with its value trimmed, and refuses a file it cannot read whole.
func FuzzLoadConfig(f *testing.F) {
	for _, s := range []string{
		"", "# c\n", "theme = dark\nfooter = hi = there\n", "nokey\n", "unknown = 1\n",
		" footer_link = https://x \r\n", "theme=\n", "=x\n", "\xff = 1\n",
	} {
		f.Add(s)
	}
	path := filepath.Join(f.TempDir(), "config")
	f.Fuzz(func(t *testing.T, content string) {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(path)
		if err != nil {
			return
		}
		for k, v := range cfg {
			if !configKeys[k] && !moduleConfigKeys[k] {
				t.Fatalf("unknown key %q accepted", k)
			}
			if v != strings.TrimSpace(v) {
				t.Fatalf("value %q of %q not trimmed", v, k)
			}
			if strings.ContainsAny(v, "\n") {
				t.Fatalf("value of %q spans lines: %q", k, v)
			}
		}
	})
}
