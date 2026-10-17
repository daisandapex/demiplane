// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package formtoken

import (
	"strings"
	"testing"
	"time"
)

func TestMintAndCheck(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	other := []byte(strings.Repeat("o", 32))
	issued := time.Unix(1_800_000_000, 123_456_789)
	tok := Mint(key, "lesson", issued)

	if !Check(key, "lesson", tok, issued) {
		t.Fatal("a token does not verify for its own slug at its issue time")
	}
	if !Check(key, "lesson", tok, time.Time{}) {
		t.Fatal("a zero notBefore must accept any issue time")
	}
	tampered := []byte(tok)
	tampered[len(tampered)-1] ^= 1
	for name, c := range map[string]struct {
		key       []byte
		slug, tok string
		notBefore time.Time
	}{
		"another slug":            {key, "lesson2", tok, issued},
		"another key":             {other, "lesson", tok, issued},
		"tampered signature":      {key, "lesson", string(tampered), issued},
		"moved issue time":        {key, "lesson", strings.Replace(tok, "123456789", "123456790", 1), issued},
		"issued before notBefore": {key, "lesson", tok, issued.Add(time.Nanosecond)},
		"empty":                   {key, "lesson", "", issued},
		"no separator":            {key, "lesson", "1800000000123456789", issued},
		"leading zero":            {key, "lesson", "0" + tok, issued},
		"negative":                {key, "lesson", "-" + tok, issued},
		"oversized":               {key, "lesson", tok + strings.Repeat("A", 64), issued},
		"no key":                  {nil, "lesson", tok, issued},
	} {
		if Check(c.key, c.slug, c.tok, c.notBefore) {
			t.Errorf("%s: token accepted", name)
		}
	}
}
