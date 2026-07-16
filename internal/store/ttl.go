// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseTTL parses a human time-to-live string into a positive duration.
//
// It accepts Go duration syntax (e.g. "30m", "2h", "90s", "1h30m") plus a day
// suffix Go's time.ParseDuration lacks: a bare "<n>d" means n*24h. An empty
// string means "no TTL" and returns (0, nil). Zero or negative TTLs are an
// error — a non-empty TTL must be in the future.
func ParseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if rest, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.ParseFloat(rest, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid ttl %q: %w", s, err)
		}
		if days <= 0 {
			return 0, fmt.Errorf("ttl %q must be positive", s)
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid ttl %q: %w", s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("ttl %q must be positive", s)
	}
	return d, nil
}
