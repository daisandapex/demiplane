// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// --- archive builders -------------------------------------------------------

type srcFile struct {
	name string
	body string
}

func makeTar(t *testing.T, files []srcFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     f.name,
			Mode:     0o644,
			Size:     int64(len(f.body)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("tar header %q: %v", f.name, err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatalf("tar write %q: %v", f.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

// makeTarWithSymlink builds a tar containing a symlink entry (used to prove
// symlink entries are skipped, never materialized).
func makeTarWithSymlink(t *testing.T, link, target string, reg []srcFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name:     link,
		Linkname: target,
		Typeflag: tar.TypeSymlink,
		Mode:     0o777,
	}); err != nil {
		t.Fatalf("tar symlink header: %v", err)
	}
	for _, f := range reg {
		_ = tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(f.body))
	}
	tw.Close()
	return buf.Bytes()
}

func makeZip(t *testing.T, files []srcFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		wr, err := zw.Create(f.name)
		if err != nil {
			t.Fatalf("zip create %q: %v", f.name, err)
		}
		if _, err := wr.Write([]byte(f.body)); err != nil {
			t.Fatalf("zip write %q: %v", f.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// postSite POSTs an archive to /publish?site=<name>&<extra> and returns the
// response and body.
func postSite(t *testing.T, ts, site, extra, contentType string, body []byte) (*http.Response, []byte) {
	t.Helper()
	q := "?site=" + site
	if extra != "" {
		q += "&" + extra
	}
	resp, err := http.Post(ts+"/publish"+q, contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST site: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// threeFileSite is the canonical multi-file site fixture: an index that links a
// stylesheet and a script in a subdirectory.
func threeFileSite() []srcFile {
	return []srcFile{
		{"index.html", "<!doctype html><html><head><link rel=stylesheet href=css/style.css></head>" +
			"<body><h1>hi</h1><script src=js/app.js></script></body></html>"},
		{"css/style.css", "body{color:#b5651d}"},
		{"js/app.js", "console.log('demiplane site')"},
	}
}

// --- happy path: tar + zip round-trip --------------------------------------

func TestSitePublishTarRoundTrip(t *testing.T) {
	ts := newTestServer(t, "")
	resp, body := postSite(t, ts.URL, "demo", "", "application/x-tar", makeTar(t, threeFileSite()))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish status=%d body=%q", resp.StatusCode, body)
	}
	siteURL := strings.TrimSpace(string(body))
	if !strings.HasSuffix(siteURL, "/demo/") {
		t.Fatalf("site URL %q does not end /demo/", siteURL)
	}
	assertSiteServes(t, ts.URL)
}

func TestSitePublishZipRoundTrip(t *testing.T) {
	ts := newTestServer(t, "")
	resp, body := postSite(t, ts.URL, "demo", "", "application/zip", makeZip(t, threeFileSite()))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish status=%d body=%q", resp.StatusCode, body)
	}
	assertSiteServes(t, ts.URL)
}

// assertSiteServes checks the index (both /demo/ and /demo/index.html), the two
// assets, correct content-types, and a 404 for a missing file.
func assertSiteServes(t *testing.T, base string) {
	t.Helper()

	// Index at the directory URL.
	resp, b := get(t, base+"/demo/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /demo/ status=%d", resp.StatusCode)
	}
	if !strings.Contains(string(b), "<h1>hi</h1>") {
		t.Errorf("index body missing marker: %q", b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("index content-type=%q, want text/html", ct)
	}
	if xn := resp.Header.Get("X-Content-Type-Options"); xn != "nosniff" {
		t.Errorf("index missing nosniff, got %q", xn)
	}

	// Index also directly addressable.
	if resp, _ := get(t, base+"/demo/index.html"); resp.StatusCode != http.StatusOK {
		t.Errorf("GET /demo/index.html status=%d", resp.StatusCode)
	}

	// CSS asset with correct type.
	resp, b = get(t, base+"/demo/css/style.css")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET css status=%d", resp.StatusCode)
	}
	if !strings.Contains(string(b), "b5651d") {
		t.Errorf("css body wrong: %q", b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("css content-type=%q, want text/css", ct)
	}

	// JS asset resolves.
	if resp, jb := get(t, base+"/demo/js/app.js"); resp.StatusCode != http.StatusOK || !strings.Contains(string(jb), "demiplane site") {
		t.Errorf("GET js status=%d body=%q", resp.StatusCode, jb)
	}

	// Missing file 404s.
	if resp, _ := get(t, base+"/demo/nope.html"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET missing status=%d, want 404", resp.StatusCode)
	}
}

// --- format detection -------------------------------------------------------

func TestSitePublishFormatDetection(t *testing.T) {
	cases := []struct {
		name        string
		extra       string
		contentType string
		body        func(*testing.T) []byte
	}{
		{"zip via magic (octet-stream)", "", "application/octet-stream", func(t *testing.T) []byte { return makeZip(t, threeFileSite()) }},
		{"tar via magic (octet-stream)", "", "application/octet-stream", func(t *testing.T) []byte { return makeTar(t, threeFileSite()) }},
		{"zip via fmt param, wrong CT", "fmt=zip", "application/x-tar", func(t *testing.T) []byte { return makeZip(t, threeFileSite()) }},
		{"tar via fmt param, wrong CT", "fmt=tar", "application/zip", func(t *testing.T) []byte { return makeTar(t, threeFileSite()) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t, "")
			resp, body := postSite(t, ts.URL, "demo", tc.extra, tc.contentType, tc.body(t))
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("status=%d body=%q", resp.StatusCode, body)
			}
			if resp, _ := get(t, ts.URL+"/demo/"); resp.StatusCode != http.StatusOK {
				t.Fatalf("index status=%d", resp.StatusCode)
			}
		})
	}
}

// --- traversal + malicious entries -----------------------------------------

func TestSitePublishRejectsTraversal(t *testing.T) {
	bad := []string{
		"../escape.html",
		"../../etc/passwd",
		"a/../../escape.html",
		"/abs/path.html",
		"foo/../../bar.html",
	}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			ts := newTestServer(t, "")
			// Pair the malicious entry with a valid index so "no files" is not the
			// reason for rejection — the traversal entry itself must be rejected.
			body := makeTar(t, []srcFile{{"index.html", "ok"}, {name, "PWNED"}})
			resp, rb := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("traversal %q status=%d body=%q, want 400", name, resp.StatusCode, rb)
			}
			// And nothing was committed (transactional): the site must not exist.
			if resp, _ := get(t, ts.URL+"/demo/"); resp.StatusCode != http.StatusNotFound {
				t.Errorf("site materialized despite rejected entry: status=%d", resp.StatusCode)
			}
		})
	}
}

