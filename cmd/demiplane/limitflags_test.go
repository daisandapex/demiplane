// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/limit"
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
