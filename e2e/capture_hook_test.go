// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// Exercise the client-side boundary directly: server authorization cannot stop
// an authorized hook from leaking a local file outside its capture directory.
func TestCaptureHookScript_CaptureDir(t *testing.T) {
	for _, bin := range []string{"bash", "curl", "jq", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("skip: %s not on PATH", bin)
		}
	}
	realCurl, err := exec.LookPath("curl")
	if err != nil {
		t.Fatal(err)
	}
	const html = "<!doctype html><html>inside capture</html>\n"
	const secret = "<!doctype html><html>outside secret</html>\n"
	for _, tc := range []struct {
		name, path, root, replace string
		publish, render           bool
	}{
		{name: "direct", path: "note.html", publish: true},
		{name: "nested", path: "nested/note.html", publish: true},
		{name: "trailing root slash", path: "note.html", root: "slash", publish: true},
		{name: "filesystem root", path: "note.html", root: "filesystem", publish: true},
		{name: "configured root symlink", path: "note.html", root: "alias", publish: true},
		{name: "configured root parent", path: "note.html", root: "parent", publish: true},
		{name: "relative root", path: "note.html", root: "relative"},
		{name: "relative file", path: "note.html", root: "relative-file"},
		{name: "parent traversal", path: "../secret.html"},
		{name: "internal parent traversal", path: "nested/../note.html"},
		{name: "sibling prefix", path: "../capture-other/note.html"},
		{name: "leaf symlink", path: "link.html"},
		{name: "directory symlink", path: "linked-dir/secret.html"},
		{name: "internal symlink", path: "internal.html"},
		{name: "missing", path: "missing.html"},
		{name: "directory", path: "directory.html"},
		{name: "fifo", path: "pipe.html"},
		{name: "fragment", path: "fragment.html"},
		{name: "markdown disabled", path: "note.md"},
		{name: "markdown enabled", path: "note.md", publish: true, render: true},
		{name: "leaf replaced before curl", path: "note.html", replace: "file", publish: true},
		{name: "directory replaced before curl", path: "nested/note.html", replace: "directory", publish: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "capture")
			for _, sub := range []string{"capture/nested", "capture/directory.html", "capture-other", "tmp"} {
				if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for name, contents := range map[string]string{
				"capture/note.html": html, "capture/nested/note.html": html,
				"capture/note.md": "# markdown\n", "capture/fragment.html": "<p>fragment</p>",
				"secret.html": secret, "note.html": secret, "capture-other/note.html": secret,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for name, target := range map[string]string{
				"capture/link.html":  filepath.Join(dir, "secret.html"),
				"capture/linked-dir": dir, "capture/internal.html": "note.html", "alias": root,
			} {
				if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "fifo" {
				if err := exec.Command("mkfifo", filepath.Join(root, "pipe.html")).Run(); err != nil {
					t.Skipf("mkfifo unavailable: %v", err)
				}
			}

			type upload struct{ body, query string }
			uploads := make(chan upload, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				uploads <- upload{string(body), r.URL.RawQuery}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"url":"http://synthetic.invalid/artifact"}`)
			}))
			defer srv.Close()

			// Concatenate deliberately: filepath.Join would erase the attack's .. .
			file := root + "/" + tc.path
			if tc.name == "sibling prefix" {
				file = filepath.Join(dir, "capture-other/note.html")
			}
			switch tc.root {
			case "slash":
				root += "/"
			case "filesystem":
				root = "/"
			case "alias":
				root = filepath.Join(dir, "alias")
				file = root + "/" + tc.path
			case "parent":
				root += "/nested/.."
				file = root + "/" + tc.path
			case "relative":
				root = "capture"
			case "relative-file":
				file = "capture/" + tc.path
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(repoRoot, "companion/capture-hook/demiplane-capture.sh"))
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "DEMIPLANE_URL="+srv.URL, "DEMIPLANE_TOKEN=",
				"DEMIPLANE_CAPTURE=1", "DEMIPLANE_CAPTURE_DIR="+root,
				"DEMIPLANE_SLUG=__derive__", "DEMIPLANE_PRIVATE=", "DEMIPLANE_RENDER=0",
				"TMPDIR="+filepath.Join(dir, "tmp"))
			if tc.render {
				cmd.Env = append(cmd.Env, "DEMIPLANE_RENDER=1")
			}
			if tc.replace != "" {
				// Swap the original path at the last boundary before curl opens its
				// input. The published bytes must still be the inspected snapshot.
				bin := filepath.Join(dir, "bin")
				if err := os.Mkdir(bin, 0o700); err != nil {
					t.Fatal(err)
				}
				wrapper := "#!/usr/bin/env bash\nset -e\n"
				if tc.replace == "file" {
					wrapper += "rm -- \"$CAPTURE_TEST_FILE\"\nln -s -- \"$CAPTURE_TEST_DIR/secret.html\" \"$CAPTURE_TEST_FILE\"\n"
				} else {
					wrapper += "mv -- \"$CAPTURE_TEST_DIR/capture/nested\" \"$CAPTURE_TEST_DIR/saved\"\nln -s -- \"$CAPTURE_TEST_DIR\" \"$CAPTURE_TEST_DIR/capture/nested\"\n"
				}
				wrapper += "exec \"$CAPTURE_TEST_CURL\" \"$@\"\n"
				if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(wrapper), 0o700); err != nil {
					t.Fatal(err)
				}
				cmd.Env = append(cmd.Env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
					"CAPTURE_TEST_DIR="+dir, "CAPTURE_TEST_FILE="+file, "CAPTURE_TEST_CURL="+realCurl)
			}
			input, err := json.Marshal(map[string]any{"tool_input": map[string]string{"file_path": file}})
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stdin = bytes.NewReader(input)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("hook failed: %v\n%s", err, out)
			}
			select {
			case got := <-uploads:
				if !tc.publish {
					t.Fatalf("restricted hook uploaded %q", got.body)
				}
				want, query := html, "slug=note"
				if tc.render {
					want, query = "# markdown\n", "slug=note&render=md"
				}
				if got.body != want || got.query != query {
					t.Fatalf("upload = %#v, want body %q query %q", got, want, query)
				}
				if !strings.Contains(string(out), "additionalContext") {
					t.Fatalf("missing hook output: %s", out)
				}
			default:
				if tc.publish {
					t.Fatalf("expected capture, got no upload: %s", out)
				}
			}
			matches, err := filepath.Glob(filepath.Join(dir, "tmp", "*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("snapshot cleanup: %v, %v", matches, err)
			}
		})
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
