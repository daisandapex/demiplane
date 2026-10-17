// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package ttl parses the time-to-live strings every publish surface accepts
// (HTTP ?ttl=, SSH receive --ttl, the MCP publish tool and the publish CLI).
// It is a dependency-free leaf so the thin MCP and CLI clients can validate
// exactly what the server will accept without importing the store.
package ttl

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"
	"time"
)

// Min is the shortest accepted TTL. Expiry is stored at one-second resolution,
// so a shorter TTL cannot be represented as a distinct future instant.
const Min = time.Second

// dayForm is the only accepted shape for the day suffix: plain decimal digits
// with an optional fractional part. Exponents, hex floats, signs, underscores,
// NaN and Inf, all of which strconv.ParseFloat would take, are refused here.
var dayForm = regexp.MustCompile(`^([0-9]+(\.[0-9]*)?|\.[0-9]+)d$`)

// nsPerDay is 24h in nanoseconds, as an exact rational.
var nsPerDay = new(big.Rat).SetInt64(int64(24 * time.Hour))

// maxNS is the largest representable time.Duration, as an exact rational.
var maxNS = new(big.Rat).SetInt64(math.MaxInt64)

// Parse parses a human time-to-live string into a duration of at least Min.
//
// It accepts Go duration syntax ("30m", "2h", "90s", "1h30m") plus a day
// suffix Go's time.ParseDuration lacks: "<n>d" means n*24h, with n a plain
// decimal ("7d", "0.5d"). An empty string means "no TTL" and returns (0, nil).
// Every other input either yields a duration in [Min, MaxInt64 ns] or an
// error, so an accepted non-empty TTL always produces an artifact that
// expires.
func Parse(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	var d time.Duration
	if strings.HasSuffix(s, "d") {
		if !dayForm.MatchString(s) {
			return 0, fmt.Errorf("invalid ttl %q: want a decimal day count such as 7d or 0.5d", s)
		}
		// Exact decimal arithmetic: no float rounding can turn a tiny positive
		// value into zero or a huge one into a wrapped negative.
		days, ok := new(big.Rat).SetString(strings.TrimSuffix(s, "d"))
		if !ok {
			return 0, fmt.Errorf("invalid ttl %q", s)
		}
		ns := new(big.Rat).Mul(days, nsPerDay)
		if ns.Cmp(maxNS) > 0 {
			return 0, fmt.Errorf("ttl %q is too long", s)
		}
		// Truncate toward zero; the Min check below rejects anything that
		// truncates below a second.
		d = time.Duration(new(big.Int).Quo(ns.Num(), ns.Denom()).Int64())
	} else {
		var err error
		d, err = time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid ttl %q: %w", s, err)
		}
	}
	if d <= 0 {
		return 0, fmt.Errorf("ttl %q must be positive", s)
	}
	if d < Min {
		return 0, fmt.Errorf("ttl %q is shorter than the %s minimum", s, Min)
	}
	return d, nil
}
