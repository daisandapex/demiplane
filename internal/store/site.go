// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

// site.go adds "site container" support to the store: a named, path-structured
// bundle of files (an HTML+CSS+JS+asset site) served under a single site name,
// distinct from the flat single-file artifacts the rest of the store handles.
//
// Sites live on disk under <root>/sites/<site>/<relpath>, mirroring the artifact
// blob layout but keeping the directory structure so relative links inside a page
// resolve. Sites are the HTTP multi-file publish target (feature T2.2,
// internal/server/site.go); they are always public and named (a site name is
// guessable, like a named slug), so there is no private/capability or password
// mode here — the flat-artifact path owns that.
//
// A publish is transactional via SiteWriter: files stream into a scratch dir and
// Commit atomically swaps it into place, so a re-publish overwrites the whole
// site in one rename (a removed file does not linger) and a mid-upload failure
// leaves the previously published site intact.

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/daisandapex/demiplane/internal/logsafe"
)

// maxSiteFilenameSegments bounds path depth defensively; a legitimate site is
// nowhere near this deep, and an absurdly deep path is a red flag.
const maxSitePathSegments = 32

// siteRoot is the parent directory that holds every site's file tree.
func (s *Store) siteRoot() string { return filepath.Join(s.root, "sites") }

// cleanSiteRelPath validates and normalizes a site-relative file path from an
// archive entry. It is the traversal gate for the multi-file publish path: it
// rejects absolute paths, "." / ".." segments, and any segment that is not a
// URL-safe slug token (which also rejects dotfiles and empty segments). The
// returned path is forward-slash separated and rooted inside the site.
//
// Unlike transport.pathToSlug (which FLATTENS "a/b" into a single "a-b" slug for
// flat artifacts), this preserves directory structure so a multi-file site keeps
// its layout — but validates each segment with the same URL-safe rule the named
// slug validator uses, so traversal is structurally impossible.
func cleanSiteRelPath(name string) (string, error) {
	// Normalize Windows separators. Deliberately do NOT path.Clean the input:
	// Clean would silently RESOLVE a "../" into a different in-tree path rather
	// than reject it, masking a malicious entry. The spec requires rejecting
	// "..", so we inspect the raw segments and refuse any traversal token.
	p := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(p, "/") {
		return "", inputErrorf("site entry %q is an absolute path", name)
	}
	var out []string
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
			continue // collapse empty and current-dir segments (a/./b -> a/b)
		case "..":
			return "", inputErrorf("site entry %q contains a parent-directory (..) segment", name)
		}
		// namedSlugRe forbids leading dots (dotfiles), "/", and non-URL-safe bytes
		// — everything left after the traversal checks above. Reserved-slug names
		// (docs/help/...) are top-level route collisions, irrelevant to a nested
		// site path, so this deliberately does NOT apply the reserved check here.
		if !namedSlugRe.MatchString(seg) {
			return "", inputErrorf("site entry %q has an invalid path segment %q", name, seg)
		}
		out = append(out, seg)
	}
	if len(out) == 0 {
		return "", inputErrorf("site entry %q has an empty path", name)
	}
	if len(out) > maxSitePathSegments {
		return "", inputErrorf("site entry %q is nested too deeply", name)
	}
	return strings.Join(out, "/"), nil
}

