// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/limit"
)

// parseRateFlag reads a rate flag: "N" (N a minute, burst N) or "N:B" (burst
// B). "0" turns the limit off.
func parseRateFlag(name, v string) (limit.Rate, error) {
	rate, burst, hasBurst := strings.Cut(strings.TrimSpace(v), ":")
	n, err := strconv.Atoi(rate)
	if err != nil || n < 0 {
		return limit.Rate{}, fmt.Errorf("--%s %q: want N or N:BURST failures a minute (0 = off)", name, v)
	}
	if n == 0 {
		if hasBurst {
			return limit.Rate{}, fmt.Errorf("--%s %q: a burst needs a rate above 0", name, v)
		}
		return limit.Rate{Every: -1}, nil
	}
	b := n
	if hasBurst {
		if b, err = strconv.Atoi(burst); err != nil || b < 1 {
			return limit.Rate{}, fmt.Errorf("--%s %q: the burst must be a positive integer", name, v)
		}
	}
	return limit.Rate{Every: time.Minute / time.Duration(n), Burst: b}, nil
}