func TestSitePublishSkipsSymlinkEntry(t *testing.T) {
	ts := newTestServer(t, "")
	// A symlink entry (evil -> /etc/passwd) alongside a real index. The symlink
	// must be skipped, the index served, and /demo/evil must 404.
	body := makeTarWithSymlink(t, "evil", "/etc/passwd", []srcFile{{"index.html", "safe"}})
	resp, rb := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d body=%q", resp.StatusCode, rb)
	}
	if resp, _ := get(t, ts.URL+"/demo/evil"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("symlink entry served: status=%d", resp.StatusCode)
	}
	if resp, b := get(t, ts.URL+"/demo/"); resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "safe") {
		t.Errorf("index not served after skipping symlink: status=%d body=%q", resp.StatusCode, b)
	}
}

func TestSitePublishRejectsDotfileSegment(t *testing.T) {
	ts := newTestServer(t, "")
	body := makeTar(t, []srcFile{{"index.html", "ok"}, {".git/config", "secret"}})
	resp, rb := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("dotfile status=%d body=%q, want 400", resp.StatusCode, rb)
	}
}

// --- caps -------------------------------------------------------------------

func TestSitePublishOversizeArchive413(t *testing.T) {
	ts := newConfiguredServer(t, Config{MaxUpload: 1024}) // 1 KiB cap
	big := makeTar(t, []srcFile{{"index.html", strings.Repeat("A", 4096)}})
	resp, rb := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status=%d body=%q, want 413", resp.StatusCode, rb)
	}
}