// withinDir reports whether target resolves inside base (belt-and-suspenders
// against traversal after cleanSiteRelPath already guaranteed it).
func withinDir(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// SiteWriter accumulates the files of one site publish into a scratch directory
// and commits them atomically. Obtain one with Store.NewSiteWriter, AddFile each
// archive entry, then Commit (or Abort on error — Abort is a safe no-op after a
// successful Commit).
type SiteWriter struct {
	store    *Store
	site     string
	tmpDir   string // scratch dir under <root>/sites that Commit renames into place
	finalDir string // <root>/sites/<site>
	count    int
	done     bool
	charged  int64 // quota bytes charged to the staged entries (quota.go)
	entries  int64 // site entries charged to the staged tree, its root included
}

// chargeEntry charges one new file or directory of the staged tree: one site
// entry and its allowance (siteentry.go).
func (w *SiteWriter) chargeEntry() error {
	if err := w.store.chargeSiteEntries(1); err != nil {
		return err
	}
	if err := w.store.charge(metaAllowance); err != nil {
		w.store.releaseSiteEntries(1)
		return err
	}
	w.charged += metaAllowance
	w.entries++
	return nil
}

// releaseEntry returns a chargeEntry whose entry was not created.
func (w *SiteWriter) releaseEntry() {
	w.store.releaseSiteEntries(1)
	w.store.release(metaAllowance)
	w.charged -= metaAllowance
	w.entries--
}

// NewSiteWriter starts a transactional publish for the named site. The site name
// is validated as a named slug (URL-safe, non-reserved) — a site named after a
// core route ("docs", "help", ...) is rejected so it can never shadow one in the
// combined same-origin mux.
func (s *Store) NewSiteWriter(site string) (*SiteWriter, error) {
	if err := ValidateNamedSlug(site); err != nil {
		return nil, err
	}
	root := s.siteRoot()
	// 0700 so a password-gated flat artifact's neighbors on disk stay unreadable
	// to other local users; sites are public content but share the store's posture.
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create sites dir: %w", err)
	}
	w := &SiteWriter{store: s, site: site, finalDir: filepath.Join(root, site)}
	// The scratch dir becomes the site's own directory: its first entry.
	if err := w.chargeEntry(); err != nil {
		return nil, err
	}
	// Created and registered in one transition, so a recount never counts it
	// as abandoned while it is also charged in flight.
	s.beginTransition()
	tmpDir, err := os.MkdirTemp(root, tempPrefix+site+"-")
	if err == nil {
		s.registerStaging(filepath.Base(tmpDir))
	}
	s.endTransition(0, 0)
	if err != nil {
		w.releaseEntry()
		return nil, fmt.Errorf("create site scratch dir: %w", err)
	}
	w.tmpDir = tmpDir
	return w, nil
}

// siteLockKey is the per-slug lock key that serialises a site's commit and
// delete; the prefix keeps it apart from artifact slugs.
func siteLockKey(site string) string { return "\x00site/" + site }

