// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package store is demiplane's content store: flat files on disk for artifact
// bytes plus a SQLite table for metadata. It owns slug resolution, streaming
// writes (never buffering a whole upload in memory), content-type inference,
// and overwrite-in-place semantics for named slugs.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daisandapex/demiplane/internal/logsafe"
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
	Series       string    // explicit ?series= family; "" = no series
	// RenderSpec is the opaque render-parameter blob captured when a rendered
	// artifact was published, kept so the page can be rebaked from its retained
	// source later (see source.go). "" = nothing retained. The store never
	// interprets it.
	RenderSpec string
	// ModifiedAt is when the stored bytes last changed: the publish, or a later
	// rebake. Zero for rows written before it was recorded.
	ModifiedAt time.Time
	// Version is an opaque token that changes on every write of the bytes, for
	// use as an HTTP entity tag. "" for rows whose bytes predate it.
	Version string
	// Replies is whether the publisher enabled replies (the reply module) for
	// this artifact. Off unless the publish asked for it.
	Replies bool
	// PublishID is a random identifier of the publish that wrote this row. It
	// is replaced by every publish and kept by a rebake, so it names one
	// publish (password, privacy, replies and source together) exactly.
	PublishID string
}

// LastModified is when the stored bytes last changed, falling back to the
// publish time for rows that predate ModifiedAt.
func (a Artifact) LastModified() time.Time {
	if a.ModifiedAt.IsZero() {
		return a.CreatedAt
	}
	return a.ModifiedAt
}

// HasPassword reports whether the artifact is gated by a view password.
func (a Artifact) HasPassword() bool { return a.PasswordHash != "" }

// Store is a content store rooted at a directory. It is safe for concurrent
// use: writers to one slug are serialized, and the metadata row names the
// exact bytes file it describes (see generation.go).
type Store struct {
	root    string
	blobDir string
	// sourceDir holds the ORIGINAL document a rendered artifact was baked from,
	// one sidecar file per slug. See source.go.
	sourceDir string
	db        *sql.DB
	// notifier, when set (via SetNotifier at startup), is fired after each
	// successful Put so the SSE live-reload feature can push a reload event. See
	// notify.go. nil = no notifications (default).
	notifier Notifier
	// locks serializes writers to the same slug (generation.go).
	locks slugLocks
	// quota is the optional aggregate storage accounting (quota.go).
	quota quotaState
	// formKey signs reply-form submission tokens (secret.go).
	formKey []byte
}

