// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

func manageStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
func managePut(t *testing.T, st *store.Store, opts store.PutOptions) store.Artifact {
	t.Helper()
	a, err := st.Put(opts, strings.NewReader("artifact bytes"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func manageSite(t *testing.T, st *store.Store, name string) {
	t.Helper()
	sw, err := st.NewSiteWriter(name)
	if err != nil {
		t.Fatal(err)
	}
	defer sw.Abort()
	if err := sw.AddFile("index.html", strings.NewReader("site bytes")); err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Commit(); err != nil {
		t.Fatal(err)
	}
}
func manageRequest(h http.Handler, method, path, token string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://control.test"+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type managePage struct {
	Artifacts []listItem `json:"artifacts"`
	Count     int        `json:"count"`
	Next      string     `json:"next"`
}

func manageDecode(t *testing.T, r *httptest.ResponseRecorder) managePage {
	t.Helper()
	if r.Code != 200 {
		t.Fatalf("status %d: %s", r.Code, r.Body)
	}
	var page managePage
	if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Count != len(page.Artifacts) {
		t.Fatalf("count mismatch: %+v", page)
	}
	return page
}

func TestManageListMetadata(t *testing.T) {
	st := manageStore(t)
	raw := managePut(t, st, store.PutOptions{Slug: "raw"})
	form := managePut(t, st, store.PutOptions{Slug: "form", Replies: true})
	rendered := managePut(t, st, store.PutOptions{Slug: "rendered", Replies: true, Series: "lessons", TTL: time.Hour, Password: "secret-hash", Source: []byte("# doc"), RenderSpec: `{"reply_slug":"rendered","reply_next":"secret-destination","reply_token":"secret-token","unknown":"secret-field"}`})
	h := New(st, Config{}).ControlHandler()
	rec := manageRequest(h, "GET", "/list", "", nil)
	page := manageDecode(t, rec)
	if len(page.Artifacts) != 3 {
		t.Fatal(page)
	}
	for _, a := range page.Artifacts {
		if a.Slug == form.Slug && (a.Render != "raw" || !a.Reply) {
			t.Fatal(a)
		}
		if a.Kind != "artifact" {
			t.Fatalf("kind: %+v", a)
		}
		if a.Slug == raw.Slug && (a.Render != "raw" || a.Reply || a.Series != "" || a.ExpiresAt != "") {
			t.Fatal(a)
		}
		if a.Slug == rendered.Slug && (a.Render != "md" || !a.Reply || a.Series != "lessons" || a.ExpiresAt != rendered.ExpiresAt.Format(time.RFC3339) || !a.Password) {
			t.Fatal(a)
		}
	}
	var body struct{ Artifacts []map[string]any }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, k := range strings.Fields("slug url filename content_type size created_at owner private password expires_at series kind render reply") {
		allowed[k] = true
	}
	for _, item := range body.Artifacts {
		if len(item) != len(allowed) {
			t.Fatalf("missing or extra fields: %v", item)
		}
		for key := range item {
			if !allowed[key] {
				t.Fatalf("non-allowlisted field %s", key)
			}
		}
	}
	for _, secret := range []string{"secret-destination", "secret-token", "secret-hash", "secret-field", "render_spec", "reply_slug"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
}

func TestManagePublicList(t *testing.T) {
	st := manageStore(t)
	managePut(t, st, store.PutOptions{Slug: "public-doc"})
	private := managePut(t, st, store.PutOptions{Private: true})
	managePut(t, st, store.PutOptions{Slug: "expired-doc", TTL: time.Nanosecond})
	manageSite(t, st, "site-only")
	for _, token := range []string{"", "owner"} {
		h := New(st, Config{AuthToken: token}).ControlHandler()
		for _, path := range []string{"/list", "/list?all=0", "/list?all=true", "/gallery"} {
			rec := manageRequest(h, "GET", path, token, nil)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "public-doc") {
				t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
			}
			for _, secret := range []string{private.Slug, "expired-doc", "site-only"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatalf("%s leaked %s", path, secret)
				}
			}
			if path != "/gallery" {
				p := manageDecode(t, rec)
				if p.Count != 1 {
					t.Fatal(p)
				}
			}
			if token != "" {
				for _, bad := range []string{"", "wrong"} {
					if got := manageRequest(h, "GET", path, bad, nil); got.Code != 401 {
						t.Fatalf("auth: %d", got.Code)
					}
				}
			}
		}
	}
}

func TestManageOwnerList(t *testing.T) {
	st := manageStore(t)
	pub := managePut(t, st, store.PutOptions{Slug: "public-doc"})
	priv := managePut(t, st, store.PutOptions{Private: true})
	managePut(t, st, store.PutOptions{Slug: "expired-doc", TTL: time.Nanosecond})
	manageSite(t, st, "site-only")
	h := New(st, Config{AuthToken: "owner", ContentBaseURL: "https://content.test"}).ControlHandler()
	rec := manageRequest(h, "GET", "/list?all=1", "owner", nil)
	page := manageDecode(t, rec)
	if page.Count != 3 || page.Next != "" {
		t.Fatal(page)
	}
	found := map[string]listItem{}
	for _, a := range page.Artifacts {
		found[a.Slug] = a
	}
	if found[pub.Slug].Kind != "artifact" || !found[priv.Slug].Private {
		t.Fatal(found)
	}
	site := found["site-only"]
	if site.Kind != "site" || site.URL != "https://content.test/site-only/" || site.Render != "raw" || site.Reply || site.Private || site.Password || site.ExpiresAt != "" || site.Series != "" || site.CreatedAt != "" || site.Size != 0 || site.ContentType != "" {
		t.Fatal(site)
	}
	for _, cursor := range []string{"bad-secret", "site:..", "evil:secret", "artifact:"} {
		r := manageRequest(h, "GET", "/list?all=1&after="+cursor, "owner", nil)
		if r.Code != 400 || r.Body.String() != "bad after cursor\n" || r.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("cursor: %d %s", r.Code, r.Body)
		}
	}
}

func TestManageOwnerEnvelope(t *testing.T) {
	st := manageStore(t)
	h := New(st, Config{AuthToken: "owner"}).ControlHandler()
	for _, populated := range []bool{false, true} {
		if populated {
			managePut(t, st, store.PutOptions{Slug: "public-doc"})
		}
		for _, path := range []string{"/list", "/list?all=0", "/list?all=true", "/list?all=1"} {
			t.Run(fmt.Sprintf("populated=%t%s", populated, path), func(t *testing.T) {
				rec := manageRequest(h, "GET", path, "owner", nil)
				manageDecode(t, rec)
				var envelope map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				marker, present := envelope["owner"]
				if path == "/list?all=1" {
					if marker != true {
						t.Fatalf("missing owner marker: %s", rec.Body)
					}
				} else if present {
					t.Fatalf("public listing has owner marker: %s", rec.Body)
				}
			})
		}
	}
}

func TestManageOwnerPagination(t *testing.T) {
	st := manageStore(t)
	for i := 0; i < 201; i++ {
		managePut(t, st, store.PutOptions{Slug: fmt.Sprintf("doc-%03d", i)})
	}
	for i := 0; i < 201; i++ {
		manageSite(t, st, fmt.Sprintf("site-%03d", i))
	}
	h := New(st, Config{AuthToken: "owner"}).ControlHandler()
	path := "/list?all=1"
	seen := map[string]bool{}
	counts := []int{}
	for {
		page := manageDecode(t, manageRequest(h, "GET", path, "owner", nil))
		counts = append(counts, page.Count)
		for _, a := range page.Artifacts {
			key := a.Kind + ":" + a.Slug
			if seen[key] {
				t.Fatalf("duplicate %s", key)
			}
			seen[key] = true
		}
		if page.Next == "" {
			break
		}
		if len(counts) > 3 {
			t.Fatal("pagination did not terminate")
		}
		path = "/list?all=1&after=" + page.Next
	}
	if fmt.Sprint(counts) != "[200 200 2]" || len(seen) != 402 {
		t.Fatalf("counts %v seen %d", counts, len(seen))
	}
}

func TestManageRouteSecurity(t *testing.T) {
	st := manageStore(t)
	browsers := []struct {
		name    string
		headers map[string]string
		blocked bool
	}{
		{"cli", nil, false},
		{"same-origin", map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
		{"none", map[string]string{"Sec-Fetch-Site": "none"}, false},
		{"same-site", map[string]string{"Sec-Fetch-Site": "same-site"}, true},
		{"cross-site", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		{"null", map[string]string{"Origin": "null"}, true},
		{"foreign", map[string]string{"Origin": "https://evil.test"}, true},
		{"proxy", map[string]string{"Origin": "https://control.test"}, false},
		{"backend", map[string]string{"Origin": "http://control.test"}, true},
	}
	for _, plane := range []string{"control", "combined", "content"} {
		for _, auth := range []struct {
			name, configured, supplied string
			code                       int
		}{{"valid", "owner", "owner", 200}, {"missing", "owner", "", 401}, {"wrong", "owner", "bad", 401}, {"unconfigured", "", "", 403}, {"unconfigured-bearer", "", "owner", 403}} {
			contentBase := "https://content.test"
			if plane == "combined" {
				contentBase = "https://control.test"
			}
			s := New(st, Config{AuthToken: auth.configured, BaseURL: "https://control.test", ContentBaseURL: contentBase})
			var h http.Handler = s.ControlHandler()
			if plane == "combined" {
				h = s.Handler()
			}
			if plane == "content" {
				h = s.ContentHandler()
			}
			for _, browser := range browsers {
				for _, method := range []string{"GET", "DELETE"} {
					t.Run(plane+"/"+auth.name+"/"+browser.name+"/"+method, func(t *testing.T) {
						path := "/list?all=1"
						want := auth.code
						if method == "DELETE" {
							path = "/sites/demo"
							manageSite(t, st, "demo")
							if want == 200 {
								want = 204
							}
							if browser.blocked && plane != "content" {
								want = 403
							}
						}
						if plane == "content" {
							want = 404
							if method == "DELETE" {
								want = 405
								if browser.blocked && browser.name != "null" {
									want = 403
								}
								if browser.name == "proxy" {
									want = 403
								}
							}
						}
						rec := manageRequest(h, method, path, auth.supplied, browser.headers)
						if rec.Code != want {
							t.Fatalf("got %d want %d: %s", rec.Code, want, rec.Body)
						}
						if method == "GET" && plane != "content" && rec.Header().Get("Cache-Control") != "private, no-store" {
							t.Fatal("owner cache header missing")
						}
						if method == "GET" && rec.Header().Get("Access-Control-Allow-Origin") != "" {
							t.Fatal("CORS granted")
						}
						if auth.configured == "" && plane != "content" && (method == "GET" || !browser.blocked) && !strings.Contains(rec.Body.String(), "configure a token") {
							t.Fatal("missing configuration advice")
						}
						if method == "DELETE" && st.SiteExists("demo") != (want != 204) {
							t.Fatal("incorrect mutation")
						}
					})
				}
			}
		}
	}
}

func TestManageDeleteSite(t *testing.T) {
	st := manageStore(t)
	manageSite(t, st, "demo")
	h := New(st, Config{AuthToken: "owner"}).ControlHandler()
	for _, tc := range []struct {
		path string
		code int
	}{{"/sites/demo", 204}, {"/sites/demo", 404}, {"/sites/bad%20name", 400}} {
		rec := manageRequest(h, "DELETE", tc.path, "owner", nil)
		if rec.Code != tc.code {
			t.Fatalf("%d: %s", rec.Code, rec.Body)
		}
		if rec.Code == 204 && rec.Body.Len() != 0 {
			t.Fatal("nonempty delete response")
		}
	}
	if st.SiteExists("demo") {
		t.Fatal("site survived")
	}
}

func TestManageDeleteIsolation(t *testing.T) {
	for _, deleteSite := range []bool{false, true} {
		t.Run(fmt.Sprint(deleteSite), func(t *testing.T) {
			st := manageStore(t)
			manageSite(t, st, "shared")
			managePut(t, st, store.PutOptions{Slug: "shared"})
			s := New(st, Config{AuthToken: "owner"})
			path := "/shared"
			survivor := "/shared/"
			body := "site bytes"
			if deleteSite {
				path = "/sites/shared"
				survivor = "/shared"
				body = "artifact bytes"
			}
			if r := manageRequest(s.ControlHandler(), "DELETE", path, "owner", nil); r.Code != 204 {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
			r := manageRequest(s.ContentHandler(), "GET", survivor, "", nil)
			if r.Code != 200 || r.Body.String() != body {
				t.Fatalf("survivor %d %s", r.Code, r.Body)
			}
		})
	}
}

func TestManageErrorRedaction(t *testing.T) {
	st := manageStore(t)
	secret := managePut(t, st, store.PutOptions{Private: true}).Slug
	st2 := manageStore(t)
	manageSite(t, st2, secret)
	logs := captureLog(t)
	h := New(st, Config{AuthToken: "owner"}).ControlHandler()
	r := manageRequest(h, "GET", "/list?all=1&after=secret:"+secret, "owner", nil)
	if r.Code != 400 {
		t.Fatal(r.Code)
	}
	st.Close()
	r = manageRequest(h, "GET", "/list?all=1", "owner", nil)
	if r.Code != 500 || r.Body.String() != "internal error\n" {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if logs.String() == "" {
		t.Fatal("failure not logged")
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(r.Body.String(), secret) || strings.Contains(logs.String(), st.Root()) {
		t.Fatal("private data leaked")
	}
	// Permission failure in site deletion must also be generic and path-free.
	lockDir(t, filepath.Join(st2.Root(), "data", "sites"), 0500)
	h = New(st2, Config{AuthToken: "owner"}).ControlHandler()
	r = manageRequest(h, "DELETE", "/sites/"+secret, "owner", nil)
	if r.Code != 500 || r.Body.String() != "internal error\n" {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), st2.Root()) {
		t.Fatal("delete leaked private data")
	}
}

func TestManageContentRedirect(t *testing.T) {
	st := manageStore(t)
	cases := []struct {
		name, base, content, host string
		code                      int
	}{
		{"valid", "https://control.test/path/?q=1", "https://content.test", "content.test", 302},
		{"http", "http://control.test/", "http://content.test", "content.test", 302},
		{"request-origin", "https://control.test/", "", "content.test", 302},
		{"ports", "http://host.test:8080", "http://host.test:8081", "host.test:8081", 302},
		{"unset", "", "http://content.test", "content.test", 404},
		{"relative", "//control.test", "http://content.test", "content.test", 404},
		{"scheme", "javascript:alert(1)", "http://content.test", "content.test", 404},
		{"missing-host", "https:///path", "http://content.test", "content.test", 404},
		{"malformed", "https://%zz", "http://content.test", "content.test", 404},
		{"credentials", "https://secret@control.test", "http://content.test", "content.test", 404},
		{"loop", "https://CONTENT.test/", "https://content.test", "content.test", 404},
		{"scheme-loop", "https://content.test/", "http://content.test", "content.test", 404},
		{"default-port", "http://content.test:80", "http://content.test", "content.test", 404},
		{"proxy-loop", "https://content.test", "https://content.test", "backend.test", 404},
		{"direct-loop", "http://backend.test", "https://content.test", "backend.test", 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(st, Config{BaseURL: tc.base, ContentBaseURL: tc.content})
			for _, method := range []string{"GET", "HEAD"} {
				for _, headers := range []map[string]string{nil, {"Origin": "null"}, {"Sec-Fetch-Site": "cross-site"}, {"Sec-Fetch-Site": "same-site"}, {"Origin": "https://control.test"}} {
					req := httptest.NewRequest(method, "http://"+tc.host+"/", nil)
					for k, v := range headers {
						req.Header.Set(k, v)
					}
					rec := httptest.NewRecorder()
					s.ContentHandler().ServeHTTP(rec, req)
					if rec.Code != tc.code {
						t.Fatalf("%s: got %d want %d", method, rec.Code, tc.code)
					}
					if tc.code == 302 {
						if rec.Header().Get("Location") != tc.base || rec.Body.Len() != 0 {
							t.Fatalf("bad redirect %v %s", rec.Header(), rec.Body)
						}
					} else if rec.Header().Get("Location") != "" || !strings.Contains(rec.Body.String(), "Not found") {
						t.Fatal("missing themed refusal")
					}
				}
			}
		})
	}
	for _, configured := range []string{"", "owner"} {
		for _, bearer := range []string{"", "wrong", "owner"} {
			s := New(st, Config{BaseURL: "https://control.test/", ContentPort: "8081", AuthToken: configured})
			for _, method := range []string{"GET", "HEAD"} {
				req := httptest.NewRequest(method, "http://control.test:8081/", nil)
				req.Header.Set("Authorization", "Bearer "+bearer)
				rec := httptest.NewRecorder()
				s.ContentHandler().ServeHTTP(rec, req)
				if rec.Code != 302 || rec.Header().Get("Location") != "https://control.test/" || rec.Body.Len() != 0 {
					t.Fatalf("public redirect %d %s", rec.Code, rec.Body)
				}
			}
		}
	}

	t.Run("same-explicit-host-across-schemes", func(t *testing.T) {
		s := New(st, Config{BaseURL: "http://content.test:443/", ContentBaseURL: "https://content.test:443"})
		req := httptest.NewRequest("GET", "https://content.test:443/", nil)
		rec := httptest.NewRecorder()
		s.ContentHandler().ServeHTTP(rec, req)
		if rec.Code != 404 || rec.Header().Get("Location") != "" {
			t.Fatalf("same host redirected: %d %s", rec.Code, rec.Header().Get("Location"))
		}
	})
	s := New(st, Config{BaseURL: "https://control.test", ContentBaseURL: "https://content.test", AuthToken: "owner"})
	for _, method := range []string{"GET", "HEAD"} {
		req := httptest.NewRequest(method, "https://content.test/missing", nil)
		r := httptest.NewRecorder()
		s.ContentHandler().ServeHTTP(r, req)
		if r.Code != 404 || !strings.Contains(r.Body.String(), "Not found") {
			t.Fatal("unknown slug changed")
		}
		r = manageRequest(s.Handler(), method, "/", "", nil)
		if r.Code != 200 || r.Header().Get("Location") != "" {
			t.Fatal("combined root changed")
		}
	}
}

func TestManageEquivalentRedirectAuthorities(t *testing.T) {
	for _, tc := range [][2]string{
		{"http://localhost:08081/", "http://localhost:8081"},
		{"http://localhost:00080/", "http://localhost"},
		{"https://localhost:00443/", "https://localhost"},
		{"http://[0:0:0:0:0:0:0:1]:8081/", "http://[::1]:8081"},
		{"http://[0:0:0:0:0:0:0:1]:0443/", "https://[::1]:443"},
	} {
		t.Run(tc[0], func(t *testing.T) {
			if redirectHostsDiffer(tc[0], tc[1]) {
				t.Errorf("equivalent authorities differ: %v", tc)
			}
			s := New(manageStore(t), Config{BaseURL: tc[0], ContentBaseURL: tc[1]})
			rec := httptest.NewRecorder()
			s.ContentHandler().ServeHTTP(rec, httptest.NewRequest("GET", tc[1]+"/", nil))
			if rec.Code != 404 || rec.Header().Get("Location") != "" {
				t.Fatalf("redirect loop: %d %v", rec.Code, rec.Header())
			}
		})
	}
}

func TestManageDistinctHTTPSPorts(t *testing.T) {
	destination, content := "https://example.test:80/", "https://example.test"
	if !redirectHostsDiffer(destination, content) {
		t.Error("HTTPS ports 80 and 443 must differ")
	}
	s := New(manageStore(t), Config{BaseURL: destination, ContentBaseURL: content})
	for _, method := range []string{"GET", "HEAD"} {
		rec := httptest.NewRecorder()
		s.ContentHandler().ServeHTTP(rec, httptest.NewRequest(method, content+"/", nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != destination {
			t.Errorf("%s: status %d, location %q", method, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestManageEarlyRefusalNoStore(t *testing.T) {
	s := New(manageStore(t), Config{AllowedHosts: []string{"allowed.test"}, Requests: RequestLimits{MaxRequests: 1}})
	for name, h := range map[string]http.Handler{"control": s.ControlHandler(), "content": s.ContentHandler(), "combined": s.Handler()} {
		t.Run(name, func(t *testing.T) {
			rec := manageRequest(h, "GET", "/list?all=1", "", nil)
			if rec.Code != 421 || rec.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("host refusal: %d %v", rec.Code, rec.Header())
			}
		})
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h := s.wrapListener(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }))
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://allowed.test/", nil))
	}()
	<-entered
	defer func() { close(release); <-done }()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://allowed.test/list?all=1", nil))
	if rec.Code != 503 || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("admission refusal: %d %v", rec.Code, rec.Header())
	}
}

func TestManageOwnerCursorNoCapability(t *testing.T) {
	st := manageStore(t)
	slugs := make([]string, listPageSize+1)
	for i := range slugs {
		slugs[i] = managePut(t, st, store.PutOptions{Private: true}).Slug
	}
	h := New(st, Config{AuthToken: "owner"}).ControlHandler()
	page := manageDecode(t, manageRequest(h, "GET", "/list?all=1", "owner", nil))
	id, err := strconv.ParseInt(strings.TrimPrefix(page.Next, "artifact:"), 10, 64)
	if err != nil || id <= 0 {
		t.Fatalf("cursor is not an opaque row ID: %q", page.Next)
	}
	for i, item := range page.Artifacts {
		if item.Slug != slugs[i] {
			t.Fatalf("row order at %d: %s != %s", i, item.Slug, slugs[i])
		}
		if strings.Contains(page.Next, item.Slug) {
			t.Fatalf("capability in cursor: %s", page.Next)
		}
	}
	next := manageDecode(t, manageRequest(h, "GET", "/list?all=1&after="+page.Next, "owner", nil))
	if next.Count != 1 || next.Artifacts[0].Slug != slugs[listPageSize] || next.Next != "" {
		t.Fatalf("bad continuation: %+v", next)
	}
	for _, cursor := range []string{"artifact:secret-capability", "artifact:-1", "artifact:0", "artifact:9223372036854775808", "artifact:01"} {
		if r := manageRequest(h, "GET", "/list?all=1&after="+cursor, "owner", nil); r.Code != 400 {
			t.Fatalf("accepted %s: %d", cursor, r.Code)
		}
	}
}

func TestManageDeleteSiteStatPermission(t *testing.T) {
	st := manageStore(t)
	manageSite(t, st, "demo")
	dir := filepath.Join(st.Root(), "data", "sites")
	if err := os.Chmod(dir, 0400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if _, err := os.Stat(filepath.Join(dir, "demo")); !os.IsPermission(err) {
		t.Skip("host permits stat through non-searchable directory")
	}
	rec := manageRequest(New(st, Config{AuthToken: "owner"}).ControlHandler(), "DELETE", "/sites/demo", "owner", nil)
	if rec.Code != 500 || rec.Body.String() != "internal error\n" {
		t.Fatalf("stat failure: %d %s", rec.Code, rec.Body)
	}
}

func TestPublishDocumentedResponse(t *testing.T) {
	doc, err := os.ReadFile("../../API.md")
	if err != nil {
		t.Fatal(err)
	}
	_, response, ok := strings.Cut(string(doc), "### Response")
	if !ok {
		t.Fatal("missing publish response")
	}
	_, example, ok := strings.Cut(response, "```json\n")
	if !ok {
		t.Fatal("missing publish JSON")
	}
	example, _, _ = strings.Cut(example, "```")
	var documented map[string]any
	if err := json.Unmarshal([]byte(example), &documented); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "http://control.test/publish?slug=reports", strings.NewReader("<p>report</p>"))
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	New(manageStore(t), Config{}).ControlHandler().ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	var actual map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	for key, value := range documented {
		if fmt.Sprintf("%T", actual[key]) != fmt.Sprintf("%T", value) {
			t.Errorf("documented %s (%T), handler sends %T", key, value, actual[key])
		}
	}
	if len(actual) != len(documented) {
		t.Errorf("documented %d fields, handler sends %d", len(documented), len(actual))
	}
}
