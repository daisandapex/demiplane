// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"net"
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
// It is checked only when scripts/fuzz.sh sets DEMIPLANE_FUZZ_TIMING: plain
// `go test` replays the seeds on machines of any speed and load, where a
// wall-clock limit would only flake.
const fuzzRequestBound = 10 * time.Second

// fuzzTiming reports whether wall-clock bounds are checked (see
// fuzzRequestBound).
func fuzzTiming() bool { return os.Getenv("DEMIPLANE_FUZZ_TIMING") != "" }

// readRequest parses a request the way net/http's server reads one off the
// wire, so a target skips only what the server itself refuses with 400
// before any handler runs. ok is false for such a request.
func readRequest(method, host, target string, hdr [][2]string, body []byte) (r *http.Request, ok bool) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\nHost: %s\r\n", method, target, host)
	for _, h := range hdr {
		fmt.Fprintf(&b, "%s: %s\r\n", h[0], h[1])
	}
	if body != nil {
		fmt.Fprintf(&b, "Content-Length: %d\r\n", len(body))
	}
	b.WriteString("\r\n")
	b.Write(body)
	r, err := http.ReadRequest(bufio.NewReader(&b))
	if err != nil {
		return nil, false
	}
	r.RemoteAddr = "192.0.2.1:40000"
	return r, true
}

// validFieldValue reports whether net/http's server accepts v as a header
// value: no control byte other than a tab.
func validFieldValue(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; c < 0x20 && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}

// pathSeeds are request paths from the canonicalization tests and the fixes
// for encoded dot segments, backslashes and network-path redirects.
var pathSeeds = []string{
	"/", "/a", "/a/", "//a", "///a", "/./a", "/../a", "/a/../b", "/a/./b/",
	"/%2e%2e/a", "/%2E/a", "/a%2fb", "/a%5cb", `/a\b`, "/a%00b", "/a%7fb",
	"/%c2%85", "//evil.example/%2e%2e", "/.//evil.example", "/a/..//evil",
	"/%", "/%zz", "/a?x=1", "/sites/s/a.html", "/_events/a", "/a#b", "/a\tb",
	"http://demi.example//evil", "*",
}

