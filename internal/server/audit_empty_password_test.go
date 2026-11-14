// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build audit

package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestAuditEmptyPasswordHeaderIsRefused: a publish that carries the
// X-Demiplane-Password header asked for a password gate. When the value is
// empty (an unset shell variable, a blank form field) the server must refuse
// the publish, as it does for a malformed ?private or a blank ?ttl, instead
// of storing the artifact with no gate. audit/05-empty-password-header.md.
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
