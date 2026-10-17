// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"time"

	"github.com/daisandapex/demiplane/internal/ttl"
)

// ParseTTL parses a human time-to-live string into a positive duration. See
// ttl.Parse for the accepted forms: Go duration syntax plus a decimal "<n>d"
// day suffix, at least ttl.Min. An empty string means "no TTL" and returns
// (0, nil); every other accepted input is a duration that expires.
func ParseTTL(s string) (time.Duration, error) { return ttl.Parse(s) }
