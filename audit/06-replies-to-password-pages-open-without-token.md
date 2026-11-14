# 06. On an instance with no token, replies to password-protected pages can be read and deleted by anyone

**Status: Fixed** in `fix(reply): keep replies to protected pages out of a tokenless inbox` and `fix(reply): record each reply's protection and filter before paging`. With no token configured, the inbox acts only on replies written to an artifact anyone could read that still answer one anyone can read. Each reply records whether its artifact was private or password-protected when it was written, so republishing the slug unprotected does not expose it; legacy replies are backfilled from their artifact at startup and count as protected until then. The filter is part of the list query, so it runs before paging and cursors come only from visible replies; ack and delete answer `404` for a hidden reply exactly as for a missing id; `DELETE /replies?slug=` removes only visible replies. With a token nothing changes, including deleting the replies of an artifact that no longer exists. Tests: `internal/modules/reply/protection_test.go` (`TestReplyKeepsItsArtifactsProtection`, `TestTokenlessPagingSkipsHiddenReplies`, `TestTokenlessChangeOfHiddenReplyLooksMissing`, `TestTokenHolderManagesRepliesOfAGoneArtifact`, `TestRepliesFromBeforeProtectionWasRecorded`) and `internal/server/reply_integration_test.go` (build tag `reply`), `TestRepliesToPasswordPageNeedACredential` and `TestTokenlessInboxKeepsPublicReplies`, outside the `audit` tag.

**Severity: Low.** It only applies when no token is configured, a state the
server warns about at start-up, and in that state anyone can already publish
and delete. What they cannot otherwise do is read a password-protected page
or what was written in answer to it; this lets them read the answers. `-tags
reply` builds only.

## Affected code

- `internal/modules/reply/reply.go:86-89`: `GET /replies`,
  `POST /replies/{id}/ack`, `DELETE /replies/{id}` and `DELETE /replies`
  are wrapped in `host.RequireAuth`.
- `internal/server/auth.go:21-24`: `requireAuth` passes every request
  through when no token is configured.

The newer owner routes do not behave this way: `requireOwner`
(`internal/server/manage.go:35-38`) answers `403` with configuration advice
when there is no token.

## Impact

The view password is a separate layer from the publish token: a tokenless
instance still refuses `GET /{slug}` and `POST /reply/{slug}` for a
protected page without its password. The reply inbox is outside that layer.
`GET /replies?status=all` returns every stored reply, with the slug it
answers and its text, to anyone who can reach the control plane, and the
same caller can acknowledge or delete them. The start-up warning lists
publish, delete and list as unauthenticated; it does not mention replies.

## Failing test

`internal/server/audit_replies_open_test.go` (build tags `audit reply`).

```go
func TestAuditRepliesToPasswordPageNeedACredential(t *testing.T) {
	ts := newTestServer(t, "") // no token
	publishPassword(t, ts, "?slug=review&reply=form", "letmein", "<html>confidential draft</html>")

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/reply/review",
		strings.NewReader(`{"kind":"comment","body":"the merger closes on the 3rd"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("x", "letmein")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("reply with the view password = %d, want 201", resp.StatusCode)
	}

	// No password, no token.
	got, err := http.Get(ts.URL + "/replies?status=all")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(got.Body)
	got.Body.Close()
	if got.StatusCode == http.StatusOK && strings.Contains(string(body), "the merger closes") {
		t.Errorf("GET /replies with no credential = 200 and returns the reply to a password-protected page: %.200s", body)
	}
}
```

```
$ go test -tags "audit reply" -run TestAuditRepliesToPasswordPage -count=1 ./internal/server/
--- FAIL: TestAuditRepliesToPasswordPageNeedACredential (0.03s)
    audit_replies_open_test.go:47: GET /replies with no credential = 200 and returns the reply to a password-protected page: {"count":1,"replies":[{"id":1,"slug":"review","kind":"comment","body":"the merger closes on the 3rd","created_at":"2026-11-14T18:12:17Z","status":"pending"}]}
FAIL
```

`TestHandlerMountsReplyModule` (`reply_integration_test.go`) currently
expects `GET /replies` to answer `200` on a tokenless server, for a page with
no password; the fix has to decide what that case should do.

## Smallest fix

Give `module.Host` the owner wrapper (`RequireOwner`) and mount the four
`/replies` routes behind it, so they fail closed without a token like
`PATCH /{slug}` and `/manage/delete`. If the tokenless inbox must stay for
unprotected pages, leave replies whose artifact has a password out of an
unauthenticated listing and refuse to acknowledge or delete them.
