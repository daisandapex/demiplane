// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"strings"
	"testing"
)

// TestPublishIDLifecycle: every publish gets a fresh identifier (or the one
// the caller chose), a rebake keeps it, and lookups and Source report it.
func TestPublishIDLifecycle(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	a := mustPut(t, s, PutOptions{Slug: "page", Source: []byte("# v1"), RenderSpec: "{}"}, "<p>v1</p>")
	if !validPublishID(a.PublishID) {
		t.Fatalf("Put returned publish id %q", a.PublishID)
	}
	got, err := s.LookupPublic("page")
	if err != nil || got.PublishID != a.PublishID {
		t.Fatalf("lookup publish id %q (%v), want %q", got.PublishID, err, a.PublishID)
	}

	var rebakeSaw string
	if _, err := s.Rebake("page", func(rec SourceRecord) ([]byte, error) {
		rebakeSaw = rec.PublishID
		return []byte("<p>v1 rebaked</p>"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if rebakeSaw != a.PublishID {
		t.Errorf("Source gave the rebake publish id %q, want %q", rebakeSaw, a.PublishID)
	}
	if got, _ := s.LookupPublic("page"); got.PublishID != a.PublishID {
		t.Errorf("a rebake changed the publish id: %q -> %q", a.PublishID, got.PublishID)
	}

	b := mustPut(t, s, PutOptions{Slug: "page", Password: "pw"}, "<p>v2</p>")
	if b.PublishID == a.PublishID || !validPublishID(b.PublishID) {
		t.Errorf("a republish kept or mangled the publish id: %q -> %q", a.PublishID, b.PublishID)
	}

	chosen, err := NewPublishID()
	if err != nil {
		t.Fatal(err)
	}
	if c := mustPut(t, s, PutOptions{Slug: "page", PublishID: chosen}, "<p>v3</p>"); c.PublishID != chosen {
		t.Errorf("Put ignored the chosen publish id: %q, want %q", c.PublishID, chosen)
	}
	if got, _ := s.LookupPublic("page"); got.PublishID != chosen {
		t.Errorf("stored publish id %q, want %q", got.PublishID, chosen)
	}

	var ie *InputError
	for _, bad := range []string{"short", strings.Repeat("G", 32), strings.ToUpper(chosen)} {
		if _, err := s.Put(PutOptions{Slug: "page", PublishID: bad}, strings.NewReader("x")); !errors.As(err, &ie) {
			t.Errorf("Put with publish id %q: %v, want an input error", bad, err)
		}
	}
}

// TestMigrationBackfillsPublishIDs: rows written before publish identifiers
// existed each get a distinct one when the store is opened.
func TestMigrationBackfillsPublishIDs(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, PutOptions{Slug: "old-a"}, "a")
	mustPut(t, s, PutOptions{Slug: "old-b"}, "b")
	if _, err := s.db.Exec(`ALTER TABLE artifacts DROP COLUMN publish_id`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen/migrate: %v", err)
	}
	t.Cleanup(func() { s2.Close() })
	a, errA := s2.LookupPublic("old-a")
	b, errB := s2.LookupPublic("old-b")
	if errA != nil || errB != nil {
		t.Fatalf("rows lost: %v %v", errA, errB)
	}
	if !validPublishID(a.PublishID) || !validPublishID(b.PublishID) || a.PublishID == b.PublishID {
		t.Errorf("backfilled publish ids %q and %q", a.PublishID, b.PublishID)
	}
}
