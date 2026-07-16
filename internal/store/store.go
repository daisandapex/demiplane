// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package store is demiplane's content store: flat files on disk for artifact
// bytes plus a SQLite table for metadata. It owns slug resolution, streaming
// writes (never buffering a whole upload in memory), content-type inference,
// and overwrite-in-place semantics for named slugs.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// DefaultOwner is the single local owner used in v1. The owner column exists
// from day one so multi-user is additive later with no migration.
const DefaultOwner = "local"

// sniffLen is the number of leading bytes http.DetectContentType inspects.
const sniffLen = 512

// genAttempts is how many plain friendly-slug triples we try before falling
// back to a numeric suffix.
const genAttempts = 8

// Artifact is the metadata record for one stored file. PasswordHash is never
// serialized to clients — callers that emit JSON build their own view types.
type Artifact struct {
	Slug         string
	Filename     string
	ContentType  string
	Size         int64
	CreatedAt    time.Time
	Owner        string
	Private      bool
	PasswordHash string    // "" = no view password
	ExpiresAt    time.Time // zero = never expires
}

// HasPassword reports whether the artifact is gated by a view password.
func (a Artifact) HasPassword() bool { return a.PasswordHash != "" }

// Store is a content store rooted at a directory. It is safe for concurrent
// use: SQLite serializes writes and blob writes are atomic renames.
type Store struct {
	root    string
	blobDir string
	db      *sql.DB
	// notifier, when set (via SetNotifier at startup), is fired after each
	// successful Put so the SSE live-reload feature can push a reload event. See
	// notify.go. nil = no notifications (default).
	notifier Notifier
}