// AddFile streams one archive entry into the scratch tree at its (validated)
// relative path, creating parent directories as needed. relpath is traversal-
// checked via cleanSiteRelPath; r is the entry's bytes (never a symlink target —
// the caller must skip non-regular entries before calling AddFile).
func (w *SiteWriter) AddFile(relpath string, r io.Reader) error {
	if w.done {
		return fmt.Errorf("site writer already committed")
	}
	clean, err := cleanSiteRelPath(relpath)
	if err != nil {
		return err
	}
	dest := filepath.Join(w.tmpDir, filepath.FromSlash(clean))
	if !withinDir(w.tmpDir, dest) {
		// Unreachable after cleanSiteRelPath; kept as a hard invariant guard.
		return inputErrorf("site entry %q escapes the site root", relpath)
	}
	if err := w.mkdirs(clean); err != nil {
		return err
	}
	// A path staged twice replaces the earlier file. Either way the temp file
	// is a new entry, reserved before it is created; a replaced file's entry
	// is returned once the rename has removed it.
	var (
		replaced  int64
		replacing bool
	)
	switch fi, err := os.Lstat(dest); {
	case err == nil && fi.Mode().IsRegular():
		replaced, replacing = fi.Size(), true
	case err == nil:
		return inputErrorf("site entry %q is both a file and a directory", relpath)
	case os.IsNotExist(err):
	default:
		return fmt.Errorf("stat site file %q: %w", clean, err)
	}
	if err := w.chargeEntry(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".w-*")
	if err != nil {
		w.releaseEntry()
		return fmt.Errorf("create site temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Decompressed bytes are charged as they are written, so an archive that
	// expands past the store quota stops here.
	cw := &chargedWriter{s: w.store, w: tmp}
	// fail removes the temp file and returns its charge, or, if it cannot be
	// removed, keeps the charge with the tree for Abort to settle.
	fail := func(err error) error {
		if rerr := os.Remove(tmpName); rerr != nil && !os.IsNotExist(rerr) {
			w.charged += cw.charged
			return errors.Join(err, fmt.Errorf("remove site temp file: %w", rerr))
		}
		w.store.release(cw.charged)
		w.releaseEntry()
		return err
	}
	if _, err := io.Copy(cw, r); err != nil {
		tmp.Close()
		return fail(fmt.Errorf("write site file %q: %w", clean, err))
	}
	if err := tmp.Close(); err != nil {
		return fail(fmt.Errorf("close site file %q: %w", clean, err))
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return fail(fmt.Errorf("commit site file %q: %w", clean, err))
	}
	w.charged += cw.charged
	if replacing {
		w.store.release(replaced)
		w.charged -= replaced
		w.releaseEntry()
	}
	w.count++
	return nil
}

// mkdirs creates the parent directories of the staged path clean, charging
// each one it creates as a site entry before creating it.
func (w *SiteWriter) mkdirs(clean string) error {
	segs := strings.Split(clean, "/")
	dir := w.tmpDir
	for _, seg := range segs[:len(segs)-1] {
		dir = filepath.Join(dir, seg)
		fi, err := os.Lstat(dir)
		if err == nil {
			if !fi.IsDir() {
				return inputErrorf("site entry %q is both a file and a directory", clean)
			}
			continue
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat site dir: %w", err)
		}
		if err := w.chargeEntry(); err != nil {
			return err
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			w.releaseEntry()
			return fmt.Errorf("create site dir: %w", err)
		}
	}
	return nil
}

// Files returns how many files have been staged so far.
func (w *SiteWriter) Files() int { return w.count }

// Commit atomically replaces any existing site with the staged files and returns
// the file count. An empty archive (no files staged) is rejected. On any failure
// the previously published site (if any) is left intact.
func (w *SiteWriter) Commit() (int, error) {
	if w.done {
		return w.count, fmt.Errorf("site writer already committed")
	}
	if w.count == 0 {
		w.Abort()
		return 0, inputErrorf("site archive contained no publishable files")
	}
	s := w.store
	unlock := s.locks.lock(siteLockKey(w.site))
	defer unlock()
	// Both renames and the accounting are one quota transition, so a
	// concurrent usage recount never walks the tree halfway through the swap.
	// The replaced tree moves to a staging name, owned until it is removed;
	// one that cannot be removed is left abandoned, still charged, for
	// Reconcile.
	s.beginTransition()
	var backup string
	if _, err := os.Stat(w.finalDir); err == nil {
		backup = filepath.Join(filepath.Dir(w.finalDir), tempPrefix+"old-"+strings.TrimPrefix(filepath.Base(w.tmpDir), tempPrefix))
		s.registerStaging(filepath.Base(backup))
		if err := os.Rename(w.finalDir, backup); err != nil {
			s.unregisterStaging(filepath.Base(backup))
			s.endTransition(0, 0)
			w.Abort()
			return 0, fmt.Errorf("stage site replace: %w", err)
		}
	}
	if err := os.Rename(w.tmpDir, w.finalDir); err != nil {
		if backup != "" {
			// Restore the previous site; best-effort.
			_ = os.Rename(backup, w.finalDir)
			s.unregisterStaging(filepath.Base(backup))
		}
		s.endTransition(0, 0)
		w.Abort()
		return 0, fmt.Errorf("commit site: %w", err)
	}
	w.done = true
	s.unregisterStaging(filepath.Base(w.tmpDir))
	s.endSiteTransition(w.charged, 0, w.entries, 0)
	if backup != "" {
		if err := s.removeLiveTree(backup); err != nil {
			log.Printf("store: could not remove a replaced site tree, will retry: %s", logsafe.Err(err))
		}
		s.unregisterStaging(filepath.Base(backup))
	}
	return w.count, nil
}

// Abort discards the scratch directory. It is safe to call after Commit (no-op)
// and safe to defer unconditionally. It releases exactly what it removed;
// whatever it cannot remove stays charged as an abandoned staging tree, which
// Reconcile retries.
func (w *SiteWriter) Abort() error {
	if w.done {
		return nil
	}
	w.done = true
	s := w.store
	s.beginTransition()
	bytes, entries, err := removeTreeCounted(w.tmpDir)
	released := bytes + entries*metaAllowance
	s.release(released)
	s.releaseSiteEntries(entries)
	s.unregisterStaging(filepath.Base(w.tmpDir))
	// The survivors' charge moves from in flight to live, where a recount
	// now measures them.
	s.endSiteTransition(max(w.charged-released, 0), 0, max(w.entries-entries, 0), 0)
	w.charged, w.entries = 0, 0
	return err
}

// beforeSiteRemove is a test hook run by DeleteSite before it removes the tree.
var beforeSiteRemove func()

// SiteExists reports whether the named site has been published.
func (s *Store) SiteExists(site string) bool {
	if ValidateNamedSlug(site) != nil {
		return false
	}
	fi, err := os.Stat(filepath.Join(s.siteRoot(), site))
	return err == nil && fi.IsDir()
}

// DeleteSite removes a published site's whole file tree. It returns ErrNotFound
// if no such site exists.
func (s *Store) DeleteSite(site string) error {
	if err := ValidateNamedSlug(site); err != nil {
		return err
	}
	unlock := s.locks.lock(siteLockKey(site))
	defer unlock()
	dir := filepath.Join(s.siteRoot(), site)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ErrNotFound
	}
	if beforeSiteRemove != nil {
		beforeSiteRemove()
	}
	if err := s.removeLiveTree(dir); err != nil {
		return fmt.Errorf("delete site: %w", err)
	}
	return nil
}

// OpenSiteFile opens a single file within a published site for serving. It
// returns the open file (caller closes), the resolved content-type, and the
// file's modtime. relpath is traversal-checked; a missing file, a directory, or
// a symlink anywhere on the path (the site directory itself, an intermediate
// directory, or the file) yields ErrNotFound. AddFile never creates a symlink,
// so one in the tree was planted out of band and is never followed.
//
// Resolution is race-free: every component is opened through an os.Root
// (which refuses to leave its directory even via a symlink planted after a
// check), and each component's Lstat is compared with the identity of what
// was actually opened, so a swap between the check and the open is detected
// rather than served.
func (s *Store) OpenSiteFile(site, relpath string) (*os.File, string, time.Time, error) {
	if err := ValidateNamedSlug(site); err != nil {
		return nil, "", time.Time{}, err
	}
	clean, err := cleanSiteRelPath(relpath)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	f, fi, err := s.openSitePath(site, strings.Split(clean, "/"))
	if err != nil {
		return nil, "", time.Time{}, err
	}
	head := make([]byte, sniffLen)
	n, rerr := io.ReadFull(f, head)
	if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
		f.Close()
		return nil, "", time.Time{}, fmt.Errorf("sniff site file: %w", rerr)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, "", time.Time{}, fmt.Errorf("rewind site file: %w", err)
	}
	// Same content-type policy as flat artifacts, including the text/html-relabel
	// guard: a .html extension only wins when the bytes actually sniff as HTML.
	ct := inferContentType(path.Base(clean), http.DetectContentType(head[:n]))
	return f, ct, fi.ModTime(), nil
}

