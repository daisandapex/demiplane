// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"crypto/rand"
	"testing"
)

func TestValidBearer(t *testing.T) {
	const want = "correct-horse-battery-staple"
	key := make([]byte, 32)
	rand.Read(key)
	cases := []struct {
		header string
		ok     bool
	}{
		{"Bearer correct-horse-battery-staple", true},
		{"bearer correct-horse-battery-staple", true}, // scheme is case-insensitive
		{"BEARER correct-horse-battery-staple", true},
		{"Bearer  correct-horse-battery-staple", true}, // extra space trimmed
		{"Bearer wrong", false},
		{"Bearer ", false},
		{"Bearer", false},
		{"", false},
		{"Basic correct-horse-battery-staple", false},
		{"correct-horse-battery-staple", false}, // missing scheme
		{"Bearer correct-horse-battery-staple-extra", false},
		{"Bearer short", false}, // different length must still fail (length-hidden)
	}
	for _, tc := range cases {
		if got := validBearer(tc.header, want, key); got != tc.ok {
			t.Errorf("validBearer(%q) = %v, want %v", tc.header, got, tc.ok)
		}
	}
}
