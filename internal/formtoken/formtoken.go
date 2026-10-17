// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package formtoken mints and checks reply-form submission tokens.
//
// A token proves that a reply submission came from a form demiplane itself
// issued for that artifact: the inline reply box baked into a ?reply=question
// page, or the reply module's /reply/{slug} form. It is bound to the target
// slug and to the artifact's publish identifier, a random value the store
// assigns to every publish and keeps across rebakes, and signed with a key
// kept in the store (so baked pages keep working across restarts):
//
//	base64url(HMAC-SHA256(key, label|slug|publish id)[:16 bytes])
//
// A page on the content origin cannot read another artifact's page (the
// content sandbox), so it cannot learn a protected artifact's token, and a
// token for one slug does not verify for another. A republish gets a new
// publish identifier, so tokens issued for an earlier publish, including one
// taken from a page while it was public, stop working when it lands.
package formtoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

const label = "demiplane reply-form v2"

// tagLen is the signature length kept in a token (128 bits).
const tagLen = 16

func tag(key []byte, slug, publishID string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(label))
	m.Write([]byte{0})
	m.Write([]byte(slug))
	m.Write([]byte{0})
	m.Write([]byte(publishID))
	return m.Sum(nil)[:tagLen]
}

// Mint returns the token for slug's publish publishID.
func Mint(key []byte, slug, publishID string) string {
	return base64.RawURLEncoding.EncodeToString(tag(key, slug, publishID))
}

// Check reports whether tok is the token for slug's publish publishID under
// key. The signature is compared in constant time. An empty key or publish
// identifier never verifies.
func Check(key []byte, slug, publishID, tok string) bool {
	if len(key) == 0 || publishID == "" || len(tok) > 64 {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(got) != tagLen {
		return false
	}
	return hmac.Equal(got, tag(key, slug, publishID))
}
