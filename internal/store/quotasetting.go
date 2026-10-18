// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// The store-level lock files (openlock_unix.go): serve and maintenance
// commands hold the serve lock, `demiplane receive` the receive lock.
const (
	receiveLockName = ".receive.lock"
	serveLockName   = ".serve.lock"
)

// ErrReceiveBusy: another receive held the receive lock for the whole wait.
var ErrReceiveBusy = errors.New("another demiplane receive is still running on this store; try again")

// ErrStoreServed: another process holds the store's serve lock.
var ErrStoreServed = errors.New("the store is in use by a running demiplane serve (or another maintenance command)")

// The quota the server runs with is kept in the store, so `demiplane
// receive`, a separate process, enforces the same quota without being told.
const settingsSchema = `
CREATE TABLE IF NOT EXISTS settings (
    name  TEXT PRIMARY KEY,
    value INTEGER NOT NULL
);`

const quotaSettingName = "store_quota"

// SaveQuotaSetting records limit (0 = none) as the store's quota.
func (s *Store) SaveQuotaSetting(limit int64) error {
	if _, err := s.db.Exec(settingsSchema); err != nil {
		return fmt.Errorf("save store quota: %w", err)
	}
	if _, err := s.db.Exec(`INSERT INTO settings (name, value) VALUES (?, ?)
ON CONFLICT(name) DO UPDATE SET value = excluded.value`, quotaSettingName, limit); err != nil {
		return fmt.Errorf("save store quota: %w", err)
	}
	return nil
}

// QuotaSetting returns the quota the server last recorded (0 = none).
func (s *Store) QuotaSetting() (int64, error) {
	if _, err := s.db.Exec(settingsSchema); err != nil {
		return 0, fmt.Errorf("read store quota: %w", err)
	}
	var v int64
	err := s.db.QueryRow(`SELECT value FROM settings WHERE name = ?`, quotaSettingName).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read store quota: %w", err)
	}
	return v, nil
}