// Open opens (creating if needed) a store rooted at dir. The store root, its
// content directories and the metadata database are created owner-only, and an
// existing store with group or world bits on any of them is tightened (and the
// change logged): the database holds capability slugs and password hashes, and
// the blobs and sources hold the bytes of password-gated artifacts.
func Open(dir string) (*Store, error) {
	blobDir := filepath.Join(dir, "blobs")
	sourceDir := filepath.Join(dir, sourceDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	if err := tightenStorePerms(dir); err != nil {
		return nil, err
	}
	// 0700, not 0755: blobs include password-gated artifacts, so other local
	// users must not be able to read them off disk and bypass the view gate.
	// This also blocks traversal into the store dir (and thus meta.db).
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		return nil, fmt.Errorf("create blob dir: %w", err)
	}
	// 0700 for the same reason as blobs: a retained source is the plaintext of a
	// password-gated page, so it must not be readable by other local users.
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		return nil, fmt.Errorf("create source dir: %w", err)
	}
	dbPath := filepath.Join(dir, "meta.db")
	// Create the database file 0600 before SQLite does (it would use 0644 under
	// the usual umask). SQLite gives the -wal and -shm files the database file's
	// mode, so they start owner-only too.
	dbFile, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create metadata db: %w", err)
	}
	dbFile.Close()
	// _pragma busy_timeout avoids spurious "database is locked" under concurrency.
	// A write takes the lock up to three times (its intent, the commit, the
	// replaced generation's deletion record), and SQLite's busy wait is not
	// first-come first-served, so in a burst of concurrent publishes (which
	// admission allows) one writer can wait several seconds on a slow disk;
	// 30s keeps that a delay rather than a failed publish.
	// _txlock=immediate takes the write lock at BEGIN, so a transaction that
	// reads a row and then rewrites it cannot fail on the lock upgrade.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open metadata db: %w", err)
	}
	s := &Store{root: dir, blobDir: blobDir, sourceDir: sourceDir, db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if s.formKey, err = s.loadSecret(formKeyName); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.measureSiteEntries(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// storePerms lists the store paths that must be owner-only and the mode each
// must have. Paths that do not exist yet are skipped; they are created
// owner-only when first needed.
var storePerms = []struct {
	name string
	mode os.FileMode
}{
	{"", 0o700},
	{"blobs", 0o700},
	{sourceDirName, 0o700},
	{"sites", 0o700},
	{"meta.db", 0o600},
	{"meta.db-wal", 0o600},
	{"meta.db-shm", 0o600},
}

// tightenStorePerms sets the store root, its content directories and the
// metadata database files to exactly the modes in storePerms, logging each path
// it changes. Exact rather than masked, so a file left without its owner bits
// is repaired too instead of failing later.
func tightenStorePerms(dir string) error {
	for _, e := range storePerms {
		p := filepath.Join(dir, e.name)
		fi, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect %s: %w", p, err)
		}
		perm := fi.Mode().Perm()
		if perm == e.mode {
			continue
		}
		if err := os.Chmod(p, e.mode); err != nil {
			return fmt.Errorf("set mode of %s: %w", p, err)
		}
		log.Printf("store: set mode of %s from %04o to %04o", logsafe.Text(p), perm, e.mode)
	}
	return nil
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
		// Explicit series family (?series= at publish), replacing slug-prefix
		// inference (render-fidelity item 4).
		{"series", "ALTER TABLE artifacts ADD COLUMN series TEXT NOT NULL DEFAULT ''"},
		// Opaque render parameters for a retained source (source.go). Additive,
		// defaulting to '', so every pre-existing artifact reads back as "no
		// retained source" instead of needing a backfill.
		{"render_spec", "ALTER TABLE artifacts ADD COLUMN render_spec TEXT NOT NULL DEFAULT ''"},
		// Generations of the bytes and retained source files the row describes
		// (generation.go). '' is the pre-generation file name, so existing rows
		// keep pointing at their existing files.
		{"blob_gen", "ALTER TABLE artifacts ADD COLUMN blob_gen TEXT NOT NULL DEFAULT ''"},
		{"source_gen", "ALTER TABLE artifacts ADD COLUMN source_gen TEXT NOT NULL DEFAULT ''"},
		// When the bytes last changed (publish or rebake); 0 = not recorded.
		{"modified_at", "ALTER TABLE artifacts ADD COLUMN modified_at INTEGER NOT NULL DEFAULT 0"},
		// The publish that wrote the row (Artifact.PublishID).
		{"publish_id", "ALTER TABLE artifacts ADD COLUMN publish_id TEXT NOT NULL DEFAULT ''"},
	}
	for _, col := range added {
		if err := s.addColumnIfMissing(col.name, col.ddl); err != nil {
			return err
		}
	}
	// Rows written before publish identifiers get one each. Pages baked
	// before then carry no token bound to it and need a rebake to take
	// browser replies.
	if _, err := s.db.Exec(
		`UPDATE artifacts SET publish_id = lower(hex(randomblob(16))) WHERE publish_id = ''`,
	); err != nil {
		return fmt.Errorf("backfill publish ids: %w", err)
	}
	// Files whose row is gone (or no longer names them) but which may still be
	// on disk: recorded in the transaction that drops the reference, removed
	// afterwards, retried by the sweeper until the unlink succeeds. See
	// generation.go.
	if _, err := s.db.Exec(pendingDeletionsSchema); err != nil {
		return fmt.Errorf("create pending deletions table: %w", err)
	}
	if _, err := s.db.Exec(pendingWritesSchema); err != nil {
		return fmt.Errorf("create pending writes table: %w", err)
	}
	// Reply enablement, persisted per artifact so the reply module accepts
	// replies only where the publisher asked for them.
	hadReplies, err := s.hasColumn("replies")
	if err != nil {
		return err
	}
	if !hadReplies {
		if _, err := s.db.Exec(`ALTER TABLE artifacts ADD COLUMN replies INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add column replies: %w", err)
		}
		// Pages published with an inline reply box before the column existed
		// keep accepting answers. Their retained render spec names the slug
		// the box posts to; the key matches the server's renderSpec.ReplySlug
		// (pinned by a server test).
		if _, err := s.db.Exec(
			`UPDATE artifacts SET replies = 1 WHERE render_spec LIKE '%"reply_slug":"_%'`,
		); err != nil {
			return fmt.Errorf("backfill replies: %w", err)
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
	has, err := s.hasColumn(name)
	if err != nil || has {
		return err
	}
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("add column %s: %w", name, err)
	}
	return nil
}

// hasColumn reports whether the artifacts table has the named column.
func (s *Store) hasColumn(name string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(artifacts)`)
	if err != nil {
		return false, fmt.Errorf("inspect schema: %w", err)
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
			return false, fmt.Errorf("scan schema: %w", err)
		}
		if colName == name {
			return true, rows.Close()
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("inspect schema: %w", err)
	}
	return false, nil
}

// PutOptions carries the per-publish settings for Put. The zero value is a
// valid public, no-password, no-expiry, auto-slug publish.
type PutOptions struct {
	Slug     string        // named slug; "" auto-generates (friendly, or capability if Private)
	Filename string        // extension hint for content-type; "" falls back to sniffing
	Private  bool          // mark private + (when auto-slugging) use a high-entropy capability slug
	Password string        // plaintext view password; "" = no gate. Hashed before storage.
	TTL      time.Duration // 0 = never expires; >0 sets expires_at = now+TTL
	Series   string        // explicit series family (validated upstream); "" = none
	// Source is the ORIGINAL document the stored bytes were rendered from, kept
	// so the page can be rebaked with a later renderer (source.go). nil = nothing
	// to retain (the ordinary, non-rendered publish).
	Source []byte
	// RenderSpec is the opaque render-parameter blob that goes with Source. The
	// store persists it verbatim and never parses it.
	RenderSpec string
	// Replies enables replies for the artifact. Like the password and expiry,
	// it is replaced by every publish to the slug.
	Replies bool
	// PublishID is the new publish's identifier (Artifact.PublishID), for a
	// caller that needs it before the write, as a render baking it into the
	// page does. "" = Put generates one. Each publish needs a fresh one
	// (NewPublishID).
	PublishID string
}

// NewPublishID returns a fresh random publish identifier.
func NewPublishID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate publish id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// validPublishID reports whether id has NewPublishID's shape.
func validPublishID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
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
	if opts.TTL < 0 {
		// Zero is the only "never expires" value. A negative TTL is a caller
		// bug, and storing it as no expiry would fail open.
		return Artifact{}, &InputError{Msg: "ttl must not be negative"}
	}
	if err := validateMeta(opts); err != nil {
		return Artifact{}, err
	}
	publishID := opts.PublishID
	if publishID == "" {
		id, err := NewPublishID()
		if err != nil {
			return Artifact{}, err
		}
		publishID = id
	} else if !validPublishID(publishID) {
		return Artifact{}, &InputError{Msg: "malformed publish id"}
	}
	slug, _, err := s.resolveSlug(opts.Slug, opts.Private)
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

	// The bytes and source go to files of a fresh generation, which nothing
	// references until the row is committed below, so a reader keeps getting
	// the previous publish (bytes and metadata together) until then.
	gen, err := newGen()
	if err != nil {
		return Artifact{}, err
	}
	// The write intent comes before any file of this generation exists, so
	// Reconcile can tell a write in flight from an orphan (generation.go).
	if err := s.beginWrite(slug, gen); err != nil {
		return Artifact{}, err
	}
	sniffed, size, err := s.writeBlob(s.blobFile(slug, gen), r)
	if err != nil {
		s.abortWrite(slug, gen, noSourceGen)
		return Artifact{}, err
	}

	// Source retention. A publish that carries a source retains it at the new
	// generation; one that does not records that there is none, and the
	// replaced generation's sidecar is removed with its bytes, so stale source
	// can never describe bytes that no longer exist.
	sourceGen := noSourceGen
	if opts.Source != nil {
		sourceGen = gen
		if err := s.writeSource(s.sourceFile(slug, sourceGen), opts.Source); err != nil {
			s.abortWrite(slug, gen, sourceGen)
			return Artifact{}, err
		}
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
		Series:       opts.Series,
		RenderSpec:   opts.RenderSpec,
		ModifiedAt:   now,
		Version:      gen,
		Replies:      opts.Replies,
		PublishID:    publishID,
	}

	if beforeMetaCommit != nil {
		beforeMetaCommit(slug)
	}
	unlock := s.locks.lock(slug)
	defer unlock()
	prior, err := s.swapMeta(&art, sourceGen)
	if err != nil {
		s.abortWrite(slug, gen, sourceGen)
		return Artifact{}, err
	}
	if prior.exists {
		s.dropReplacedGen(slug, prior.blobGen, prior.sourceGen)
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

// writeBlob streams r to final (a file in blobDir) atomically (temp file +
// rename) and returns the sniffed content-type and total byte count. Only the
// first sniffLen bytes are held in memory; the rest is copied straight through.
func (s *Store) writeBlob(final string, r io.Reader) (sniffed string, size int64, err error) {
	tmp, err := os.CreateTemp(s.blobDir, tempPattern(final))
	if err != nil {
		return "", 0, fmt.Errorf("create temp blob: %w", err)
	}
	tmpName := tmp.Name()
	// Every byte written is charged against the store quota first (quota.go).
	cw := &chargedWriter{s: s, w: tmp}
	defer func() {
		// Remove the temp file if we did not successfully rename it away, and
		// give back what it was charged.
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
			s.release(cw.charged)
		}
	}()

	head := make([]byte, sniffLen)
	n, rerr := io.ReadFull(r, head)
	if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
		return "", 0, fmt.Errorf("read body: %w", rerr)
	}
	head = head[:n]
	sniffed = http.DetectContentType(head)

	if _, werr := cw.Write(head); werr != nil {
		return "", 0, fmt.Errorf("write blob head: %w", werr)
	}
	copied, cerr := io.Copy(cw, r)
	if cerr != nil {
		return "", 0, fmt.Errorf("stream blob: %w", cerr)
	}
	size = int64(n) + copied

	if cerr := tmp.Close(); cerr != nil {
		err = fmt.Errorf("close blob: %w", cerr)
		return "", 0, err
	}
	if rerr := s.commitRename(tmpName, final, cw.charged); rerr != nil {
		err = fmt.Errorf("commit blob: %w", rerr)
		return "", 0, err
	}
	return sniffed, size, nil
}

