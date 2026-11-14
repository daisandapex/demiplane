# 05. An empty `X-Demiplane-Password` publishes with no password and says nothing

**Severity: Low.** The page the publisher meant to protect is stored public
and readable by anyone who can reach the content origin. It takes a mistake
on the publisher's side (an unset variable in `-H "X-Demiplane-Password:
$PW"`, a blank form field) and not an attacker, and the JSON response does
say `"password": false`; the default text response, a bare URL, does not.

## Affected code

- `internal/server/server.go:745`: `if pw := r.Header.Get(PasswordHeader);
  pw != ""`. A header that is present but empty, or only white space (Go trims
  it), is treated as no header.

The site path already distinguishes the two: `unsupportedSiteOption`
(`server.go:832`) refuses a site upload when the header is present at all.
The same handler refuses an unparseable `?private` and a blank `?ttl` "rather
than store the artifact without the protection the caller asked for".

## Impact

A caller who sent the header asked for a gate. The server answers `201` with
the usual URL, and the artifact is served to anyone, listed in `/list` and
the gallery without the lock, with no `no-store` or `no-referrer`.

## Failing test

`internal/server/audit_empty_password_test.go` (build tag `audit`).

```go
func TestAuditEmptyPasswordHeaderIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"an empty", ""},
		{"a blank", "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t, "")
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/publish?slug=locked", strings.NewReader("classified"))
			req.Header[PasswordHeader] = []string{tc.value}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("publish: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("publish with %s %s header = %d (%q), want 400", tc.name, PasswordHeader, resp.StatusCode, strings.TrimSpace(string(body)))
			}
			// Whatever the publish answered, the bytes must not be readable
			// without a password.
			got, page := get(t, ts.URL+"/locked")
			if got.StatusCode == http.StatusOK {
				t.Errorf("GET /locked without credentials = 200 %q: stored with no password gate", page)
			}
		})
	}
}
```

```
$ go test -tags audit -run TestAuditEmptyPasswordHeader -count=1 ./internal/server/
--- FAIL: TestAuditEmptyPasswordHeaderIsRefused (0.09s)
    --- FAIL: TestAuditEmptyPasswordHeaderIsRefused/an_empty (0.05s)
        audit_empty_password_test.go:36: publish with an empty X-Demiplane-Password header = 201 ("http://127.0.0.1:40671/locked"), want 400
        audit_empty_password_test.go:42: GET /locked without credentials = 200 "classified": stored with no password gate
    --- FAIL: TestAuditEmptyPasswordHeaderIsRefused/a_blank (0.04s)
        audit_empty_password_test.go:36: publish with a blank X-Demiplane-Password header = 201 ("http://127.0.0.1:37305/locked"), want 400
        audit_empty_password_test.go:42: GET /locked without credentials = 200 "classified": stored with no password gate
FAIL
```

## Smallest fix

At the top of `handlePublish`, next to the `?password` refusal: if
`len(r.Header.Values(PasswordHeader)) > 0` and the value is empty, answer
`400` ("the X-Demiplane-Password header is empty; omit it to publish without
a password"). The CLI and MCP clients only send the header when a password is
set, so they are unaffected.
