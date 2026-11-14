// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package formtoken

import "testing"

// FuzzCheck checks the reply-form token: a minted token verifies for its own
// slug and publish only, and Check accepts exactly one spelling of it.
func FuzzCheck(f *testing.F) {
	key := []byte("fuzz key of some length")
	good := Mint(key, "report", "pid-1")
	f.Add("report", "pid-1", good)
	f.Add("report", "pid-1", good+"\n")
	f.Add("report", "pid-1", good[:len(good)-1]+"B")
	f.Add("report\x00pid", "1", Mint(key, "report\x00pid", "1"))
	f.Add("report", "", good)
	f.Add("", "pid-1", "")
	f.Fuzz(func(t *testing.T, slug, publishID, tok string) {
		if publishID != "" && !Check(key, slug, publishID, Mint(key, slug, publishID)) {
			t.Fatalf("minted token for %q/%q does not verify", slug, publishID)
		}
		if Check(key, slug, publishID, tok) {
			if tok != Mint(key, slug, publishID) {
				t.Fatalf("Check accepted %q, a spelling Mint never writes", tok)
			}
			if publishID == "" {
				t.Fatal("Check accepted a token for an empty publish id")
			}
			if Check(nil, slug, publishID, tok) {
				t.Fatal("Check accepted a token without a key")
			}
			// The fields are NUL-separated, so moving bytes between slug
			// and publish id gives another token.
			if len(slug) > 0 && Check(key, slug[:len(slug)-1], string(slug[len(slug)-1])+publishID, tok) {
				t.Fatalf("token for %q/%q also verifies with the boundary moved", slug, publishID)
			}
		}
	})
}
