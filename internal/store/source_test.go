// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const markdownSource = "# Notes\n\nA paragraph the renderer bakes away.\n"

// TestPutRetainsSource is the regression test for the data-loss defect: the
// document a rendered artifact was baked from must survive the publish that
// baked it. Before source retention only the HTML was kept, and five pages were
// permanently lost in the 2026-08-30 render overhaul because of it.
func TestPutRetainsSource(t *testing.T) {
	s := newTestStore(t)
	baked := []byte("<h1>Notes</h1>")

	art, err := s.Put(PutOptions{
		Slug:       "notes",
		Filename:   "notes.html",
		Source:     []byte(markdownSource),
		RenderSpec: `{"named_slug":"notes"}`,
	}, bytes.NewReader(baked))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if art.RenderSpec != `{"named_slug":"notes"}` {
		t.Errorf("artifact render spec = %q", art.RenderSpec)
	}

	rec, err := s.Source("notes")
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if string(rec.Body) != markdownSource {
		t.Errorf("retained source =\n %q\nwant\n %q", rec.Body, markdownSource)
	}
	if rec.Spec != `{"named_slug":"notes"}` {
		t.Errorf("retained spec = %q", rec.Spec)
	}
	if rec.Slug != "notes" {
		t.Errorf("record slug = %q, want notes", rec.Slug)
	}
	if rec.CreatedAt.IsZero() {
		t.Error("record created-at is zero; the colophon has no publish time to replay")
	}

	// The retained source must not be readable by other local users: it is the
	// plaintext of a page that may be password-gated.
	info, err := os.Stat(filepath.Join(s.root, sourceDirName))
	if err != nil {
		t.Fatalf("stat source dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("source dir mode = %o, want 700", perm)
	}

	// The artifact itself still serves the baked bytes, unchanged.
	_, got := readBack(t, s, "notes")
	if !bytes.Equal(got, baked) {
		t.Errorf("stored bytes = %q, want the baked HTML %q", got, baked)
	}
}

// TestSourceAbsent covers the legacy path: an artifact published without a
// source (a plain upload, or anything predating retention) is a normal artifact,
// not a broken one. It reports ErrNoSource so callers skip it, and an unknown
// slug is still ErrNotFound — the two must stay distinguishable.
func TestSourceAbsent(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put(PutOptions{Slug: "plain", Filename: "plain.html"},
		bytes.NewReader([]byte("<p>hand-written</p>"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if _, err := s.Source("plain"); !errors.Is(err, ErrNoSource) {
		t.Errorf("Source of a sourceless artifact = %v, want ErrNoSource", err)
	}
	if _, err := s.Source("no-such-slug"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Source of a missing artifact = %v, want ErrNotFound", err)
	}
}

// TestRepublishWithoutSourceDropsRetained pins the lifecycle invariant: source
// follows the bytes. Overwriting a rendered page with a plain upload must not
// leave a sidecar describing content that is no longer there — a later rebake
// would resurrect the old page over the new one.
func TestRepublishWithoutSourceDropsRetained(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put(PutOptions{Slug: "notes", Filename: "notes.html",
		Source: []byte(markdownSource), RenderSpec: `{"named_slug":"notes"}`},
		bytes.NewReader([]byte("<h1>Notes</h1>"))); err != nil {
		t.Fatalf("Put (rendered): %v", err)
	}
	if _, err := s.Put(PutOptions{Slug: "notes", Filename: "notes.html"},
		bytes.NewReader([]byte("<p>replaced by hand</p>"))); err != nil {
		t.Fatalf("Put (overwrite): %v", err)
	}

	if _, err := s.Source("notes"); !errors.Is(err, ErrNoSource) {
		t.Errorf("Source after a sourceless overwrite = %v, want ErrNoSource", err)
	}
	art, _ := readBack(t, s, "notes")
	if art.RenderSpec != "" {
		t.Errorf("render spec survived a sourceless overwrite: %q", art.RenderSpec)
	}
	if _, err := os.Stat(filepath.Join(s.root, sourceDirName, "notes")); !os.IsNotExist(err) {
		t.Errorf("source sidecar still on disk: %v", err)
	}
}

// TestRepublishReplacesRetainedSource: a second rendered publish to the same
// slug replaces the retained source rather than keeping the first one.
func TestRepublishReplacesRetainedSource(t *testing.T) {
	s := newTestStore(t)
	for _, src := range []string{"# One\n", "# Two\n"} {
		if _, err := s.Put(PutOptions{Slug: "notes", Filename: "notes.html", Source: []byte(src)},
			bytes.NewReader([]byte("<h1>x</h1>"))); err != nil {
			t.Fatalf("Put %q: %v", src, err)
		}
	}
	rec, err := s.Source("notes")
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if string(rec.Body) != "# Two\n" {
		t.Errorf("retained source = %q, want the latest publish", rec.Body)
	}
}

// TestDeleteRemovesSource: a deleted artifact must not leave its plaintext on
// disk.
func TestDeleteRemovesSource(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put(PutOptions{Slug: "notes", Source: []byte(markdownSource)},
		bytes.NewReader([]byte("<h1>Notes</h1>"))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete("notes"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.root, sourceDirName, "notes")); !os.IsNotExist(err) {
		t.Errorf("source survived Delete: %v", err)
	}
}

// TestSweepExpiredRemovesSource: an expired artifact's source expires with it,
// or a TTL'd page would leave its content behind forever.
func TestSweepExpiredRemovesSource(t *testing.T) {
	s := newTestStore(t)
	art, err := s.Put(PutOptions{Source: []byte(markdownSource), TTL: time.Minute},
		bytes.NewReader([]byte("<h1>Notes</h1>")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	n, err := s.SweepExpired(time.Now().Add(2 * time.Minute))
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d artifacts, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(s.root, sourceDirName, art.Slug)); !os.IsNotExist(err) {
		t.Errorf("source survived expiry: %v", err)
	}
}

// TestReplaceBodyPreservesMetadata is the guard on the rebake path. A rebake
// changes the rendering and nothing else: re-rendering must never unlock a
// password-gated page, extend an expiring one, restamp its publish time, or move
// it out of its series.
func TestReplaceBodyPreservesMetadata(t *testing.T) {
	s := newTestStore(t)
	before, err := s.Put(PutOptions{
		Slug:     "notes",
		Filename: "notes.html",
		Password: "hunter2",
		TTL:      time.Hour,
		Series:   "lessons",
		Source:   []byte(markdownSource),
	}, bytes.NewReader([]byte("<h1>Old rendering</h1>")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rebaked := []byte("<h1>New rendering</h1><footer>new</footer>")
	after, err := s.ReplaceBody("notes", bytes.NewReader(rebaked))
	if err != nil {
		t.Fatalf("ReplaceBody: %v", err)
	}

	if after.Size != int64(len(rebaked)) {
		t.Errorf("size = %d, want %d", after.Size, len(rebaked))
	}
	if !after.HasPassword() || after.PasswordHash != before.PasswordHash {
		t.Error("password gate did not survive the rebake")
	}
	// Timestamps are stored at second granularity, so compare what was persisted
	// rather than the sub-second value Put returned from memory.
	if after.ExpiresAt.Unix() != before.ExpiresAt.Unix() {
		t.Errorf("expiry moved: %v -> %v", before.ExpiresAt, after.ExpiresAt)
	}
	if after.CreatedAt.Unix() != before.CreatedAt.Unix() {
		t.Errorf("publish time moved: %v -> %v", before.CreatedAt, after.CreatedAt)
	}
	if after.Series != "lessons" || after.Filename != "notes.html" || after.ContentType != before.ContentType {
		t.Errorf("metadata changed: %+v", after)
	}

	_, got := readBack(t, s, "notes")
	if !bytes.Equal(got, rebaked) {
		t.Errorf("stored bytes = %q, want the rebaked page", got)
	}
	// The source is still there, so the page can be rebaked again.
	if _, err := s.Source("notes"); err != nil {
		t.Errorf("Source after rebake: %v", err)
	}
}

// TestReplaceBodyMissingSlug: rebaking something that is not there is a
// not-found, not a silent create.
func TestReplaceBodyMissingSlug(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.ReplaceBody("ghost", bytes.NewReader([]byte("x"))); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReplaceBody on a missing slug = %v, want ErrNotFound", err)
	}
}

// TestSlugsIncludesPrivateAndExcludesExpired: the bulk-rebake candidate list.
// Private pages must be included — excluding them would strand every private
// page on an old rendering — and expired ones must not be.
func TestSlugsIncludesPrivateAndExcludesExpired(t *testing.T) {
	s := newTestStore(t)
	pub, err := s.Put(PutOptions{Slug: "public-page", Source: []byte(markdownSource)},
		bytes.NewReader([]byte("<h1>a</h1>")))
	if err != nil {
		t.Fatalf("Put public: %v", err)
	}
	priv, err := s.Put(PutOptions{Private: true, Source: []byte(markdownSource)},
		bytes.NewReader([]byte("<h1>b</h1>")))
	if err != nil {
		t.Fatalf("Put private: %v", err)
	}
	gone, err := s.Put(PutOptions{TTL: time.Nanosecond}, bytes.NewReader([]byte("<h1>c</h1>")))
	if err != nil {
		t.Fatalf("Put expiring: %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	slugs, err := s.Slugs(DefaultOwner)
	if err != nil {
		t.Fatalf("Slugs: %v", err)
	}
	seen := map[string]bool{}
	for _, slug := range slugs {
		seen[slug] = true
	}
	if !seen[pub.Slug] {
		t.Error("public artifact missing from Slugs")
	}
	if !seen[priv.Slug] {
		t.Error("private artifact missing from Slugs — it would never be rebaked")
	}
	if seen[gone.Slug] {
		t.Error("expired artifact listed in Slugs")
	}
}

// TestSourceRetentionUpgradesInPlace: a store written by a binary without the
// render_spec column opens, keeps its artifacts, and reads back as "no retained
// source" — the additive-migration promise. Simulated by dropping the column
// from a store this build created.
func TestSourceRetentionUpgradesInPlace(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Put(PutOptions{Slug: "legacy"}, bytes.NewReader([]byte("<p>old</p>"))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE artifacts DROP COLUMN render_spec`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })

	art, f, err := reopened.Get("legacy")
	if err != nil {
		t.Fatalf("Get after upgrade: %v", err)
	}
	f.Close()
	if art.RenderSpec != "" {
		t.Errorf("upgraded artifact render spec = %q, want empty", art.RenderSpec)
	}
	if _, err := reopened.Source("legacy"); !errors.Is(err, ErrNoSource) {
		t.Errorf("Source after upgrade = %v, want ErrNoSource", err)
	}
}
