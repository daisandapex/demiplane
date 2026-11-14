# 09. The MCP `publish` tool tells a model whether any path outside the publish root exists

**Status: Fixed** in `fix(mcp): refuse a publish path outside the root before stat-ing it` and `fix(mcp): resolve a publish path inside the root before any lstat`. A path whose text is not under the root is refused with the one outside-the-root message before any filesystem call. A path whose text is under it is resolved one component at a time through the root's descriptor (`os.Root` lstat and readlink), so only entries inside the root are examined; a symlink whose target leaves the root, or a path that reaches the root through a `..` outside it, gets the same message before anything past it is looked at. Tests: `internal/mcp/guard_test.go`, `TestPublishPathOutsideRootAnswersUniformly`, `TestPublishPathRootThroughSymlink` and `TestPublishPathThroughSymlinkOutOfRootAnswersUniformly`, plus the `FuzzOpenPublishPath` seeds, outside the `audit` tag.

**Severity: Low.** Nothing is read or published from outside the root; the
confinement itself held up under review. But the tool's arguments are chosen
by the model, and a model that has been told what to do by a page it fetched
can use the refusal text to learn whether a path exists and whether it is a
regular file, anywhere the process can `stat`: other users' home directories,
key files, `/proc/<pid>`.

## Affected code

- `internal/mcp/tools.go:279-285` (`openPublishPath`): `os.Lstat(p)` runs on
  the raw argument, and its error, or the "must be a regular file" refusal,
  is returned.
- `internal/mcp/tools.go:286-297`: the publish-root check comes after.

## Impact

Three different answers for a path outside the root: `read path: lstat
<path>: no such file or directory` (or `permission denied`), `path must be a
regular file`, and `path is outside the publish root`. `SECURITY.md` treats
the `path` argument as hostile ("a prompt-injected model can pick them") and
promises confinement to the root; the answer to a refused path is part of
what the model gets back.

## Failing test

`internal/mcp/audit_path_probe_test.go` (build tag `audit`).

```go
func TestAuditPublishPathOutsideRootAnswersUniformly(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	file := filepath.Join(outside, "exists.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, f := newGuardClient(t, "")
	setPublishRoot(t, c, root)

	answers := map[string]string{}
	for name, p := range map[string]string{
		"a file":      file,
		"a directory": outside,
		"nothing":     filepath.Join(outside, "missing.txt"),
	} {
		rerr, _ := publishPathCall(t, c, f, p)
		if rerr == nil {
			t.Fatalf("%s outside the publish root was accepted", name)
		}
		answers[name] = rerr.Message
	}
	if answers["a file"] != answers["a directory"] || answers["a file"] != answers["nothing"] {
		t.Errorf("refusals for paths outside the publish root differ by what is there:\n  a file:      %q\n  a directory: %q\n  nothing:     %q",
			answers["a file"], answers["a directory"], answers["nothing"])
	}
}
```

```
$ go test -tags audit -run TestAuditPublishPath -count=1 ./internal/mcp/
--- FAIL: TestAuditPublishPathOutsideRootAnswersUniformly (0.00s)
    audit_path_probe_test.go:45: refusals for paths outside the publish root differ by what is there:
          a file:      "path is outside the publish root"
          a directory: "path must be a regular file (symlinks and special files are refused)"
          nothing:     "read path: lstat …/002/missing.txt: no such file or directory"
FAIL
```

## Smallest fix

Decide "inside or outside" from the path text first: make the argument
absolute and clean it, and refuse with the one "outside the publish root"
message unless it is lexically under `publishRootPath`. Only then `Lstat` and
resolve symlinks, and give a path that resolves outside the root that same
message.
