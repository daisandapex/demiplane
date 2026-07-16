// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"net/http"
	"strings"
)

// requireAuth wraps a handler with bearer-token authentication for the publish
// auth layer (who can WRITE/LIST). If no token is configured the wrapper is a
// pass-through — the server logs a loud warning at startup in that case, so the
// "unauthenticated" posture is explicit rather than silent. View auth for
// GET /{slug} is a separate layer (network / capability URL) and is not gated
// here.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.authToken == "" {
			next(w, r)
			return
		}
		if !validBearer(r.Header.Get("Authorization"), s.authToken, s.tokenMAC) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="demiplane"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// validBearer reports whether the Authorization header carries the expected
// bearer token. The scheme match is case-insensitive per RFC 7235.
//
// The token match HMACs both candidate and expected token under a per-process
// random key and compares the fixed-length digests with hmac.Equal. This is
// both constant-time AND length-hiding: a plain subtle.ConstantTimeCompare
// short-circuits when the lengths differ, leaking the token's length; hashing to
// equal-length digests first removes that side channel.
func validBearer(header, want string, key []byte) bool {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	got := strings.TrimSpace(header[len(prefix):])
	return hmac.Equal(tokenDigest(key, got), tokenDigest(key, want))
}

// tokenDigest returns HMAC-SHA256(key, token) — a fixed 32-byte digest.
func tokenDigest(key []byte, token string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(token))
	return mac.Sum(nil)
}
