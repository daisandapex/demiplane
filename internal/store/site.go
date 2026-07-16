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
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
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
	tmpDir, err := os.MkdirTemp(root, ".tmp-"+site+"-")
	if err != nil {
		return nil, fmt.Errorf("create site scratch dir: %w", err)
	}
	return &SiteWriter{
		store:    s,
		site:     site,
		tmpDir:   tmpDir,
		finalDir: filepath.Join(root, site),
	}, nil
}

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
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("create site dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".w-*")
	if err != nil {
		return fmt.Errorf("create site temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write site file %q: %w", clean, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close site file %q: %w", clean, err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("commit site file %q: %w", clean, err)
	}
	w.count++
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
	var backup string
	if _, err := os.Stat(w.finalDir); err == nil {
		backup = w.finalDir + ".old-" + filepath.Base(w.tmpDir)
		if err := os.Rename(w.finalDir, backup); err != nil {
			w.Abort()
			return 0, fmt.Errorf("stage site replace: %w", err)
		}
	}
	if err := os.Rename(w.tmpDir, w.finalDir); err != nil {
		if backup != "" {
			// Restore the previous site; best-effort.
			_ = os.Rename(backup, w.finalDir)
		}
		w.Abort()
		return 0, fmt.Errorf("commit site: %w", err)
	}
	w.done = true
	if backup != "" {
		os.RemoveAll(backup)
	}
	return w.count, nil
}

// Abort discards the scratch directory. It is safe to call after Commit (no-op)
// and safe to defer unconditionally.
func (w *SiteWriter) Abort() error {
	if w.done {
		return nil
	}
	w.done = true
	return os.RemoveAll(w.tmpDir)
}

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
	dir := filepath.Join(s.siteRoot(), site)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ErrNotFound
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("delete site: %w", err)
	}
	return nil
}

// OpenSiteFile opens a single file within a published site for serving. It
// returns the open file (caller closes), the resolved content-type, and the
// file's modtime. relpath is traversal-checked; a missing file, a directory, or
// a symlink yields ErrNotFound (the last two never occur via AddFile, but the
// serve path is defensive against a store mutated out of band).
func (s *Store) OpenSiteFile(site, relpath string) (*os.File, string, time.Time, error) {
	if err := ValidateNamedSlug(site); err != nil {
		return nil, "", time.Time{}, err
	}
	clean, err := cleanSiteRelPath(relpath)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	base := filepath.Join(s.siteRoot(), site)
	dest := filepath.Join(base, filepath.FromSlash(clean))
	if !withinDir(base, dest) {
		return nil, "", time.Time{}, ErrNotFound
	}
	// Lstat (not Stat): a symlink must be rejected as-is, never followed, so a
	// site file can never be a link out of the tree even if one were planted.
	fi, err := os.Lstat(dest)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", time.Time{}, ErrNotFound
		}
		return nil, "", time.Time{}, fmt.Errorf("stat site file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, "", time.Time{}, ErrNotFound
	}
	f, err := os.Open(dest)
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("open site file: %w", err)
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
