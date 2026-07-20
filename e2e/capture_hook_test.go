// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCaptureHookScript_CLIMode exercises
// companion/capture-hook/demiplane-capture.sh — which has ZERO existing test
// coverage — against a real running server, in its CLI publish mode
// (`demiplane-capture <file>`, as opposed to the Claude Code PostToolUse hook
// mode). Requires bash, curl, and jq on PATH; skips cleanly (not a failure)
// if any are missing, since the script itself declares them as dependencies.
func TestCaptureHookScript_CLIMode(t *testing.T) {
	for _, bin := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("skip: %s not on PATH (a documented dependency of demiplane-capture.sh)", bin)
		}
	}

	scriptPath := filepath.Join(repoRoot, "companion", "capture-hook", "demiplane-capture.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("capture-hook script not found at %s: %v", scriptPath, err)
	}

	srv := startServer(t, serverOpts{Token: "capture-hook-token"})

	htmlFile := filepath.Join(t.TempDir(), "report.html")
	html := "<!doctype html><html><body><h1>capture hook e2e</h1></body></html>"
	if err := os.WriteFile(htmlFile, []byte(html), 0o644); err != nil {
		t.Fatalf("write fixture html: %v", err)
	}

	cmd := exec.Command("bash", scriptPath, htmlFile)
	cmd.Env = append(os.Environ(),
		"DEMIPLANE_URL="+srv.ControlURL,
		"DEMIPLANE_TOKEN="+srv.Token,
	)
	// The script deliberately logs progress to stderr and prints ONLY the URL
	// to stdout (so `url=$(demiplane-capture file.html)` composes in scripts) —
	// keep the streams separate rather than CombinedOutput(), or the log()
	// line would corrupt the URL this test parses from stdout.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("demiplane-capture.sh CLI mode failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	url := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(url, srv.ContentURL+"/") {
		t.Fatalf("capture-hook stdout was %q, expected exactly a URL on %s (stderr:\n%s)", url, srv.ContentURL, stderr.String())
	}

	got := srv.do(t, "GET", url, nil, nil)
	if got.Status != 200 {
		t.Fatalf("GET %s: status=%d", url, got.Status)
	}
	if string(got.Body) != html {
		t.Fatalf("published body mismatch: got %q want %q", got.Body, html)
	}

	// The token must never appear in the script's own stdout/stderr, mirroring
	// the invariant-4 sweep applied to the REST/MCP surfaces.
	assertNoSecret(t, srv.Token, "capture-hook script output", stdout.String(), stderr.String())
}

// TestCaptureHookScript_HookMode exercises the Claude Code PostToolUse hook
// path: JSON on stdin naming a just-written file, gated by DEMIPLANE_CAPTURE,
// emitting a hookSpecificOutput.additionalContext JSON blob on success.
func TestCaptureHookScript_HookMode(t *testing.T) {
	for _, bin := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("skip: %s not on PATH", bin)
		}
	}

	scriptPath := filepath.Join(repoRoot, "companion", "capture-hook", "demiplane-capture.sh")
	srv := startServer(t, serverOpts{Token: "capture-hook-token-2"})

	dir := t.TempDir()
	htmlFile := filepath.Join(dir, "note.html")
	html := "<!doctype html><html><body>hook mode</body></html>"
	if err := os.WriteFile(htmlFile, []byte(html), 0o644); err != nil {
		t.Fatalf("write fixture html: %v", err)
	}

	hookInput := `{"tool_input":{"file_path":` + jsonQuote(htmlFile) + `}}`

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"DEMIPLANE_URL="+srv.ControlURL,
		"DEMIPLANE_TOKEN="+srv.Token,
		"DEMIPLANE_CAPTURE=1",
	)
	cmd.Stdin = strings.NewReader(hookInput)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("demiplane-capture.sh hook mode failed: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(string(out), "additionalContext") {
		t.Fatalf("hook mode did not emit hookSpecificOutput: %s", out)
	}
	if !strings.Contains(string(out), srv.ContentURL) {
		t.Fatalf("hook mode output missing the published URL: %s", out)
	}
	assertNoSecret(t, srv.Token, "capture-hook hook-mode output", string(out))

	// The gate: without DEMIPLANE_CAPTURE=1, the hook must no-op (never
	// publish), since the script promises "no surprise publishing".
	cmdGated := exec.Command("bash", scriptPath)
	cmdGated.Env = append(os.Environ(),
		"DEMIPLANE_URL="+srv.ControlURL,
		"DEMIPLANE_TOKEN="+srv.Token,
	)
	cmdGated.Stdin = strings.NewReader(hookInput)
	gatedOut, err := cmdGated.CombinedOutput()
	if err != nil {
		t.Fatalf("gated hook run failed: %v\noutput:\n%s", err, gatedOut)
	}
	if strings.TrimSpace(string(gatedOut)) != "" {
		t.Fatalf("hook published without DEMIPLANE_CAPTURE=1: %s", gatedOut)
	}
}

func jsonQuote(s string) string {
	b := strings.Builder{}
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
