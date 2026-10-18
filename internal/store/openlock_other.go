// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix

package store

// openLockName is the open lock's file (openlock_unix.go); unused here but
// for the permission pass.
const openLockName = ".open.lock"

// lockOpen is a no-op outside Unix, which the releases do not target; there
// concurrent first opens of a store that needs migrating are not serialized.
func lockOpen(string) (func(), error) { return func() {}, nil }
