// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"testing"
	"time"
)

func TestParseTTL(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"30m", 30 * time.Minute, false},
		{"2h", 2 * time.Hour, false},
		{"90s", 90 * time.Second, false},
		{"1h30m", 90 * time.Minute, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"0.5d", 12 * time.Hour, false},
		{"  2h  ", 2 * time.Hour, false},
		{"0", 0, true},
		{"-5m", 0, true},
		{"0d", 0, true},
		{"-1d", 0, true},
		{"garbage", 0, true},
		{"10x", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseTTL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseTTL(%q) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTTL(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseTTL(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
