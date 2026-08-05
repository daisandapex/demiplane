// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"testing"

	"github.com/daisandapex/demiplane/internal/store"
)

// TestMain lowers the password work factor for this package.
//
// These tests exercise the full publish path, so they hash at production cost
// through store.Open even though none of them is testing the KDF. At 600,000
// iterations that is roughly a third of a second per hash, and it made this the
// slowest package in the suite by a wide margin.
//
// The shipped default is unchanged and guarded by
// store.TestProductionIterationCount.
func TestMain(m *testing.M) {
	restore := store.SetPBKDF2IterForTest(1)
	defer restore()
	m.Run()
}
