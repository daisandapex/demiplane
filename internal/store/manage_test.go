// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func ownerArt(t *testing.T, s *Store, slug string) Artifact {
	t.Helper()
	rows, err := s.OwnerArtifacts(context.Background(), DefaultOwner, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rows {
		if a.Slug == slug {
			return a.Artifact
		}
	}
	t.Fatalf("missing owner artifact %s", slug)
	return Artifact{}
}
func pinArt(t *testing.T, s *Store, slug string) Artifact {
	t.Helper()
	p := true
	a, err := s.Patch(slug, nil, &p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestManageMigration(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, PutOptions{Slug: "old"}, "3.0 bytes")
	// Remove exactly the additive 3.1 schema to reproduce the 3.0 schema.
	if _, err = s.db.Exec(`ALTER TABLE artifacts DROP COLUMN pinned; DROP TABLE management_policy`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for n := 0; n < 2; n++ {
		s, err = Open(root)
		if err != nil {
			t.Fatal(err)
		}
		a := ownerArt(t, s, "old")
		if a.Pinned || a.Revision == "" {
			t.Fatalf("migration: %+v", a)
		}
		_, b := readBack(t, s, "old")
		if string(b) != "3.0 bytes" {
			t.Fatal("content changed")
		}
		s.Close()
	}
}
func TestManageDowngrade(t *testing.T) {
	s := newTestStore(t)
	mustPut(t, s, PutOptions{Slug: "old", TTL: time.Hour}, "old")
	pinArt(t, s, "old")
	// These are the 3.0 read and sweep predicates, without the new column.
	var expiry int64
	if err := s.db.QueryRow(`SELECT expires_at FROM artifacts WHERE slug='old'`).Scan(&expiry); err != nil || expiry != 0 {
		t.Fatalf("old reader expiry=%d err=%v", expiry, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM artifacts WHERE expires_at > 0 AND expires_at <= ?`, time.Now().Add(100*365*24*time.Hour).Unix()).Scan(&count); err != nil || count != 0 {
		t.Fatalf("old sweeper sees pin: %d %v", count, err)
	}
	// A 3.0-shaped writer supplies writer_tag but knows nothing about pinned.
	// It remains compatible; older writers cannot promise pin enforcement.
	if _, err := s.db.Exec(`UPDATE artifacts SET expires_at=?, writer_tag=lower(hex(randomblob(8))) WHERE slug='old'`, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
}
func TestManagePinnedPublish(t *testing.T) {
	s := newTestStore(t)
	mustPut(t, s, PutOptions{Slug: "p", Source: []byte("source")}, "one")
	pinArt(t, s, "p")
	if _, err := s.Put(PutOptions{Slug: "p"}, strings.NewReader("two")); err != nil {
		t.Fatal(err)
	}
	a := ownerArt(t, s, "p")
	if !a.Pinned || !a.ExpiresAt.IsZero() {
		t.Fatalf("lost pin: %+v", a)
	}
	for _, o := range []PutOptions{{Slug: "p", TTL: time.Hour}, {Slug: "p", TTLSet: true}} {
		if _, err := s.Put(o, strings.NewReader("bad")); !errors.Is(err, ErrPinned) {
			t.Fatalf("explicit ttl accepted: %v", err)
		}
		_, b := readBack(t, s, "p")
		if string(b) != "two" {
			t.Fatal("refusal changed content")
		}
	}
}
func TestManageDefaultTTL(t *testing.T) {
	s := newTestStore(t)
	if a := mustPut(t, s, PutOptions{Slug: "off"}, "x"); !a.ExpiresAt.IsZero() {
		t.Fatal("default not off")
	}
	if err := s.SetDefaultTTL(2 * time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		opts PutOptions
		want time.Duration
	}{{"absent", PutOptions{}, 2 * time.Hour}, {"never", PutOptions{TTLSet: true}, 0}, {"explicit", PutOptions{TTL: time.Hour}, time.Hour}} {
		tc.opts.Slug = tc.name
		a := mustPut(t, s, tc.opts, "x")
		if tc.want == 0 {
			if !a.ExpiresAt.IsZero() {
				t.Fatal("explicit never")
			}
		} else if d := time.Until(a.ExpiresAt); d < tc.want-time.Second || d > tc.want {
			t.Fatalf("%s expiry %s", tc.name, d)
		}
	}
	pinArt(t, s, "never")
	mustPut(t, s, PutOptions{Slug: "never"}, "x")
	if a := ownerArt(t, s, "never"); !a.Pinned || !a.ExpiresAt.IsZero() {
		t.Fatal("default applied to pin")
	}
	other, err := OpenShared(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if a := mustPut(t, other, PutOptions{Slug: "shared"}, "x"); a.ExpiresAt.IsZero() {
		t.Fatal("policy not shared")
	}
	if err := s.SetDefaultTTL(0); err != nil {
		t.Fatal(err)
	}
	if a := mustPut(t, other, PutOptions{Slug: "cleared"}, "x"); !a.ExpiresAt.IsZero() {
		t.Fatal("policy not cleared")
	}
	for _, d := range []time.Duration{-1, time.Second, 366 * 24 * time.Hour} {
		if s.SetDefaultTTL(d) == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}
func TestManageAtomic(t *testing.T) {
	s := newTestStore(t)
	other, err := OpenShared(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	mustPut(t, s, PutOptions{Slug: "race"}, "old")
	beforeMetaCommit = func(string) { pinArt(t, other, "race") }
	defer func() { beforeMetaCommit = nil }()
	if _, err := s.Put(PutOptions{Slug: "race", TTL: time.Hour}, strings.NewReader("new")); !errors.Is(err, ErrPinned) {
		t.Fatalf("pin during publish: %v", err)
	}
	beforeMetaCommit = nil
	_, b := readBack(t, s, "race")
	if string(b) != "old" {
		t.Fatal("race replaced bytes")
	}
	// Sweep and PATCH share the transaction boundary: a successful pin must
	// survive reaping even when the sweeper's cutoff is far in the future.
	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Go(func() {
			_, e := s.Patch("race", nil, nil)
			if e != nil {
				t.Error(e)
			}
		})
		wg.Go(func() {
			_, e := other.SweepExpired(time.Now().Add(24 * time.Hour))
			if e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if !ownerArt(t, s, "race").Pinned {
		t.Fatal("sweeper removed pin")
	}
}
func TestManageBulkAtomic(t *testing.T) {
	s := newTestStore(t)
	mustPut(t, s, PutOptions{Slug: "race"}, "one")
	old := ownerArt(t, s, "race")
	other, err := OpenShared(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	mustPut(t, other, PutOptions{Slug: "race"}, "two")
	if err = s.DeleteRevision("race", old.Revision, false); !errors.Is(err, ErrChanged) {
		t.Fatalf("deleted replacement: %v", err)
	}
	cur := ownerArt(t, s, "race")
	p := pinArt(t, other, "race")
	if err = s.DeleteRevision("race", cur.Revision, true); !errors.Is(err, ErrChanged) {
		t.Fatalf("stale manifest deleted pin: %v", err)
	}
	if err = s.DeleteRevision("race", p.Revision, false); !errors.Is(err, ErrChanged) {
		t.Fatalf("pin not checked: %v", err)
	}
	if err = s.DeleteRevision("race", p.Revision, true); err != nil {
		t.Fatal(err)
	}
	if err = publishSiteBody(s, "site", "index.html", "one"); err != nil {
		t.Fatal(err)
	}
	meta, err := s.SiteMetadata("site")
	if err != nil {
		t.Fatal(err)
	}
	if err = publishSiteBody(s, "site", "index.html", "two"); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteSiteRevision("site", meta.Revision); !errors.Is(err, ErrChanged) {
		t.Fatalf("site replacement deleted: %v", err)
	}
}
func TestManageBulkSources(t *testing.T) {
	s := newTestStore(t)
	mustPut(t, s, PutOptions{Slug: "src", Source: []byte("secret")}, "rendered")
	a := ownerArt(t, s, "src")
	if err := s.DeleteRevision("src", a.Revision, false); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(s.sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("source survived")
	}
	blobs, _ := filepath.Glob(filepath.Join(s.blobDir, "src*"))
	if len(blobs) != 0 {
		t.Fatal("body survived")
	}
}
func TestManageInventoryRevision(t *testing.T) {
	s := newTestStore(t)
	mustPut(t, s, PutOptions{Slug: "r"}, "x")
	a := ownerArt(t, s, "r")
	b := ownerArt(t, s, "r")
	if a.Revision == "" || a.Revision != b.Revision {
		t.Fatal("unstable artifact revision")
	}
	d := time.Hour
	c, err := s.Patch("r", &d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Revision == a.Revision {
		t.Fatal("patch did not revise")
	}
	mustPut(t, s, PutOptions{Slug: "r"}, "x")
	if ownerArt(t, s, "r").Revision == c.Revision {
		t.Fatal("publish did not revise")
	}
	if err = publishSiteBody(s, "site", "index.html", "1234"); err != nil {
		t.Fatal(err)
	}
	x, err := s.SiteMetadata("site")
	if err != nil {
		t.Fatal(err)
	}
	y, err := s.SiteMetadata("site")
	if err != nil {
		t.Fatal(err)
	}
	if x.Revision == "" || x.Revision != y.Revision || x.Size != 4 || x.CreatedAt.IsZero() {
		t.Fatalf("site metadata: %+v %+v", x, y)
	}
}

func TestManagePinPersistence(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, PutOptions{Slug: "pin", TTL: time.Hour}, "persistent")
	pinArt(t, s, "pin")
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := ownerArt(t, s, "pin")
	if !a.Pinned || !a.ExpiresAt.IsZero() {
		t.Fatal("pin did not survive reopen")
	}
	if n, err := s.SweepExpired(time.Now().Add(366 * 24 * time.Hour)); err != nil || n != 0 {
		t.Fatalf("reopened pin swept: %d %v", n, err)
	}
}

func TestManageGeneratedPinCollision(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "explicit"}[explicit], func(t *testing.T) {
			oneFriendlySlug(t)
			s := newTestStore(t)
			if err := s.SetDefaultTTL(time.Hour); err != nil {
				t.Fatal(err)
			}
			var taken string
			beforeMetaCommit = func(slug string) {
				beforeMetaCommit = nil
				taken = slug
				mustPut(t, s, PutOptions{Slug: slug}, "named")
				pinArt(t, s, slug)
			}
			defer func() { beforeMetaCommit = nil }()
			opts := PutOptions{}
			if explicit {
				opts.TTL = 2 * time.Hour
			}
			a, err := s.Put(opts, strings.NewReader("generated"))
			if err != nil {
				t.Fatal(err)
			}
			if a.Slug == taken || a.Pinned || a.ExpiresAt.IsZero() {
				t.Fatalf("generated publish inherited collision pin: %+v", a)
			}
			if !ownerArt(t, s, taken).Pinned || readBody(t, s, taken) != "named" {
				t.Fatal("generated publish changed pinned collision")
			}
		})
	}
}

func TestManageReadMetadata(t *testing.T) {
	s := newTestStore(t)
	published := mustPut(t, s, PutOptions{Slug: "metadata"}, "body")
	if published.Revision == "" || published.Revision != ownerArt(t, s, "metadata").Revision {
		t.Error("publish result omitted current revision")
	}
	pinned := pinArt(t, s, "metadata")
	read, body := readBack(t, s, "metadata")
	if !read.Pinned || read.Revision != pinned.Revision || string(body) != "body" {
		t.Errorf("read omitted pin/revision: %+v", read)
	}
}
