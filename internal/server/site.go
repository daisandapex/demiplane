// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/daisandapex/demiplane/internal/store"
)

// site.go owns feature T2.2: HTTP multi-file / directory publish.
//
//   - Ingest (CONTROL plane): POST /publish?site=<name> with a tar or zip body
//     (detected by ?fmt=, the Content-Type, or the archive magic). handlePublish
//     delegates here through the publishSite hook (routes.go). Every entry path is
//     traversal-checked (store.cleanSiteRelPath via SiteWriter.AddFile); non-
//     regular entries (dirs, symlinks, devices) are skipped; the whole archive is
//     bounded by --max-upload and a decompressed-bytes budget (zip-bomb defense)
//     plus a file-count cap. The publish is transactional: a mid-upload failure
//     leaves any previously published site intact.
//   - Serve (CONTENT origin): GET /{site}/{path...} resolves a file inside the
//     site; GET /{site}/ (and a bare /{site}/{path...} with empty path) serves its
//     index.html. Assets keep the flat-artifact security posture: global nosniff
//     plus a script-src 'none' CSP on SVG/XML so a published asset cannot become a
//     stored-XSS vector.
//
// The /{site}/{path...} and /{site}/{$} shapes are multi-segment, disjoint from
// the single-segment GET /{slug} artifact catch-all, so no reserved slug is
// needed (nil reserved). A site NAME is still validated as a named slug at
// publish time, so it can never be a reserved route name.
func init() {
	registerCoreContentRoute(nil, func(mux *http.ServeMux, s *Server) {
		mux.HandleFunc("GET /{site}/{path...}", s.handleSiteAsset)
		// {$} (exact trailing slash) is more specific than {path...}, so GET
		// /{site}/ routes to the index handler; verified conflict-free by tests.
		mux.HandleFunc("GET /{site}/{$}", s.handleSiteIndex)
	})
	// Wire the multi-file publish hook so handlePublish delegates ?site= here.
	publishSite = func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.handleSitePublish(w, r)
	}
}

const (
	// maxSiteFiles caps how many files one site publish may contain — a coarse
	// guard against an archive with a pathological number of tiny entries.
	maxSiteFiles = 4096
	// defaultSiteArchiveCap bounds the uploaded archive when --max-upload is
	// unset (0 = "unlimited" for single files). A site publish must always be
	// bounded because zip ingest buffers the archive to a scratch file.
	defaultSiteArchiveCap = 100 << 20 // 100 MiB
)

// siteDecompressBudget bounds TOTAL bytes written across all extracted files,
// independent of the compressed archive size — the zip/sparse-tar-bomb defense
// (a small archive can inflate enormously). Shared with the SSH untar path via
// store.BudgetReader; see internal/store/budget.go. A var (not a const) only so
// tests can lower it to exercise the cap cheaply.
var siteDecompressBudget = store.DefaultDecompressBudget

// errTooManyFiles is the file-count rejection, mapped to 413 by
// handleSitePublish (a payload-limit condition). The decompressed-bytes overflow
// is store.ErrDecompressBudget, mapped alongside it in writeSiteExtractError.
var errTooManyFiles = errors.New("site archive contains too many files")

