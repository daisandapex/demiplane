// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"strings"
	"testing"
	"time"
)

func TestRepliesFlagFollowsThePublish(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	put := func(on bool) {
		if _, err := s.Put(PutOptions{Slug: "page", Replies: on}, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	for _, on := range []bool{true, false, true} {
		put(on)
		a, err := s.LookupPublic("page")
		if err != nil {
			t.Fatal(err)
		}
		if a.Replies != on {
			t.Fatalf("after a publish with Replies=%v, the row says %v", on, a.Replies)
		}
	}
}

func TestLookupPublicHidesPrivateAndExpired(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	priv, err := s.Put(PutOptions{Private: true, Replies: true}, strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(PutOptions{Slug: "brief", TTL: time.Second}, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupPublic(priv.Slug); err != ErrNotFound {
		t.Fatalf("private artifact: %v, want ErrNotFound", err)
	}
	if _, err := s.LookupPublic("missing"); err != ErrNotFound {
		t.Fatalf("missing artifact: %v, want ErrNotFound", err)
	}
	if _, err := s.db.Exec(`UPDATE artifacts SET expires_at = ? WHERE slug = 'brief'`, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupPublic("brief"); err != ErrNotFound {
		t.Fatalf("expired artifact: %v, want ErrNotFound", err)
	}
}

// TestRepliesColumnBackfillsReplyBoxPages upgrades a store from before the
// replies column: a page whose retained render spec names a reply slug keeps
// accepting replies; every other row starts with replies off.
func TestRepliesColumnBackfillsReplyBoxPages(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for slug, spec := range map[string]string{
		"lesson": `{"named_slug":"lesson","reply_slug":"lesson","published":"2026-01-01T00:00:00Z"}`,
		"report": `{"named_slug":"report","published":"2026-01-01T00:00:00Z"}`,
		"raw":    "",
	} {
		opts := PutOptions{Slug: slug, RenderSpec: spec}
		if spec != "" {
			opts.Source = []byte("# x")
		}
		if _, err := s.Put(opts, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`ALTER TABLE artifacts DROP COLUMN replies`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for slug, want := range map[string]bool{"lesson": true, "report": false, "raw": false} {
		a, err := s.LookupPublic(slug)
		if err != nil {
			t.Fatal(err)
		}
		if a.Replies != want {
			t.Errorf("%s: replies = %v after upgrade, want %v", slug, a.Replies, want)
		}
	}
	// The backfill runs once: a later reopen leaves a since-changed row alone.
	if _, err := s.Put(PutOptions{Slug: "lesson", RenderSpec: `{"reply_slug":"lesson"}`, Source: []byte("# x")}, strings.NewReader("y")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := s.LookupPublic("lesson"); a.Replies {
		t.Error("the backfill ran again on a store that already had the column")
	}
}
