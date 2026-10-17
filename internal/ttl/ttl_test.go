// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package ttl

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"   ", 0, false},
		{"30m", 30 * time.Minute, false},
		{"2h", 2 * time.Hour, false},
		{"90s", 90 * time.Second, false},
		{"1h30m", 90 * time.Minute, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"0.5d", 12 * time.Hour, false},
		{".5d", 12 * time.Hour, false},
		{"1.d", 24 * time.Hour, false},
		{"  2h  ", 2 * time.Hour, false},
		// Boundaries around the one-second minimum.
		{"1s", time.Second, false},
		{"999ms", 0, true},
		{"1ms", 0, true},
		{"1ns", 0, true},
		{"0.00001157407407407407d", 0, true},             // just under 1s
		{"0.000011574074074074075d", time.Second, false}, // 1s after truncation
		// Boundaries around the largest representable duration.
		{"106751d", 106751 * 24 * time.Hour, false},
		{"106752d", 0, true},
		{"2562047h", 2562047 * time.Hour, false},
		{"2562048h", 0, true},
		// Zero, negative and malformed input.
		{"0", 0, true},
		{"-5m", 0, true},
		{"0d", 0, true},
		{"-1d", 0, true},
		{"+1d", 0, true},
		{"garbage", 0, true},
		{"10x", 0, true},
		{"d", 0, true},
		{".d", 0, true},
		// Values strconv.ParseFloat accepts that must not reach the store.
		{"1e-20d", 0, true},
		{"1e3d", 0, true},
		{"NaNd", 0, true},
		{"nand", 0, true},
		{"Infd", 0, true},
		{"+Infd", 0, true},
		{"-Infd", 0, true},
		{"infinityd", 0, true},
		{"0x1p-2d", 0, true},
		{"1_000d", 0, true},
		{"1e400d", 0, true},
		{"99999999999999999999999d", 0, true},
	}
	for _, tc := range cases {
		got, err := Parse(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Parse(%q) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestParseNeverUnexpiring is the invariant the store relies on: an accepted
// non-empty TTL is always at least Min and never wraps negative.
func TestParseNeverUnexpiring(t *testing.T) {
	inputs := []string{
		"1s", "1d", "0.1d", "0.0000116d", "106751.99d", "2562047h47m16s",
		"1e-300d", "5e-324d", "0.0000000000000000001d", "106751.991167300640d",
	}
	for _, in := range inputs {
		d, err := Parse(in)
		if err != nil {
			continue
		}
		if d < Min {
			t.Errorf("Parse(%q) = %v: accepted a TTL below Min", in, d)
		}
	}
}
