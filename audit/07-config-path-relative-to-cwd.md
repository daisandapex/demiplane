# 07. With no `HOME`, the config file is read from the working directory

**Status: Fixed** in `00ecfbc`. `configPath` returns `""` with no absolute home, `loadConfig("")` is empty, and a relative `XDG_CONFIG_HOME` is ignored. Test: `cmd/demiplane/config_test.go`, `TestConfigPathIsNeverRelative` and `TestConfigPathIgnoresRelativeXDG`, outside the `audit` tag.

**Severity: Low.** The config file can name a command the server runs
(`reply_hook_exec`, in a `-tags reply` build), so whoever can write
`demiplane/config` under the server's working directory can run code as the
server. It needs an unusual start-up: no `XDG_CONFIG_HOME` and no `HOME` (a
bare service environment), in a working directory someone else can write to.
A systemd system unit's default working directory is `/`, where only root
can create the file.

## Affected code

- `cmd/demiplane/config.go:81-93` (`configPath`): when `os.UserHomeDir`
  fails it returns `filepath.Join("demiplane", "config")`, with the comment
  "Return a path that won't exist". The path is relative, so `loadConfig`
  resolves it against the working directory, where it may exist.

## Impact

Besides `reply_hook_exec` and `reply_hook_url`, the file sets the TLS keys
(`tls = off`) and the reply limits. Nothing is logged about which config file
was read, so the operator has no sign of it.

## Failing test

`cmd/demiplane/audit_startup_test.go` (build tag `audit`).

```go
func TestAuditConfigPathIsNeverRelative(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.MkdirAll(filepath.Join(cwd, "demiplane"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "demiplane", "config"), []byte("footer = off\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := configPath()
	if p != "" && !filepath.IsAbs(p) {
		t.Errorf("configPath() = %q with no HOME: relative to the working directory", p)
	}
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig(%q): %v", p, err)
	}
	if len(cfg) != 0 {
		t.Errorf("a config file in the working directory was loaded: %v", cfg)
	}
}
```

```
$ go test -tags audit -run TestAuditConfigPath -count=1 ./cmd/demiplane/
--- FAIL: TestAuditConfigPathIsNeverRelative (0.00s)
    audit_startup_test.go:34: configPath() = "demiplane/config" with no HOME: relative to the working directory
    audit_startup_test.go:41: a config file in the working directory was loaded: map[footer:off]
FAIL
```

## Smallest fix

Return `""` from `configPath` when there is no home directory, and have
`loadConfig("")` return an empty map. A relative `XDG_CONFIG_HOME` should be
ignored the same way (the XDG specification says so). Logging the config path
at start-up, when a file was read, would make this visible.
