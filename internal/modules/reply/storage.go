// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo), same as core's store
)

// Reply is one stored response to an artifact. Body is free-text; Kind is one of
// the validated kinds (approve|defer|comment); Status is pending|read.
type Reply struct {
	ID        int64     `json:"id"`
	Slug      string    `json:"slug"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"`
}

// replyStore is the module's own SQLite store, isolated from core's meta.db
// under the module data dir. The module owns it end to end; core knows nothing
// about replies (ADR 0002).
type replyStore struct {
	db *sql.DB
}

// openReplyStore opens (creating + migrating if needed) replies.db under dir.
func openReplyStore(dir string) (*replyStore, error) {
	dbPath := filepath.Join(dir, "replies.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open replies db: %w", err)
	}
	rs := &replyStore{db: db}
	if err := rs.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return rs, nil
}

func (rs *replyStore) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS replies (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    slug       TEXT NOT NULL,
    kind       TEXT NOT NULL,
    body       TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending'
);
CREATE INDEX IF NOT EXISTS idx_replies_slug   ON replies(slug);
CREATE INDEX IF NOT EXISTS idx_replies_status ON replies(status);`
	if _, err := rs.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate replies schema: %w", err)
	}
	return nil
}

func (rs *replyStore) close() error { return rs.db.Close() }

// add inserts a reply and returns it with its assigned id.
func (rs *replyStore) add(slug, kind, body string, now time.Time) (Reply, error) {
	res, err := rs.db.Exec(
		`INSERT INTO replies (slug, kind, body, created_at, status) VALUES (?, ?, ?, ?, 'pending')`,
		slug, kind, body, now.Unix(),
	)
	if err != nil {
		return Reply{}, fmt.Errorf("insert reply: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Reply{}, fmt.Errorf("reply id: %w", err)
	}
	return Reply{ID: id, Slug: slug, Kind: kind, Body: body, CreatedAt: now.UTC(), Status: "pending"}, nil
}

// count returns how many replies exist for slug (for the per-artifact cap).
func (rs *replyStore) count(slug string) (int, error) {
	var n int
	if err := rs.db.QueryRow(`SELECT COUNT(*) FROM replies WHERE slug = ?`, slug).Scan(&n); err != nil {
		return 0, fmt.Errorf("count replies: %w", err)
	}
	return n, nil
}

// list returns replies, newest first, optionally filtered by slug and by status.
// status "" or "all" returns every status; otherwise an exact match.
func (rs *replyStore) list(slug, status string) ([]Reply, error) {
	q := `SELECT id, slug, kind, body, created_at, status FROM replies`
	var (
		conds []string
		args  []any
	)
	if slug != "" {
		conds = append(conds, "slug = ?")
		args = append(args, slug)
	}
	if status != "" && status != "all" {
		conds = append(conds, "status = ?")
		args = append(args, status)
	}
	for i, c := range conds {
		if i == 0 {
			q += " WHERE "
		} else {
			q += " AND "
		}
		q += c
	}
	q += " ORDER BY created_at DESC, id DESC"

	rows, err := rs.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list replies: %w", err)
	}
	defer rows.Close()

	var out []Reply
	for rows.Next() {
		var (
			r       Reply
			created int64
		)
		if err := rows.Scan(&r.ID, &r.Slug, &r.Kind, &r.Body, &created, &r.Status); err != nil {
			return nil, fmt.Errorf("scan reply: %w", err)
		}
		r.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate replies: %w", err)
	}
	return out, nil
}

// errNoReply is returned by ack when the id is unknown.
var errNoReply = errors.New("reply not found")

// ack marks a reply read. Returns errNoReply if the id does not exist.
func (rs *replyStore) ack(id int64) error {
	res, err := rs.db.Exec(`UPDATE replies SET status = 'read' WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("ack reply: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("ack reply: %w", err)
	}
	if n == 0 {
		return errNoReply
	}
	return nil
}