// handleSitePublish ingests POST /publish?site=<name>. It runs on the control
// plane (handlePublish delegates before its own body handling), so the returned
// site URL is minted on the CONTENT origin, like a flat artifact's URL.
func (s *Server) handleSitePublish(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	site := q.Get("site")
	if err := store.ValidateNamedSlug(site); err != nil {
		http.Error(w, "invalid site name: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Bound the uploaded archive. Unlike a single-file publish (which may stream
	// unbounded when --max-upload is unset), a site publish must always cap the
	// body: zip ingest needs the whole archive buffered to a scratch file for
	// random access, so an unbounded body would be an unbounded scratch write.
	cap := s.maxUpload
	if cap <= 0 {
		cap = defaultSiteArchiveCap
	}
	r.Body = http.MaxBytesReader(w, r.Body, cap)

	// Buffer the archive to a scratch file. This bounds detection (magic sniff),
	// unifies tar (stream) and zip (needs io.ReaderAt + size), and the
	// MaxBytesReader above stops an oversize body loudly (*http.MaxBytesError).
	tmp, err := os.CreateTemp("", "demiplane-site-*.arc")
	if err != nil {
		log.Printf("site publish: scratch file: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	size, err := io.Copy(tmp, r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "upload exceeds the configured size limit", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read upload: "+err.Error(), http.StatusBadRequest)
		return
	}

	format := detectArchiveFormat(q.Get("fmt"), r.Header.Get("Content-Type"), tmp)

	sw, err := s.store.NewSiteWriter(site)
	if err != nil {
		if isClientError(err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("site publish: new writer: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer sw.Abort() // no-op after a successful Commit

	switch format {
	case "zip":
		err = extractZip(sw, tmp, size)
	default: // "tar"
		err = extractTar(sw, tmp)
	}
	if err != nil {
		s.writeSiteExtractError(w, err)
		return
	}

	count, err := sw.Commit()
	if err != nil {
		if isClientError(err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("site publish: commit: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	url := s.contentBase(r) + "/" + site + "/"
	if wantsJSON(r) {
		writeJSON(w, http.StatusCreated, map[string]any{
			"url":   url,
			"site":  site,
			"files": count,
		})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintln(w, url)
}

// writeSiteExtractError maps an extraction failure to the right status: the
// archive-shape caps to 413, a traversal / bad-path (InputError) to 400, and an
// otherwise-malformed archive to 400.
func (s *Server) writeSiteExtractError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errTooManyFiles):
		http.Error(w, "site archive contains too many files", http.StatusRequestEntityTooLarge)
	case errors.Is(err, store.ErrDecompressBudget):
		http.Error(w, "site archive expands beyond the size limit", http.StatusRequestEntityTooLarge)
	case isClientError(err):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, "invalid archive: "+err.Error(), http.StatusBadRequest)
	}
}

// detectArchiveFormat resolves "zip" or "tar" from, in order: an explicit ?fmt=,
// the request Content-Type, then the archive's magic bytes. tar is the fallback
// (it has no reliable leading magic). ra is rewound to the start on return.
func detectArchiveFormat(fmtParam, contentType string, ra io.ReadSeeker) string {
	switch strings.ToLower(strings.TrimSpace(fmtParam)) {
	case "zip":
		return "zip"
	case "tar":
		return "tar"
	}
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "zip"):
		return "zip"
	case strings.Contains(ct, "tar"):
		return "tar"
	}
	// Magic sniff: local zip entry header "PK\x03\x04" or empty-archive EOCD
	// "PK\x05\x06". Everything else is treated as a tar.
	if _, err := ra.Seek(0, io.SeekStart); err == nil {
		magic := make([]byte, 4)
		io.ReadFull(ra, magic)
		ra.Seek(0, io.SeekStart)
		if bytes.HasPrefix(magic, []byte("PK\x03\x04")) || bytes.HasPrefix(magic, []byte("PK\x05\x06")) {
			return "zip"
		}
	}
	return "tar"
}

// extractTar streams every regular file from a tar into the site writer.
// Non-regular entries (dirs, symlinks, hardlinks, devices) are skipped, so a
// symlink entry can never plant a link in the served tree. The uploaded archive
// body was already size-capped, but that does NOT bound the DECOMPRESSED output:
// a PAX/GNU sparse tar with Typeflag TypeReg declares a tiny stored payload yet
// expands to attacker-chosen gigabytes of zero-fill through tar.Reader. So the
// same total-bytes budget the zip path uses is enforced here — otherwise a ~10KB
// sparse tar would write GBs to the sites scratch dir, defeating the
// whole-archive size cap.
func extractTar(sw *store.SiteWriter, ra io.ReadSeeker) error {
	if _, err := ra.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind archive: %w", err)
	}
	tr := tar.NewReader(ra)
	budget := siteDecompressBudget
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // skip dirs, symlinks, hardlinks, devices
		}
		if sw.Files() >= maxSiteFiles {
			return errTooManyFiles
		}
		if err := sw.AddFile(hdr.Name, &store.BudgetReader{R: tr, Remaining: &budget}); err != nil {
			return err
		}
	}
	return nil
}

// extractZip extracts every regular file from a zip into the site writer. It
// enforces a total decompressed-bytes budget across all entries (zip-bomb
// defense) since a small zip can inflate enormously past the archive size cap.
func extractZip(sw *store.SiteWriter, ra io.ReaderAt, size int64) error {
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	budget := siteDecompressBudget
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// Only regular files are extracted; a zip symlink (unix mode with
		// ModeSymlink) or other special entry is skipped, so it can never plant a
		// link in the served tree.
		if !f.Mode().IsRegular() {
			continue
		}
		if sw.Files() >= maxSiteFiles {
			return errTooManyFiles
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %q: %w", f.Name, err)
		}
		err = sw.AddFile(f.Name, &store.BudgetReader{R: rc, Remaining: &budget})
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// handleSiteAsset serves GET /{site}/{path...}: a single file inside the site.
func (s *Server) handleSiteAsset(w http.ResponseWriter, r *http.Request) {
	rel := r.PathValue("path")
	if rel == "" {
		// GET /{site}/ normally routes to handleSiteIndex ({$} is more specific);
		// this guards the case where {path...} matches an empty tail.
		rel = "index.html"
	}
	s.serveSiteFile(w, r, r.PathValue("site"), rel)
}

// handleSiteIndex serves GET /{site}/ — the site's index.html.
func (s *Server) handleSiteIndex(w http.ResponseWriter, r *http.Request) {
	s.serveSiteFile(w, r, r.PathValue("site"), "index.html")
}

// serveSiteFile resolves and serves one file within a site with the flat-artifact
// security posture. A bad path or missing file is a 404 (never distinguished, so
// a malformed request cannot probe the tree); nosniff is applied globally by
// withSecurityHeaders on the content handler.
func (s *Server) serveSiteFile(w http.ResponseWriter, r *http.Request, site, rel string) {
	f, ct, mod, err := s.store.OpenSiteFile(site, rel)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || isClientError(err) {
			http.NotFound(w, r)
			return
		}
		log.Printf("serve site file failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", ct)
	if store.IsScriptableNonHTML(ct) {
		// SVG/XML/XHTML render as active documents; block embedded scripts so a
		// published asset can't become stored XSS (same guard as handleGet).
		// Re-states the baseline frame-ancestors directive this Set replaces.
		w.Header().Set("Content-Security-Policy", "script-src 'none'; "+frameAncestorsSelf)
	}
	if store.IsInline(ct) {
		w.Header().Set("Content-Disposition", "inline")
	} else {
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", sanitizeFilename(path.Base(rel))))
	}
	http.ServeContent(w, r, path.Base(rel), mod, f)
}
