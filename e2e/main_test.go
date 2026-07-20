// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

// Package e2e is demiplane's process-level end-to-end suite: it builds the
// real `demiplane` binary, runs `demiplane serve` as a real subprocess, and
// drives it two ways — plain HTTP (the REST surface) and a real `demiplane
// mcp` subprocess speaking stdio JSON-RPC (the MCP surface) — asserting the
// two surfaces agree.
//
// Why a build tag and not testing.Short(): the repo already uses build tags
// (`reply`, `tls`) to gate optional module code, so e2e follows the existing
// idiom rather than introducing a second gating mechanism. A tag also lets CI
// (or a developer) exclude this package by construction — `go test ./...`
// with no tags never touches it, never spawns a subprocess, and never binds a
// port — whereas testing.Short() still compiles and starts the package,
// relying on every test to remember to check the flag. The cost is a build
// tag on every file in this package; that is one line, paid once per file.
//
// Run with:
//
//	go build -tags "reply tls" -o /tmp/demiplane-e2e-bin ./cmd/demiplane   (implicit; TestMain does this)
//	go test -tags "e2e reply tls" -race ./e2e/...
//
// The suite builds its own binary in TestMain (once, not per test) and never
// touches the module-tagged build above directly — that line is just what
// TestMain runs under the hood via `go build`.
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// binPath is the path to the demiplane binary built once by TestMain. Tests
// read it directly; nothing else in the package writes to it.
var binPath string

// repoRoot is the module root (this package's parent directory), computed
// once in TestMain and used to build the binary and locate companion/.
var repoRoot string

func TestMain(m *testing.M) {
	root, err := findRepoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: locate repo root:", err)
		os.Exit(1)
	}
	repoRoot = root

	dir, err := os.MkdirTemp("", "demiplane-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: mkdir temp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	bin := filepath.Join(dir, "demiplane")
	if runtimeIsWindows() {
		bin += ".exe"
	}

	// Build ONCE for the whole run, with every module tag so the full feature
	// grid (reply, tls) is present in the binary under test — matching CI's
	// `go build -tags reply ./...` plus tls, which nothing currently builds
	// together with an actual server/MCP integration test.
	cmd := exec.Command("go", "build", "-tags", "reply tls", "-o", bin, "./cmd/demiplane")
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: build demiplane binary:", err)
		os.Exit(1)
	}
	binPath = bin

	os.Exit(m.Run())
}

// findRepoRoot walks up from the working directory (package dir under `go
// test`) looking for go.mod, so the suite works regardless of the directory
// `go test` is invoked from.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

func runtimeIsWindows() bool { return os.PathSeparator == '\\' }
