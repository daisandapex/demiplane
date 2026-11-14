// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// fuzzRequestBound is the longest one fuzzed request may take in process.
const fuzzRequestBound = 5 * time.Second

// pathSeeds are request paths from the canonicalization tests and the fixes
// for encoded dot segments, backslashes and network-path redirects.
var pathSeeds = []string{
	"/", "/a", "/a/", "//a", "///a", "/./a", "/../a", "/a/../b", "/a/./b/",
	"/%2e%2e/a", "/%2E/a", "/a%2fb", "/a%5cb", `/a\b`, "/a%00b", "/a%7fb",
	"/%c2%85", "//evil.example/%2e%2e", "/.//evil.example", "/a/..//evil",
	"/%", "/%zz", "/a?x=1", "/sites/s/a.html", "/_events/a",
}

// FuzzCanonicalPaths checks the path canonicalizer that sits in front of
// both muxes: a path is refused, passed on unchanged in canonical form, or
// redirected once to a same-origin canonical path.
func FuzzCanonicalPaths(f *testing.F) {
	for _, p := range pathSeeds {
		f.Add(p, "")
		f.Add(p, "q=1&r=%2F%2Fx")
	}
	var reached string
	h := canonicalPaths(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = r.URL.EscapedPath()
		w.WriteHeader(http.StatusTeapot)
	}))
	serve := func(target string) *httptest.ResponseRecorder {
		reached = ""
		r := httptest.NewRequest(http.MethodGet, "http://demi.example", nil)
		u, err := url.ParseRequestURI(target)
		if err != nil {
			return nil
		}
		r.URL, r.RequestURI = u, target
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	f.Fuzz(func(t *testing.T, p, query string) {
		target := p
		if query != "" {
			target += "?" + query
		}
		if strings.ContainsAny(target, " \r\n\t#") {
			return // not a request-target net/http would accept
		}
		w := serve(target)
		if w == nil {
			return
		}
		switch w.Code {
		case http.StatusBadRequest:
		case http.StatusTeapot:
			if reached != cleanEscapedPath(reached) || strings.Contains(reached, "//") {
				t.Fatalf("non-canonical path %q reached the mux", reached)
			}
			if !unambiguousSegments(reached) {
				t.Fatalf("ambiguous path %q reached the mux", reached)
			}
		case http.StatusTemporaryRedirect:
			loc := w.Header().Get("Location")
			lu, err := url.Parse(loc)
			// A backslash is a separator to browsers only in the path; the
			// query is kept as sent.
			locPath, _, _ := strings.Cut(loc, "?")
			if err != nil || lu.Scheme != "" || lu.Host != "" || !strings.HasPrefix(loc, "/") ||
				strings.HasPrefix(loc, "//") || strings.ContainsRune(locPath, '\\') || strings.ContainsAny(loc, "\r\n") {
				t.Fatalf("redirect for %q leaves the origin: Location %q", target, loc)
			}
			// One redirect reaches the canonical form.
			if w2 := serve(loc); w2 == nil || w2.Code != http.StatusTeapot {
				code := 0
				if w2 != nil {
					code = w2.Code
				}
				t.Fatalf("redirect target %q of %q answers %d, not the route", loc, target, code)
			}
		default:
			t.Fatalf("unexpected status %d for %q", w.Code, target)
		}
	})
}

// FuzzHostPolicy checks --allowed-hosts parsing, Host matching and the Host
// sanitizer that guards reflected URLs.
func FuzzHostPolicy(f *testing.F) {
	for _, s := range [][2]string{
		{"demi.example", "demi.example"}, {"demi.example:443", "DEMI.example"},
		{"[fd00::1]", "[fd00::1]:8080"}, {"a,b:80", "b"}, {"http://x", "x"},
		{"*.example", "a.example"}, {"user@h", "h"}, {"h", "h\"><script>"},
		{"h", "h:80@evil"}, {"h", "[h]"}, {"", ""},
	} {
		f.Add(s[0], s[1], false)
	}
	f.Fuzz(func(t *testing.T, csv, host string, tls bool) {
		clean := sanitizeHost(host)
		if len(clean) > maxHostLen {
			t.Fatalf("sanitized host is %d bytes", len(clean))
		}
		for _, r := range clean {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".-:[]_", r)) {
				t.Fatalf("sanitized host %q kept %q", clean, r)
			}
		}
		if sanitizeHost(clean) != clean {
			t.Fatalf("sanitizeHost not idempotent on %q", host)
		}
		list, err := ParseHostList(csv)
		if err != nil {
			return
		}
		for _, e := range list {
			if strings.ContainsAny(e, "/@*, ?#") {
				t.Fatalf("host entry %q accepted from %q", e, csv)
			}
		}
		rules := buildHostRules(list)
		if !hostAllowed(rules, host, tls) {
			return
		}
		h, p := splitHost(host)
		if p == "" {
			p = map[bool]string{false: "80", true: "443"}[tls]
		}
		for _, r := range rules {
			if r.host == h && (r.port == "" || r.port == p) {
				return
			}
		}
		t.Fatalf("host %q allowed by no rule of %q", host, csv)
	})
}

