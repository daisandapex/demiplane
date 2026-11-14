// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

var (
	// linkAttrRe finds the navigation targets the reply pages write: href
	// values and meta-refresh URLs.
	linkAttrRe = regexp.MustCompile(`(?i)(?:href="|url=)([^"]*)"`)
	tagNameRe  = regexp.MustCompile(`<(/?[a-zA-Z][a-zA-Z0-9-]*)`)
)

// sameOriginPath fails unless target, as a browser reads it, is a path on
// the origin that served the page.
func sameOriginPath(t *testing.T, what, target string) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil ||
		!strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") ||
		strings.ContainsAny(target, "\\\t\r\n") {
		t.Fatalf("%s %q leaves the origin", what, target)
	}
	base, _ := url.Parse("http://demi.example/answer/report")
	if r := base.ResolveReference(u); r.Host != "demi.example" {
		t.Fatalf("%s %q resolves to host %q", what, target, r.Host)
	}
}

// FuzzNextRedirect drives the forward-flow wait endpoint the open-redirect
// scan finding points at: GET /answer/{slug}/next?to=. Whatever ?to= holds,
// the redirect and every link on the wait page stay on this origin.
func FuzzNextRedirect(f *testing.F) {
	for _, s := range []string{
		"lesson-02", "report", "nope", "..%2Fetc", "//evil.example", "/\\evil.example",
		"https://evil.example", "evil.example", "%2F%2Fevil", " lesson-02 ", "lesson-02\r\nX: y",
		"a\\b", "javascript:alert(1)", "lesson-03",
	} {
		f.Add(s)
	}
	st, err := store.Open(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { st.Close() })
	for _, s := range []string{"report", "lesson-02"} {
		if _, err := st.Put(store.PutOptions{Slug: s, Replies: true}, strings.NewReader("x")); err != nil {
			f.Fatal(err)
		}
	}
	h := &fakeHost{st: st, dataDir: f.TempDir()}
	m := &Module{}
	m.setLimits(openLimits())
	mux := http.NewServeMux()
	m.Routes(mux, h)
	f.Cleanup(func() { m.rs.close() })
	f.Fuzz(func(t *testing.T, to string) {
		r := httptest.NewRequest(http.MethodGet, "http://demi.example/answer/report/next", nil)
		r.URL.RawQuery = url.Values{"to": {to}}.Encode()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		switch w.Code {
		case http.StatusFound:
			loc := w.Header().Get("Location")
			sameOriginPath(t, "redirect", loc)
			if store.ValidateNamedSlug(strings.TrimPrefix(loc, "/")) != nil {
				t.Fatalf("redirect %q does not name one slug", loc)
			}
		case http.StatusOK:
			for _, m := range linkAttrRe.FindAllStringSubmatch(w.Body.String(), -1) {
				sameOriginPath(t, "link", strings.ReplaceAll(m[1], "&amp;", "&"))
			}
		case http.StatusBadRequest, http.StatusNotFound:
		default:
			t.Fatalf("?to=%q answered %d", to, w.Code)
		}
	})
}

// FuzzAnswerPage checks the server-composed answer page: the reader's
// message and recorded answer are escaped, so whatever they hold the page
// has the same markup as with plain text, and its links stay on this origin.
func FuzzAnswerPage(f *testing.F) {
	f.Add("report", "lesson-02", "msg", "<script>alert(1)</script>", true)
	f.Add("report", "", "\"><img src=x onerror=alert(1)>", "</blockquote><a href=//evil>", false)
	f.Add("a", "//evil", "&lt;", "&#60;script&#62;", true)
	f.Fuzz(func(t *testing.T, slug, next, message, recorded string, ok bool) {
		if store.ValidateNamedSlug(slug) != nil {
			slug = "report"
		}
		next = forwardSlug(slug, next)
		page := answerPage(slug, ok, message, recorded, next)
		// An empty answer is not echoed, so compare like with like.
		plainRecorded := "r"
		if recorded == "" {
			plainRecorded = ""
		}
		plain := answerPage(slug, ok, "m", plainRecorded, next)
		got, want := tagNameRe.FindAllString(page, -1), tagNameRe.FindAllString(plain, -1)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("reader text changed the page markup:\n%v\n%v", got, want)
		}
		for _, m := range linkAttrRe.FindAllStringSubmatch(page, -1) {
			sameOriginPath(t, "link", strings.ReplaceAll(m[1], "&amp;", "&"))
		}
	})
}

