// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package formtoken

import (
	"strings"
	"testing"
)

func TestMintAndCheck(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	other := []byte(strings.Repeat("o", 32))
	tok := Mint(key, "lesson", "pub-1")

	if !Check(key, "lesson", "pub-1", tok) {
		t.Fatal("a token does not verify for its own slug and publish")
	}
	if Mint(key, "lesson", "pub-1") != tok {
		t.Fatal("minting is not deterministic for one publish")
	}
	tampered := []byte(tok)
	tampered[len(tampered)-1] ^= 1
	for name, c := range map[string]struct {
		key                []byte
		slug, publish, tok string
	}{
		"another slug":       {key, "lesson2", "pub-1", tok},
		"another publish":    {key, "lesson", "pub-2", tok},
		"another key":        {other, "lesson", "pub-1", tok},
		"tampered signature": {key, "lesson", "pub-1", string(tampered)},
		"truncated":          {key, "lesson", "pub-1", tok[:len(tok)-2]},
		"empty":              {key, "lesson", "pub-1", ""},
		"padded":             {key, "lesson", "pub-1", tok + "=="},
		"oversized":          {key, "lesson", "pub-1", tok + strings.Repeat("A", 64)},
		"no key":             {nil, "lesson", "pub-1", tok},
		"no publish id":      {key, "lesson", "", Mint(key, "lesson", "")},
	} {
		if Check(c.key, c.slug, c.publish, c.tok) {
			t.Errorf("%s: token accepted", name)
		}
	}
}

// TestCheckRefusesNonCanonicalSpelling: the same tag written with different
// unused low bits in its last character is refused; a token has one
// spelling.
func TestCheckRefusesNonCanonicalSpelling(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	tok := Mint(key, "s", "0123456789abcdef0123456789abcdef")
	if !Check(key, "s", "0123456789abcdef0123456789abcdef", tok) {
		t.Fatal("minted token refused")
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, tok[len(tok)-1])
	for d := 1; d < 16; d++ {
		alt := tok[:len(tok)-1] + string(alphabet[last^d])
		if Check(key, "s", "0123456789abcdef0123456789abcdef", alt) {
			t.Errorf("non-canonical spelling %q accepted", alt)
		}
	}
	for _, alt := range []string{tok + "\n", tok + "\r\n", tok[:5] + "\n" + tok[5:], "\r" + tok} {
		if Check(key, "s", "0123456789abcdef0123456789abcdef", alt) {
			t.Errorf("spelling with a line break %q accepted", alt)
		}
	}
}