// Open opens (creating if needed) a store rooted at dir.
func Open(dir string) (*Store, error) {
	blobDir := filepath.Join(dir, "blobs")
	// 0700, not 0755: blobs include password-gated artifacts, so other local
	// users must not be able to read them off disk and bypass the view gate.
	// This also blocks traversal into the store dir (and thus meta.db).
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		return nil, fmt.Errorf("create blob dir: %w", err)
	}
	dbPath := filepath.Join(dir, "meta.db")
	// _pragma busy_timeout avoids spurious "database is locked" under concurrency.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open metadata db: %w", err)
	}
	s := &Store{root: dir, blobDir: blobDir, db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the metadata database handle.
func (s *Store) Close() error { return s.db.Close() }

// Root returns the store's root directory. Modules use it (via the module Host)
// to carve out isolated per-module storage under <root>/modules/<name>.
func (s *Store) Root() string { return s.root }

// migrate creates the base table and additively adds later-milestone columns.
// The M3 columns (private, password_hash, expires_at) are added via ALTER TABLE
// guarded by a column-existence check, so an M1/M2 database upgrades in place
// with no data loss — the migration-friendly schema promise from M1.
func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS artifacts (
    slug         TEXT PRIMARY KEY,
    filename     TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size         INTEGER NOT NULL,
    created_at   INTEGER NOT NULL,
    owner        TEXT NOT NULL DEFAULT 'local'
);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}

	// M3 additive columns.
	added := []struct{ name, ddl string }{
		{"private", "ALTER TABLE artifacts ADD COLUMN private INTEGER NOT NULL DEFAULT 0"},
		{"password_hash", "ALTER TABLE artifacts ADD COLUMN password_hash TEXT NOT NULL DEFAULT ''"},
		{"expires_at", "ALTER TABLE artifacts ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0"},
	}
	for _, col := range added {
		if err := s.addColumnIfMissing(col.name, col.ddl); err != nil {
			return err
		}
	}
	// Index the sweeper's predicate (expires_at > 0 AND <= now).
	if _, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_artifacts_expires_at ON artifacts(expires_at) WHERE expires_at > 0`,
	); err != nil {
		return fmt.Errorf("create expiry index: %w", err)
	}
	return nil
}

// addColumnIfMissing runs ddl only when the named column is absent.
func (s *Store) addColumnIfMissing(name, ddl string) error {
	rows, err := s.db.Query(`PRAGMA table_info(artifacts)`)
	if err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			colName    string
			colType    string
			notNull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &dfltValue, &primaryKey); err != nil {
			return fmt.Errorf("scan schema: %w", err)
		}
		if colName == name {
			return rows.Close()
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("add column %s: %w", name, err)
	}
	return nil
}

// PutOptions carries the per-publish settings for Put. The zero value is a
// valid public, no-password, no-expiry, auto-slug publish.
type PutOptions struct {
	Slug     string        // named slug; "" auto-generates (friendly, or capability if Private)
	Filename string        // extension hint for content-type; "" falls back to sniffing
	Private  bool          // mark private + (when auto-slugging) use a high-entropy capability slug
	Password string        // plaintext view password; "" = no gate. Hashed before storage.
	TTL      time.Duration // 0 = never expires; >0 sets expires_at = now+TTL
}

// Put stores r under a slug and records its metadata, returning the artifact.
//
//   - opts.Slug == "": a slug is generated. Public publishes get a friendly,
//     collision-checked slug; private publishes get a high-entropy capability
//     slug (the unguessable URL is the secret). Either way it is one-shot.
//   - opts.Slug != "": the (validated) name is used and OVERWRITES in place, so a
//     stable URL like /reports always serves the latest bytes.
//
// The body is streamed to disk — it is never fully buffered in memory.
func (s *Store) Put(opts PutOptions, r io.Reader) (Artifact, error) {
	slug, overwrite, err := s.resolveSlug(opts.Slug, opts.Private)
	if err != nil {
		return Artifact{}, err
	}

	var passwordHash string
	if opts.Password != "" {
		passwordHash, err = hashPassword(opts.Password)
		if err != nil {
			return Artifact{}, err
		}
	}

	sniffed, size, err := s.writeBlob(slug, r)
	if err != nil {
		return Artifact{}, err
	}

	contentType := inferContentType(opts.Filename, sniffed)
	filename := opts.Filename
	if filename == "" {
		filename = slug
	}

	now := time.Now().UTC()
	var expiresAt time.Time
	if opts.TTL > 0 {
		expiresAt = now.Add(opts.TTL)
	}

	art := Artifact{
		Slug:         slug,
		Filename:     filename,
		ContentType:  contentType,
		Size:         size,
		CreatedAt:    now,
		Owner:        DefaultOwner,
		Private:      opts.Private,
		PasswordHash: passwordHash,
		ExpiresAt:    expiresAt,
	}
	if err := s.upsertMeta(art); err != nil {
		// Best-effort rollback of the blob we just wrote on a fresh slug; for an
		// overwrite we leave the new bytes (the row still points at a valid file).
		if !overwrite {
			os.Remove(filepath.Join(s.blobDir, slug))
		}
		return Artifact{}, err
	}
	// Fire the publish notification (guarded no-op when no notifier is set) so a
	// live-preview tab on this slug can reload. Emitted only after the metadata
	// row commits, so a subscriber that reacts sees the new bytes.
	s.notify(art.Slug)
	return art, nil
}

// ErrPrivateNamedSlug rejects the incoherent private + named-slug combination.
// Privacy's protection is the unguessable capability slug; a named slug is
// guessable, so the combination would yield a "private" artifact with no real
// protection. Enforced here so every ingest path (HTTP and SSH) is covered.
// It is an *InputError so the HTTP layer maps it to 400.
var ErrPrivateNamedSlug = &InputError{Msg: "private artifacts cannot use a named slug"}

// resolveSlug validates a named slug (reporting whether it already exists, i.e.
// an overwrite) or generates a fresh unused slug — capability-grade when the
// artifact is private, friendly otherwise.
func (s *Store) resolveSlug(named string, private bool) (slug string, overwrite bool, err error) {
	if named != "" {
		if private {
			return "", false, ErrPrivateNamedSlug
		}
		if err := ValidateNamedSlug(named); err != nil {
			return "", false, err
		}
		exists, err := s.exists(named)
		if err != nil {
			return "", false, err
		}
		return named, exists, nil
	}
	if private {
		slug, err = s.generateUnusedCapabilitySlug()
		return slug, false, err
	}
	slug, err = s.generateUnusedSlug()
	return slug, false, err
}

// generateUnusedCapabilitySlug returns an unused high-entropy slug. Collisions
// are astronomically unlikely, but the existence check keeps the invariant.
func (s *Store) generateUnusedCapabilitySlug() (string, error) {
	for i := 0; i < genAttempts; i++ {
		candidate, err := GenerateCapabilitySlug()
		if err != nil {
			return "", err
		}
		exists, err := s.exists(candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", errors.New("could not generate an unused capability slug")
}

// generateUnusedSlug returns a friendly slug not already present, retrying and
// then falling back to a numeric suffix if the space is crowded.
func (s *Store) generateUnusedSlug() (string, error) {
	for i := 0; i < genAttempts; i++ {
		candidate, err := GenerateFriendlySlug()
		if err != nil {
			return "", err
		}
		exists, err := s.exists(candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	// Crowded base space — switch to suffixed candidates.
	for i := 0; i < genAttempts; i++ {
		candidate, err := generateFriendlySlugSuffixed()
		if err != nil {
			return "", err
		}
		exists, err := s.exists(candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", errors.New("could not generate an unused slug after retries")
}

// HasPublic reports whether a slug names a currently VISIBLE artifact: it exists,
// is not private, and has not expired. Public-facing surfaces that must not reveal
// the existence of a private (capability-slug) or expired artifact — e.g. the
// reply form — use this instead of Has, so it mirrors the GET /list filter and a
// private slug's existence is never leaked via a 200-vs-404 oracle.
func (s *Store) HasPublic(slug string) (bool, error) {
	var one int
	err := s.db.QueryRow(
		`SELECT 1 FROM artifacts WHERE slug = ? AND private = 0 AND (expires_at = 0 OR expires_at > ?)`,
		slug, time.Now().Unix(),
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lookup public slug: %w", err)
	}
	return true, nil
}

func (s *Store) exists(slug string) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM artifacts WHERE slug = ?`, slug).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lookup slug: %w", err)
	}
	return true, nil
}

