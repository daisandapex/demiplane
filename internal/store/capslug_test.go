// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import "testing"

// TestCapabilitySlugsAreValidNamedSlugs: a generated capability slug must be
// addressable everywhere a slug is validated (a named republish, ?next=, the
// SSH --slug flag). base64url can start with '-' or '_', which the named-slug
// rule rejects, so about one in 32 generated slugs used to be unaddressable
// that way. 2000 draws make a miss of the old behavior vanishingly unlikely.
func TestCapabilitySlugsAreValidNamedSlugs(t *testing.T) {
	for i := 0; i < 2000; i++ {
		s, err := GenerateCapabilitySlug()
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateNamedSlug(s); err != nil {
			t.Fatalf("draw %d: capability slug %q fails named-slug validation: %v", i, s, err)
		}
		if len(s) != 24 {
			t.Fatalf("draw %d: capability slug %q has length %d, want 24", i, s, len(s))
		}
	}
}
