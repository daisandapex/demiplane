// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build audit && reply

package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestAuditRepliesToPasswordPageNeedACredential: on an instance with no
// token, the reply inbox (GET /replies) passes through requireAuth like the
// legacy listing does. But the inbox holds what viewers wrote in answer to a
// password-protected page, which only someone holding the view password could
// read or answer. Reading it must take a credential: the page's password, or
// a configured token (fail closed, as PATCH and /manage/delete do).
// audit/06-replies-to-password-pages-open-without-token.md.
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
