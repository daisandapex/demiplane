// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"crypto/rand"
	"fmt"
)

// Store secrets are random keys that must outlive a restart and be shared by
// every process opening the store (serve and SSH receive). They live in the
// owner-only metadata database. Creation is INSERT OR IGNORE followed by a
// read, so two processes opening a new store at once agree on one key.

const secretsSchema = `
CREATE TABLE IF NOT EXISTS secrets (
    name  TEXT PRIMARY KEY,
    value BLOB NOT NULL
);`

// formKeyName names the key that signs reply-form submission tokens.
const formKeyName = "reply-form"

// loadSecret returns the named secret, creating a 32-byte random one first if
// the store has none.
func (s *Store) loadSecret(name string) ([]byte, error) {
	if _, err := s.db.Exec(secretsSchema); err != nil {
		return nil, fmt.Errorf("create secrets table: %w", err)
	}
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("generate %s key: %w", name, err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO secrets (name, value) VALUES (?, ?)`, name, fresh); err != nil {
		return nil, fmt.Errorf("store %s key: %w", name, err)
	}
	var key []byte
	if err := s.db.QueryRow(`SELECT value FROM secrets WHERE name = ?`, name).Scan(&key); err != nil {
		return nil, fmt.Errorf("read %s key: %w", name, err)
	}
	if len(key) < 32 {
		return nil, fmt.Errorf("%s key is too short", name)
	}
	return key, nil
}

// FormKey returns the store's reply-form signing key (internal/formtoken).
func (s *Store) FormKey() []byte { return s.formKey }
