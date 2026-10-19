// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/limit"
)

func TestParseRateFlag(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want limit.Rate
	}{
		{"20", limit.Rate{Every: 3 * time.Second, Burst: 20}},
		{"20:10", limit.Rate{Every: 3 * time.Second, Burst: 10}},
		{" 120:30 ", limit.Rate{Every: 500 * time.Millisecond, Burst: 30}},
		{"0", limit.Rate{Every: -1}},
	} {
		got, err := parseRateFlag("x", tc.in)
		if err != nil || got != tc.want {
			t.Errorf("parseRateFlag(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "-1", "ten", "10:0", "10:-2", "10:x", "0:5", "1.5"} {
		if _, err := parseRateFlag("x", bad); err == nil {
			t.Errorf("parseRateFlag(%q) accepted", bad)
		}
	}
}

// TestServeRejectsBadLimitFlags: invalid password and live-reload limit
// flags stop serve at startup, before the store is opened.
func TestServeRejectsBadLimitFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--password-concurrency", "-1"},
		{"--password-rate-client", "-5"},
		{"--password-rate-target", "10:0"},
		{"--password-rate-global", "lots"},
		{"--password-rate-client-target", "0:3"},
		{"--live-max-per-slug", "0"},
		{"--live-max-per-client", "-1"},
		{"--live-max-per-client-slug", "0"},
		{"--live-max-total", "0"},
		{"--live-max-pending", "0"},
		{"--live-max-pending-per-client", "-1"},
	} {
		dir := t.TempDir() + "/store"
		err := runServe(append([]string{"--store", dir}, args...))
		if err == nil {
			t.Errorf("serve %v started", args)
			continue
		}
		if _, statErr := os.Stat(dir); statErr == nil {
			t.Errorf("serve %v opened the store before refusing: %v", args, err)
		}
	}
}
