# 03. A loopback instance answers any Host, so a web page can reach it by DNS rebinding

**Severity: Medium.** On the default instance (`demiplane serve --store dir`:
loopback, no token) a web page the operator merely visits can publish, delete
and list. With a token set, the same page can still read every public
artifact of an instance that was meant to be reachable from the machine
only. It needs the operator's browser to load a hostile page and a browser
that does not block public-to-loopback requests, which is why it is not rated
higher; the default configuration is the affected one, which is why it is not
rated lower.

## Affected code

- `internal/server/hostpolicy.go:149-152` (`hostPolicy`): with no
  `--allowed-hosts`, there is no policy at all and every `Host` is served.
- `cmd/demiplane/main.go:263`: the list comes from the flag only; nothing is
  derived from a loopback `--bind`.
- `internal/server/server.go:414-435` (`crossOriginWrite`): `Sec-Fetch-Site:
  same-origin` is accepted as proof that a write is first-party, and without
  `--base-url` the fallback compares `Origin` with the request's own `Host`
  (`controlOrigin`, `server.go:398-409`). Both are true for a rebound page.

## Impact

A page served from `http://name.attacker.example:8080/` whose name is then
re-pointed at `127.0.0.1` is, to the browser, the same origin as the control
plane. Its requests carry `Sec-Fetch-Site: same-origin`, so the cross-origin
write guard admits them, and it may read the responses. Without a token that
is the whole control API: `POST /publish` (including over existing named
slugs), `DELETE /{slug}`, `GET /list`. With a token the control plane is
closed to it, but the content listener is not: the page can fetch public
artifacts (friendly slugs come from about 5,700 combinations) and send them
elsewhere. `SECURITY.md` names network reachability as the first tier of view
authorisation and says the write guard "holds on bare loopback with no DNS,
certs, or proxy"; a loopback bind is exactly where a rebound name still
reaches the listener. `--allowed-hosts` closes it, but it is off by default
and documented for public deployments and Host reflection, not for this.

## Failing test

`internal/server/audit_loopback_host_test.go` (build tag `audit`).

```go
func TestAuditLoopbackListenerRefusesForeignHost(t *testing.T) {
	control, content := newSplitServers(t, Config{})
	publish(t, control, "?slug=notes", "<p>internal notes</p>")

	const foreign = "rebind.attacker.example:8080"
	send := func(method, url, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		// What a browser sends from a page it loaded from http://<foreign>/
		// once that name resolves to this listener.
		req.Host = foreign
		req.Header.Set("Origin", "http://"+foreign)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}

	for _, tc := range []struct {
		name, method, url, body string
	}{
		{"publish", http.MethodPost, control.URL + "/publish?slug=planted", "<p>planted</p>"},
		{"list", http.MethodGet, control.URL + "/list", ""},
		{"read artifact", http.MethodGet, content.URL + "/notes", ""},
		{"delete", http.MethodDelete, control.URL + "/notes", ""},
	} {
		code, body := send(tc.method, tc.url, tc.body)
		if code != http.StatusMisdirectedRequest {
			t.Errorf("%s with Host %s on a loopback listener = %d (%.80q), want 421", tc.name, foreign, code, body)
		}
	}
}
```

```
$ go test -tags audit -run TestAuditLoopbackListener -count=1 ./internal/server/
--- FAIL: TestAuditLoopbackListenerRefusesForeignHost (0.04s)
    audit_loopback_host_test.go:55: publish with Host rebind.attacker.example:8080 on a loopback listener = 201 ("http://127.0.0.1:37969/planted"), want 421
    audit_loopback_host_test.go:55: list with Host rebind.attacker.example:8080 on a loopback listener = 200 ("{\"artifacts\":[{\"kind\":\"artifact\",\"render\":\"raw\",\"reply\":false,\"slug\":\"notes\",\"ur"), want 421
    audit_loopback_host_test.go:55: read artifact with Host rebind.attacker.example:8080 on a loopback listener = 200 ("<p>internal notes</p>"), want 421
    audit_loopback_host_test.go:55: delete with Host rebind.attacker.example:8080 on a loopback listener = 204 (""), want 421
FAIL
```

The same request against the built binary, started as `demiplane serve
--store <dir> --bind 127.0.0.1:18080` with nothing else:

```
$ curl -s -D - -H 'Host: rebind.attacker.example:18080' \
    -H 'Origin: http://rebind.attacker.example:18080' -H 'Sec-Fetch-Site: same-origin' \
    --data-binary '<p>planted</p>' 'http://127.0.0.1:18080/publish?slug=planted'
HTTP/1.1 201 Created
http://rebind.attacker.example:18081/planted
```

## Smallest fix

In `hostPolicy`, when no rules are configured, look at the address the
request arrived on (`http.LocalAddrContextKey`): if it is a loopback address,
accept only a `Host` whose name is `localhost` or a loopback IP literal (plus
the `--base-url` and `--content-base-url` hosts) and answer `421` otherwise.
LAN, mesh and wildcard binds keep today's behaviour, and nothing needs
configuring. The existing tests use `127.0.0.1` hosts and keep passing.