// FuzzValidBearer checks the bearer comparison against its plain meaning:
// the scheme matches case-insensitively and the token exactly, after the
// surrounding space HTTP allows.
func FuzzValidBearer(f *testing.F) {
	key := []byte("0123456789abcdef0123456789abcdef")
	for _, s := range [][2]string{
		{"Bearer tok", "tok"}, {"bearer tok", "tok"}, {"BEARER  tok ", "tok"},
		{"Bearer", "tok"}, {"Bearer ", ""}, {"Basic tok", "tok"}, {"Bearer tok2", "tok"},
		{"Bearertok", "tok"}, {"Bearer to", "tok"},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, header, want string) {
		got := validBearer(header, want, key)
		exp := len(header) > len("bearer ") && strings.EqualFold(header[:7], "bearer ") &&
			strings.TrimSpace(header[7:]) == want
		if got != exp {
			t.Fatalf("validBearer(%q, %q) = %v, want %v", header, want, got, exp)
		}
	})
}

// FuzzSanitizeFilename checks the Content-Disposition filename filter and
// the Range filter, the two download headers built from request or stored
// values.
func FuzzSanitizeFilename(f *testing.F) {
	for _, s := range []string{"report.pdf", "a\"b", "a\r\nX-Evil: 1", `..\..\x`, "a/b", "", "\x7f", "é.txt"} {
		f.Add(s, "bytes=0-1")
	}
	f.Add("x", "bytes=0-1,2-3")
	f.Add("x", "bytes="+strings.Repeat("1", 200)+"-")
	f.Fuzz(func(t *testing.T, name, rng string) {
		s := sanitizeFilename(name)
		if s == "" || strings.ContainsAny(s, "\"\\/") {
			t.Fatalf("sanitizeFilename(%q) = %q", name, s)
		}
		for _, r := range s {
			if r < 0x20 {
				t.Fatalf("sanitizeFilename(%q) kept control %q", name, r)
			}
		}
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.Header.Set("Range", rng)
		singleRangeOnly(r)
		if v := r.Header.Get("Range"); v != "" && (len(v) > maxRangeHeader || strings.Contains(v, ",")) {
			t.Fatalf("Range %q kept", v)
		}
	})
}

// fuzzSiteBudget replaces the 512 MiB decompression budget while fuzzing.
const fuzzSiteBudget = 1 << 20

func tarOf(t testing.TB, entries map[string]string, link bool) []byte {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for n, c := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(c)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(c))
	}
	if link {
		tw.WriteHeader(&tar.Header{Name: "evil", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	}
	tw.Close()
	return b.Bytes()
}

func zipOf(t testing.TB, entries map[string]string) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for n, c := range entries {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(c))
	}
	zw.Close()
	return b.Bytes()
}

// FuzzSiteArchive extracts hostile tar and zip archives into a real store
// and checks the result: every extracted file is a regular file inside the
// site, the decompressed total holds to the budget, the entry cap holds, and
// extraction finishes in bounded time.
func FuzzSiteArchive(f *testing.F) {
	ok := map[string]string{"index.html": "<p>x</p>", "css/a.css": "a{}", "a/b/c.txt": "c"}
	evil := map[string]string{"../escape": "x", "/abs": "x", `a\..\b`: "x", "a/../../b": "x", ".git/config": "x"}
	f.Add(tarOf(f, ok, true), false)
	f.Add(tarOf(f, evil, false), false)
	f.Add(zipOf(f, ok), true)
	f.Add(zipOf(f, evil), true)
	f.Add([]byte("PK\x05\x06"+strings.Repeat("\x00", 18)), true)
	f.Add(bytes.Repeat([]byte{0}, 1024), false)

	prev := siteDecompressBudget
	siteDecompressBudget = fuzzSiteBudget
	f.Cleanup(func() { siteDecompressBudget = prev })
	st, err := store.Open(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { st.Close() })
	sitesRoot := filepath.Join(st.Root(), "data", "sites")
	f.Fuzz(func(t *testing.T, archive []byte, isZip bool) {
		sw, err := st.NewSiteWriter("fuzzsite")
		if err != nil {
			t.Fatal(err)
		}
		defer sw.Abort()
		start := time.Now()
		ra := bytes.NewReader(archive)
		if isZip {
			err = extractZip(sw, ra, int64(len(archive)))
		} else {
			err = extractTar(sw, ra)
		}
		if el := time.Since(start); el > fuzzRequestBound {
			t.Fatalf("extract took %v", el)
		}
		if err != nil {
			if errors.Is(err, store.ErrDecompressBudget) || errors.Is(err, errTooManyFiles) || errors.Is(err, errTarHeaderTooLarge) {
				return
			}
			return // malformed archive or refused path: a 400
		}
		if sw.Files() > maxSiteEntries {
			t.Fatalf("%d files past the entry cap", sw.Files())
		}
		if _, err := sw.Commit(); err != nil {
			return
		}
		var total int64
		werr := filepath.WalkDir(sitesRoot, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, rerr := filepath.Rel(sitesRoot, p)
			if rerr != nil || strings.HasPrefix(rel, "..") {
				t.Fatalf("walked outside the sites root: %q", p)
			}
			if d.Type()&fs.ModeSymlink != 0 {
				t.Fatalf("extracted a symlink %q", rel)
			}
			if d.Type().IsRegular() {
				fi, err := d.Info()
				if err != nil {
					return err
				}
				total += fi.Size()
			}
			return nil
		})
		if werr != nil {
			t.Fatal(werr)
		}
		if total > fuzzSiteBudget {
			t.Fatalf("site holds %d bytes past a %d byte budget", total, fuzzSiteBudget)
		}
		// Nothing was written beside the store.
		if _, err := os.Lstat(filepath.Join(st.Root(), "escape")); err == nil {
			t.Fatal("an entry escaped the sites root")
		}
		if err := st.DeleteSite("fuzzsite"); err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
	})
}

