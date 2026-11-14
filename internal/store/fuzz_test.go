// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// slugSeeds are names from the slug, series and site path tests and from
// earlier traversal fixes.
var slugSeeds = []string{
	"", ".", "..", "...", "a", "a.b", "a..b", ".hidden", "-x", "_x", "x-",
	"publish", "list", "docs", "help.json", "llms.txt", "a/b", "../etc/passwd",
	`a\b`, "a b", "a%2Fb", "a\x00b", "é", strings.Repeat("a", 128),
	strings.Repeat("a", 129), "report.v2_final-3", "A9",
}

// underRoot reports whether joining name onto root stays strictly inside it.
func underRoot(root, name string) bool {
	j := filepath.Join(root, name)
	rel, err := filepath.Rel(root, j)
	return err == nil && rel != "." && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// FuzzValidateNamedSlug checks that an accepted slug is one URL-safe path
// segment that names a child of any directory it is joined to.
func FuzzValidateNamedSlug(f *testing.F) {
	for _, s := range slugSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		err := ValidateNamedSlug(name)
		serr := ValidateSeries(name)
		if serr != nil && err == nil {
			t.Fatalf("slug %q accepted but refused as a series: %v", name, serr)
		}
		// The reject side: a name that is empty, a dot segment, longer
		// than 128 bytes, or holds a NUL, a separator or a backslash is
		// refused, as a slug and as a series.
		if name == "" || name == "." || name == ".." || len(name) > 128 ||
			strings.ContainsAny(name, "\x00/\\") {
			if err == nil || serr == nil {
				t.Fatalf("name %q accepted (slug err %v, series err %v)", name, err, serr)
			}
		}
		if err != nil {
			return
		}
		if len(name) > 128 {
			t.Fatalf("slug %q longer than 128 bytes", name)
		}
		if strings.ContainsAny(name, `/\%?#&"'<> `) || name == "." || name == ".." {
			t.Fatalf("slug %q carries a separator or URL metacharacter", name)
		}
		if url.PathEscape(name) != name {
			t.Fatalf("slug %q changes when path-escaped", name)
		}
		if !underRoot("/srv/store/blobs", name) {
			t.Fatalf("slug %q escapes the directory it is joined to", name)
		}
		if filepath.Base(filepath.Join("/srv", name)) != name {
			t.Fatalf("slug %q is not a single path element", name)
		}
		if reservedSlugs[name] {
			t.Fatalf("reserved slug %q accepted", name)
		}
	})
}

// FuzzCleanSiteRelPath checks the archive entry path cleaner: an accepted
// path stays inside the site directory, is already clean, has only valid
// segments and is a fixed point of the cleaner.
func FuzzCleanSiteRelPath(f *testing.F) {
	for _, s := range slugSeeds {
		f.Add(s)
	}
	for _, s := range []string{
		"index.html", "a/b/c.css", "a/./b", "a//b", "./a", "a/../b", "/abs",
		`a\..\b`, "a/.git/config", "a/b/", strings.Repeat("a/", 33) + "x",
		strings.Repeat("a/", 31) + "x",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		clean, err := cleanSiteRelPath(name)
		// The reject side: an absolute path, a ".." segment, a NUL or a
		// backslash anywhere, or more than the segment cap once empty and
		// "." segments are dropped, is refused.
		kept := 0
		dotdot := false
		for _, seg := range strings.Split(name, "/") {
			dotdot = dotdot || seg == ".."
			if seg != "" && seg != "." {
				kept++
			}
		}
		if strings.HasPrefix(name, "/") || dotdot || strings.ContainsAny(name, "\x00\\") || kept > maxSitePathSegments {
			if err == nil {
				t.Fatalf("site path %q accepted as %q", name, clean)
			}
			return
		}
		if err != nil {
			return
		}
		root := "/srv/store/data/sites/site"
		if !underRoot(root, clean) || !withinDir(root, filepath.Join(root, clean)) {
			t.Fatalf("site path %q (clean %q) escapes the site", name, clean)
		}
		if filepath.Clean(clean) != clean || strings.HasPrefix(clean, "/") {
			t.Fatalf("clean path %q is not clean", clean)
		}
		segs := strings.Split(clean, "/")
		if len(segs) > maxSitePathSegments {
			t.Fatalf("clean path %q has %d segments", clean, len(segs))
		}
		for _, seg := range segs {
			if !namedSlugRe.MatchString(seg) {
				t.Fatalf("clean path %q has invalid segment %q", clean, seg)
			}
		}
		again, err := cleanSiteRelPath(clean)
		if err != nil || again != clean {
			t.Fatalf("cleaner is not idempotent: %q -> %q -> %q (%v)", name, clean, again, err)
		}
	})
}

