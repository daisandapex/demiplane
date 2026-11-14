// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package ttl

import (
	"strings"
	"testing"
	"time"
)

// FuzzParse checks the TTL grammar shared by the server, receive and the MCP
// client: a value past MaxLen is refused, an accepted TTL is zero or at
// least Min and never wraps negative, and an accepted duration reads back
// through its own String form.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"", "   ", "\t", "30m", "2h", "90s", "1h30m", "7d", "1d", "0.5d", ".5d",
		"1.d", "  2h  ", "1s", "999ms", "1ms", "1ns", "0.00001157407407407407d",
		"0.000011574074074074075d", "106751d", "106752d", "2562047h", "2562048h",
		"0", "none", "-5m", "0d", "-1d", "+1d", "garbage", "10x", "d", ".d",
		"1e-20d", "1e3d", "NaNd", "nand", "Infd", "+Infd", "-Infd", "infinityd",
		strings.Repeat("9", 64) + "d", strings.Repeat("1", 65),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := Parse(s)
		if err != nil {
			if d != 0 {
				t.Fatalf("Parse(%q) returned %v with error %v", s, d, err)
			}
			return
		}
		if len(s) > MaxLen {
			t.Fatalf("Parse accepted %d bytes", len(s))
		}
		if d < 0 || (d != 0 && d < Min) {
			t.Fatalf("Parse(%q) = %v", s, d)
		}
		if d == 0 && s != "" && s != "0" && s != "none" {
			t.Fatalf("Parse(%q) = 0 for a value that is not a no-expiry spelling", s)
		}
		if d != 0 {
			if back, err := Parse(d.String()); err != nil || back != d {
				t.Fatalf("round trip %q -> %v -> %q -> %v (%v)", s, d, d.String(), back, err)
			}
		}
		m, err := Management(s)
		if err == nil && m != 0 && (m < time.Minute || m > 365*24*time.Hour) {
			t.Fatalf("Management(%q) = %v out of range", s, m)
		}
	})
}