func TestSitePublishFileCountCap413(t *testing.T) {
	ts := newTestServer(t, "")
	files := make([]srcFile, maxSiteFiles+5)
	for i := range files {
		files[i] = srcFile{name: fmt.Sprintf("f%d.txt", i), body: "x"}
	}
	resp, rb := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", makeTar(t, files))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("file-count status=%d body=%q, want 413", resp.StatusCode, rb)
	}
}

// TestSitePublishZipBomb413 builds a zip whose entries decompress far past the
// decompressed-budget while the compressed archive stays tiny, proving the
// per-total decompression cap (not just the archive-size cap) fires.
func TestSitePublishZipBomb413(t *testing.T) {
	// Lower the decompressed budget so the bomb stays cheap to build and extract;
	// the code path exercised (running total across entries) is identical.
	saved := siteDecompressBudget
	siteDecompressBudget = 2 << 20 // 2 MiB
	defer func() { siteDecompressBudget = saved }()

	ts := newTestServer(t, "")
	bomb := makeStoredHighRatioZip(t, siteDecompressBudget+(1<<20))
	resp, rb := postSite(t, ts.URL, "demo", "fmt=zip", "application/zip", bomb)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("zip-bomb status=%d body=%q, want 413", resp.StatusCode, rb)
	}
}

// TestSitePublishTarBomb413 proves extractTar enforces the same total
// decompressed-byte budget as the zip path. Regression for the archive-bomb gap:
// extractTar previously had NO byte budget, so a PAX/GNU sparse tar (Typeflag
// TypeReg) could expand from a tiny archive to gigabytes of zero-fill. This test
// lowers the budget and feeds a single regular file larger than it; the running
// total must trip errArchiveTooBig -> 413 (the sparse expansion is the same code
// path — a TypeReg entry whose reader yields more bytes than the budget allows).
func TestSitePublishTarBomb413(t *testing.T) {
	saved := siteDecompressBudget
	siteDecompressBudget = 2 << 20 // 2 MiB
	defer func() { siteDecompressBudget = saved }()

	ts := newTestServer(t, "")
	big := strings.Repeat("A", int(siteDecompressBudget)+(1<<20)) // budget + 1 MiB
	bomb := makeTar(t, []srcFile{{"index.html", big}})
	resp, rb := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", bomb)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("tar-bomb status=%d body=%q, want 413", resp.StatusCode, rb)
	}
}

// makeStoredHighRatioZip hand-builds a small zip whose single DEFLATE entry
// inflates to `uncompressed` bytes. It uses flate directly so the on-disk archive
// stays tiny (a run of a single byte compresses ~1000:1), then wraps it in a
// minimal zip container.
func makeStoredHighRatioZip(t *testing.T, uncompressed int64) []byte {
	t.Helper()
	// DEFLATE a long run of zero bytes.
	var comp bytes.Buffer
	fw, _ := flate.NewWriter(&comp, flate.BestCompression)
	buf := make([]byte, 1<<16)
	remaining := int64(uncompressed)
	for remaining > 0 {
		n := int64(len(buf))
		if n > remaining {
			n = remaining
		}
		if _, err := fw.Write(buf[:n]); err != nil {
			t.Fatalf("flate write: %v", err)
		}
		remaining -= n
	}
	fw.Close()
	// The extraction fails on the decompressed-byte budget before the zip reader
	// validates the CRC, so a zero CRC in the container is acceptable here.
	return buildZipDeflateEntry(t, "index.html", comp.Bytes(), uint64(uncompressed))
}

