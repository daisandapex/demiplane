// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"time"

	"github.com/daisandapex/demiplane/v3/internal/ttl"
)

// ParseTTL parses a human time-to-live string into a positive duration. See
// ttl.Parse for the accepted forms: Go duration syntax plus a decimal "<n>d"
// day suffix, at least ttl.Min. Empty, "0", and "none" mean no TTL and return
// (0, nil); every other accepted input is a duration that expires.
func ParseTTL(s string) (time.Duration, error) { return ttl.Parse(s) }
