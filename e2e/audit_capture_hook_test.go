// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && audit

package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAuditCaptureHookDoesNotImportFromWorkingDirectory: in restricted mode
// (DEMIPLANE_CAPTURE_DIR set) the hook copies the written file through
// `python3 -` with the script on stdin. Python then puts the working
// directory first on its module path, and the script's `import re` loads
// ./re.py when one exists. The hook runs in the project directory after every
// Write or Edit, so a file a model was told to write there, or one a cloned
// repository ships, runs as the user, outside the harness's permission
// checks, the first time the hook fires. The marker module below only
// creates an empty file. audit/01-capture-hook-imports-from-cwd.md.
func TestAuditCaptureHookDoesNotImportFromWorkingDirectory(t *testing.T) {
	for _, bin := range []string{"bash", "curl", "jq", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("skip: %s not on PATH", bin)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"url":"http://127.0.0.1/x"}`))
	}))
	t.Cleanup(srv.Close)

	project := t.TempDir()
	marker := filepath.Join(project, "imported-from-cwd")
	module := "import os\nopen(" + jsonQuote(marker) + ", 'w').close()\n"
	if err := os.WriteFile(filepath.Join(project, "re.py"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(project, "note.html")
	if err := os.WriteFile(page, []byte("<!doctype html><html>note</html>\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(repoRoot, "companion/capture-hook/demiplane-capture.sh"))
	cmd.Dir = project // a hook runs in the project directory
	cmd.Env = append(os.Environ(), "DEMIPLANE_URL="+srv.URL, "DEMIPLANE_TOKEN=",
		"DEMIPLANE_CAPTURE=1", "DEMIPLANE_CAPTURE_DIR="+project, "NO_PROXY=*", "no_proxy=*")
	cmd.Stdin = strings.NewReader(`{"tool_input":{"file_path":` + jsonQuote(page) + `}}`)
	out, _ := cmd.CombinedOutput()

	if _, err := os.Stat(marker); err == nil {
		t.Errorf("the hook imported re.py from its working directory and ran it\nhook output:\n%s", out)
	}
}