// buildZipDeflateEntry assembles a minimal single-entry zip using a DEFLATE
// (method 8) member whose already-compressed bytes are provided. Sizes use the
// classic 32-bit fields (values fit under 4 GiB here).
func buildZipDeflateEntry(t *testing.T, name string, comp []byte, uncompressed uint64) []byte {
	t.Helper()
	var b bytes.Buffer
	nameBytes := []byte(name)
	compSize := uint32(len(comp))
	uncompSize := uint32(uncompressed)

	// Local file header.
	localOffset := uint32(b.Len())
	b.Write([]byte("PK\x03\x04"))
	writeU16(&b, 20)         // version needed
	writeU16(&b, 0)          // flags
	writeU16(&b, 8)          // method: deflate
	writeU16(&b, 0)          // mod time
	writeU16(&b, 0)          // mod date
	writeU32(&b, 0)          // crc-32 (bomb fails before validation)
	writeU32(&b, compSize)   // compressed size
	writeU32(&b, uncompSize) // uncompressed size
	writeU16(&b, uint16(len(nameBytes)))
	writeU16(&b, 0) // extra len
	b.Write(nameBytes)
	b.Write(comp)

	// Central directory.
	cdOffset := uint32(b.Len())
	b.Write([]byte("PK\x01\x02"))
	writeU16(&b, 20) // version made by
	writeU16(&b, 20) // version needed
	writeU16(&b, 0)  // flags
	writeU16(&b, 8)  // method
	writeU16(&b, 0)  // time
	writeU16(&b, 0)  // date
	writeU32(&b, 0)  // crc
	writeU32(&b, compSize)
	writeU32(&b, uncompSize)
	writeU16(&b, uint16(len(nameBytes)))
	writeU16(&b, 0) // extra
	writeU16(&b, 0) // comment
	writeU16(&b, 0) // disk number
	writeU16(&b, 0) // internal attrs
	writeU32(&b, 0) // external attrs
	writeU32(&b, localOffset)
	b.Write(nameBytes)
	cdSize := uint32(b.Len()) - cdOffset

	// End of central directory.
	b.Write([]byte("PK\x05\x06"))
	writeU16(&b, 0) // disk
	writeU16(&b, 0) // cd start disk
	writeU16(&b, 1) // entries this disk
	writeU16(&b, 1) // total entries
	writeU32(&b, cdSize)
	writeU32(&b, cdOffset)
	writeU16(&b, 0) // comment len
	return b.Bytes()
}

func writeU16(b *bytes.Buffer, v uint16) { _ = binary.Write(b, binary.LittleEndian, v) }
func writeU32(b *bytes.Buffer, v uint32) { _ = binary.Write(b, binary.LittleEndian, v) }

// --- misc rejections + republish -------------------------------------------

func TestSitePublishEmptyArchive400(t *testing.T) {
	ts := newTestServer(t, "")
	resp, rb := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", makeTar(t, nil))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty status=%d body=%q, want 400", resp.StatusCode, rb)
	}
}

func TestSitePublishReservedName400(t *testing.T) {
	ts := newTestServer(t, "")
	resp, rb := postSite(t, ts.URL, "docs", "fmt=tar", "application/x-tar", makeTar(t, threeFileSite()))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("reserved-name status=%d body=%q, want 400", resp.StatusCode, rb)
	}
}

func TestSitePublishBadArchive400(t *testing.T) {
	ts := newTestServer(t, "")
	resp, rb := postSite(t, ts.URL, "demo", "fmt=zip", "application/zip", []byte("not a zip at all"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad-zip status=%d body=%q, want 400", resp.StatusCode, rb)
	}
}

// TestSitePublishRepublishReplaces proves a re-publish overwrites the whole site
// atomically: a file present in v1 but absent from v2 is gone afterward.
func TestSitePublishRepublishReplaces(t *testing.T) {
	ts := newTestServer(t, "")
	// v1: index + stale.html
	if resp, rb := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar",
		makeTar(t, []srcFile{{"index.html", "v1"}, {"stale.html", "old"}})); resp.StatusCode != http.StatusCreated {
		t.Fatalf("v1 status=%d body=%q", resp.StatusCode, rb)
	}
	if resp, _ := get(t, ts.URL+"/demo/stale.html"); resp.StatusCode != http.StatusOK {
		t.Fatalf("stale.html should exist after v1")
	}
	// v2: index only.
	if resp, rb := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar",
		makeTar(t, []srcFile{{"index.html", "v2"}})); resp.StatusCode != http.StatusCreated {
		t.Fatalf("v2 status=%d body=%q", resp.StatusCode, rb)
	}
	if resp, b := get(t, ts.URL+"/demo/"); resp.StatusCode != http.StatusOK || string(b) != "v2" {
		t.Errorf("index not replaced: status=%d body=%q", resp.StatusCode, b)
	}
	if resp, _ := get(t, ts.URL+"/demo/stale.html"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("stale.html survived republish: status=%d", resp.StatusCode)
	}
}

