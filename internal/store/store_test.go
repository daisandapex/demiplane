// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// readBack returns the stored bytes and metadata for a slug.
func readBack(t *testing.T, s *Store, slug string) (Artifact, []byte) {
	t.Helper()
	art, f, err := s.Get(slug)
	if err != nil {
		t.Fatalf("Get(%q): %v", slug, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	return art, b
}

func TestPutRandomSlugRoundTrip(t *testing.T) {
	s := newTestStore(t)
	body := []byte("<!DOCTYPE html><html><body>hello</body></html>")

	art, err := s.Put(PutOptions{}, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if art.Owner != DefaultOwner {
		t.Errorf("owner = %q, want %q", art.Owner, DefaultOwner)
	}
	if art.Size != int64(len(body)) {
		t.Errorf("size = %d, want %d", art.Size, len(body))
	}
	if !strings.HasPrefix(art.ContentType, "text/html") {
		t.Errorf("content type = %q, want text/html", art.ContentType)
	}

	got, blob := readBack(t, s, art.Slug)
	if !bytes.Equal(blob, body) {
		t.Errorf("round-trip mismatch:\n got %q\nwant %q", blob, body)
	}
	if got.Slug != art.Slug {
		t.Errorf("slug mismatch: %q vs %q", got.Slug, art.Slug)
	}
}

func TestRandomSlugIsFriendlyAndUnique(t *testing.T) {
	s := newTestStore(t)
	friendly := regexp.MustCompile(`^[a-z]+-[a-z]+(-\d{4})?$`)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		art, err := s.Put(PutOptions{}, strings.NewReader("x"))
		if err != nil {
			t.Fatalf("Put #%d: %v", i, err)
		}
		if !friendly.MatchString(art.Slug) {
			t.Errorf("slug %q is not adjective-creature shaped", art.Slug)
		}
		if seen[art.Slug] {
			t.Errorf("duplicate slug generated: %q", art.Slug)
		}
		seen[art.Slug] = true
	}
}

func TestNamedSlugOverwritesInPlace(t *testing.T) {
	s := newTestStore(t)
	v1 := []byte("<html>v1</html>")
	v2 := []byte("<html>v2 — longer payload</html>")

	a1, err := s.Put(PutOptions{Slug: "reports", Filename: "report.html"}, bytes.NewReader(v1))
	if err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if a1.Slug != "reports" {
		t.Fatalf("named slug = %q, want reports", a1.Slug)
	}

	a2, err := s.Put(PutOptions{Slug: "reports", Filename: "report.html"}, bytes.NewReader(v2))
	if err != nil {
		t.Fatalf("Put v2: %v", err)
	}
	if a2.Slug != "reports" {
		t.Fatalf("named slug changed on overwrite: %q", a2.Slug)
	}

	_, blob := readBack(t, s, "reports")
	if !bytes.Equal(blob, v2) {
		t.Errorf("overwrite did not take: got %q, want %q", blob, v2)
	}

	// Exactly one row/blob — overwrite in place, not append.
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM artifacts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1 (overwrite in place)", count)
	}
	entries, _ := os.ReadDir(s.blobDir)
	if len(entries) != 1 {
		t.Errorf("blob count = %d, want 1", len(entries))
	}
}

func TestContentTypeInference(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		name       string
		filename   string
		body       string
		wantPrefix string
		wantInline bool
	}{
		{"html sniffed", "", "<!DOCTYPE html><html></html>", "text/html", true},
		{"css by ext", "style.css", "body{color:red}", "text/css", true},
		{"json by ext", "data.json", `{"a":1}`, "application/json", true},
		{"png sniffed", "", "\x89PNG\r\n\x1a\n\x00\x00", "image/png", true},
		{"binary attachment", "blob.bin", "\x00\x01\x02\x03rawbytes", "application/octet-stream", false},
		// Security (ADR 0003): a filename hint must NOT relabel a non-HTML body to
		// executable text/html — the stored-XSS content-type-confusion vector. The
		// sniffed type wins so the artifact keeps its no-script CSP gate.
		{"svg body not relabeled to html", "page.html",
			`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, "text/plain", true},
		{"xml body not relabeled to html", "page.html",
			`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`, "text/xml", true},
		// A genuinely-HTML body still honors the .html hint — the hosting feature
		// is preserved; only mismatched relabels are refused.
		{"genuine html relabel honored", "page.html",
			"<!DOCTYPE html><html><body>hi</body></html>", "text/html", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			art, err := s.Put(PutOptions{Filename: tc.filename}, strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if !strings.HasPrefix(art.ContentType, tc.wantPrefix) {
				t.Errorf("content type = %q, want prefix %q", art.ContentType, tc.wantPrefix)
			}
			if got := IsInline(art.ContentType); got != tc.wantInline {
				t.Errorf("IsInline(%q) = %v, want %v", art.ContentType, got, tc.wantInline)
			}
		})
	}
}

// TestLargeBodyStreams writes a body far larger than the sniff buffer and
// verifies byte-for-byte integrity. The implementation uses io.Copy (never
// io.ReadAll), so memory stays bounded regardless of this size.
func TestLargeBodyStreams(t *testing.T) {
	s := newTestStore(t)
	const size = 8 << 20 // 8 MiB
	h := sha256.New()
	src := io.TeeReader(io.LimitReader(repeatingReader{}, size), h)

	art, err := s.Put(PutOptions{Filename: "big.bin"}, src)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if art.Size != size {
		t.Fatalf("size = %d, want %d", art.Size, size)
	}

	f, err := os.Open(filepath.Join(s.blobDir, art.Slug))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stored := sha256.New()
	if _, err := io.Copy(stored, f); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.Sum(nil), h.Sum(nil)) {
		t.Error("stored bytes differ from source (streaming corruption)")
	}
}

// repeatingReader yields an endless deterministic byte pattern.
type repeatingReader struct{}

func (repeatingReader) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = byte(i % 251)
	}
	return len(b), nil
}

func TestStoreDirsArePrivate(t *testing.T) {
	// Use a not-yet-existing path so Open() creates the store + blobs dirs
	// itself (the real fresh-deploy case); t.TempDir() pre-creates its dir with
	// looser perms, which Open does not re-permission.
	dir := filepath.Join(t.TempDir(), "store")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, p := range []string{dir, s.blobDir} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s perms = %04o, want 0700 (other local users must not read blobs)", p, perm)
		}
	}
}

func TestCount(t *testing.T) {
	s := newTestStore(t)
	if n, err := s.Count(DefaultOwner); err != nil || n != 0 {
		t.Fatalf("empty Count = %d, %v; want 0, nil", n, err)
	}
	mustPut(t, s, PutOptions{Slug: "a"}, "x")
	mustPut(t, s, PutOptions{Slug: "b"}, "y")
	mustPut(t, s, PutOptions{Slug: "gone", TTL: time.Millisecond}, "z")
	time.Sleep(5 * time.Millisecond)
	n, err := s.Count(DefaultOwner)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 { // expired one excluded
		t.Errorf("Count = %d, want 2 (expired excluded)", n)
	}
}

func TestGetNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.Get("does-not-exist"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestValidateNamedSlug(t *testing.T) {
	good := []string{"reports", "my-report", "build_123", "a", "v1.2.3", "UPPER-ok"}
	bad := []string{"", ".", "..", "../etc/passwd", "a/b", "has space", "publish", "x\x00y", strings.Repeat("a", 200)}
	for _, g := range good {
		if err := ValidateNamedSlug(g); err != nil {
			t.Errorf("ValidateNamedSlug(%q) = %v, want nil", g, err)
		}
	}
	for _, b := range bad {
		if err := ValidateNamedSlug(b); err == nil {
			t.Errorf("ValidateNamedSlug(%q) = nil, want error", b)
		}
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	art, err := s.Put(PutOptions{Slug: "doomed"}, strings.NewReader("<html>x</html>"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete(art.Slug); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Row and blob both gone.
	if _, _, err := s.Get(art.Slug); err != ErrNotFound {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(s.blobDir, art.Slug)); !os.IsNotExist(err) {
		t.Errorf("blob still present after delete: %v", err)
	}
	// Deleting a missing slug is ErrNotFound.
	if err := s.Delete("never-existed"); err != ErrNotFound {
		t.Errorf("Delete missing = %v, want ErrNotFound", err)
	}
}

func TestList(t *testing.T) {
	s := newTestStore(t)
	if arts, err := s.List(DefaultOwner); err != nil || len(arts) != 0 {
		t.Fatalf("empty List = %v, %v; want 0, nil", arts, err)
	}
	want := map[string]bool{}
	for _, name := range []string{"one", "two", "three"} {
		if _, err := s.Put(PutOptions{Slug: name, Filename: name + ".html"}, strings.NewReader("x")); err != nil {
			t.Fatalf("Put %q: %v", name, err)
		}
		want[name] = true
	}
	arts, err := s.List(DefaultOwner)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(arts) != len(want) {
		t.Fatalf("List len = %d, want %d", len(arts), len(want))
	}
	for _, a := range arts {
		if !want[a.Slug] {
			t.Errorf("unexpected slug %q in list", a.Slug)
		}
		if a.Owner != DefaultOwner {
			t.Errorf("owner = %q, want %q", a.Owner, DefaultOwner)
		}
	}
	// A foreign owner sees nothing (per-owner isolation is wired for later).
	if arts, err := s.List("someone-else"); err != nil || len(arts) != 0 {
		t.Errorf("foreign-owner List = %v, %v; want 0, nil", arts, err)
	}
}

func TestPrivatePublishGetsCapabilitySlug(t *testing.T) {
	s := newTestStore(t)
	friendly := regexp.MustCompile(`^[a-z]+-[a-z]+(-\d{4})?$`)
	capRe := regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)

	pub, err := s.Put(PutOptions{}, strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if !friendly.MatchString(pub.Slug) {
		t.Errorf("public slug %q should be friendly", pub.Slug)
	}
	if pub.Private {
		t.Error("public artifact marked private")
	}

	priv, err := s.Put(PutOptions{Private: true}, strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if !priv.Private {
		t.Error("private artifact not marked private")
	}
	if friendly.MatchString(priv.Slug) || !capRe.MatchString(priv.Slug) {
		t.Errorf("private slug %q should be a high-entropy capability slug", priv.Slug)
	}

	// Privacy flag round-trips through Get.
	got, f, err := s.Get(priv.Slug)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if !got.Private {
		t.Error("Get lost the private flag")
	}
}

func TestPrivateNamedSlugRejectedAtStore(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put(PutOptions{Slug: "named", Private: true}, strings.NewReader("x"))
	if err != ErrPrivateNamedSlug {
		t.Errorf("Put(private+named) = %v, want ErrPrivateNamedSlug", err)
	}
	// No blob should be written when the slug is rejected.
	if entries, _ := os.ReadDir(s.blobDir); len(entries) != 0 {
		t.Errorf("blob written despite rejected slug: %d files", len(entries))
	}
}

func TestPasswordStoredHashedAndVerifies(t *testing.T) {
	s := newTestStore(t)
	art, err := s.Put(PutOptions{Slug: "secret", Password: "hunter2"}, strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if !art.HasPassword() {
		t.Fatal("artifact should report HasPassword")
	}
	// Hash is never the plaintext.
	if strings.Contains(art.PasswordHash, "hunter2") || art.PasswordHash == "hunter2" {
		t.Error("password stored in plaintext")
	}
	got, f, err := s.Get("secret")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if !PasswordMatches(got.PasswordHash, "hunter2") {
		t.Error("correct password did not verify")
	}
	if PasswordMatches(got.PasswordHash, "wrong") {
		t.Error("wrong password verified")
	}
}

func TestTTLExpiryLazyOnGet(t *testing.T) {
	s := newTestStore(t)
	// Past TTL: 1ms, then wait it out.
	if _, err := s.Put(PutOptions{Slug: "ephemeral", TTL: time.Millisecond}, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, _, err := s.Get("ephemeral"); err != ErrNotFound {
		t.Errorf("expired Get = %v, want ErrNotFound", err)
	}
	// Lazy reclaim removed the blob too.
	if _, err := os.Stat(filepath.Join(s.blobDir, "ephemeral")); !os.IsNotExist(err) {
		t.Error("expired blob not lazily reclaimed")
	}

	// Future TTL is served normally and appears in list.
	if _, err := s.Put(PutOptions{Slug: "fresh", TTL: time.Hour}, strings.NewReader("y")); err != nil {
		t.Fatal(err)
	}
	if _, f, err := s.Get("fresh"); err != nil {
		t.Errorf("unexpired Get error: %v", err)
	} else {
		f.Close()
	}
}

func TestSweepExpired(t *testing.T) {
	s := newTestStore(t)
	mustPut(t, s, PutOptions{Slug: "keep"}, "no ttl")
	mustPut(t, s, PutOptions{Slug: "keep-future", TTL: time.Hour}, "future")
	mustPut(t, s, PutOptions{Slug: "gone1", TTL: time.Millisecond}, "x")
	mustPut(t, s, PutOptions{Slug: "gone2", TTL: time.Millisecond}, "y")
	time.Sleep(5 * time.Millisecond)

	n, err := s.SweepExpired(time.Now())
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if n != 2 {
		t.Errorf("swept %d, want 2", n)
	}
	arts, _ := s.List(DefaultOwner)
	got := map[string]bool{}
	for _, a := range arts {
		got[a.Slug] = true
	}
	if !got["keep"] || !got["keep-future"] {
		t.Errorf("sweeper removed a non-expired artifact: have %v", got)
	}
	if got["gone1"] || got["gone2"] {
		t.Errorf("sweeper left an expired artifact: have %v", got)
	}
	// Blobs gone too.
	for _, slug := range []string{"gone1", "gone2"} {
		if _, err := os.Stat(filepath.Join(s.blobDir, slug)); !os.IsNotExist(err) {
			t.Errorf("expired blob %q still present", slug)
		}
	}
}

// TestSweepDoesNotReapRenewedArtifact reproduces the TOCTOU race: an artifact
// that was expired at scan time but re-published (TTL cleared) before the delete
// must survive. reapIfExpired re-checks the predicate atomically.
func TestSweepDoesNotReapRenewedArtifact(t *testing.T) {
	s := newTestStore(t)
	mustPut(t, s, PutOptions{Slug: "renew", TTL: time.Millisecond}, "v1")
	time.Sleep(5 * time.Millisecond)
	now := time.Now() // scan time: "renew" is expired as of here

	// Concurrent re-publish renews it (overwrite-in-place, no TTL) AFTER the scan.
	mustPut(t, s, PutOptions{Slug: "renew"}, "v2-renewed")

	// The guarded delete for the already-scanned slug must be a no-op.
	reaped, err := s.reapIfExpired("renew", now)
	if err != nil {
		t.Fatalf("reapIfExpired: %v", err)
	}
	if reaped {
		t.Error("reaped a renewed artifact — TOCTOU guard ineffective")
	}
	got, f, err := s.Get("renew")
	if err != nil {
		t.Fatalf("renewed artifact lost: %v", err)
	}
	defer f.Close()
	if !got.ExpiresAt.IsZero() {
		t.Errorf("renewed artifact still has expiry: %v", got.ExpiresAt)
	}
}

// TestMigrationAddsColumnsToLegacyDB simulates an M1/M2 database (base schema
// only) and verifies migrate() adds the M3 columns without data loss.
func TestMigrationAddsColumnsToLegacyDB(t *testing.T) {
	dir := t.TempDir()
	// Build a legacy store, then drop the M3 columns to mimic an old DB.
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, PutOptions{Slug: "legacy"}, "old data")
	// The partial index references expires_at; drop it before the columns.
	if _, err := s.db.Exec(`DROP INDEX IF EXISTS idx_artifacts_expires_at`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	for _, col := range []string{"private", "password_hash", "expires_at"} {
		if _, err := s.db.Exec(`ALTER TABLE artifacts DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop column %s: %v", col, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen → migrate must re-add the columns and preserve the row.
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen/migrate: %v", err)
	}
	defer s2.Close()
	art, f, err := s2.Get("legacy")
	if err != nil {
		t.Fatalf("legacy row lost after migration: %v", err)
	}
	f.Close()
	if art.Private || art.HasPassword() || !art.ExpiresAt.IsZero() {
		t.Errorf("migrated columns have wrong defaults: %+v", art)
	}
	// New columns are usable post-migration (named+TTL, and a separate private).
	if _, err := s2.Put(PutOptions{Slug: "after", TTL: time.Hour}, strings.NewReader("z")); err != nil {
		t.Fatalf("publish after migration: %v", err)
	}
	if _, err := s2.Put(PutOptions{Private: true}, strings.NewReader("z")); err != nil {
		t.Fatalf("private publish after migration: %v", err)
	}
}

func mustPut(t *testing.T, s *Store, opts PutOptions, body string) Artifact {
	t.Helper()
	a, err := s.Put(opts, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Put(%+v): %v", opts, err)
	}
	return a
}

func TestValidationErrorsAreTyped(t *testing.T) {
	// Both validation paths must surface as *InputError so the HTTP layer can
	// classify 400 vs 500 by type, not by error text.
	var ie *InputError
	if err := ValidateNamedSlug("docs"); !errors.As(err, &ie) {
		t.Errorf("reserved-slug error is %T, want *InputError", err)
	}
	if err := ValidateNamedSlug("bad/slug"); !errors.As(err, &ie) {
		t.Errorf("bad-slug error is %T, want *InputError", err)
	}
	if !errors.As(error(ErrPrivateNamedSlug), &ie) {
		t.Errorf("ErrPrivateNamedSlug is not an *InputError")
	}
	// A wrapped InputError (as store.Put surfaces it) is still detected.
	wrapped := fmt.Errorf("publish failed: %w", ValidateNamedSlug("help"))
	if !errors.As(wrapped, &ie) {
		t.Errorf("wrapped InputError not detected via errors.As")
	}
}

func TestPutRejectsBadNamedSlug(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put(PutOptions{Slug: "../escape"}, strings.NewReader("x")); err == nil {
		t.Error("Put with traversal slug succeeded, want error")
	}
	// No blob should have been written on validation failure.
	entries, _ := os.ReadDir(s.blobDir)
	if len(entries) != 0 {
		t.Errorf("blob written despite invalid slug: %d files", len(entries))
	}
}

// TestListExcludesPrivate guards the capability-URL privacy model: a private
// artifact's slug IS its secret, so List must never return private artifacts
// (the /list disclosure Shannon exploited).
func TestListExcludesPrivate(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put(PutOptions{Slug: "public-one", Filename: "p.txt"}, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	priv, err := s.Put(PutOptions{Private: true}, strings.NewReader("secret"))
	if err != nil {
		t.Fatal(err)
	}
	arts, err := s.List(DefaultOwner)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(arts) != 1 {
		t.Errorf("List len = %d, want 1 (public only)", len(arts))
	}
	for _, a := range arts {
		if a.Private || a.Slug == priv.Slug {
			t.Errorf("List leaked private artifact/slug %q", a.Slug)
		}
	}
}