// openSitePath opens sites/<site>/<segs...> refusing any symlink on the way.
func (s *Store) openSitePath(site string, segs []string) (*os.File, os.FileInfo, error) {
	root, err := os.OpenRoot(s.siteRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("open sites dir: %w", err)
	}
	// root always names the innermost directory opened so far; each step
	// closes its parent (a file opened through a root outlives the root).
	defer func() { root.Close() }()
	dirs := append([]string{site}, segs[:len(segs)-1]...)
	for _, d := range dirs {
		sub, err := openRootNoFollow(root, d)
		if err != nil {
			return nil, nil, err
		}
		root.Close()
		root = sub
	}
	name := segs[len(segs)-1]
	want, err := root.Lstat(name)
	if err != nil {
		return nil, nil, siteNotFoundOr(err, "stat site file")
	}
	if !want.Mode().IsRegular() {
		return nil, nil, ErrNotFound
	}
	// O_NONBLOCK: a FIFO swapped in after the Lstat must not stall the open;
	// the identity check below then rejects it.
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, siteNotFoundOr(err, "open site file")
	}
	got, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("stat site file: %w", err)
	}
	if !got.Mode().IsRegular() || !os.SameFile(want, got) {
		f.Close()
		return nil, nil, ErrNotFound
	}
	return f, got, nil
}

// openRootNoFollow opens the directory name inside root as a new root,
// refusing a symlink or anything that is not a directory, and confirming the
// opened directory is the one that was checked.
func openRootNoFollow(root *os.Root, name string) (*os.Root, error) {
	want, err := root.Lstat(name)
	if err != nil {
		return nil, siteNotFoundOr(err, "stat site dir")
	}
	if !want.IsDir() {
		return nil, ErrNotFound
	}
	sub, err := root.OpenRoot(name)
	if err != nil {
		return nil, siteNotFoundOr(err, "open site dir")
	}
	got, err := sub.Stat(".")
	if err != nil {
		sub.Close()
		return nil, fmt.Errorf("stat site dir: %w", err)
	}
	if !os.SameFile(want, got) {
		sub.Close()
		return nil, ErrNotFound
	}
	return sub, nil
}

// siteNotFoundOr maps a missing path, or a path os.Root refused because it
// escapes, to ErrNotFound, and wraps anything else.
func siteNotFoundOr(err error, op string) error {
	if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ELOOP) || isEscape(err) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}

// isEscape reports whether err is os.Root refusing a path outside the root.
// The standard library does not export that error, so it is matched by text;
// it can only surface when a component is swapped for a symlink between its
// Lstat and its open, and a miss would turn a 404 into a 500, nothing more.
func isEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), "path escapes from parent")
}