// wellFormedHash reports whether encoded has the shape hashPassword writes,
// read with the standard library: the scheme, a positive decimal
// iteration count, and base64 salt and key of the stored lengths.
func wellFormedHash(encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	if n, err := strconv.ParseUint(parts[1], 10, 63); err != nil || n == 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) != pbkdf2SaltLen {
		return false
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[3])
	return err == nil && len(key) == pbkdf2KeyLen
}

// FuzzPasswordHash checks the stored-hash parser and verifier: a hash it
// accepts on Put is at the store's own work factor; a malformed hash (wrong
// shape, a count that is not a positive number, salt or key of the wrong
// length) never verifies any password, as PasswordMatches promises; a
// well-formed hash at another count, as an older work factor left in the
// store, verifies the password it was made from and no other; and a fresh
// hash verifies only its own password.
func FuzzPasswordHash(f *testing.F) {
	restore := SetPBKDF2IterForTest(2)
	f.Cleanup(restore)
	good, err := hashPassword("seed")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(good, "seed")
	f.Add(good, "seed ")
	f.Add("pbkdf2-sha256$2$$", "")
	f.Add("pbkdf2-sha256$2$AAAAAAAAAAAAAAAAAAAAAA$AA", "")
	f.Add("pbkdf2-sha256$600000$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "x")
	f.Add("pbkdf2-sha256$-1$a$b", "x")
	f.Add("pbkdf2-sha256$0$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "x")
	f.Add("pbkdf2-sha256$99999999999999999999$a$b", "x")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, encoded, pw string) {
		parts := strings.Split(encoded, "$")
		if len(parts) == 4 {
			// The verifier honours the iteration count in a stored record,
			// which only Put (validPasswordHash) writes. Keep the fuzzer off
			// counts that would only measure PBKDF2.
			if n, err := strconv.Atoi(parts[1]); err == nil && n > 1000 {
				if validPasswordHash(encoded) {
					t.Fatalf("hash with %d iterations accepted at a work factor of %d", n, pbkdf2Iter)
				}
				return
			}
		}
		if validPasswordHash(encoded) && parts[1] != strconv.Itoa(pbkdf2Iter) {
			t.Fatalf("hash %q accepted at another work factor", encoded)
		}
		if validPasswordHash(encoded) && !wellFormedHash(encoded) {
			t.Fatalf("malformed hash %q accepted for Put", encoded)
		}
		if !wellFormedHash(encoded) {
			for _, cand := range []string{pw, "", "x", pw + "x"} {
				if PasswordMatches(encoded, cand) {
					t.Fatalf("malformed hash %q verified password %q", encoded, cand)
				}
			}
		}

		// A well-formed hash at another count, built here with the
		// standard library, verifies exactly its own password.
		salt := []byte("0123456789abcdef")
		key, err := pbkdf2.Key(sha256.New, pw, salt, 3, pbkdf2KeyLen)
		if err != nil {
			t.Fatal(err)
		}
		enc := base64.RawStdEncoding.EncodeToString
		old := fmt.Sprintf("pbkdf2-sha256$3$%s$%s", enc(salt), enc(key))
		if !PasswordMatches(old, pw) || PasswordMatches(old, pw+"x") {
			t.Fatalf("hash at count 3 of %q verifies wrongly", pw)
		}

		h, err := hashPassword(pw)
		if err != nil {
			t.Fatal(err)
		}
		if !validPasswordHash(h) || !PasswordMatches(h, pw) {
			t.Fatalf("fresh hash of %q does not verify", pw)
		}
		// PBKDF2-HMAC zero-pads a short key to the hash block size, so a
		// password and the same password with trailing NUL bytes derive the
		// same key ("" and "\x00" match each other). That is the KDF, not the
		// parser, and still needs the password, so only a dropped non-NUL
		// byte must change the result.
		dropped := pw != "" && pw[len(pw)-1] != 0 && PasswordMatches(h, pw[:len(pw)-1])
		if PasswordMatches(h, pw+"x") || dropped {
			t.Fatalf("hash of %q matched another password", pw)
		}
	})
}