// priorGen is what a row named before swapMeta replaced it.
type priorGen struct {
	exists             bool
	blobGen, sourceGen string
}

// swapMeta inserts or, for an existing (named) slug, overwrites the metadata
// row in place, pointing it at a.Version's bytes and sourceGen's source. The
// stored modified time is a.ModifiedAt, the wall-clock time of the write: never
// later than now, so Last-Modified stays a valid HTTP date. Two writes in one
// second share it; the version (the ETag) is what tells them apart. Owner
// is preserved on overwrite; privacy, password, and expiry are replaced with the
// new publish's values (re-publishing resets them). It returns the generations
// the row named before, read in the same transaction, so the caller removes
// exactly the files this write replaced.
func (s *Store) swapMeta(a *Artifact, sourceGen string) (priorGen, error) {
	const q = `
INSERT INTO artifacts (slug, filename, content_type, size, created_at, owner, private, password_hash, expires_at, series, render_spec, blob_gen, source_gen, modified_at, replies, publish_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
    expires_at    = excluded.expires_at,
    series        = excluded.series,
    -- Follows the bytes: a re-publish with no retained source clears the spec
    -- along with the sidecar file Put just removed.
    render_spec   = excluded.render_spec,
    blob_gen      = excluded.blob_gen,
    source_gen    = excluded.source_gen,
    modified_at   = excluded.modified_at,
    replies       = excluded.replies,
    publish_id    = excluded.publish_id
RETURNING modified_at;`
	tx, err := s.db.Begin()
	if err != nil {
		return priorGen{}, fmt.Errorf("upsert metadata: %w", err)
	}
	defer tx.Rollback()
	// Retire the write intent in the commit itself. If Reconcile reclaimed
	// it as abandoned, its files may be gone: fail rather than commit a row
	// naming missing bytes.
	if err := endWrite(tx, a.Slug, a.Version); err != nil {
		return priorGen{}, err
	}
	var p priorGen
	err = tx.QueryRow(`SELECT blob_gen, source_gen FROM artifacts WHERE slug = ?`, a.Slug).Scan(&p.blobGen, &p.sourceGen)
	switch {
	case err == nil:
		p.exists = true
	case errors.Is(err, sql.ErrNoRows):
	default:
		return priorGen{}, fmt.Errorf("upsert metadata: %w", err)
	}
	if p.exists {
		// Recorded before the row stops naming these files, in the same
		// transaction, so a failed unlink after the commit is retried.
		if err := queueDeletion(tx, a.Slug, p.blobGen, p.sourceGen); err != nil {
			return priorGen{}, fmt.Errorf("upsert metadata: %w", err)
		}
	}
	// A new row takes its metadata allowance from the quota (quota.go). The
	// transaction holds the write lock, so whether the row exists cannot
	// change before the commit.
	var charged int64
	if !p.exists {
		if err := s.charge(metaAllowance); err != nil {
			return priorGen{}, err
		}
		charged = metaAllowance
	}
	committed := false
	defer func() {
		if !committed {
			s.release(charged)
		}
	}()
	var modified int64
	if err := tx.QueryRow(q,
		a.Slug, a.Filename, a.ContentType, a.Size, a.CreatedAt.Unix(), a.Owner,
		boolToInt(a.Private), a.PasswordHash, unixOrZero(a.ExpiresAt), a.Series, a.RenderSpec,
		a.Version, sourceGen, unixOrZero(a.ModifiedAt), boolToInt(a.Replies), a.PublishID,
	).Scan(&modified); err != nil {
		return priorGen{}, fmt.Errorf("upsert metadata: %w", err)
	}
	s.beginTransition()
	if err := tx.Commit(); err != nil {
		s.endTransition(0, 0)
		return priorGen{}, fmt.Errorf("upsert metadata: %w", err)
	}
	committed = true
	s.endTransition(charged, 0)
	a.ModifiedAt = time.Unix(modified, 0).UTC()
	return p, nil
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

// getAttempts bounds how often Get re-reads a row whose bytes file was
// replaced between the row read and the open.
const getAttempts = 3

// Get returns an artifact's metadata and an open reader for its bytes. The
// caller must Close the reader. A past-TTL artifact is treated as not found and
// is deleted lazily (the background sweeper is the primary reclaimer; this just
// guarantees an expired artifact is never served even between sweeps).
//
// The reader is always the bytes file the returned metadata names, so bytes
// from one publish are never paired with another publish's password, privacy
// or expiry.
func (s *Store) Get(slug string) (Artifact, *os.File, error) {
	for attempt := 1; ; attempt++ {
		a, err := s.lookup(slug)
		if err != nil {
			return Artifact{}, nil, err
		}
		if !a.ExpiresAt.IsZero() && !a.ExpiresAt.After(time.Now()) {
			// Predicate-guarded reclaim: if a concurrent re-publish renewed the
			// TTL between this read and now, reapIfExpired is a no-op and the
			// renewed artifact survives (it still 404s for THIS request).
			_, _ = s.reapIfExpired(slug, time.Now())
			return Artifact{}, nil, ErrNotFound
		}
		f, err := os.Open(s.blobFile(slug, a.Version))
		if err == nil {
			return a, f, nil
		}
		if !os.IsNotExist(err) {
			return Artifact{}, nil, fmt.Errorf("open blob: %w", err)
		}
		// A write or delete committed between the row read and the open and
		// removed the generation that row named. Read the row again.
		if attempt == getAttempts {
			// Metadata without bytes is a corrupt store, not a normal 404.
			return Artifact{}, nil, errors.New("blob missing for artifact")
		}
	}
}

// lookup reads slug's metadata row.
func (s *Store) lookup(slug string) (Artifact, error) {
	var (
		a        Artifact
		created  int64
		expires  int64
		modified int64
		private  int
		replies  int
	)
	err := s.db.QueryRow(
		`SELECT slug, filename, content_type, size, created_at, owner, private, password_hash, expires_at, series, render_spec, blob_gen, modified_at, replies, publish_id
		   FROM artifacts WHERE slug = ?`,
		slug,
	).Scan(&a.Slug, &a.Filename, &a.ContentType, &a.Size, &created, &a.Owner, &private, &a.PasswordHash, &expires, &a.Series, &a.RenderSpec, &a.Version, &modified, &replies, &a.PublishID)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, ErrNotFound
	}
	if err != nil {
		return Artifact{}, fmt.Errorf("lookup metadata: %w", err)
	}
	a.CreatedAt = time.Unix(created, 0).UTC()
	a.Private = private != 0
	a.Replies = replies != 0
	if expires > 0 {
		a.ExpiresAt = time.Unix(expires, 0).UTC()
	}
	if modified > 0 {
		a.ModifiedAt = time.Unix(modified, 0).UTC()
	}
	return a, nil
}