// writeBlob streams r to <blobDir>/<slug> atomically (temp file + rename) and
// returns the sniffed content-type and total byte count. Only the first
// sniffLen bytes are held in memory; the rest is copied straight through.
func (s *Store) writeBlob(slug string, r io.Reader) (sniffed string, size int64, err error) {
	tmp, err := os.CreateTemp(s.blobDir, ".tmp-*")
	if err != nil {
		return "", 0, fmt.Errorf("create temp blob: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Remove the temp file if we did not successfully rename it away.
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	head := make([]byte, sniffLen)
	n, rerr := io.ReadFull(r, head)
	if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
		return "", 0, fmt.Errorf("read body: %w", rerr)
	}
	head = head[:n]
	sniffed = http.DetectContentType(head)

	if _, werr := tmp.Write(head); werr != nil {
		return "", 0, fmt.Errorf("write blob head: %w", werr)
	}
	copied, cerr := io.Copy(tmp, r)
	if cerr != nil {
		return "", 0, fmt.Errorf("stream blob: %w", cerr)
	}
	size = int64(n) + copied

	if cerr := tmp.Close(); cerr != nil {
		err = fmt.Errorf("close blob: %w", cerr)
		return "", 0, err
	}
	final := filepath.Join(s.blobDir, slug)
	if rerr := os.Rename(tmpName, final); rerr != nil {
		err = fmt.Errorf("commit blob: %w", rerr)
		return "", 0, err
	}
	return sniffed, size, nil
}

// upsertMeta inserts or, for an existing (named) slug, overwrites the metadata
// row in place. Owner is preserved on overwrite; privacy, password, and expiry
// are replaced with the new publish's values (re-publishing resets them).
func (s *Store) upsertMeta(a Artifact) error {
	const q = `
INSERT INTO artifacts (slug, filename, content_type, size, created_at, owner, private, password_hash, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(slug) DO UPDATE SET
    filename      = excluded.filename,
    content_type  = excluded.content_type,
    size          = excluded.size,
    created_at    = excluded.created_at,
    -- Private is sticky: once an artifact is private it stays private across
    -- re-publishes to the same slug. Without this, in no-auth mode anyone who
    -- learns a private capability slug could POST /publish?slug=<it> (private=0)
    -- to flip it public and surface it in GET /list. MAX() keeps the private bit
    -- latched; changing privacy requires an explicit delete + re-publish.
    private       = MAX(artifacts.private, excluded.private),
    password_hash = excluded.password_hash,
    expires_at    = excluded.expires_at;`
	_, err := s.db.Exec(q,
		a.Slug, a.Filename, a.ContentType, a.Size, a.CreatedAt.Unix(), a.Owner,
		boolToInt(a.Private), a.PasswordHash, unixOrZero(a.ExpiresAt),
	)
	if err != nil {
		return fmt.Errorf("upsert metadata: %w", err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// unixOrZero returns t's Unix seconds, or 0 for the zero time (never-expires).
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// ErrNotFound is returned by Get when no artifact has the given slug.
var ErrNotFound = errors.New("artifact not found")

// Get returns an artifact's metadata and an open reader for its bytes. The
// caller must Close the reader. A past-TTL artifact is treated as not found and
// is deleted lazily (the background sweeper is the primary reclaimer; this just
// guarantees an expired artifact is never served even between sweeps).
func (s *Store) Get(slug string) (Artifact, *os.File, error) {
	var (
		a       Artifact
		created int64
		expires int64
		private int
	)
	err := s.db.QueryRow(
		`SELECT slug, filename, content_type, size, created_at, owner, private, password_hash, expires_at
		   FROM artifacts WHERE slug = ?`,
		slug,
	).Scan(&a.Slug, &a.Filename, &a.ContentType, &a.Size, &created, &a.Owner, &private, &a.PasswordHash, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, nil, ErrNotFound
	}
	if err != nil {
		return Artifact{}, nil, fmt.Errorf("lookup metadata: %w", err)
	}
	a.CreatedAt = time.Unix(created, 0).UTC()
	a.Private = private != 0
	if expires > 0 {
		a.ExpiresAt = time.Unix(expires, 0).UTC()
		if !a.ExpiresAt.After(time.Now()) {
			// Predicate-guarded reclaim: if a concurrent re-publish renewed the
			// TTL between this read and now, reapIfExpired is a no-op and the
			// renewed artifact survives (it still 404s for THIS request).
			_, _ = s.reapIfExpired(slug, time.Now())
			return Artifact{}, nil, ErrNotFound
		}
	}

	f, err := os.Open(filepath.Join(s.blobDir, slug))
	if err != nil {
		if os.IsNotExist(err) {
			// Metadata without bytes is a corrupt store, not a normal 404.
			return Artifact{}, nil, fmt.Errorf("blob missing for slug %q: %w", slug, err)
		}
		return Artifact{}, nil, fmt.Errorf("open blob: %w", err)
	}
	return a, f, nil
}

// Delete removes an artifact's metadata row and its blob. It returns
// ErrNotFound if no such slug exists. The metadata row is removed first so a
// concurrent Get cannot observe a row whose blob is already gone.
func (s *Store) Delete(slug string) error {
	res, err := s.db.Exec(`DELETE FROM artifacts WHERE slug = ?`, slug)
	if err != nil {
		return fmt.Errorf("delete metadata: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete metadata: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := os.Remove(filepath.Join(s.blobDir, slug)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete blob: %w", err)
	}
	return nil
}

// List returns owner's artifacts, newest first, excluding any already past TTL
// (which the sweeper will reap). In v1 there is a single local owner; the
// parameter keeps per-owner listing additive later.
func (s *Store) List(owner string) ([]Artifact, error) {
	now := time.Now().Unix()
	rows, err := s.db.Query(
		`SELECT slug, filename, content_type, size, created_at, owner, private, password_hash, expires_at
		   FROM artifacts
		  WHERE owner = ? AND private = 0 AND (expires_at = 0 OR expires_at > ?)
		  ORDER BY created_at DESC, slug ASC`,
		owner, now,
	)
	if err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	defer rows.Close()

	var out []Artifact
	for rows.Next() {
		var (
			a       Artifact
			created int64
			expires int64
			private int
		)
		if err := rows.Scan(&a.Slug, &a.Filename, &a.ContentType, &a.Size, &created, &a.Owner, &private, &a.PasswordHash, &expires); err != nil {
			return nil, fmt.Errorf("scan artifact: %w", err)
		}
		a.CreatedAt = time.Unix(created, 0).UTC()
		a.Private = private != 0
		if expires > 0 {
			a.ExpiresAt = time.Unix(expires, 0).UTC()
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifacts: %w", err)
	}
	return out, nil
}

// Count returns the number of non-expired artifacts owned by owner.
func (s *Store) Count(owner string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM artifacts WHERE owner = ? AND (expires_at = 0 OR expires_at > ?)`,
		owner, time.Now().Unix(),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count artifacts: %w", err)
	}
	return n, nil
}

// SweepExpired deletes every artifact whose TTL has passed as of now, removing
// both metadata rows and blobs. It returns the number of artifacts reclaimed.
// Intended to be called periodically by a background sweeper.
//
// Each delete is predicate-guarded (see reapIfExpired) so that an artifact
// re-published in the window between the scan and the delete — which clears or
// renews its TTL via overwrite-in-place — is NOT destroyed.
func (s *Store) SweepExpired(now time.Time) (int, error) {
	rows, err := s.db.Query(
		`SELECT slug FROM artifacts WHERE expires_at > 0 AND expires_at <= ?`,
		now.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("scan expired: %w", err)
	}
	var slugs []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired slug: %w", err)
		}
		slugs = append(slugs, slug)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate expired: %w", err)
	}
	rows.Close()

	n := 0
	for _, slug := range slugs {
		reaped, err := s.reapIfExpired(slug, now)
		if err != nil {
			return n, fmt.Errorf("delete expired %q: %w", slug, err)
		}
		if reaped {
			n++
		}
	}
	return n, nil
}

// reapIfExpired deletes slug only if it is still past-TTL as of now, removing
// the blob only when the row was actually deleted. The DELETE re-checks the
// expiry predicate atomically, so a concurrent re-publish that renewed or
// cleared the TTL leaves the artifact intact (RowsAffected == 0). Returns
// whether an artifact was reaped.
func (s *Store) reapIfExpired(slug string, now time.Time) (bool, error) {
	res, err := s.db.Exec(
		`DELETE FROM artifacts WHERE slug = ? AND expires_at > 0 AND expires_at <= ?`,
		slug, now.Unix(),
	)
	if err != nil {
		return false, fmt.Errorf("reap expired: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reap expired: %w", err)
	}
	if affected == 0 {
		return false, nil
	}
	if err := os.Remove(filepath.Join(s.blobDir, slug)); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reap blob: %w", err)
	}
	return true, nil
}

// inferContentType prefers a known extension mapping from the filename hint and
// falls back to the sniffed type. The result always carries a charset for text
// types (mime.TypeByExtension already does; DetectContentType does for text).
func inferContentType(filenameHint, sniffed string) string {
	if ext := filepath.Ext(filenameHint); ext != "" {
		if ct := mime.TypeByExtension(ext); ct != "" {
			// Security (ADR 0003, defense-in-depth): a filename hint may not
			// relabel a body to executable text/html unless the body itself
			// sniffs as HTML. text/html is served WITHOUT the no-script CSP that
			// guards SVG/XML (hosting executable HTML is an intended feature), so
			// ?filename=page.html on a non-HTML body (SVG/XML/plain) was the
			// content-type-confusion stored-XSS vector. When the hint would force
			// text/html but the sniff disagrees, ignore the hint and keep the
			// sniffed type — which then retains its CSP gate.
			if isHTMLType(ct) && !isHTMLType(sniffed) {
				return sniffedOrOctet(sniffed)
			}
			return ct
		}
	}
	return sniffedOrOctet(sniffed)
}

func sniffedOrOctet(sniffed string) string {
	if sniffed == "" {
		return "application/octet-stream"
	}
	return sniffed
}

// isHTMLType reports whether a content-type is text/html (parameters ignored).
func isHTMLType(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct == "text/html"
}

// inlineTypes are content-type prefixes/values that browsers render safely
// inline. Everything else is served as a download (attachment).
var inlineTypes = []string{
	"text/html", "text/css", "text/plain", "text/xml", "text/javascript",
	"application/javascript", "application/json", "application/xml",
	"application/pdf", "image/", "audio/", "video/", "font/",
}

// IsInline reports whether a content-type should be served inline (vs. as an
// attachment download). Charset/boundary parameters are ignored.
func IsInline(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	for _, t := range inlineTypes {
		if strings.HasSuffix(t, "/") {
			if strings.HasPrefix(ct, t) {
				return true
			}
		} else if ct == t {
			return true
		}
	}
	return false
}

// IsScriptableNonHTML reports whether an inline content-type renders as an
// active document that executes embedded scripts but is expected to be inert —
// SVG, XML, and XHTML. handleGet serves these with a no-script CSP so a
// published image/document can't become a stored-XSS vector. text/html is
// deliberately excluded: hosting executable HTML pages is an intended feature.
func IsScriptableNonHTML(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "image/svg+xml", "text/xml", "application/xml", "application/xhtml+xml":
		return true
	}
	return false
}
