// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build audit

package server

import (
	"net/http"
	"testing"

	"github.com/daisandapex/demiplane/v3/internal/limit"
)

// TestAuditThrottledClientGetsNoAnswerFromVerifiedCache: the remembered
// credential (viewauth.go, credKey) is keyed by the stored hash and the
// password only, and is consulted before any budget. Once any viewer has
// opened a password-protected artifact, a different client whose failure
// budget is spent is still told, for the cost of one HMAC and at no rate
// limit, whether a guess is the password: 429 for a wrong one, 200 with the
// body for the right one. A client that the gate refuses "without hashing"
// must be refused whatever it sends. audit/02-password-cache-bypasses-lockout.md.
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