// LookupPublic returns the metadata of a currently VISIBLE artifact (it
// exists, is not private and has not expired) without opening its bytes, or
// ErrNotFound. Like HasPublic, it never distinguishes a private or expired
// artifact from a missing one.
func (s *Store) LookupPublic(slug string) (Artifact, error) {
	a, err := s.lookup(slug)
	if err != nil {
		return Artifact{}, err
	}
	if a.Private || (!a.ExpiresAt.IsZero() && !a.ExpiresAt.After(time.Now())) {
		return Artifact{}, ErrNotFound
	}
	return a, nil
}

// Delete removes an artifact's metadata row, its bytes and its retained source.
// It returns ErrNotFound if no such slug exists. The row is removed first, so a
// concurrent Get either sees the whole artifact or none of it.
//
// The files to remove are recorded as pending deletions in the transaction
// that removes the row. Once that commits the artifact is deleted: it is no
// longer served or listed, and a file that cannot be unlinked right now (a
// read-only or failing disk) is logged and retried by the sweeper rather than
// orphaned, so a confidential page's bytes and source are always reclaimed.
func (s *Store) Delete(slug string) error {
	unlock := s.locks.lock(slug)
	defer unlock()
	blobGen, sourceGen, err := s.deleteRow(slug,
		`DELETE FROM artifacts WHERE slug = ? RETURNING blob_gen, source_gen`, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete metadata: %w", err)
	}
	// The retained source goes with it: a deleted artifact must not leave its
	// plaintext behind on disk.
	s.finishDeletion(slug, blobGen, sourceGen)
	return nil
}

