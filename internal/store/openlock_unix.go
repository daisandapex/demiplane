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
	return lockFile(filepath.Join(root, openLockName), true)
}

// LockServe takes the store's serve lock without waiting, and returns its
// release, or ErrStoreServed if a server (or a maintenance command) holds
// it. One process at a time recovers, scavenges and writes sites.
func LockServe(root string) (func(), error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	return lockFile(filepath.Join(root, serveLockName), false)
}

// lockFile takes an exclusive lock on path, creating it owner-only. Without
// wait, a held lock is ErrStoreServed. Closing the descriptor releases it.
func lockFile(path string, wait bool) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open store lock: %w", err)
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	for {
		err = syscall.Flock(int(f.Fd()), how)
		if err != syscall.EINTR {
			break
		}
	}
	if err == syscall.EWOULDBLOCK {
		f.Close()
		return nil, ErrStoreServed
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock store: %w", err)
	}
	return func() { f.Close() }, nil
}
