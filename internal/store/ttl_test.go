// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"strings"
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
		{" ", 0, true},
		// Inputs that once parsed as a day count but converted to zero, a
		// wrapped value, or garbage: each must be rejected, never stored as
		// "no expiry".
		{"1e-20d", 0, true},
		{"NaNd", 0, true},
		{"+Infd", 0, true},
		{"-Infd", 0, true},
		{"1e300d", 0, true},
		{"106752d", 0, true},
		{"1ms", 0, true},
		{"0x1p-2d", 0, true},
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

// TestPutRejectsNegativeTTL: zero is the only "never expires" value, so a
// negative TTL reaching Put is refused rather than stored without an expiry.
func TestPutRejectsNegativeTTL(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put(PutOptions{Slug: "neg", TTL: -time.Second}, strings.NewReader("x"))
	var ie *InputError
	if !errors.As(err, &ie) {
		t.Fatalf("Put with negative TTL: err = %v, want *InputError", err)
	}
	if _, _, err := s.Get("neg"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rejected Put left a row behind: Get err = %v", err)
	}
}