// deleteRow runs a DELETE ... RETURNING blob_gen, source_gen for slug's row
// and records the returned generations as a pending deletion in the same
// transaction. It returns sql.ErrNoRows when nothing matched.
func (s *Store) deleteRow(slug, query string, args ...any) (blobGen, sourceGen string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	if err := tx.QueryRow(query, args...).Scan(&blobGen, &sourceGen); err != nil {
		return "", "", err
	}
	if err := queueDeletion(tx, slug, blobGen, sourceGen); err != nil {
		return "", "", err
	}
	// The row's metadata allowance leaves the quota with the row (quota.go).
	s.beginTransition()
	if err := tx.Commit(); err != nil {
		s.endTransition(0, 0)
		return "", "", err
	}
	s.endTransition(0, metaAllowance)
	return blobGen, sourceGen, nil
}

// SeriesSlugs returns, in slug order, the slugs of owner's public, unexpired
// artifacts in series other than except, at most limit of them. Callers pass
// one more than the most they will use, to tell a longer series apart.
func (s *Store) SeriesSlugs(owner, series, except string, limit int) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT slug FROM artifacts
		  WHERE owner = ? AND private = 0 AND (expires_at = 0 OR expires_at > ?)
		    AND series = ? AND slug != ?
		  ORDER BY slug ASC LIMIT ?`,
		owner, time.Now().Unix(), series, except, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list series: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, fmt.Errorf("scan series: %w", err)
		}
		out = append(out, slug)
	}
	return out, rows.Err()
}

// List returns owner's artifacts, newest first, excluding any already past TTL
// (which the sweeper will reap). In v1 there is a single local owner; the
// parameter keeps per-owner listing additive later.
func (s *Store) List(owner string) ([]Artifact, error) {
	now := time.Now().Unix()
	rows, err := s.db.Query(
		`SELECT slug, filename, content_type, size, created_at, owner, private, password_hash, expires_at, series, render_spec
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
		if err := rows.Scan(&a.Slug, &a.Filename, &a.ContentType, &a.Size, &created, &a.Owner, &private, &a.PasswordHash, &expires, &a.Series, &a.RenderSpec); err != nil {
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
			return n, fmt.Errorf("delete expired %s: %w", logsafe.Slug(slug), err)
		}
		if reaped {
			n++
		}
	}
	// Finish any deletion whose unlink failed earlier (this pass's included).
	if err := s.retryPendingDeletions(); err != nil {
		return n, err
	}
	return n, nil
}

// reapIfExpired deletes slug only if it is still past-TTL as of now, removing
// the blob only when the row was actually deleted. The DELETE re-checks the
// expiry predicate atomically, so a concurrent re-publish that renewed or
// cleared the TTL leaves the artifact intact (RowsAffected == 0). Returns
// whether an artifact was reaped.
func (s *Store) reapIfExpired(slug string, now time.Time) (bool, error) {
	unlock := s.locks.lock(slug)
	defer unlock()
	blobGen, sourceGen, err := s.deleteRow(slug,
		`DELETE FROM artifacts WHERE slug = ? AND expires_at > 0 AND expires_at <= ? RETURNING blob_gen, source_gen`,
		slug, now.Unix())
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reap expired: %w", err)
	}
	// An expired artifact's retained source expires with it. Only the
	// generation the deleted row named is removed, never a newer publish's.
	// A failed unlink stays pending and is retried (see Delete).
	s.finishDeletion(slug, blobGen, sourceGen)
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