// FuzzParseListCursor checks the /list cursor round trip.
func FuzzParseListCursor(f *testing.F) {
	for _, s := range []string{"0.a", "1700000000.slug", "-1.a", "1.", ".a", "1.a.b", "x.y",
		"99999999999999999999.a", "1." + strings.Repeat("a", 256), "+1.a", "01.a"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		c, err := ParseListCursor(v)
		// The reject side: no '.', an empty or over-long (255 byte) slug,
		// or a creation time that is not a decimal number of seconds at or
		// after the epoch is refused.
		ts, slug, ok := strings.Cut(v, ".")
		body := ts
		if ts != "" && (ts[0] == '+' || ts[0] == '-') {
			body = ts[1:]
		}
		digits := body != "" && strings.Trim(body, "0123456789") == ""
		negative := strings.HasPrefix(ts, "-") && strings.Trim(body, "0") != ""
		if !ok || slug == "" || len(slug) > 255 || !digits || negative {
			if err == nil {
				t.Fatalf("cursor %q accepted as %+v", v, c)
			}
			return
		}
		if err != nil {
			return
		}
		if c.Created < 0 || c.Slug == "" || len(c.Slug) > 255 {
			t.Fatalf("cursor %q parsed out of range: %+v", v, c)
		}
		again, err := ParseListCursor(c.String())
		if err != nil || again != c {
			t.Fatalf("cursor round trip: %q -> %+v -> %q -> %+v (%v)", v, c, c.String(), again, err)
		}
	})
}

// FuzzListPage drives the listing query with hostile cursors and owners
// against a real store. Each input adds a public and a private artifact
// owned by the fuzzed owner (and deletes them again, so the store stays the
// same size); the setup holds three public artifacts of DefaultOwner and
// one of another owner. The page must be exactly the expected rows: the
// owner's public artifacts after the cursor, newest first then by slug, up
// to the limit. Every value must reach SQLite as a bound parameter, so the
// query never fails and never returns another owner's or a private
// artifact.
func FuzzListPage(f *testing.F) {
	s, err := Open(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { s.Close() })
	type row struct {
		slug, owner string
		created     int64
	}
	var rows []row
	put := func(tb testing.TB, slug, owner string, private bool) row {
		a, err := s.Put(PutOptions{Slug: slug, Private: private}, strings.NewReader("x"))
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE artifacts SET owner = ? WHERE slug = ?`, owner, a.Slug); err != nil {
			tb.Fatal(err)
		}
		return row{a.Slug, owner, a.CreatedAt.Unix()}
	}
	for _, sl := range []string{"alpha", "beta", "gamma"} {
		rows = append(rows, put(f, sl, DefaultOwner, false))
	}
	rows = append(rows, put(f, "delta", "other", false))
	put(f, "", DefaultOwner, true)
	now := strconv.FormatInt(time.Now().Unix(), 10)
	for _, c := range []string{now + ".alpha", now + ".a' OR '1'='1", "0.a", now + ".zzz", "9999999999.a", ""} {
		f.Add(c, DefaultOwner, uint8(2))
		f.Add(c, "other", uint8(5))
	}
	f.Add("1.a", "local' OR 1=1 --", uint8(200))
	f.Add("9999999999.a", "x\") UNION SELECT * FROM artifacts --", uint8(0))
	n := 0
	f.Fuzz(func(t *testing.T, cursor, owner string, limit uint8) {
		var after *ListCursor
		if c, err := ParseListCursor(cursor); err == nil {
			after = &c
		}
		n++
		mine := put(t, fmt.Sprintf("fz-%d", n), owner, false)
		hidden := put(t, "", owner, true)
		defer func() {
			for _, sl := range []string{mine.slug, hidden.slug} {
				if err := s.Delete(sl); err != nil {
					t.Fatal(err)
				}
			}
		}()
		var want []string
		cands := append(slices.Clone(rows), mine)
		slices.SortFunc(cands, func(a, b row) int {
			if a.created != b.created {
				return int(b.created - a.created)
			}
			return strings.Compare(a.slug, b.slug)
		})
		lim := max(int(limit), 1)
		for _, r := range cands {
			if r.owner != owner || len(want) == lim {
				continue
			}
			if after != nil && (r.created > after.Created || r.created == after.Created && r.slug <= after.Slug) {
				continue
			}
			want = append(want, r.slug)
		}
		arts, _, err := s.ListPage(context.Background(), owner, after, int(limit))
		if err != nil {
			t.Fatalf("ListPage(%q, %q): %v", owner, cursor, err)
		}
		var got []string
		for _, a := range arts {
			if a.Private || a.Owner != owner {
				t.Fatalf("listed %+v for owner %q", a, owner)
			}
			got = append(got, a.Slug)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("ListPage(%q, %q, %d) = %q, want %q", owner, cursor, limit, got, want)
		}
	})
}