// FuzzCanonicalPaths checks the path canonicalizer that sits in front of
// both muxes: a path is refused, passed on unchanged in canonical form, or
// redirected once to a same-origin canonical path.
//
// Requests are parsed by http.ReadRequest, so the only inputs skipped are
// the ones net/http answers with 400 itself (a space or CR/LF in the
// request-target, a target url.ParseRequestURI refuses); everything the
// server would hand to a handler, including '#', tabs and absolute-form
// targets, reaches the canonicalizer.
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
		if strings.ContainsAny(target, "\r\n") {
			return nil // would end the request line: not one request-target
		}
		r, ok := readRequest(http.MethodGet, "demi.example", target, nil, nil)
		if !ok {
			return nil
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	f.Fuzz(func(t *testing.T, p, query string) {
		target := p
		if query != "" {
			target += "?" + query
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
			// A backslash is a separator to browsers only in the path; the
			// query is kept as sent. The origin is judged by the Location's
			// start, not by url.Parse: a query the client sent with '#' in
			// it comes back as a fragment that url.Parse may refuse ("/?#%",
			// testdata dee5fd3eac46e290), and a browser still resolves that
			// to a path on this origin.
			locPath, _, _ := strings.Cut(loc, "?")
			locPath, _, _ = strings.Cut(locPath, "#")
			if lu, err := url.Parse(loc); err == nil && (lu.Scheme != "" || lu.Host != "") || !strings.HasPrefix(loc, "/") ||
				strings.HasPrefix(loc, "//") || strings.ContainsRune(locPath, '\\') || strings.ContainsAny(loc, "\r\n\t") {
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

// hostKey lowercases a Host value or an --allowed-hosts entry and removes
// its port. A value with no bracket splits with net.SplitHostPort, or is the
// bare host with port "" when that fails. A value with a bracket must be
// "[" IPv6 "]" optionally followed by ":" and a port with no colon; it yields
// the address without brackets. The IPv6 address may end in "%" and a zone:
// the zone must be non-empty and hold no bracket, and it is kept, so
// "[fe80::1%eth0]:80" yields "fe80::1%eth0". Any other bracket yields host
// "", which no entry names.
func hostKey(v string) (host, port string) {
	v = strings.ToLower(v)
	if !strings.ContainsAny(v, "[]") {
		if h, p, err := net.SplitHostPort(v); err == nil {
			return h, p
		}
		return v, ""
	}
	end := strings.IndexByte(v, ']')
	if v[0] != '[' || end < 0 {
		return "", ""
	}
	inner, rest := v[1:end], v[end+1:]
	addr, zone, scoped := strings.Cut(inner, "%")
	if net.ParseIP(addr) == nil || !strings.Contains(addr, ":") || scoped && (zone == "" || strings.Contains(zone, "[")) {
		return "", ""
	}
	switch {
	case rest == "":
		return inner, ""
	case rest[0] == ':' && !strings.ContainsAny(rest[1:], ":[]"):
		return inner, rest[1:]
	}
	return "", ""
}

// fuzzScopedBase is the base URL FuzzHostPolicy configures beside the
// --allowed-hosts list. It names a scoped IPv6 address, so the policy also
// accepts fuzzScopedHost, zone included, on any port.
const (
	fuzzScopedBase = "http://[fe80::1%25eth0]:8080"
	fuzzScopedHost = "fe80::1%eth0"
)

// FuzzHostPolicy checks --allowed-hosts parsing, Host matching and the Host
// sanitizer that guards reflected URLs. A Host is allowed exactly when,
// after lowercasing and port removal (hostKey), it equals one configured
// entry's host and the entry names no port or the request's port (a Host
// without one means the scheme's default port), or it equals the host of
// the scoped base URL, on any port.
func FuzzHostPolicy(f *testing.F) {
	for _, s := range [][2]string{
		{"demi.example", "demi.example"}, {"demi.example", "DEMI.Example"}, {"demi.example:443", "DEMI.example"},
		{"[fd00::1]", "[fd00::1]:8080"}, {"a,b:80", "b"}, {"http://x", "x"},
		{"*.example", "a.example"}, {"user@h", "h"}, {"h", "h\"><script>"},
		{"h", "h:80@evil"}, {"h", "[h]"}, {"", ""}, {"h:8080", "h:8080"}, {"h:8080", "h"},
		{"[fd00::1]", "fd00::1"}, {"h", "h:"},
		{"h", "[h"}, {"h", "h]"}, {"h", "[h]:80"}, {"[h]", "h"},
		{"[::1]", "[::1]"}, {"[::1]", "[::1]:8443"}, {"[::1]", "[::1"},
		{"[::1]:8443", "[::1]:8443"},
		{"h", "[fe80::1%eth0]"}, {"h", "[fe80::1%eth0]:8080"}, {"h", "[FE80::1%ETH0]:80"},
		{"h", "fe80::1%eth0"}, {"h", "[fe80::1%]"}, {"h", "[fe80::1%]:8080"},
		{"h", "[fe80::1%eth]0]"}, {"h", "[fe80::1%eth/0]"}, {"h", "[fe80::1%eth0"},
		{"h", "fe80::1%eth0]"}, {"h", "[fe80::1%eth1]"}, {"[fd00::1]", "[fd00::1%eth0]"},
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
		rh, rp := hostKey(host)
		if rp == "" {
			rp = "80"
			if tls {
				rp = "443"
			}
		}
		want := ""
		for _, e := range list {
			if eh, ep := hostKey(e); rh != "" && eh == rh && (ep == "" || ep == rp) {
				want = e
				break
			}
		}
		if want == "" && len(list) > 0 && rh == fuzzScopedHost {
			want = fuzzScopedBase
		}
		got := hostAllowed(buildHostRules(list, fuzzScopedBase), host, tls)
		if got && want == "" {
			t.Fatalf("host %q (tls %v) allowed by no entry of %q", host, tls, csv)
		}
		if !got && want != "" {
			t.Fatalf("host %q (tls %v) refused though entry %q of %q names it", host, tls, want, csv)
		}
	})
}

// FuzzValidBearer checks the bearer comparison by stated properties, not by
// restating the implementation. For a configured token want (never empty,
// as requireAuth skips the check when no token is set, and with no
// surrounding white space, as a token is one word):
//
//   - "Bearer " in any letter case, then want, with any spaces before or
//     after it, is accepted;
//   - any other token, including one that only extends or shortens want, is
//     refused;
//   - an empty token, with or without spaces, is refused;
//   - the scheme without its space, directly followed by want, is refused;
//   - want with bytes before or after it other than surrounding white space
//     is refused;
//   - another scheme carrying want is refused;
//   - and for an arbitrary header, acceptance implies the header starts with
//     the scheme and a space and contains want.
func FuzzValidBearer(f *testing.F) {
	key := []byte("0123456789abcdef0123456789abcdef")
	f.Add("tok", "tok2", "x", uint8(0), uint8(0), uint8(0), "Bearer tok")
	f.Add("tok", "to", " x", uint8(0x3f), uint8(2), uint8(1), "bearer tok")
	f.Add("tok", "", "\t", uint8(0x15), uint8(1), uint8(3), "BEARER  tok ")
	f.Add("0123", " 0123 ", "0", uint8(0x2a), uint8(0), uint8(0), "Bearertok")
	f.Add("tok", "TOK", "\x00", uint8(1), uint8(0), uint8(0), "Basic tok")
	f.Fuzz(func(t *testing.T, want, other, extra string, caseMask, lead, trail uint8, header string) {
		if want == "" || strings.TrimSpace(want) != want {
			return
		}
		var scheme strings.Builder
		for i, c := range "bearer" {
			if caseMask>>i&1 == 1 {
				c -= 'a' - 'A'
			}
			scheme.WriteRune(c)
		}
		pre := strings.Repeat(" ", int(lead%4))
		post := strings.Repeat(" ", int(trail%4))
		check := func(h string, ok bool) {
			t.Helper()
			if got := validBearer(h, want, key); got != ok {
				t.Fatalf("validBearer(%q, %q) = %v, want %v", h, want, got, ok)
			}
		}
		check(scheme.String()+" "+pre+want+post, true)
		if strings.TrimSpace(other) != want {
			check(scheme.String()+" "+other, false)
			check("Bearer "+other, false)
		}
		check(scheme.String()+" "+pre+post, false)
		check(scheme.String(), false)
		check(scheme.String()+want, false)
		if strings.TrimSpace(extra) != "" {
			check("Bearer "+want+extra, false)
			check("Bearer "+extra+want, false)
		}
		check("Basic "+want, false)
		check("Token "+want, false)
		if validBearer(header, want, key) {
			if len(header) < len("bearer ") || !strings.EqualFold(header[:len("bearer ")], "bearer ") ||
				!strings.Contains(header, want) {
				t.Fatalf("validBearer(%q, %q) accepted a header without the scheme and token", header, want)
			}
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
	var seq [][2]string
	for n, c := range entries {
		seq = append(seq, [2]string{n, c})
	}
	return tarSeq(t, seq, link)
}

// tarSeq writes the entries in the order given, so a seed can put a good
// entry before a refused one (a partial write, then an error).
func tarSeq(t testing.TB, entries [][2]string, link bool) []byte {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, e := range entries {
		n, c := e[0], e[1]
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

// treeOf records every path under dir with its file type.
func treeOf(t testing.TB, dir string) map[string]fs.FileMode {
	t.Helper()
	tree := map[string]fs.FileMode{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		tree[p] = d.Type()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// checkSiteTree fails when an extraction left anything outside sitesRoot
// that was not under parent before (a new path or a changed file type,
// anywhere in parent), or left under sitesRoot a symlink, a special file,
// more bytes than the budget or more files than the entry cap. It holds
// after every extraction, a refused one included: a refusal may come after
// partial writes.
func checkSiteTree(t *testing.T, parent, sitesRoot string, before map[string]fs.FileMode) {
	t.Helper()
	inSites := func(p string) bool {
		return p == sitesRoot || strings.HasPrefix(p, sitesRoot+string(filepath.Separator))
	}
	var total int64
	files := 0
	for p, typ := range treeOf(t, parent) {
		if !inSites(p) {
			if old, ok := before[p]; !ok || old != typ {
				t.Fatalf("extraction created %q (type %v) outside the sites root", p, typ)
			}
			continue
		}
		switch {
		case typ.IsDir():
		case typ.IsRegular():
			fi, err := os.Lstat(p)
			if err != nil {
				t.Fatal(err)
			}
			total += fi.Size()
			files++
		default:
			t.Fatalf("extracted %q of type %v, not a regular file", p, typ)
		}
	}
	if total > fuzzSiteBudget {
		t.Fatalf("site holds %d bytes past a %d byte budget", total, fuzzSiteBudget)
	}
	if files > maxSiteEntries {
		t.Fatalf("%d files past the entry cap of %d", files, maxSiteEntries)
	}
}

// siteFixture is the store FuzzSiteArchive extracts into: a store in its
// own parent directory, so the sites root is a subdirectory and the whole
// parent can be walked for escapes, holding a committed site "other" and two
// sentinel files with known bytes, one beside the sites root and one inside
// "other". tree is the parent's tree once that is in place.
type siteFixture struct {
	parent    string
	sitesRoot string
	st        *store.Store
	sentinels []string
	tree      map[string]fs.FileMode
}

const siteSentinel = "sentinel bytes, unchanged by any extraction\n"

// newSiteFixture builds a siteFixture in a new directory under base.
func newSiteFixture(t testing.TB, base string) *siteFixture {
	t.Helper()
	parent, err := os.MkdirTemp(base, "site")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(parent, "store"))
	if err != nil {
		t.Fatal(err)
	}
	fx := &siteFixture{parent: parent, sitesRoot: filepath.Join(st.Root(), "data", "sites"), st: st}
	other, err := st.NewSiteWriter("other")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.AddFile("index.html", strings.NewReader(siteSentinel)); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Commit(); err != nil {
		t.Fatal(err)
	}
	fx.sentinels = []string{
		filepath.Join(filepath.Dir(fx.sitesRoot), "sentinel"),
		filepath.Join(fx.sitesRoot, "other", "index.html"),
	}
	if err := os.WriteFile(fx.sentinels[0], []byte(siteSentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if bad := fx.changedSentinel(); bad != "" {
		t.Fatalf("sentinel %q not in place", bad)
	}
	fx.tree = treeOf(t, parent)
	return fx
}

// changedSentinel returns the first sentinel that no longer reads back its
// bytes, or "".
func (fx *siteFixture) changedSentinel() string {
	for _, p := range fx.sentinels {
		if b, err := os.ReadFile(p); err != nil || string(b) != siteSentinel {
			return p
		}
	}
	return ""
}

// pristine reports whether the fixture is as newSiteFixture left it: the
// same tree, file types included, and both sentinels intact.
func (fx *siteFixture) pristine(t testing.TB) bool {
	return maps.Equal(treeOf(t, fx.parent), fx.tree) && fx.changedSentinel() == ""
}

// FuzzSiteArchive extracts hostile tar and zip archives into a real store
// and checks the result: every extracted file is a regular file inside the
// sites root, nothing is created anywhere else, the decompressed total holds
// to the budget and the entry cap holds, whether extraction succeeded or
// not. The store is a siteFixture, so the whole parent directory is walked
// for escapes.
//
// The walk sees new paths and changed file types, not new bytes in a file
// that was already there, so the fixture's two sentinel files must read back
// unchanged after every extraction.
//
// Opening a store costs about a hundred times the extraction, and the fuzzer
// minimises each new interesting input by running it thousands of times, so
// a fresh store per input left a timed run stuck minimising one input for
// the whole -fuzzminimizetime. Each worker process therefore keeps its
// fixture across inputs: the extraction is staged and aborted, which leaves
// the fixture as it was, and an input that leaves it otherwise (a failed
// one) gets a new fixture built for the next input, so every input starts
// from the same tree.
func FuzzSiteArchive(f *testing.F) {
	ok := map[string]string{"index.html": "<p>x</p>", "css/a.css": "a{}", "a/b/c.txt": "c"}
	evil := map[string]string{"../escape": "x", "/abs": "x", `a\..\b`: "x", "a/../../b": "x", ".git/config": "x"}
	f.Add(tarOf(f, ok, true), false)
	f.Add(tarOf(f, evil, false), false)
	f.Add(zipOf(f, ok), true)
	f.Add(zipOf(f, evil), true)
	f.Add(tarSeq(f, [][2]string{{"ok.txt", "x"}, {"../../escape", "y"}}, false), false)
	f.Add([]byte("PK\x05\x06"+strings.Repeat("\x00", 18)), true)
	f.Add(bytes.Repeat([]byte{0}, 1024), false)
	overwrite := [][2]string{{"../other/index.html", "overwritten"}, {"../../sentinel", "overwritten"}}
	f.Add(tarSeq(f, overwrite, false), false)
	f.Add(zipOf(f, map[string]string{overwrite[0][0]: "overwritten", overwrite[1][0]: "overwritten"}), true)

	prev := siteDecompressBudget
	siteDecompressBudget = fuzzSiteBudget
	f.Cleanup(func() { siteDecompressBudget = prev })
	base := f.TempDir()
	var fx *siteFixture
	f.Cleanup(func() {
		if fx != nil {
			fx.st.Close()
		}
	})
	f.Fuzz(func(t *testing.T, archive []byte, isZip bool) {
		if fx == nil || !fx.pristine(t) {
			if fx != nil {
				fx.st.Close()
				if err := os.RemoveAll(fx.parent); err != nil {
					t.Fatal(err)
				}
			}
			fx = nil
			fx = newSiteFixture(t, base)
		}
		sw, err := fx.st.NewSiteWriter("fuzzsite")
		if err != nil {
			t.Fatal(err)
		}
		defer sw.Abort()
		before := treeOf(t, fx.parent)
		start := time.Now()
		ra := bytes.NewReader(archive)
		if isZip {
			err = extractZip(sw, ra, int64(len(archive)))
		} else {
			err = extractTar(sw, ra)
		}
		if el := time.Since(start); fuzzTiming() && el > fuzzRequestBound {
			t.Fatalf("extract took %v", el)
		}
		// The staged tree lives under the sites root, so it is checked
		// before any commit; committing would only add database work per
		// input (an SQLite commit each) without reaching more parsing.
		checkSiteTree(t, fx.parent, fx.sitesRoot, before)
		if p := fx.changedSentinel(); p != "" {
			t.Errorf("extraction changed %q", p)
		}
		if err == nil && sw.Files() > maxSiteEntries {
			t.Fatalf("%d files past the entry cap", sw.Files())
		}
	})
}

// publishRefusals are the statuses an authorised publish may be refused
// with: a bad request (query, slug, TTL, body), a missing ?next= target,
// a slug conflict, an oversized body or header. Anything else, 503 and 507
// included, is a failure: the harness sets no quota, no concurrency limit
// and no drain, so neither can be its own doing.
var publishRefusals = map[int]bool{
	http.StatusBadRequest:                  true,
	http.StatusNotFound:                    true,
	http.StatusConflict:                    true,
	http.StatusRequestEntityTooLarge:       true,
	http.StatusRequestHeaderFieldsTooLarge: true,
}

// publishMustAccept are ordinary, authorised publishes FuzzPublish requires
// to succeed: plain text under a generated, a named and a private slug, a
// TTL, a rendered page, a series member, a filename and a multipart upload.
var publishMustAccept = []struct{ query, ctype, body string }{
	{"", "text/plain", "hello"},
	{"slug=named", "text/plain", "hello"},
	{"private=1", "text/plain", "hello"},
	{"ttl=1h&slug=short", "text/plain", "hello"},
	{"render=md&slug=doc", "text/markdown", "# Title\n\nbody\n"},
	{"series=notes&slug=notes-1", "text/plain", "hello"},
	{"filename=a.txt&slug=file", "text/plain", "hello"},
	{"slug=upload", "multipart/form-data; boundary=x", "--x\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.html\"\r\n\r\nhi\r\n--x--\r\n"},
}

// FuzzPublish drives POST /publish with hostile query strings, content types
// and bodies through the combined handler, in process, against a fresh store
// per input (removed with the input's temporary directory, so the disk does
// not grow over a run). A publish without the token is refused with 401; an
// authorised one is accepted with 201 or refused with a status in
// publishRefusals; an accepted one names a valid slug on this instance and
// reads back 200 with the published bytes.
//
// Requests are parsed by http.ReadRequest, so only what net/http itself
// refuses is skipped: a CR or LF in the query (it would end the request
// line) or a control byte in the Content-Type. The byte-for-byte readback is
// skipped where the stored bytes are not the body by design: a rendered
// (?render=) page, a site archive, a multipart upload (the stored file is
// one part) and a password-protected artifact (the readback needs the
// password exchange).
//
// A request that names a slug and is accepted is published under exactly
// that slug. The requests in publishMustAccept are also seeds and must be
// accepted with 201, so a server that refuses every publish fails the seed
// corpus instead of passing as "refused with an allowed status".
func FuzzPublish(f *testing.F) {
	for _, q := range []string{
		"", "slug=a", "slug=../x", "slug=publish", "slug=a%2Fb", "private=1", "private=1&slug=a",
		"ttl=1h", "ttl=-1d", "ttl=" + strings.Repeat("9", 70), "render=md", "render=md&slug=doc",
		"filename=x.html", "filename=a.txt&slug=b", "series=s&slug=s-1", "reply=form&slug=r",
		"reply=question&next=r2&slug=r", "next=//evil", "site=s", "password=", "fmt=zip&site=s",
		"slug=a#b", "slug=a\tb", "slug= a ",
	} {
		f.Add(q, "text/plain", []byte("hello <script>x</script>"), true)
	}
	f.Add("slug=b", "multipart/form-data; boundary=x", []byte("--x\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.html\"\r\n\r\nhi\r\n--x--\r\n"), true)
	f.Add("slug=c", "text/plain", []byte("x"), false)
	f.Add("slug=d", "", []byte{}, true)
	for _, p := range publishMustAccept {
		f.Add(p.query, p.ctype, []byte(p.body), true)
	}

	restore := store.SetPBKDF2IterForTest(2)
	f.Cleanup(restore)
	const token = "fuzz-token"
	f.Fuzz(func(t *testing.T, query, ctype string, body []byte, authed bool) {
		if strings.ContainsAny(query, "\r\n") || !validFieldValue(ctype) {
			return
		}
		hdr := [][2]string{{"Content-Type", ctype}}
		if authed {
			hdr = append(hdr, [2]string{"Authorization", "Bearer " + token})
		}
		r, ok := readRequest(http.MethodPost, "127.0.0.1:8080", "/publish?"+query, hdr, body)
		if !ok {
			return
		}
		st, err := store.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		h := New(st, Config{AuthToken: token}).Handler()
		w := httptest.NewRecorder()
		start := time.Now()
		h.ServeHTTP(w, r)
		if el := time.Since(start); fuzzTiming() && el > fuzzRequestBound {
			t.Fatalf("publish took %v", el)
		}
		if !authed {
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("publish without the token answered %d", w.Code)
			}
			return
		}
		if w.Code != http.StatusCreated {
			for _, p := range publishMustAccept {
				if p.query == query && p.ctype == ctype && p.body == string(body) {
					t.Fatalf("known-good publish %q refused with %d: %s", query, w.Code, w.Body.String())
				}
			}
			if !publishRefusals[w.Code] {
				t.Fatalf("publish %q answered %d: %s", query, w.Code, w.Body.String())
			}
			return
		}
		q := r.URL.Query()
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
		if want := q.Get("slug"); want != "" && slug != want {
			t.Fatalf("published under %q, asked for %q", slug, want)
		}
		if q.Get("render") != "" || q.Has("password") || r.Header.Get("X-Demiplane-Password") != "" ||
			strings.HasPrefix(strings.ToLower(strings.TrimSpace(ctype)), "multipart/") {
			return
		}
		g, ok := readRequest(http.MethodGet, loc.Host, loc.RequestURI(), nil, nil)
		if !ok {
			t.Fatalf("publish URL %q is not a request net/http accepts", loc)
		}
		gw := httptest.NewRecorder()
		h.ServeHTTP(gw, g)
		if gw.Code != http.StatusOK {
			t.Fatalf("artifact %q published with 201 reads back %d", loc, gw.Code)
		}
		if !bytes.Equal(gw.Body.Bytes(), body) {
			t.Fatalf("artifact %q reads back %d bytes, published %d", loc, gw.Body.Len(), len(body))
		}
	})
}

// jsonReadBack is what encoding/json gives back for a string it stored:
// invalid UTF-8 comes back as U+FFFD.
func jsonReadBack(t *testing.T, v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out string
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// FuzzRenderSpec checks the render spec stored with a retained source. What
// a publish writes reads back unchanged on rebake (string fields as
// encoding/json reads them back, the publish time to the nanosecond), a
// publish time outside RFC 3339's years is not stored at all, and a spec
// read back from a damaged or foreign record either fails to decode (the
// rebake then uses the defaults) or decodes to a spec whose stored form is
// a fixed point: re-encoding it and reading it again gives the same bytes.
func FuzzRenderSpec(f *testing.F) {
	f.Add("doc", "series", "doc", "doc-2", int64(1_700_000_000), int64(5), []byte(`{"named_slug":"a","published":"2026-01-02T03:04:05Z"}`))
	f.Add("", "", "", "", int64(0), int64(0), []byte(`{"published":"not a time"}`))
	f.Add("a", "", "", "", int64(-62135596800), int64(0), []byte(`[]`))
	f.Add("\xff", "", "", "", int64(1<<39), int64(0), []byte(`{"published":"2026-01-02T03:04:05+05:30","series":"\ud800"}`))
	f.Fuzz(func(t *testing.T, named, series, reply, next string, sec, nsec int64, stored []byte) {
		var junk renderSpec
		if json.Unmarshal(stored, &junk) == nil {
			enc := marshalRenderSpec(junk)
			if enc == "" {
				t.Fatalf("decoded spec %+v from %q does not encode again", junk, stored)
			}
			var again renderSpec
			if err := json.Unmarshal([]byte(enc), &again); err != nil {
				t.Fatalf("re-encoded spec %q does not decode: %v", enc, err)
			}
			if enc2 := marshalRenderSpec(again); enc2 != enc {
				t.Fatalf("stored spec is not a fixed point: %q -> %q -> %q", stored, enc, enc2)
			}
		}

		pub := time.Unix(sec%(1<<40), nsec%1e9).UTC()
		spec := renderSpecFor(named, series, reply, next, pub)
		enc := marshalRenderSpec(spec)
		if y := pub.Year(); y < 0 || y > 9999 {
			if enc != "" {
				t.Fatalf("spec with year %d stored as %q", y, enc)
			}
			return // not stored: the source is retained without a spec
		}
		if enc == "" {
			t.Fatalf("spec %+v did not marshal", spec)
		}
		var back renderSpec
		if err := json.Unmarshal([]byte(enc), &back); err != nil {
			t.Fatalf("stored spec %q does not read back: %v", enc, err)
		}
		if back.NamedSlug != jsonReadBack(t, named) || back.Series != jsonReadBack(t, series) ||
			back.ReplySlug != jsonReadBack(t, reply) || back.ReplyNext != jsonReadBack(t, next) ||
			!back.Published.Equal(pub) {
			t.Fatalf("spec round trip: %+v -> %q -> %+v", spec, enc, back)
		}
	})
}
