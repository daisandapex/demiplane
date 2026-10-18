// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// openLockName is the file whose lock serializes the start of Open across
// processes (and Store values): the layout move and the schema and secret
// migrations, which inspect and then change shared state. serve and receive
// starting together on a store that still needs them would otherwise race.
const openLockName = ".open.lock"

// lockOpen takes the store's open lock, blocking until it is free, and
// returns its release.
func lockOpen(root string) (func(), error) {
	path := filepath.Join(root, openLockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open store lock: %w", err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock store: %w", err)
	}
	// Closing the descriptor releases the lock.
	return func() { f.Close() }, nil
}
