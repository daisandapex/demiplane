// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Per-artifact view passwords are stored as salted PBKDF2-HMAC-SHA256 hashes
// (stdlib crypto/pbkdf2 — no external dependency). PBKDF2 is a standards-blessed
// KDF; this gate is defense-in-depth on top of network/capability protection,
// not the primary control (a plain-HTTP password is only meaningful behind TLS —
// the README states this), so PBKDF2 is an appropriate fit.
const (
	pbkdf2Iter    = 600_000 // OWASP-recommended floor for PBKDF2-HMAC-SHA256
	pbkdf2SaltLen = 16
	pbkdf2KeyLen  = 32
	pbkdf2Scheme  = "pbkdf2-sha256"
)

// hashPassword returns an encoded hash of pw: "pbkdf2-sha256$<iter>$<salt>$<key>"
// with salt and key base64 (raw, std alphabet).
func hashPassword(pw string) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iter, pbkdf2KeyLen)
	if err != nil {
		return "", fmt.Errorf("derive key: %w", err)
	}
	enc := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("%s$%d$%s$%s", pbkdf2Scheme, pbkdf2Iter, enc(salt), enc(key)), nil
}

// PasswordMatches reports whether pw matches the encoded hash. A malformed or
// empty hash never matches. Comparison is constant-time.
func PasswordMatches(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != pbkdf2Scheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}
