// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMCPPublishPathConfined drives the real `demiplane mcp` binary: `path`
// publishes of the process environment, a file outside the publish root, and
// symlinks and hardlinks to the token file are all refused, and nothing
// carrying the token reaches the instance.
func TestMCPPublishPathConfined(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "confine-token"})
	root := t.TempDir()
	mcp := startMCP(t, srv.ControlURL, srv.ContentURL, srv.TokenFile, "--publish-root", root)

	outside := filepath.Join(t.TempDir(), "outside.html")
	if err := os.WriteFile(outside, []byte("<p>outside</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	sym := filepath.Join(root, "sym.html")
	if err := os.Symlink(srv.TokenFile, sym); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"proc environ":     "/proc/self/environ",
		"outside the root": outside,
		"symlink to token": sym,
		// Concatenated, not filepath.Join: Join cleans the ".." away.
		"relative escape": root + "/../" + filepath.Base(filepath.Dir(outside)) + "/outside.html",
	}
	if !strings.Contains(cases["relative escape"], "/../") {
		t.Fatalf("relative escape case %q has no .. component", cases["relative escape"])
	}
	hard := filepath.Join(root, "hard.html")
	if err := os.Link(srv.TokenFile, hard); err == nil {
		cases["hardlink to token"] = hard
	} else {
		t.Logf("hardlink case skipped: %v", err)
	}

	for name, p := range cases {
		msg := mcp.callToolExpectError(t, "publish", map[string]any{"path": p, "slug": "confine-" + strings.ReplaceAll(name, " ", "-")})
		if strings.Contains(msg, "confine-token") {
			t.Errorf("%s: error text carries the token", name)
		}
	}

	list := mcp.callTool(t, "list", nil)
	if strings.Contains(list, "confine-") {
		t.Fatalf("a refused path was published: %s", list)
	}

	inside := filepath.Join(root, "page.html")
	if err := os.WriteFile(inside, []byte("<p>inside</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := mcp.callTool(t, "publish", map[string]any{"path": inside, "slug": "confine-inside"}); !strings.Contains(out, "confine-inside") {
		t.Fatalf("in-root publish result = %q", out)
	}
}
