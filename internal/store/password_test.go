// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
)

func TestHashPasswordAndMatch(t *testing.T) {
	hash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, pbkdf2Scheme+"$") {
		t.Errorf("hash %q missing scheme prefix", hash)
	}
	if strings.Contains(hash, "correct horse") {
		t.Error("plaintext leaked into hash")
	}
	if !PasswordMatches(hash, "correct horse battery staple") {
		t.Error("correct password did not match")
	}
	if PasswordMatches(hash, "wrong") {
		t.Error("wrong password matched")
	}
}

func TestHashPasswordUsesRandomSalt(t *testing.T) {
	h1, _ := hashPassword("same")
	h2, _ := hashPassword("same")
	if h1 == h2 {
		t.Error("two hashes of the same password are identical — salt not random")
	}
	if !PasswordMatches(h1, "same") || !PasswordMatches(h2, "same") {
		t.Error("salted hashes failed to verify")
	}
}

func TestPasswordMatchesRejectsMalformed(t *testing.T) {
	for _, bad := range []string{
		"", "notahash", "pbkdf2-sha256$", "pbkdf2-sha256$abc$x$y",
		"bcrypt$1$salt$key", "pbkdf2-sha256$1000$!!!$!!!",
	} {
		if PasswordMatches(bad, "anything") {
			t.Errorf("malformed hash %q matched", bad)
		}
	}
}

// TestPasswordMatchesRequiresStoredLengths: a stored hash whose salt or key is
// not the length hashPassword writes is malformed and never verifies, even when
// its key is the correct PBKDF2 output for that salt and password. Such a record
// can only come from a damaged or foreign database, since Put refuses it. The
// "derived" rows are built so the bytes really match: a verifier that derived a
// key of the stored length without checking it would accept them.
func TestPasswordMatchesRequiresStoredLengths(t *testing.T) {
	const pw = "77"
	enc := base64.RawStdEncoding.EncodeToString
	derived := func(iter, saltLen, keyLen int) string {
		salt := make([]byte, saltLen)
		for i := range salt {
			salt[i] = byte(i + 1)
		}
		key, err := pbkdf2.Key(sha256.New, pw, salt, iter, keyLen)
		if err != nil {
			t.Fatal(err)
		}
		return pbkdf2Scheme + "$" + strconv.Itoa(iter) + "$" + enc(salt) + "$" + enc(key)
	}
	salt16 := enc(make([]byte, pbkdf2SaltLen))
	key32 := enc(make([]byte, pbkdf2KeyLen))
	for _, tc := range []struct {
		name    string
		encoded string
		want    bool
	}{
		{"well formed, current count", derived(pbkdf2Iter, pbkdf2SaltLen, pbkdf2KeyLen), true},
		{"well formed, older count", derived(1, pbkdf2SaltLen, pbkdf2KeyLen), true},
		{"fuzz crasher", "pbkdf2-sha256$1$$00", false},
		{"empty salt", derived(1, 0, pbkdf2KeyLen), false},
		{"short salt", derived(1, pbkdf2SaltLen-1, pbkdf2KeyLen), false},
		{"long salt", derived(1, pbkdf2SaltLen+1, pbkdf2KeyLen), false},
		{"empty key", "pbkdf2-sha256$1$" + salt16 + "$", false},
		{"one byte key", derived(1, pbkdf2SaltLen, 1), false},
		{"short key", derived(1, pbkdf2SaltLen, pbkdf2KeyLen-1), false},
		{"long key", derived(1, pbkdf2SaltLen, pbkdf2KeyLen+1), false},
		{"zero count", "pbkdf2-sha256$0$" + salt16 + "$" + key32, false},
		{"negative count", "pbkdf2-sha256$-1$" + salt16 + "$" + key32, false},
		{"count not a number", "pbkdf2-sha256$x$" + salt16 + "$" + key32, false},
		{"salt not base64", "pbkdf2-sha256$1$!!!!!!!!!!!!!!!!!!!!!!$" + key32, false},
		{"key not base64", "pbkdf2-sha256$1$" + salt16 + "$!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!", false},
		{"salt padded base64", "pbkdf2-sha256$1$" + base64.StdEncoding.EncodeToString(make([]byte, pbkdf2SaltLen)) + "$" + key32, false},
		{"hex salt and key", "pbkdf2-sha256$1$00000000000000000000000000000000$" + strings.Repeat("00", pbkdf2KeyLen), false},
		{"extra field", derived(1, pbkdf2SaltLen, pbkdf2KeyLen) + "$", false},
		{"missing field", "pbkdf2-sha256$1$" + salt16, false},
		{"other scheme", "pbkdf2-sha1" + strings.TrimPrefix(derived(1, pbkdf2SaltLen, pbkdf2KeyLen), pbkdf2Scheme), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PasswordMatches(tc.encoded, pw); got != tc.want {
				t.Errorf("PasswordMatches(%q, %q) = %v, want %v", tc.encoded, pw, got, tc.want)
			}
			if PasswordMatches(tc.encoded, pw+"x") {
				t.Errorf("PasswordMatches(%q) accepted a wrong password", tc.encoded)
			}
		})
	}
}
