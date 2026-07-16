// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
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
