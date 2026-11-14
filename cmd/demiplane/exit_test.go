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

// TestResolveTokenClearsTheEnvironment: a token taken from DEMIPLANE_TOKEN
// leaves the process environment, so nothing the server starts inherits it.
func TestResolveTokenClearsTheEnvironment(t *testing.T) {
	t.Setenv("DEMIPLANE_TOKEN", " tok ")
	got, err := resolveToken("")
	if err != nil || got != "tok" {
		t.Fatalf("resolveToken = %q, %v", got, err)
	}
	if v, ok := os.LookupEnv("DEMIPLANE_TOKEN"); ok {
		t.Fatalf("DEMIPLANE_TOKEN still set to %q", v)
	}
}

// TestBlankTokenEnvIsRefused: an empty token file is a startup error, and so
// is DEMIPLANE_TOKEN set to an empty or blank value (a failed command
// substitution, a missing secret), which would otherwise start the server
// with no authentication. See audit/08-blank-token-env-starts-open.md.
func TestBlankTokenEnvIsRefused(t *testing.T) {
	for _, v := range []string{"", "  \n"} {
		t.Setenv("DEMIPLANE_TOKEN", v)
		tok, err := resolveToken("")
		if err == nil {
			t.Errorf("resolveToken with DEMIPLANE_TOKEN=%q = %q, nil: want an error, as for an empty token file", v, tok)
		}
	}
}
