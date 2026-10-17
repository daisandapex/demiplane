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
