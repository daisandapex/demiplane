// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package store is demiplane's content store: flat files on disk for artifact
// bytes plus a SQLite table for metadata. It owns slug resolution, streaming
// writes (never buffering a whole upload in memory), content-type inference,
// and overwrite-in-place semantics for named slugs.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
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
	// rowsExamined counts the index entries the listing reads have examined
	// (tests).
	rowsExamined atomic.Int64
	// quota is the optional aggregate storage accounting (quota.go).
	quota quotaState
	// open counts readers of open store files (readers.go).
	open openFiles
	// deferDeletions leaves removed generations' files to the serving
	// process (DeferDeletions).
	deferDeletions bool
	// shared marks a store opened by a process other than the server
	// (OpenShared): it leaves sites/ alone.
	shared bool
	// formKey signs reply-form submission tokens (secret.go).
	formKey []byte
}

// Open opens (creating if needed) a store rooted at dir. The store root, its
// content directories and the metadata database are created owner-only, and an
// existing store with group or world bits on any of them is tightened (and the
// change logged): the database holds capability slugs and password hashes, and
// the blobs and sources hold the bytes of password-gated artifacts.
func Open(dir string) (*Store, error) {
	return open(dir, false)
}

// open is Open, or OpenShared with shared set.
func open(dir string, shared bool) (*Store, error) {
	blobDir := filepath.Join(dir, contentDirName, "blobs")
	sourceDir := filepath.Join(dir, contentDirName, sourceDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	// Content directories move under data/ and the old names become marker
	// files that stop older binaries at open (layout.go).
	if err := migrateLayout(dir); err != nil {
		return nil, err
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
	s := &Store{root: dir, blobDir: blobDir, sourceDir: sourceDir, db: db, shared: shared, deferDeletions: shared}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if s.formKey, err = s.loadSecret(formKeyName); err != nil {
		db.Close()
		return nil, err
	}
	// A site replace a crash interrupted is finished or rolled back first,
	// so its trees are never mistaken for leftovers (siteswap.go). Staging
	// trees a crash left behind go next; younger ones are counted below and
	// reclaimed by a later Reconcile.
	if err := s.recoverSiteSwaps(); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := s.reclaimStagingTrees(time.Now()); err != nil {
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
	{contentDirName, 0o700},
	{filepath.Join(contentDirName, "blobs"), 0o700},
	{filepath.Join(contentDirName, sourceDirName), 0o700},
	{filepath.Join(contentDirName, "sites"), 0o700},
	// The marker files at the old names (layout.go).
	{"blobs", markerMode},
	{sourceDirName, markerMode},
	{"sites", markerMode},
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
		// A fresh value from every write of the row by this version or later
		// (Put and Rebake), which writers from before it cannot set; the
		// triggers below refuse their writes (writerTriggers).
		{"writer_tag", "ALTER TABLE artifacts ADD COLUMN writer_tag TEXT NOT NULL DEFAULT ''"},
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
	if err := s.migrateSiteSwaps(); err != nil {
		return err
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
	// Index the listings: public artifacts in List order (ListPage,
	// NewestPerFamily) and by series and slug (SeriesSlugs, the series
	// counts). Each listing query seeks on (owner, private) and reads in index
	// order, so it neither sorts the table nor walks private rows. Earlier
	// shapes of these indexes are dropped.
	for _, q := range []string{
		`DROP INDEX IF EXISTS idx_artifacts_list`,
		`DROP INDEX IF EXISTS idx_artifacts_series`,
		`CREATE INDEX IF NOT EXISTS idx_artifacts_public_order ON artifacts(owner, private, created_at DESC, slug)`,
		`CREATE INDEX IF NOT EXISTS idx_artifacts_public_series ON artifacts(owner, private, series, slug)`,
		// SweepPage's walk of everything stored, private included, in rowid
		// order (an index on owner alone keeps rowid order within an owner).
		`DROP INDEX IF EXISTS idx_artifacts_owner_slug`,
		`CREATE INDEX IF NOT EXISTS idx_artifacts_owner ON artifacts(owner)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("create listing indexes: %w", err)
		}
	}
	for _, q := range writerTriggers {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("create writer triggers: %w", err)
		}
	}
	return s.stampSchemaVersion()
}

// writerTriggers make the store refuse writes from demiplane binaries older
// than this schema, which share the database file when not every writer was
// upgraded together (an SSH forced-command receive, say). Their writes do not
// know the replies and publish_id columns, so they would republish a page
// with replies still on and every reply token still valid. Every current
// write of a row sets writer_tag afresh, which older writers cannot:
//   - an INSERT without it is refused. An upsert fires the INSERT trigger
//     before resolving the conflict, so an older republish is refused too;
//   - an UPDATE that replaces the bytes (blob_gen) without a new writer_tag,
//     which is an older rebake, is refused.
//
// Other updates (expiry, migrations) are unaffected.
var writerTriggers = []string{
	`CREATE TRIGGER IF NOT EXISTS artifacts_writer_insert BEFORE INSERT ON artifacts
	  WHEN NEW.writer_tag = ''
	  BEGIN SELECT RAISE(ABORT, 'demiplane: this store has been upgraded; this demiplane binary is too old to write to it'); END`,
	`CREATE TRIGGER IF NOT EXISTS artifacts_writer_update BEFORE UPDATE OF blob_gen ON artifacts
	  WHEN NEW.blob_gen IS NOT OLD.blob_gen AND NEW.writer_tag IS OLD.writer_tag
	  BEGIN SELECT RAISE(ABORT, 'demiplane: this store has been upgraded; this demiplane binary is too old to write to it'); END`,
}

// storeSchemaVersion is recorded in the database's user_version. A binary
// refuses a store stamped with a later version than it knows, so a future
// incompatible schema can lock out writers from before it the same way.
const storeSchemaVersion = 1

func (s *Store) stampSchemaVersion() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	switch {
	case v > storeSchemaVersion:
		return fmt.Errorf("this store was written by a newer demiplane (schema %d, this binary knows %d); upgrade this binary", v, storeSchemaVersion)
	case v < storeSchemaVersion:
		if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, storeSchemaVersion)); err != nil {
			return fmt.Errorf("record schema version: %w", err)
		}
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
	Slug     string // named slug; "" auto-generates (friendly, or capability if Private)
	Filename string // extension hint for content-type; "" falls back to sniffing
	Private  bool   // mark private + (when auto-slugging) use a high-entropy capability slug
	Password string // plaintext view password; "" = no gate. Hashed before storage.
	// PasswordHash is a view password already hashed with HashPassword, stored
	// as is. Set at most one of Password and PasswordHash. A server hashes
	// under its KDF concurrency limit and passes the hash here.
	PasswordHash string
	TTL          time.Duration // 0 = never expires; >0 sets expires_at = now+TTL
	Series       string        // explicit series family (validated upstream); "" = none
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
	named := opts.Slug != ""
	slug, err := s.resolveNamedSlug(opts.Slug, opts.Private)
	if err != nil {
		return Artifact{}, err
	}

	passwordHash := opts.PasswordHash
	switch {
	case opts.Password != "" && opts.PasswordHash != "":
		return Artifact{}, &InputError{Msg: "set a password or a password hash, not both"}
	case opts.PasswordHash != "" && !validPasswordHash(opts.PasswordHash):
		return Artifact{}, &InputError{Msg: "malformed password hash"}
	case opts.Password != "":
		passwordHash, err = newHash(opts.Password)
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
	// Reconcile can tell a write in flight from an orphan (generation.go). A
	// generated slug is drawn and reserved by the same intent, so no other
	// generated publish, in this process or another, can draw it meanwhile.
	if named {
		err = s.beginWrite(slug, gen)
	} else {
		slug, err = s.reserveGeneratedSlug(opts.Private, gen)
	}
	if err != nil {
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
	defaultName := filename == ""
	if defaultName {
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
	// Only a named publish replaces an existing artifact. A generated one
	// commits only as a new row; if a named publish took its slug after it
	// was reserved, the write moves to a fresh slug and commits there.
	for attempt := 0; ; attempt++ {
		unlock := s.locks.lock(slug)
		prior, err := s.swapMeta(&art, sourceGen, !named)
		if errors.Is(err, errSlugTaken) && attempt < genAttempts {
			unlock()
			moved, merr := s.moveGeneratedWrite(slug, gen, sourceGen, opts.Private)
			if merr != nil {
				s.abortWrite(slug, gen, sourceGen)
				return Artifact{}, merr
			}
			slug, art.Slug = moved, moved
			if defaultName {
				art.Filename = moved
			}
			continue
		}
		if err != nil {
			unlock()
			s.abortWrite(slug, gen, sourceGen)
			if errors.Is(err, errSlugTaken) {
				return Artifact{}, errNoUnusedSlug
			}
			return Artifact{}, err
		}
		if prior.exists {
			s.dropReplacedGen(slug, prior.blobGen, prior.sourceGen)
		}
		// Fire the publish notification (guarded no-op when no notifier is set) so a
		// live-preview tab on this slug can reload. Emitted only after the metadata
		// row commits, so a subscriber that reacts sees the new bytes.
		s.notify(art.Slug)
		unlock()
		return art, nil
	}
}

// ErrPrivateNamedSlug rejects the incoherent private + named-slug combination.
// Privacy's protection is the unguessable capability slug; a named slug is
// guessable, so the combination would yield a "private" artifact with no real
// protection. Enforced here so every ingest path (HTTP and SSH) is covered.
// It is an *InputError so the HTTP layer maps it to 400.
var ErrPrivateNamedSlug = &InputError{Msg: "private artifacts cannot use a named slug"}

// resolveNamedSlug validates a named slug, which may replace an existing
// artifact. It returns "" for a generated publish, whose slug is drawn and
// reserved later (reserveGeneratedSlug).
func (s *Store) resolveNamedSlug(named string, private bool) (string, error) {
	if named == "" {
		return "", nil
	}
	if private {
		return "", ErrPrivateNamedSlug
	}
	if err := ValidateNamedSlug(named); err != nil {
		return "", err
	}
	return named, nil
}

// errSlugTaken is swapMeta's refusal to commit a generated publish over an
// existing row.
var errSlugTaken = errors.New("slug already in use")

// errNoUnusedSlug: no unused slug could be drawn and committed.
var errNoUnusedSlug = errors.New("could not generate an unused slug after retries")

// reserveGeneratedSlug draws a slug for a generated publish and reserves it
// with the write intent for gen (beginWrite's record). Friendly slugs are
// tried first, then friendly slugs with a numeric suffix once the plain space
// is crowded; private publishes draw capability slugs. A candidate is taken
// only if no artifact and no write in flight uses it, checked and recorded
// in one transaction, which holds the database's write lock, so two
// publishes, in one process or two, never reserve the same slug.
func (s *Store) reserveGeneratedSlug(private bool, gen string) (string, error) {
	attempts := 2 * genAttempts
	if private {
		attempts = genAttempts
	}
	for i := 0; i < attempts; i++ {
		var candidate string
		var err error
		switch {
		case private:
			candidate, err = GenerateCapabilitySlug()
		case i < genAttempts:
			candidate, err = GenerateFriendlySlug()
		default:
			candidate, err = generateFriendlySlugSuffixed()
		}
		if err != nil {
			return "", err
		}
		ok, err := s.reserveSlug(candidate, gen)
		if err != nil {
			return "", err
		}
		if ok {
			return candidate, nil
		}
	}
	return "", errNoUnusedSlug
}

// reserveSlug records the write intent for slug at gen if no artifact and no
// other write in flight uses slug, reporting whether it did.
func (s *Store) reserveSlug(slug, gen string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("reserve slug: %w", err)
	}
	defer tx.Rollback()
	var taken bool
	if err := tx.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM artifacts WHERE slug = ?) OR EXISTS(SELECT 1 FROM pending_writes WHERE slug = ?)`,
		slug, slug,
	).Scan(&taken); err != nil {
		return false, fmt.Errorf("reserve slug: %w", err)
	}
	if taken {
		return false, nil
	}
	if _, err := tx.Exec(
		`INSERT INTO pending_writes (slug, gen, started_at) VALUES (?, ?, ?)`,
		slug, gen, time.Now().Unix()); err != nil {
		return false, fmt.Errorf("record write intent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("reserve slug: %w", err)
	}
	return true, nil
}

// renameCounted renames a counted blob or source file and moves its quota
// ledger entry (ledger.go) with it, so removing the file under its new name
// releases what was charged for it. The rename is a quota transition: a
// recount whose walk overlaps it is not applied, since it may have seen the
// file under its old name, or missed it between listing and measuring it.
func (s *Store) renameCounted(from, to string) error {
	s.beginTransition()
	err := os.Rename(from, to)
	if err == nil {
		s.moveCounted(from, to)
	}
	s.endTransition(0, 0)
	return err
}

// beforeGenRename, when set by a test, runs in moveGeneratedWrite before it
// renames the blob (0) and the source (1).
var beforeGenRename func(file int)

// moveGeneratedWrite moves a generated publish's uncommitted generation from
// slug to a freshly reserved one: the new slug's intent is recorded first,
// then the files are renamed, then the old intent is dropped, so at every
// step each file is covered by an intent (Reconcile reclaims stale ones). It
// returns the new slug. On failure the files are back under slug and only
// its intent remains.
func (s *Store) moveGeneratedWrite(slug, gen, sourceGen string, private bool) (string, error) {
	moved, err := s.reserveGeneratedSlug(private, gen)
	if err != nil {
		return "", err
	}
	unreserve := func() {
		_, _ = s.db.Exec(`DELETE FROM pending_writes WHERE slug = ? AND gen = ?`, moved, gen)
	}
	if beforeGenRename != nil {
		beforeGenRename(0)
	}
	if err := s.renameCounted(s.blobFile(slug, gen), s.blobFile(moved, gen)); err != nil {
		unreserve()
		return "", fmt.Errorf("move generated write: %w", err)
	}
	if sourceGen != noSourceGen {
		if beforeGenRename != nil {
			beforeGenRename(1)
		}
		if err := s.renameCounted(s.sourceFile(slug, sourceGen), s.sourceFile(moved, sourceGen)); err != nil {
			_ = s.renameCounted(s.blobFile(moved, gen), s.blobFile(slug, gen))
			unreserve()
			return "", fmt.Errorf("move generated write: %w", err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM pending_writes WHERE slug = ? AND gen = ?`, slug, gen); err != nil {
		log.Printf("store: could not clear a moved write intent: %s", logsafe.Err(err, slug))
	}
	return moved, nil
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
	tmp, err := s.createTemp(s.blobDir, tempPattern(final))
	if err != nil {
		return "", 0, fmt.Errorf("create temp blob: %w", err)
	}
	tmpName := tmp.Name()
	// Every byte written is charged against the store quota first (quota.go).
	cw := &chargedWriter{s: s, w: tmp}
	defer func() {
		// Remove the temp file if we did not successfully rename it away, and
		// give back what it was charged (or, if it cannot be removed, leave it
		// charged for Reconcile).
		if err != nil {
			tmp.Close()
			s.discardTemp(tmpName, cw.charged)
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
// exactly the files this write replaced. With exclusive set (a generated
// publish) it refuses with errSlugTaken when the row already exists.
func (s *Store) swapMeta(a *Artifact, sourceGen string, exclusive bool) (priorGen, error) {
	const q = `
INSERT INTO artifacts (slug, filename, content_type, size, created_at, owner, private, password_hash, expires_at, series, render_spec, blob_gen, source_gen, modified_at, replies, publish_id, writer_tag)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, lower(hex(randomblob(8))))
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
    publish_id    = excluded.publish_id,
    writer_tag    = excluded.writer_tag
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
	if exclusive && p.exists {
		// A generated publish never replaces a row; rolling back keeps its
		// write intent.
		return priorGen{}, errSlugTaken
	}
	// A new row takes its metadata allowance from the quota (quota.go), and
	// so does the pending deletion a replaced row leaves. The transaction
	// holds the write lock, so whether the row exists cannot change before
	// the commit.
	var (
		charged int64
		queued  bool
	)
	if p.exists {
		// Recorded before the row stops naming these files, in the same
		// transaction, so a failed unlink after the commit is retried.
		queued, err = queueDeletion(tx, a.Slug, p.blobGen, p.sourceGen, true)
		if err != nil {
			return priorGen{}, fmt.Errorf("upsert metadata: %w", err)
		}
		if queued {
			if err := s.chargeQueued(); err != nil {
				return priorGen{}, err
			}
			charged = metaAllowance
		}
	} else {
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
	// What was charged is now counted (ledger.go).
	if queued {
		s.countPending(pendingKey{a.Slug, p.blobGen, p.sourceGen})
	} else if !p.exists {
		s.countRow(a.Slug)
	}
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
func (s *Store) Get(slug string) (Artifact, *File, error) {
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
		f, _, err := s.openTracked(func() (*os.File, os.FileInfo, error) {
			f, err := os.Open(s.blobFile(slug, a.Version))
			if err != nil {
				return nil, nil, err
			}
			fi, err := f.Stat()
			if err != nil {
				f.Close()
				return nil, nil, err
			}
			return f, fi, nil
		})
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
	// A delete is never refused for the cap: it is how space comes back.
	queued, err := queueDeletion(tx, slug, blobGen, sourceGen, false)
	if err != nil {
		return "", "", err
	}
	s.beginTransition()
	if err := tx.Commit(); err != nil {
		s.endTransition(0, 0)
		return "", "", err
	}
	// The row's metadata allowance leaves the quota with the row (quota.go),
	// or passes to the pending deletion that now stands for its files; either
	// way only if this process counted the row (ledger.go).
	var released int64
	if s.uncountRow(slug) {
		if queued {
			s.countPending(pendingKey{slug, blobGen, sourceGen})
		} else {
			released = metaAllowance
		}
	}
	s.endTransition(0, released)
	return blobGen, sourceGen, nil
}

// ExamineSlack is how many entries past their TTL but not yet swept a
// listing read steps over beyond the rows it returns. The listing queries do
// not filter expiry in SQL, where it would apply before LIMIT: they read a
// bounded number of index entries and skip the expired ones in Go, so a
// large unswept batch cannot make one request walk all of it.
const ExamineSlack = 1000

// ErrListingTruncated reports that a listing read examined its whole budget
// of index entries before finding what it was asked for.
var ErrListingTruncated = errors.New("listing truncated: too many expired entries")

// expired reports whether an expires_at value is past as of now.
func expired(expires, now int64) bool { return expires != 0 && expires <= now }

const qSeriesSlugs = `SELECT slug, expires_at FROM artifacts
  WHERE owner = ? AND private = 0 AND series = ? AND slug != ?
  ORDER BY slug ASC LIMIT ?`

// SeriesSlugs returns, in slug order, the slugs of owner's public, unexpired
// artifacts in series other than except, at most limit of them. Callers pass
// one more than the most they will use, to tell a longer series apart. It
// examines at most limit+ExamineSlack entries and returns ErrListingTruncated
// (with what it found) if they run out first.
func (s *Store) SeriesSlugs(ctx context.Context, owner, series, except string, limit int) ([]string, error) {
	budget := limit + ExamineSlack
	rows, err := s.db.QueryContext(ctx, qSeriesSlugs, owner, series, except, budget)
	if err != nil {
		return nil, fmt.Errorf("list series: %w", err)
	}
	defer rows.Close()
	now := time.Now().Unix()
	var out []string
	examined := 0
	for len(out) < limit && rows.Next() {
		var slug string
		var expires int64
		if err := rows.Scan(&slug, &expires); err != nil {
			return nil, fmt.Errorf("scan series: %w", err)
		}
		examined++
		s.rowsExamined.Add(1)
		if !expired(expires, now) {
			out = append(out, slug)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) < limit && examined == budget {
		return out, ErrListingTruncated
	}
	return out, nil
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
	return scanArtifacts(rows)
}

// scanArtifact reads one row of List's columns.
func scanArtifact(rows *sql.Rows) (Artifact, error) {
	var (
		a       Artifact
		created int64
		expires int64
		private int
	)
	if err := rows.Scan(&a.Slug, &a.Filename, &a.ContentType, &a.Size, &created, &a.Owner, &private, &a.PasswordHash, &expires, &a.Series, &a.RenderSpec); err != nil {
		return Artifact{}, fmt.Errorf("scan artifact: %w", err)
	}
	a.CreatedAt = time.Unix(created, 0).UTC()
	a.Private = private != 0
	if expires > 0 {
		a.ExpiresAt = time.Unix(expires, 0).UTC()
	}
	return a, nil
}

// scanArtifacts reads List's columns from rows.
func scanArtifacts(rows *sql.Rows) ([]Artifact, error) {
	var out []Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifacts: %w", err)
	}
	return out, nil
}

// ListCursor marks a position in List order (newest first, then slug): the
// last artifact of a page. Its String form is "<created unix>.<slug>".
type ListCursor struct {
	Created int64
	Slug    string
}

func (c ListCursor) String() string { return strconv.FormatInt(c.Created, 10) + "." + c.Slug }

// ParseListCursor parses a ListCursor's String form.
func ParseListCursor(v string) (ListCursor, error) {
	ts, slug, ok := strings.Cut(v, ".")
	created, err := strconv.ParseInt(ts, 10, 64)
	if !ok || err != nil || created < 0 || slug == "" || len(slug) > 255 {
		return ListCursor{}, fmt.Errorf("bad list cursor")
	}
	return ListCursor{Created: created, Slug: slug}, nil
}

// The two halves of a ListPage read after a cursor: the rest of the
// cursor's creation second (slugs after the cursor's), then older artifacts.
// Each is a seek and an in-order walk of idx_artifacts_public_order. Expiry
// is checked in Go (ExamineSlack).
const (
	qListTies = `SELECT slug, filename, content_type, size, created_at, owner, private, password_hash, expires_at, series, render_spec
   FROM artifacts
  WHERE owner = ? AND private = 0 AND created_at = ? AND slug > ?
  ORDER BY created_at DESC, slug ASC
  LIMIT ?`
	qListOlder = `SELECT slug, filename, content_type, size, created_at, owner, private, password_hash, expires_at, series, render_spec
   FROM artifacts
  WHERE owner = ? AND private = 0 AND created_at < ?
  ORDER BY created_at DESC, slug ASC
  LIMIT ?`
)

// ListPage returns up to limit of owner's unexpired artifacts in List order,
// starting after the cursor (nil = from the newest), and the cursor for the
// next page (nil when this page is the last). It examines at most
// limit+1+ExamineSlack index entries. If they run out first, the page holds
// fewer than limit artifacts (possibly none) and next points past the last
// entry examined, so paging continues.
func (s *Store) ListPage(ctx context.Context, owner string, after *ListCursor, limit int) ([]Artifact, *ListCursor, error) {
	if limit < 1 {
		limit = 1
	}
	now := time.Now().Unix()
	budget := limit + 1 + ExamineSlack
	examined := 0
	var out []Artifact
	var last *ListCursor // the last entry examined
	read := func(q string, args ...any) error {
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("list artifacts: %w", err)
		}
		defer rows.Close()
		for len(out) <= limit && rows.Next() {
			a, err := scanArtifact(rows)
			if err != nil {
				return err
			}
			examined++
			s.rowsExamined.Add(1)
			last = &ListCursor{Created: a.CreatedAt.Unix(), Slug: a.Slug}
			if a.ExpiresAt.IsZero() || a.ExpiresAt.Unix() > now {
				out = append(out, a)
			}
		}
		return rows.Err()
	}
	older := int64(math.MaxInt64)
	if after != nil {
		older = after.Created
		if err := read(qListTies, owner, after.Created, after.Slug, budget); err != nil {
			return nil, nil, err
		}
	}
	if len(out) <= limit && examined < budget {
		if err := read(qListOlder, owner, older, budget-examined); err != nil {
			return nil, nil, err
		}
	}
	switch {
	case len(out) > limit:
		out = out[:limit]
		l := out[limit-1]
		return out, &ListCursor{Created: l.CreatedAt.Unix(), Slug: l.Slug}, nil
	case examined == budget:
		return out, last, nil // truncated: continue after the entries examined
	}
	return out, nil, nil
}

// FamilyScanBudget bounds how many index entries NewestPerFamily examines
// looking for families, SeriesCountCap how far it counts a series' members,
// and seriesCountExamine how many entries it examines per series doing so.
const (
	FamilyScanBudget   = 1000
	SeriesCountCap     = 100
	seriesCountExamine = 4 * (SeriesCountCap + 1)
)

const (
	qFamilies = `SELECT slug, created_at, password_hash, series, expires_at
   FROM artifacts
  WHERE owner = ? AND private = 0
  ORDER BY created_at DESC, slug ASC
  LIMIT ?`
	// INDEXED BY: with LIMIT, the planner may prefer a table scan, which
	// would walk every row before the series' members.
	qSeriesMembers = `SELECT expires_at FROM artifacts INDEXED BY idx_artifacts_public_series
   WHERE owner = ? AND private = 0 AND series = ?
   LIMIT ?`
)

// SeriesSize is a series' unexpired member count as far as it was counted:
// N members, and More if there are or may be more (the count stopped at
// SeriesCountCap, or examined its entries first).
type SeriesSize struct {
	N    int
	More bool
}

// NewestPerFamily returns, in List order, up to limit of owner's unexpired
// public artifacts with one per family: the newest member of each explicit
// series, and every artifact without one. Only Slug, CreatedAt, PasswordHash
// and Series are filled in. It examines at most FamilyScanBudget index
// entries, newest first; truncated reports that they ran out first, so older
// families may exist. members holds each returned series' size.
func (s *Store) NewestPerFamily(ctx context.Context, owner string, limit int) (newest []Artifact, members map[string]SeriesSize, truncated bool, err error) {
	now := time.Now().Unix()
	rows, err := s.db.QueryContext(ctx, qFamilies, owner, FamilyScanBudget)
	if err != nil {
		return nil, nil, false, fmt.Errorf("list families: %w", err)
	}
	seen := map[string]bool{}
	examined := 0
	for len(newest) < limit && rows.Next() {
		var (
			a                Artifact
			created, expires int64
		)
		if err := rows.Scan(&a.Slug, &created, &a.PasswordHash, &a.Series, &expires); err != nil {
			rows.Close()
			return nil, nil, false, fmt.Errorf("scan artifact: %w", err)
		}
		examined++
		s.rowsExamined.Add(1)
		if expired(expires, now) {
			continue
		}
		if a.Series != "" {
			if seen[a.Series] {
				continue
			}
			seen[a.Series] = true
		}
		a.CreatedAt = time.Unix(created, 0).UTC()
		newest = append(newest, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, false, fmt.Errorf("iterate families: %w", err)
	}
	truncated = len(newest) < limit && examined == FamilyScanBudget

	members = map[string]SeriesSize{}
	for name := range seen {
		size, err := s.seriesSize(ctx, owner, name, now)
		if err != nil {
			return nil, nil, false, err
		}
		members[name] = size
	}
	return newest, members, truncated, nil
}

// seriesSize counts a series' unexpired members up to SeriesCountCap+1,
// examining at most seriesCountExamine entries.
func (s *Store) seriesSize(ctx context.Context, owner, series string, now int64) (SeriesSize, error) {
	rows, err := s.db.QueryContext(ctx, qSeriesMembers, owner, series, seriesCountExamine)
	if err != nil {
		return SeriesSize{}, fmt.Errorf("count series: %w", err)
	}
	defer rows.Close()
	var size SeriesSize
	examined := 0
	for size.N <= SeriesCountCap && rows.Next() {
		var expires int64
		if err := rows.Scan(&expires); err != nil {
			return SeriesSize{}, fmt.Errorf("count series: %w", err)
		}
		examined++
		s.rowsExamined.Add(1)
		if !expired(expires, now) {
			size.N++
		}
	}
	if err := rows.Err(); err != nil {
		return SeriesSize{}, fmt.Errorf("count series: %w", err)
	}
	if size.N > SeriesCountCap {
		size.N, size.More = SeriesCountCap, true
	} else if examined == seriesCountExamine {
		size.More = true
	}
	return size, nil
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
