// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPPublishRoot(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEMIPLANE_PUBLISH_ROOT", "")

	t.Run("flag", func(t *testing.T) {
		got, err := mcpPublishRoot(dir)
		if err != nil || got != real {
			t.Fatalf("got %q, %v; want %q", got, err, real)
		}
	})
	t.Run("env", func(t *testing.T) {
		t.Setenv("DEMIPLANE_PUBLISH_ROOT", dir)
		got, err := mcpPublishRoot("")
		if err != nil || got != real {
			t.Fatalf("got %q, %v; want %q", got, err, real)
		}
	})
	t.Run("explicit missing dir is an error", func(t *testing.T) {
		if _, err := mcpPublishRoot(filepath.Join(dir, "nope")); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("explicit file is an error", func(t *testing.T) {
		if _, err := mcpPublishRoot(file); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("default is the working directory", func(t *testing.T) {
		t.Chdir(dir)
		got, err := mcpPublishRoot("")
		if err != nil || got != real {
			t.Fatalf("got %q, %v; want %q", got, err, real)
		}
	})
	t.Run("working directory / disables path publishing", func(t *testing.T) {
		t.Chdir("/")
		got, err := mcpPublishRoot("")
		if err != nil || got != "" {
			t.Fatalf("got %q, %v; want disabled", got, err)
		}
	})
	t.Run("explicit / is honoured", func(t *testing.T) {
		got, err := mcpPublishRoot("/")
		if err != nil || got != "/" {
			t.Fatalf("got %q, %v; want /", got, err)
		}
	})
}
