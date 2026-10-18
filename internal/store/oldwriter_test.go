// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"strings"
	"testing"
	"time"
)

// The artifact writes of demiplane 2.0.0, verbatim (internal/store/store.go
// and source.go at v2.0.0). An un-upgraded binary sharing an upgraded store
// (an SSH forced-command `receive`, say) would send exactly these.
const (
	v200Upsert = `
INSERT INTO artifacts (slug, filename, content_type, size, created_at, owner, private, password_hash, expires_at, series, render_spec, blob_gen, source_gen, modified_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(slug) DO UPDATE SET
    filename      = excluded.filename,
    content_type  = excluded.content_type,
    size          = excluded.size,
    created_at    = excluded.created_at,
    private       = MAX(artifacts.private, excluded.private),
    password_hash = excluded.password_hash,
    expires_at    = excluded.expires_at,
    series        = excluded.series,
    render_spec   = excluded.render_spec,
    blob_gen      = excluded.blob_gen,
    source_gen    = excluded.source_gen,
    modified_at   = excluded.modified_at
RETURNING modified_at;`
	v200Rebake = `UPDATE artifacts SET size = ?, blob_gen = ?, modified_at = ?
		  WHERE slug = ? AND blob_gen = ? RETURNING modified_at`
)

// TestOldWriterCannotWriteAnUpgradedStore: a 2.0.0 writer's republish would
// leave replies on and the publish id (and so every reply token) unchanged,
// and its insert would leave both unset; the store refuses all of its writes.
// Current writes, a publish and a rebake, still go through.
func TestOldWriterCannotWriteAnUpgradedStore(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Put(PutOptions{Slug: "asks", Replies: true, Source: []byte("# q")}, strings.NewReader("<p>q</p>")); err != nil {
		t.Fatal(err)
	}
	row := func() (replies bool, publishID, blobGen string) {
		t.Helper()
		if err := s.db.QueryRow(`SELECT replies, publish_id, blob_gen FROM artifacts WHERE slug = 'asks'`).Scan(&replies, &publishID, &blobGen); err != nil {
			t.Fatal(err)
		}
		return
	}
	_, pid, gen := row()
	now := time.Now().Unix()

	var m int64
	if err := s.db.QueryRow(v200Rebake, 1, "g201", now, "asks", gen).Scan(&m); err == nil {
		t.Error("2.0.0 rebake accepted")
	}
	upsert := func(slug string) error {
		return s.db.QueryRow(v200Upsert, slug, "page.html", "text/html", 1, now, DefaultOwner, 0, "", 0, "", "", "g200", "", now).Scan(&m)
	}
	if err := upsert("asks"); err == nil {
		replies, pid2, _ := row()
		t.Errorf("2.0.0 republish accepted: replies still %v, publish id unchanged %v", replies, pid2 == pid)
	}
	if err := upsert("fresh"); err == nil {
		t.Error("2.0.0 insert accepted")
	}
	// Undo any accepted 2.0.0 write, so the current writers below start from
	// the row the store wrote (they fail on a red run otherwise).
	if _, err := s.db.Exec(`UPDATE artifacts SET blob_gen = ? WHERE slug = 'asks'`, gen); err != nil {
		t.Logf("restoring the row: %v", err)
	}

	// Current writers are unaffected.
	if _, err := s.Put(PutOptions{Slug: "asks", Source: []byte("# q2")}, strings.NewReader("<p>q2</p>")); err != nil {
		t.Fatalf("current republish: %v", err)
	}
	if _, err := s.Rebake("asks", func(SourceRecord) ([]byte, error) { return []byte("<p>rebaked</p>"), nil }); err != nil {
		t.Fatalf("current rebake: %v", err)
	}
}
