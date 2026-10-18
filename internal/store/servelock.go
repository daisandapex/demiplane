// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import "errors"

// serveLockName is the file serve and maintenance commands hold a lock on
// (LockServe, openlock_unix.go).
const serveLockName = ".serve.lock"

// ErrStoreServed: another process holds the store's serve lock.
var ErrStoreServed = errors.New("the store is in use by a running demiplane serve (or another maintenance command)")
