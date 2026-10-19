// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/manage"
	"github.com/daisandapex/demiplane/v3/internal/store"
)

func mutate(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://control.test"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer owner")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
func patched(t *testing.T, h http.Handler, body string) listItem {
	t.Helper()
	rec := mutate(t, h, "PATCH", "/page", body)
	if rec.Code != 200 {
		t.Fatalf("patch %s: %d %s", body, rec.Code, rec.Body)
	}
	var item listItem
	if json.Unmarshal(rec.Body.Bytes(), &item) != nil {
		t.Fatal("bad entry")
	}
	return item
}
func newManageHandler(t *testing.T) (*store.Store, http.Handler) {
	s := manageStore(t)
	managePut(t, s, store.PutOptions{Slug: "page", TTL: time.Hour})
	return s, New(s, Config{AuthToken: "owner"}).ControlHandler()
}
func TestManagePatchTTL(t *testing.T) {
	_, h := newManageHandler(t)
	for _, dur := range []string{"1m", "2h", "365d"} {
		a := patched(t, h, `{"ttl":"`+dur+`"}`)
		expires, err := time.Parse(time.RFC3339, a.ExpiresAt)
		if err != nil {
			t.Fatal(err)
		}
		d, _ := store.ParseTTL(dur)
		left := time.Until(expires)
		if left > d || left < d-2*time.Second {
			t.Fatalf("set rather than extend: %v", left)
		}
		b := patched(t, h, `{}`)
		if b.ExpiresAt != a.ExpiresAt || b.Revision != a.Revision {
			t.Fatal("omission changed metadata")
		}
	}
	for _, value := range []string{`null`, `"0"`} {
		a := patched(t, h, `{"ttl":`+value+`}`)
		if a.ExpiresAt != "" || a.Revision == "" {
			t.Fatal("clear failed")
		}
	}
}
func TestManagePatchValidation(t *testing.T) {
	_, h := newManageHandler(t)
	before := patched(t, h, `{}`)
	for _, body := range []string{`null`, `[]`, `{"ttl":""}`, `{"ttl":"59s"}`, `{"ttl":"366d"}`, `{"ttl":"-1m"}`, `{"ttl":"99999999999999h"}`, `{"ttl":1}`, `{"pinned":null}`, `{"pinned":"true"}`, `{"unknown":true}`, `{} {}`, `{`} {
		rec := mutate(t, h, "PATCH", "/page", body)
		if rec.Code != 400 {
			t.Errorf("%s: %d", body, rec.Code)
		}
		after := patched(t, h, `{}`)
		if after.Revision != before.Revision || after.ExpiresAt != before.ExpiresAt {
			t.Fatal("invalid patch changed row")
		}
	}
}
func TestManagePatchPins(t *testing.T) {
	_, h := newManageHandler(t)
	a := patched(t, h, `{"pinned":true}`)
	if !a.Pinned || a.ExpiresAt != "" {
		t.Fatal("pin did not clear expiry")
	}
	for _, body := range []string{`{"ttl":"1m"}`, `{"ttl":null}`, `{"ttl":"0"}`, `{"pinned":true,"ttl":"2h"}`} {
		r := mutate(t, h, "PATCH", "/page", body)
		if r.Code != 409 || !strings.Contains(r.Body.String(), "unpin") {
			t.Fatalf("pin conflict: %d %s", r.Code, r.Body)
		}
	}
	a = patched(t, h, `{"pinned":false}`)
	if a.Pinned || a.ExpiresAt != "" {
		t.Fatal("unpin restored expiry")
	}
	patched(t, h, `{"pinned":true}`)
	a = patched(t, h, `{"pinned":false,"ttl":"2h"}`)
	if a.Pinned || a.ExpiresAt == "" {
		t.Fatal("atomic unpin and ttl")
	}
}
func TestManagePatchNotFound(t *testing.T) {
	s, h := newManageHandler(t)
	before := patched(t, h, `{}`)
	managePut(t, s, store.PutOptions{Slug: "expired", TTL: time.Nanosecond})
	for _, slug := range []string{"missing", "expired"} {
		r := mutate(t, h, "PATCH", "/"+slug, `{"pinned":true}`)
		if r.Code != 404 {
			t.Fatalf("%s: %d", slug, r.Code)
		}
	}
	if a := patched(t, h, `{}`); a.Revision != before.Revision {
		t.Fatal("404 changed unrelated artifact")
	}
	if n, err := s.SweepExpired(time.Now()); err != nil || n != 1 {
		t.Fatalf("expired row resurrected: %d %v", n, err)
	}
}
func serverManifest(t *testing.T, h http.Handler) manage.Manifest {
	t.Helper()
	rec := manageRequest(h, "GET", "/list?all=1", "owner", nil)
	var page struct {
		Instance  string `json:"instance"`
		Artifacts []struct {
			manage.Item
			Slug string `json:"slug"`
		} `json:"artifacts"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	m := manage.Manifest{Instance: page.Instance, SelectedAt: time.Now().UTC(), Items: []manage.Item{}}
	for _, i := range page.Artifacts {
		i.ID = i.Slug
		m.Items = append(m.Items, i.Item)
	}
	return m
}
func bulk(t *testing.T, h http.Handler, m manage.Manifest) manage.Report {
	t.Helper()
	b, _ := json.Marshal(manage.DeleteRequest{Manifest: m})
	r := mutate(t, h, "POST", "/manage/delete", string(b))
	if r.Code != 200 {
		t.Fatalf("bulk: %d %s", r.Code, r.Body)
	}
	var report manage.Report
	if json.Unmarshal(r.Body.Bytes(), &report) != nil {
		t.Fatal("bad report")
	}
	return report
}
func TestManageBulkDelete(t *testing.T) {
	s, h := newManageHandler(t)
	managePut(t, s, store.PutOptions{Slug: "changed"})
	managePut(t, s, store.PutOptions{Slug: "missing"})
	managePut(t, s, store.PutOptions{Slug: "pin"})
	m := serverManifest(t, h)
	managePut(t, s, store.PutOptions{Slug: "changed"})
	if err := s.Delete("missing"); err != nil {
		t.Fatal(err)
	}
	p := true
	if _, err := s.Patch("pin", nil, &p); err != nil {
		t.Fatal(err)
	}
	report := bulk(t, h, m)
	if len(report.Deleted) != 1 || report.Deleted[0].ID != "page" || len(report.Skipped) != 2 || len(report.Missing) != 1 || len(report.Failed) != 0 {
		t.Fatalf("report %+v", report)
	}
	retry := bulk(t, h, m)
	if len(retry.Deleted) != 0 || len(retry.Missing) != 2 || len(retry.Skipped) != 2 {
		t.Fatalf("retry %+v", retry)
	}
	s.Close()
	failed := bulk(t, h, m)
	if len(failed.Failed) != len(m.Items) {
		t.Fatal("operational failures not reported")
	}
}
func TestManageBulkValidation(t *testing.T) {
	_, h := newManageHandler(t)
	m := serverManifest(t, h)
	b, _ := json.Marshal(manage.DeleteRequest{Manifest: m})
	for _, body := range []string{`[]`, `null`, `{}`, string(bytes.Replace(b, []byte(m.Instance), []byte("http://other.test"), 1)), string(bytes.Replace(b, []byte(m.Items[0].Revision), []byte(""), 1)), strings.TrimSuffix(string(b), "}") + `,"match":"*"}`, strings.TrimSuffix(string(b), "}") + `,"confirm_host":"other.test"}`, string(b) + " {}"} {
		r := mutate(t, h, "POST", "/manage/delete", body)
		if r.Code != 400 {
			t.Errorf("invalid manifest: %d", r.Code)
		}
		if len(serverManifest(t, h).Items) != 1 {
			t.Fatal("invalid manifest deleted data")
		}
	}
	for _, change := range []func(*manage.Manifest){
		func(m *manage.Manifest) { m.SelectedAt = time.Time{} },
		func(m *manage.Manifest) { m.SelectedAt = time.Now().Add(time.Hour) },
		func(m *manage.Manifest) { m.Items = make([]manage.Item, manage.MaxBatch+1) },
		func(m *manage.Manifest) {
			m.Items = []manage.Item{m.Items[0], {Kind: "invalid", ID: "x", Revision: "r"}}
		},
	} {
		bad := m
		change(&bad)
		raw, _ := json.Marshal(manage.DeleteRequest{Manifest: bad})
		if mutate(t, h, "POST", "/manage/delete", string(raw)).Code != 400 {
			t.Fatal("invalid envelope accepted")
		}
		if len(serverManifest(t, h).Items) != 1 {
			t.Fatal("invalid envelope mutated")
		}
	}
	dup := m
	dup.Items = append(append([]manage.Item{}, m.Items...), m.Items[0])
	db, _ := json.Marshal(manage.DeleteRequest{Manifest: dup})
	if mutate(t, h, "POST", "/manage/delete", string(db)).Code != 400 {
		t.Fatal("duplicate accepted")
	}
}
func TestManageBulkIndependence(t *testing.T) {
	for _, kind := range []string{"artifact", "site"} {
		t.Run(kind, func(t *testing.T) {
			s, h := newManageHandler(t)
			manageSite(t, s, "page")
			m := serverManifest(t, h)
			var chosen manage.Item
			for _, i := range m.Items {
				if i.Kind == kind {
					chosen = i
				}
			}
			m.Items = []manage.Item{chosen}
			r := bulk(t, h, m)
			if len(r.Deleted) != 1 {
				t.Fatalf("delete %+v", r)
			}
			left := serverManifest(t, h)
			if len(left.Items) != 1 || left.Items[0].Kind == kind {
				t.Fatalf("cross-kind deletion %+v", left)
			}
		})
	}
}
func TestManageHTTPDefaultTTL(t *testing.T) {
	s := manageStore(t)
	if err := s.SetDefaultTTL(time.Hour); err != nil {
		t.Fatal(err)
	}
	h := New(s, Config{AuthToken: "owner"}).ControlHandler()
	for _, tc := range []struct {
		slug, q string
		want    time.Duration
	}{{"absent", "", time.Hour}, {"zero", "&ttl=0", 0}, {"none", "&ttl=none", 0}, {"explicit", "&ttl=2h", 2 * time.Hour}} {
		r := mutate(t, h, "POST", "/publish?slug="+tc.slug+tc.q, "x")
		if r.Code != 201 {
			t.Fatalf("publish %s: %d %s", tc.slug, r.Code, r.Body)
		}
		a, err := s.Patch(tc.slug, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == 0 {
			if !a.ExpiresAt.IsZero() {
				t.Fatal("never ignored")
			}
		} else if d := time.Until(a.ExpiresAt); d > tc.want || d < tc.want-2*time.Second {
			t.Fatalf("default %v", d)
		}
	}
	p := true
	if _, err := s.Patch("absent", nil, &p); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"0", "none", "2h"} {
		r := mutate(t, h, "POST", "/publish?slug=absent&ttl="+q, "bad")
		if r.Code != 409 {
			t.Fatalf("pinned publish: %d %s", r.Code, r.Body)
		}
	}
}
func TestManageSiteTTLExclusion(t *testing.T) {
	s, h := newManageHandler(t)
	if err := s.SetDefaultTTL(time.Hour); err != nil {
		t.Fatal(err)
	}
	archive := string(makeTar(t, []srcFile{{"index.html", "site body"}}))
	if r := mutate(t, h, "POST", "/publish?site=site&fmt=tar", archive); r.Code != 201 {
		t.Fatalf("default applied to site: %d %s", r.Code, r.Body)
	}
	for _, ttl := range []string{"0", "none", "1h"} {
		r := mutate(t, h, "POST", "/publish?site=site&fmt=tar&ttl="+ttl, archive)
		if r.Code != 400 {
			t.Fatal("site ttl accepted")
		}
	}
	if r := mutate(t, h, "PATCH", "/sites/site", `{"pinned":true}`); r.Code == 200 {
		t.Fatal("site patch exposed")
	}
	if n, err := s.SweepExpired(time.Now().Add(24 * time.Hour)); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if !s.SiteExists("site") {
		t.Fatal("site expired")
	}
}
func TestManageMutationRouteSecurity(t *testing.T) {
	for _, combined := range []bool{false, true} {
		for _, method := range []string{"PATCH", "POST"} {
			for _, tc := range []struct {
				name, configured, token, base string
				headers                       map[string]string
				want                          int
			}{
				{"valid", "owner", "owner", "", nil, 200}, {"missing", "owner", "", "", nil, 401}, {"wrong", "owner", "bad", "", nil, 401}, {"unconfigured", "", "owner", "", nil, 403},
				{"cross-site", "owner", "owner", "", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403}, {"same-site", "owner", "owner", "", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
				{"null", "owner", "owner", "", map[string]string{"Origin": "null"}, 403},
				{"null-with-fetch", "owner", "owner", "", map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"}, 403},
				{"foreign-with-fetch", "owner", "owner", "", map[string]string{"Origin": "http://content.test", "Sec-Fetch-Site": "same-origin"}, 403}, {"foreign", "owner", "owner", "", map[string]string{"Origin": "http://content.test"}, 403}, {"same-origin", "owner", "owner", "", map[string]string{"Origin": "http://control.test"}, 200},
				{"proxy", "owner", "owner", "https://public.test", map[string]string{"Origin": "https://public.test"}, 200}, {"proxy-direct", "owner", "owner", "https://public.test", map[string]string{"Origin": "http://control.test"}, 403}, {"spoof-forwarded", "owner", "owner", "", map[string]string{"Origin": "https://foreign.test", "X-Forwarded-Host": "foreign.test", "X-Forwarded-Proto": "https"}, 403},
			} {
				t.Run(method+"/"+tc.name+map[bool]string{true: "/combined", false: "/split"}[combined], func(t *testing.T) {
					s := manageStore(t)
					managePut(t, s, store.PutOptions{Slug: "page"})
					srv := New(s, Config{AuthToken: tc.configured, BaseURL: tc.base})
					var h http.Handler = srv.ControlHandler()
					if combined {
						h = srv.Handler()
					}
					route, body := "/page", `{"pinned":true}`
					if method == "POST" {
						route = "/manage/delete"
						instance := "http://control.test:80"
						if tc.base != "" {
							instance = "https://public.test:443"
						}
						b, _ := json.Marshal(manage.DeleteRequest{Manifest: manage.Manifest{Instance: instance, SelectedAt: time.Now(), Items: []manage.Item{}}})
						body = string(b)
					}
					req := httptest.NewRequest(method, "http://control.test"+route, strings.NewReader(body))
					if tc.token != "" {
						req.Header.Set("Authorization", "Bearer "+tc.token)
					}
					for k, v := range tc.headers {
						req.Header.Set(k, v)
					}
					r := httptest.NewRecorder()
					h.ServeHTTP(r, req)
					if r.Code != tc.want {
						t.Fatalf("status %d: %s", r.Code, r.Body)
					}
					if r.Header().Get("Cache-Control") != "private, no-store" {
						t.Fatal("owner response cacheable")
					}
					a, err := s.Patch("page", nil, nil)
					if err != nil {
						t.Fatal(err)
					}
					if tc.want != 200 && a.Pinned {
						t.Fatal("refused write mutated")
					}
				})
			}
		}
	}
}
func TestManageContentIsolation(t *testing.T) {
	s := manageStore(t)
	managePut(t, s, store.PutOptions{Slug: "page"})
	srv := New(s, Config{AuthToken: "owner"})
	m := serverManifest(t, srv.ControlHandler())
	b, _ := json.Marshal(manage.DeleteRequest{Manifest: m})
	for _, tc := range []struct{ method, path, body string }{{"PATCH", "/page", `{"pinned":true}`}, {"POST", "/manage/delete", string(b)}} {
		r := mutate(t, srv.ContentHandler(), tc.method, tc.path, tc.body)
		if r.Code < 400 {
			t.Fatalf("content mutation %d", r.Code)
		}
	}
	a, err := s.Patch("page", nil, nil)
	if err != nil || a.Pinned {
		t.Fatal("content changed row")
	}
}
func TestManageRoutePrivacy(t *testing.T) {
	s := manageStore(t)
	a := managePut(t, s, store.PutOptions{Private: true, Source: []byte("source-secret"), RenderSpec: `{"next":"destination-secret","reply_token":"reply-secret"}`})
	srv := New(s, Config{AuthToken: "owner"})
	h := srv.ControlHandler()
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	list := manageRequest(h, "GET", "/list?all=1", "owner", nil)
	if strings.Contains(list.Body.String(), "destination-secret") || strings.Contains(list.Body.String(), "reply-secret") {
		t.Fatal("metadata leaked")
	}
	managePut(t, s, store.PutOptions{Slug: "visible"})
	pin := true
	if _, err := s.Patch("visible", nil, &pin); err != nil {
		t.Fatal(err)
	}
	public := manageRequest(h, "GET", "/list", "owner", nil)
	if strings.Contains(public.Body.String(), `"revision"`) || strings.Contains(public.Body.String(), `"pinned"`) {
		t.Fatal("public owner metadata leaked")
	}
	if strings.Contains(public.Body.String(), a.Slug) {
		t.Fatal("public inventory leaked")
	}
	for _, tc := range []struct{ method, path, body string }{{"PATCH", "/" + a.Slug, `{"ttl":"` + a.Slug + `"}`}, {"POST", "/manage/delete", `{"instance":"` + a.Slug + `"}`}} {
		r := mutate(t, h, tc.method, tc.path, tc.body)
		if r.Code < 400 || strings.Contains(r.Body.String(), a.Slug) || r.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("private error %d %s", r.Code, r.Body)
		}
	}
	s.Close()
	for _, tc := range []struct{ method, path, body string }{{"PATCH", "/" + a.Slug, `{}`}, {"POST", "/manage/delete", `{"instance":"http://control.test:80","selected_at":"2026-01-01T00:00:00Z","items":[{"kind":"artifact","id":"` + a.Slug + `","revision":"old","size":0}]}`}} {
		mutate(t, h, tc.method, tc.path, tc.body)
	}
	if strings.Contains(logs.String(), a.Slug) {
		t.Fatal("capability in log")
	}
}
