# 08. A blank `DEMIPLANE_TOKEN` starts the server with no authentication

**Severity: Low.** The operator meant to set a token and gets an open
instance. The generic "no auth token configured" warning is logged and the
banner says `OPEN`, so it is visible to someone who reads the log; under a
service manager it usually is not read.

## Affected code

- `cmd/demiplane/main.go:807-811` (`resolveToken`):
  `strings.TrimSpace(os.Getenv("DEMIPLANE_TOKEN"))`, and an empty result
  means "no token".
- `cmd/demiplane/main.go:802-804`: the same condition for `--token-file` is a
  hard error, `token file %q is empty`.

## Impact

`DEMIPLANE_TOKEN=$(cat /run/secrets/token)` with the secret missing, or a
compose file whose variable did not resolve, leaves the variable set and
empty. The server starts and accepts unauthenticated publish, delete and
list.

## Failing test

`cmd/demiplane/audit_startup_test.go` (build tag `audit`).

```go
func TestAuditBlankTokenEnvIsRefused(t *testing.T) {
	for _, v := range []string{"", "  \n"} {
		t.Setenv("DEMIPLANE_TOKEN", v)
		tok, err := resolveToken("")
		if err == nil {
			t.Errorf("resolveToken with DEMIPLANE_TOKEN=%q = %q, nil: want an error, as for an empty token file", v, tok)
		}
	}
}
```

```
$ go test -tags audit -run TestAuditBlankTokenEnv -count=1 ./cmd/demiplane/
--- FAIL: TestAuditBlankTokenEnvIsRefused (0.00s)
    audit_startup_test.go:54: resolveToken with DEMIPLANE_TOKEN="" = "", nil: want an error, as for an empty token file
    audit_startup_test.go:54: resolveToken with DEMIPLANE_TOKEN="  \n" = "", nil: want an error, as for an empty token file
FAIL
```

## Smallest fix

Use `os.LookupEnv`: unset means no token, set but blank is an error
("DEMIPLANE_TOKEN is set but empty; unset it to run without a token"). The
e2e harness passes `DEMIPLANE_TOKEN=` to mean "none" (`e2e/harness_test.go`,
`startServerWithStoreDir`) and would need to remove the variable instead.
