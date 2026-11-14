# 02. A locked-out client still gets an answer for the correct password

**Status: Fixed** in `badd483`. The client key is part of the remembered-credential HMAC (the first half of the smallest fix). A guesser that shares the viewer's client key can still be answered from the cache. Test: `internal/server/viewauth_test.go`, `TestThrottledClientGetsNoAnswerFromVerifiedCache`, outside the `audit` tag.

**Severity: Medium.** It removes the online-guessing bound on a
password-protected page that someone is currently reading: no token is
needed, the per-client and per-artifact failure budgets stop applying, and
each guess costs the server one HMAC instead of a 600,000-iteration KDF. It
does not reveal anything by itself; a password that would survive offline
guessing survives this too, which is why it is not rated High. Short or
dictionary passwords do not survive it.

## Affected code

- `internal/server/viewauth.go:239-243` (`viewGate.check`): the remembered
  credential is looked up, and `ViewOK` returned, before any budget is read.
- `internal/server/viewauth.go:183-191` (`credKey`): the cache key is an HMAC
  of the stored hash and the password. It has no client in it.
- `internal/server/viewauth.go:284-302` (`verify`): the budgets that refuse a
  client "without hashing" are only reached on a cache miss.

Every caller of the gate shares it: `GET /{slug}` (`server.go:872`), the
live-reload stream and wrapper (`live.go:444`, `live.go:597`) and the reply
routes (`internal/modules/reply/admission.go:139`).

## Impact

Once any viewer has opened a protected page, its password stays remembered
for ten minutes after each use. During that time a different client that has
spent its ten failures is answered `429` for a wrong guess and `200` with the
page for the right one. The `429` costs no KDF run and is not counted, so the
client can keep asking as fast as requests are served (32 in progress per
client by default). `SECURITY.md` states the bound as 10 failed attempts a
minute per client per artifact, "past it `429` without hashing"; for a page in
use, that bound is not there.

## Failing test

`internal/server/audit_password_cache_test.go` (build tag `audit`).

```go
func TestAuditThrottledClientGetsNoAnswerFromVerifiedCache(t *testing.T) {
	proxies, err := limit.ParseProxies("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ts := newConfiguredServer(t, Config{TrustedProxies: proxies})
	url := publishPassword(t, ts, "?slug=locked", "correct horse", "<html>classified</html>")

	as := func(client, password string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("X-Forwarded-For", client)
		req.SetBasicAuth("x", password)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	const viewer, guesser = "198.51.100.7", "203.0.113.9"

	// A legitimate viewer opens the page; the credential is now remembered.
	if code := as(viewer, "correct horse"); code != http.StatusOK {
		t.Fatalf("viewer with the password = %d, want 200", code)
	}

	// Another client spends its failure budget for this artifact (burst 10).
	throttled := false
	for i := 0; i < 15 && !throttled; i++ {
		switch code := as(guesser, "wrong guess"); code {
		case http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			throttled = true
		default:
			t.Fatalf("wrong guess %d = %d, want 401 or 429", i, code)
		}
	}
	if !throttled {
		t.Fatal("the guessing client was never throttled")
	}

	// Locked out, it must learn nothing more about any guess.
	if code := as(guesser, "another wrong guess"); code != http.StatusTooManyRequests {
		t.Fatalf("throttled client, wrong guess = %d, want 429", code)
	}
	if code := as(guesser, "correct horse"); code != http.StatusTooManyRequests {
		t.Errorf("throttled client, correct guess = %d, want 429: the lockout does not hold for a remembered password", code)
	}
}
```

```
$ go test -tags audit -run TestAuditThrottledClient -count=1 ./internal/server/
--- FAIL: TestAuditThrottledClientGetsNoAnswerFromVerifiedCache (0.03s)
    audit_password_cache_test.go:70: throttled client, correct guess = 200, want 429: the lockout does not hold for a remembered password
FAIL
FAIL	github.com/daisandapex/demiplane/v3/internal/server	0.033s
```

## Smallest fix

Put the client in the cache key: `credKey(client, hash, pass)`, with
`limit.ClientKey(r)` computed before the lookup. A viewer keeps skipping the
KDF on its own repeat requests, which is what the cache is for, and another
address gets no answer from it. A guesser that shares the viewer's client key
(the same NAT address, or every client when a proxy is not listed in
`--trusted-proxy`) would still get one, so also refuse before the lookup when
the client's own budget for the artifact is empty and the request is not from
a flight already in progress. The first change alone makes the test pass.