// TestSitePublishNotWiredReturns501 guards the scaffold contract: when the
// publishSite hook is unset the ?site= branch of handlePublish returns 501. We
// swap the hook out and restore it so we do not disturb other tests.
func TestSitePublishHookGuard(t *testing.T) {
	saved := publishSite
	publishSite = nil
	defer func() { publishSite = saved }()

	ts := newTestServer(t, "")
	resp, _ := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", makeTar(t, threeFileSite()))
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("unwired ?site= status=%d, want 501", resp.StatusCode)
	}
}

// --- serve-side security posture -------------------------------------------

func TestSiteAssetSVGGetsNoScriptCSP(t *testing.T) {
	ts := newTestServer(t, "")
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	body := makeTar(t, []srcFile{{"index.html", "ok"}, {"pic.svg", svg}})
	if resp, rb := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", body); resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish status=%d body=%q", resp.StatusCode, rb)
	}
	resp, _ := get(t, ts.URL+"/demo/pic.svg")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("svg status=%d", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); csp != "script-src 'none'; frame-ancestors 'self'" {
		t.Errorf("svg CSP=%q, want script-src 'none'; frame-ancestors 'self'", csp)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/svg+xml") {
		t.Errorf("svg content-type=%q", resp.Header.Get("Content-Type"))
	}
}

// TestSiteAssetHTMLRelabelGuard proves the store's text/html-relabel guard: a
// .html-named entry whose bytes are NOT html is not served as executable HTML.
func TestSiteAssetHTMLRelabelGuard(t *testing.T) {
	ts := newTestServer(t, "")
	// SVG bytes under a .html name — must NOT become text/html (which would drop
	// the no-script CSP and enable stored XSS).
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	body := makeTar(t, []srcFile{{"index.html", "ok"}, {"trick.html", svg}})
	postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", body)
	resp, _ := get(t, ts.URL+"/demo/trick.html")
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		t.Errorf("SVG-bytes .html served as text/html (%q) — relabel guard bypassed", ct)
	}
}

// --- combined + split handler both mount the routes without conflict --------

func TestSiteRoutesMountOnSplitAndCombined(t *testing.T) {
	// newSplitServers builds ContentHandler + ControlHandler; the combined
	// Handler is built by newTestServer. If either registration conflicted with
	// /docs/{page}, /_events/{slug}, or /{slug}, ServeMux would panic at build —
	// reaching this point without panic is the assertion.
	control, content := newSplitServers(t, Config{})
	// Publish on the control plane; serve on the content origin.
	resp, rb := postSite(t, control.URL, "demo", "fmt=tar", "application/x-tar", makeTar(t, threeFileSite()))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("split publish status=%d body=%q", resp.StatusCode, rb)
	}
	if resp, _ := get(t, content.URL+"/demo/"); resp.StatusCode != http.StatusOK {
		t.Errorf("content-origin index status=%d", resp.StatusCode)
	}
	// The control plane must NOT serve site assets (origin isolation).
	if resp, _ := get(t, control.URL+"/demo/css/style.css"); resp.StatusCode == http.StatusOK {
		t.Errorf("control plane served a site asset — origin isolation breach")
	}
}
