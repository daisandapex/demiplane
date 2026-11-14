# 01. The capture hook runs `re.py` from the project directory

**Severity: High.** The outcome is arbitrary code running as the user. It needs
two things that are both ordinary: the hook wired in its restricted mode
(`DEMIPLANE_CAPTURE=1` with `DEMIPLANE_CAPTURE_DIR`, the setting that is
meant to confine what the hook may publish), and a file named `re.py` in the directory the
hook runs in. A model that may write files but must ask before running
commands can create that file itself, and the hook fires on that same write;
a cloned repository can simply ship one. It is not rated Critical because the
hook is an opt-in companion script, not the server.

## Affected code

- `companion/capture-hook/demiplane-capture.sh:224` starts the copy step as
  `python3 - "$DEMIPLANE_CAPTURE_DIR" "$file" ...` with the script on stdin.
- `companion/capture-hook/demiplane-capture.sh:227` is `import re`.

When Python reads its script from stdin it puts the empty string, meaning the
current directory, first on `sys.path`. `os`, `stat` and `sys` are already
loaded at start-up, so they are not looked up again; `re` is not, so
`import re` loads `./re.py` when one exists. No `-I`, `-P` or `-E` is passed.

## Impact

A Claude Code `PostToolUse` hook runs in the project directory after every
`Write` or `Edit`. With the hook in restricted mode, the copy step runs for
every written file, whatever its type, before the hook decides whether the
file is an artifact. So the first write after `re.py` exists runs it, with the
user's full privileges and outside the harness's permission prompts and
sandbox: the write of `re.py` is itself enough to trigger it. The rest of the
restricted-mode logic (directory descriptors, `O_NOFOLLOW`, hard-link and size
checks, publishing from a private snapshot) was read and is sound; this
import path goes around all of it. In the reproduction the hook still
published the page afterwards and reported success, so nothing looks wrong to
the user.

## Failing test

`e2e/audit_capture_hook_test.go` (build tags `e2e audit`), in the style of
`e2e/capture_hook_test.go`. The planted module only creates an empty marker
file.

```go
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
```

```
$ go test -tags "e2e audit" -run TestAuditCaptureHook -count=1 ./e2e/
    audit_capture_hook_test.go:62: the hook imported re.py from its working directory and ran it
        hook output:
        demiplane-capture: published note.html → http://127.0.0.1/x
        {
          "hookSpecificOutput": {
            "hookEventName": "PostToolUse",
            "additionalContext": "Published note.html to demiplane: http://127.0.0.1/x — share this internal URL, not a public host."
          }
        }
FAIL
FAIL	github.com/daisandapex/demiplane/v3/e2e	0.961s
```

## Smallest fix

Run the step as `python3 -I - ...` (isolated mode: no current directory, no
user site directory, no `PYTHON*` variables). `-I` exists from Python 3.4.
The script uses only the standard library, so nothing else changes.
