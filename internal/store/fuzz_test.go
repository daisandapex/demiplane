// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"net/url"
	"path/filepath"
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

// FuzzPasswordHash checks the stored-hash parser: a hash it accepts on Put
// verifies at the store's own work factor, and a hash read back never matches
// a password it was not made from.
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
	f.Add("pbkdf2-sha256$600000$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "x")
	f.Add("pbkdf2-sha256$-1$a$b", "x")
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
		if encoded == "" && PasswordMatches(encoded, pw) {
			t.Fatal("empty hash matched")
		}
		_ = PasswordMatches(encoded, pw)

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
// against a real store: every value must reach SQLite as a bound parameter,
// so the query never fails, never returns another owner's or a private
// artifact, and only returns artifacts after the cursor.
func FuzzListPage(f *testing.F) {
	s, err := Open(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { s.Close() })
	for _, o := range []PutOptions{{Slug: "alpha"}, {Slug: "beta"}, {Slug: "gamma"}, {Private: true}} {
		if _, err := s.Put(o, strings.NewReader("x")); err != nil {
			f.Fatal(err)
		}
	}
	now := strconv.FormatInt(time.Now().Unix(), 10)
	for _, c := range []string{now + ".alpha", now + ".a' OR '1'='1", "0.a", now + ".zzz", "9999999999.a"} {
		f.Add(c, DefaultOwner, uint8(2))
	}
	f.Add("1.a", "local' OR 1=1 --", uint8(200))
	f.Add("1.a", "x\") UNION SELECT * FROM artifacts --", uint8(0))
	f.Fuzz(func(t *testing.T, cursor, owner string, limit uint8) {
		var after *ListCursor
		if c, err := ParseListCursor(cursor); err == nil {
			after = &c
		}
		arts, next, err := s.ListPage(context.Background(), owner, after, int(limit))
		if err != nil {
			t.Fatalf("ListPage(%q, %q): %v", owner, cursor, err)
		}
		if owner != DefaultOwner && len(arts) > 0 {
			t.Fatalf("owner %q listed %d artifacts of another owner", owner, len(arts))
		}
		if len(arts) > max(int(limit), 1) {
			t.Fatalf("page of %d past limit %d", len(arts), limit)
		}
		for _, a := range arts {
			if a.Private || a.Owner != owner {
				t.Fatalf("listed %+v for owner %q", a, owner)
			}
			if after != nil {
				c := a.CreatedAt.Unix()
				if c > after.Created || (c == after.Created && a.Slug <= after.Slug) {
					t.Fatalf("listed %q (%d) not after cursor %+v", a.Slug, c, *after)
				}
			}
		}
		_ = next
	})
}
