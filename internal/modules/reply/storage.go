// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/daisandapex/demiplane/internal/logsafe"
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

	// mu serializes limited inserts in this process; the IMMEDIATE
	// transaction serializes them across connections.
	mu sync.Mutex

	// The retention sweeper (startSweeper): stop ends it, done closes when it
	// has returned.
	sweepMu sync.Mutex
	stop    chan struct{}
	done    chan struct{}
}

// retentionEvery is how often the retention sweeper runs.
const retentionEvery = time.Hour

var (
	// errReplyCap: the target artifact has its maximum number of replies.
	errReplyCap = errors.New("reply limit reached for this artifact")
	// errReplyStoreFull: the aggregate reply count or byte limit is reached.
	errReplyStoreFull = errors.New("reply storage is full")
)

// openReplyStore opens (creating + migrating if needed) replies.db under dir.
func openReplyStore(dir string) (*replyStore, error) {
	dbPath := filepath.Join(dir, "replies.db")
	// _txlock=immediate takes the write lock at BEGIN, so the count that
	// admits a reply and the insert happen under one lock.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate")
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
CREATE INDEX IF NOT EXISTS idx_replies_status ON replies(status);
CREATE INDEX IF NOT EXISTS idx_replies_created ON replies(created_at);`
	if _, err := rs.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate replies schema: %w", err)
	}
	// Aggregate totals for the storage limits, kept in the database by
	// triggers so they change in the same transaction as the rows they count.
	// Created once, seeded from the rows already there.
	const stats = `
CREATE TABLE IF NOT EXISTS reply_stats (
    id    INTEGER PRIMARY KEY CHECK (id = 1),
    n     INTEGER NOT NULL,
    bytes INTEGER NOT NULL
);
INSERT OR IGNORE INTO reply_stats (id, n, bytes)
    SELECT 1, COUNT(*), COALESCE(SUM(LENGTH(CAST(body AS BLOB))), 0) FROM replies;
CREATE TRIGGER IF NOT EXISTS replies_stats_insert AFTER INSERT ON replies BEGIN
    UPDATE reply_stats SET n = n + 1, bytes = bytes + LENGTH(CAST(NEW.body AS BLOB)) WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS replies_stats_delete AFTER DELETE ON replies BEGIN
    UPDATE reply_stats SET n = n - 1, bytes = bytes - LENGTH(CAST(OLD.body AS BLOB)) WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS replies_stats_update AFTER UPDATE OF body ON replies BEGIN
    UPDATE reply_stats SET bytes = bytes - LENGTH(CAST(OLD.body AS BLOB)) + LENGTH(CAST(NEW.body AS BLOB)) WHERE id = 1;
END;`
	tx, err := rs.db.Begin()
	if err != nil {
		return fmt.Errorf("migrate reply stats: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(stats); err != nil {
		return fmt.Errorf("migrate reply stats: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate reply stats: %w", err)
	}
	return nil
}

func (rs *replyStore) close() error {
	rs.stopSweeper()
	return rs.db.Close()
}

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

// beforeReplyLock, when set by a test, runs in addLimited before it waits for
// the store's mutex and write lock.
var beforeReplyLock func()

// addLimited inserts a reply if lim allows it: fewer than lim.PerArtifact
// replies for slug, and room under lim.Total and lim.TotalBytes. The checks
// and the insert run in one IMMEDIATE transaction, so concurrent submissions,
// from this process or another one sharing the database, cannot all pass a
// check that only one of them fits.
// It never deletes anything: retention is the sweeper's job (sweep).
//
// verify, when non-nil, runs inside the transaction after every wait (the
// mutex, and the write lock BEGIN IMMEDIATE takes), immediately before the
// INSERT; an error from it rolls the transaction back and is returned. Only
// the INSERT and the COMMIT follow it, and holding the write lock, neither
// waits for another writer.
func (rs *replyStore) addLimited(slug, kind, body string, now time.Time, lim Limits, verify func() error) (Reply, error) {
	if beforeReplyLock != nil {
		beforeReplyLock()
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	tx, err := rs.db.Begin()
	if err != nil {
		return Reply{}, fmt.Errorf("insert reply: %w", err)
	}
	defer tx.Rollback()
	var (
		total      int
		totalBytes int64
	)
	if err := tx.QueryRow(`SELECT n, bytes FROM reply_stats WHERE id = 1`).Scan(&total, &totalBytes); err != nil {
		return Reply{}, fmt.Errorf("read reply totals: %w", err)
	}
	if total >= lim.Total || totalBytes+int64(len(body)) > lim.TotalBytes {
		return Reply{}, errReplyStoreFull
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM replies WHERE slug = ?`, slug).Scan(&n); err != nil {
		return Reply{}, fmt.Errorf("count replies: %w", err)
	}
	if n >= lim.PerArtifact {
		return Reply{}, errReplyCap
	}
	if verify != nil {
		if err := verify(); err != nil {
			return Reply{}, err
		}
	}
	res, err := tx.Exec(
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
	if err := tx.Commit(); err != nil {
		return Reply{}, fmt.Errorf("insert reply: %w", err)
	}
	return Reply{ID: id, Slug: slug, Kind: kind, Body: body, CreatedAt: now.UTC(), Status: "pending"}, nil
}

// sweep deletes replies created before now-retention in one transaction (the
// triggers update the totals in the same transaction), and returns how many it
// deleted. retention <= 0 deletes nothing.
func (rs *replyStore) sweep(now time.Time, retention time.Duration) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	tx, err := rs.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("delete old replies: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM replies WHERE created_at < ?`, now.Add(-retention).Unix())
	if err != nil {
		return 0, fmt.Errorf("delete old replies: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete old replies: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("delete old replies: %w", err)
	}
	return n, nil
}

// startSweeper runs the retention sweep now and then every interval, with the
// retention retention() returns at each pass (0 = keep replies, skip the
// pass), until stopSweeper or close. It returns a channel closed when the
// sweeper has exited. A second call while one runs returns the running one's.
func (rs *replyStore) startSweeper(retention func() time.Duration, every time.Duration) <-chan struct{} {
	rs.sweepMu.Lock()
	defer rs.sweepMu.Unlock()
	if rs.stop != nil {
		return rs.done
	}
	rs.stop, rs.done = make(chan struct{}), make(chan struct{})
	stop, done := rs.stop, rs.done
	go func() {
		defer close(done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			if n, err := rs.sweep(time.Now(), retention()); err != nil {
				log.Printf("reply: retention pass failed: %s", logsafe.Err(err))
			} else if n > 0 {
				log.Printf("reply: retention removed %d old replies", n)
			}
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
	return done
}

// stopSweeper stops the retention sweeper, if one runs, and waits for it.
func (rs *replyStore) stopSweeper() {
	rs.sweepMu.Lock()
	stop, done := rs.stop, rs.done
	rs.stop, rs.done = nil, nil
	rs.sweepMu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
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