// FuzzPublish drives POST /publish with hostile query strings, content types
// and bodies through the combined handler, in process. A publish without the
// token is refused; one that is accepted names a valid slug on this origin and
// reads back byte for byte; nothing answers 500.
func FuzzPublish(f *testing.F) {
	for _, q := range []string{
		"", "slug=a", "slug=../x", "slug=publish", "slug=a%2Fb", "private=1", "private=1&slug=a",
		"ttl=1h", "ttl=-1d", "ttl=" + strings.Repeat("9", 70), "render=md", "render=md&slug=doc",
		"filename=x.html", "filename=a.txt&slug=b", "series=s&slug=s-1", "reply=form&slug=r",
		"reply=question&next=r2&slug=r", "next=//evil", "site=s", "password=", "fmt=zip&site=s",
	} {
		f.Add(q, "text/plain", []byte("hello <script>x</script>"), true)
	}
	f.Add("slug=b", "multipart/form-data; boundary=x", []byte("--x\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.html\"\r\n\r\nhi\r\n--x--\r\n"), true)
	f.Add("slug=c", "text/plain", []byte("x"), false)

	st, err := store.Open(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { st.Close() })
	restore := store.SetPBKDF2IterForTest(2)
	f.Cleanup(restore)
	const token = "fuzz-token"
	h := New(st, Config{AuthToken: token}).Handler()
	f.Fuzz(func(t *testing.T, query, ctype string, body []byte, authed bool) {
		if strings.ContainsAny(query, " #\r\n\t") || strings.ContainsAny(ctype, "\r\n\x00") {
			return
		}
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/publish", bytes.NewReader(body))
		u, err := url.ParseRequestURI("/publish?" + query)
		if err != nil {
			return
		}
		r.URL, r.RequestURI = u, "/publish?"+query
		r.Header.Set("Content-Type", ctype)
		if authed {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		start := time.Now()
		h.ServeHTTP(w, r)
		if el := time.Since(start); el > fuzzRequestBound {
			t.Fatalf("publish took %v", el)
		}
		if w.Code >= 500 && w.Code != http.StatusServiceUnavailable && w.Code != http.StatusInsufficientStorage {
			t.Fatalf("publish %q answered %d: %s", query, w.Code, w.Body.String())
		}
		if !authed {
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("publish without the token answered %d", w.Code)
			}
			return
		}
		if w.Code != http.StatusCreated {
			return
		}
		q := u.Query()
		loc, err := url.Parse(strings.TrimSpace(w.Body.String()))
		if err != nil || loc.Host != "127.0.0.1:8081" && loc.Host != "127.0.0.1:8080" || loc.User != nil {
			t.Fatalf("publish URL %q is not on this instance", w.Body.String())
		}
		if q.Has("site") {
			return
		}
		slug := strings.TrimPrefix(loc.Path, "/")
		if strings.Contains(slug, "/") || slug == "" || slug == "." || slug == ".." {
			t.Fatalf("publish URL %q does not name one slug", loc)
		}
		if want := q.Get("slug"); want != "" && strings.TrimSpace(want) != slug {
			if store.ValidateNamedSlug(slug) != nil {
				t.Fatalf("published under %q, asked for %q", slug, want)
			}
		}
		if q.Get("render") != "" || q.Has("password") || r.Header.Get("X-Demiplane-Password") != "" ||
			strings.HasPrefix(strings.ToLower(ctype), "multipart/") {
			return
		}
		g := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/"+slug, nil)
		gw := httptest.NewRecorder()
		h.ServeHTTP(gw, g)
		if gw.Code == http.StatusOK && !bytes.Equal(gw.Body.Bytes(), body) {
			t.Fatalf("artifact %q reads back %d bytes, published %d", slug, gw.Body.Len(), len(body))
		}
	})
}
