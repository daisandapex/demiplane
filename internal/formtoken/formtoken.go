// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package formtoken mints and checks reply-form submission tokens.
//
// A token proves that a reply submission came from a form demiplane itself
// issued for that artifact: the inline reply box baked into a ?reply=question
// page, or the reply module's /reply/{slug} form. It is bound to the target
// slug and to the moment it was issued, and signed with a key kept in the
// store (so baked pages keep working across restarts):
//
//	<issued-at unix nanoseconds>.<base64url(HMAC-SHA256(key, label|slug|issued-at))[:16 bytes]>
//
// A page on the content origin cannot read another artifact's page (the
// content sandbox), so it cannot learn a protected artifact's token, and a
// token for one slug does not verify for another. The issue time lets the
// checker refuse tokens minted before the artifact's current publish, so a
// token taken from a page while it was public does not carry over to a
// republish that added a password.
package formtoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

const label = "demiplane reply-form v1"

// tagLen is the signature length kept in a token (128 bits).
const tagLen = 16

func tag(key []byte, slug string, issued int64) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(label))
	m.Write([]byte{0})
	m.Write([]byte(slug))
	m.Write([]byte{0})
	m.Write([]byte(strconv.FormatInt(issued, 10)))
	return m.Sum(nil)[:tagLen]
}

// Mint returns a token for slug issued at the given time.
func Mint(key []byte, slug string, issued time.Time) string {
	n := issued.UnixNano()
	return strconv.FormatInt(n, 10) + "." + base64.RawURLEncoding.EncodeToString(tag(key, slug, n))
}

// Check reports whether tok is a token for slug, signed with key and issued
// no earlier than notBefore (a zero notBefore accepts any issue time). The
// signature is compared in constant time.
func Check(key []byte, slug, tok string, notBefore time.Time) bool {
	if len(key) == 0 || len(tok) > 64 {
		return false
	}
	ts, sig, ok := strings.Cut(tok, ".")
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != ts {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(got) != tagLen {
		return false
	}
	if !hmac.Equal(got, tag(key, slug, n)) {
		return false
	}
	return notBefore.IsZero() || n >= notBefore.UnixNano()
}