// FuzzParseSubmission checks the submission parser on hostile bodies under
// the request cap: it never reads past the cap, and a kind it returns is
// already folded.
func FuzzParseSubmission(f *testing.F) {
	f.Add("application/json", []byte(`{"kind":"Approve","body":"x","token":"t"}`))
	f.Add("application/json; charset=utf-8", []byte(`{"kind":"comment"} {"x":1}`))
	f.Add("application/json", []byte(`{"body":"`+strings.Repeat("a", 2000)+`"}`))
	f.Add("application/x-www-form-urlencoded", []byte("kind=+Defer+&body=%3Cscript%3E&token=t"))
	f.Add("multipart/form-data; boundary=x", []byte("--x\r\nContent-Disposition: form-data; name=\"kind\"\r\n\r\napprove\r\n--x--\r\n"))
	f.Add("", []byte("kind=approve"))
	const cap = 1024
	f.Fuzz(func(t *testing.T, ctype string, body []byte) {
		if strings.ContainsAny(ctype, "\r\n") {
			return
		}
		r := httptest.NewRequest(http.MethodPost, "http://demi.example/answer/report", bytes.NewReader(body))
		r.Header.Set("Content-Type", ctype)
		w := httptest.NewRecorder()
		counted := &countingReader{r: r.Body}
		r.Body = http.MaxBytesReader(w, counted, cap)
		start := time.Now()
		kind, _, _, _, err := parseSubmission(r)
		if el := time.Since(start); el > 2*time.Second {
			t.Fatalf("parse took %v", el)
		}
		if counted.n > cap+1 {
			t.Fatalf("parser read %d bytes past a %d byte cap", counted.n, cap)
		}
		if err == nil && kind != strings.ToLower(strings.TrimSpace(kind)) {
			t.Fatalf("kind %q not folded", kind)
		}
	})
}

type countingReader struct {
	r interface{ Read([]byte) (int, error) }
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func (c *countingReader) Close() error { return nil }

// FuzzReplyList checks GET /replies' query builder against a real reply
// store: the slug, status and cursor reach SQLite only as bound values, so
// every row returned matches the filter and follows the cursor.
func FuzzReplyList(f *testing.F) {
	rs, err := openReplyStore(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { rs.close() })
	now := time.Unix(1_700_000_000, 0)
	for i, s := range []string{"report", "report", "other", "x' OR '1'='1"} {
		if _, err := rs.add(s, "comment", "b", now.Add(time.Duration(i)*time.Second)); err != nil {
			f.Fatal(err)
		}
	}
	f.Add("report", "pending", "1700000002.3", uint8(10))
	f.Add("report' OR '1'='1", "all", "", uint8(10))
	f.Add("", "read", "1700000009.99", uint8(1))
	f.Add("x' OR '1'='1", "pending') OR 1=1 --", "9223372036854775807.1", uint8(0))
	f.Fuzz(func(t *testing.T, slug, status, cursor string, limit uint8) {
		var after *listCursor
		if c, err := parseListCursor(cursor); err == nil {
			after = &c
			if again, err := parseListCursor(c.String()); err != nil || again != c {
				t.Fatalf("cursor round trip %q -> %v -> %v (%v)", cursor, c, again, err)
			}
		}
		lim := int(limit%100) + 1
		got, _, err := rs.list(slug, status, after, lim)
		if err != nil {
			t.Fatalf("list(%q, %q, %q): %v", slug, status, cursor, err)
		}
		if len(got) > lim {
			t.Fatalf("page of %d past limit %d", len(got), lim)
		}
		for _, r := range got {
			if slug != "" && r.Slug != slug {
				t.Fatalf("slug filter %q returned %q", slug, r.Slug)
			}
			if status != "" && status != "all" && r.Status != status {
				t.Fatalf("status filter %q returned %q", status, r.Status)
			}
			if after != nil {
				c := r.CreatedAt.Unix()
				if c > after.created || (c == after.created && r.ID >= after.id) {
					t.Fatalf("reply %d (%d) not after cursor %v", r.ID, c, *after)
				}
			}
		}
	})
}
