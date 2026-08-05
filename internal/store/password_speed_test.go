// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import "testing"

// TestMain lowers the PBKDF2 work factor for the whole package.
//
// At the production 600,000 iterations a single hash costs roughly a third of
// a second. Most cases in this package hash at least once, which made password
// derivation the largest single cost in a twelve-minute suite -- slow enough
// that the tests stop being run while working, which is the real damage.
//
// Lowering it here is safe because these tests assert the SHAPE of hashing and
// verification, not its cost. TestProductionIterationCount below is what
// guards the number that actually ships.
func TestMain(m *testing.M) {
	pbkdf2Iter = 1
	m.Run()
}

// TestProductionIterationCount is the guard that makes the speedup safe.
//
// Without it, lowering the work factor for test speed could quietly become the
// shipped value, and the only symptom would be that offline cracking got
// cheaper -- invisible until it mattered.
func TestProductionIterationCount(t *testing.T) {
	const owaspFloor = 600_000
	if DefaultPBKDF2Iter < owaspFloor {
		t.Fatalf("production PBKDF2 iterations %d are below the OWASP floor of %d",
			DefaultPBKDF2Iter, owaspFloor)
	}
}

// TestHashRecordsItsOwnIterationCount is why the speedup cannot corrupt data.
//
// Verification reads the iteration count out of the stored hash rather than
// assuming the current setting, so hashes written under any work factor keep
// verifying after it changes -- including hashes written by an older release.
func TestHashRecordsItsOwnIterationCount(t *testing.T) {
	saved := pbkdf2Iter
	defer func() { pbkdf2Iter = saved }()

	pbkdf2Iter = 2
	cheap, err := hashPassword("correct horse")
	if err != nil {
		t.Fatalf("hash at iter=2: %v", err)
	}

	// Change the work factor, as a release bumping it would.
	pbkdf2Iter = 5
	if !PasswordMatches(cheap, "correct horse") {
		t.Error("a hash written at a different iteration count must still verify")
	}
	if PasswordMatches(cheap, "wrong horse") {
		t.Error("verification accepted the wrong password")
	}
}
