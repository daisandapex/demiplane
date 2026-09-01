// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/store"
)

// The markdown used across these tests. The H1 becomes the page title and the
// paragraph proves the body survived a rebake.
const rerenderSource = "# Field notes\n\nA sentence that must survive every rebake.\n"

// renderConfig is a publish-time render configuration with everything the
// rendered-page chrome is driven by turned on, differing only in the footer link
// — an unambiguous, config-derived string to look for in the output.
func renderConfig(footerLink string) Config {
	return Config{
		RenderHeader:     true,
		RenderFooter:     true,
		RenderFooterLink: footerLink,
		RenderMetaHeader: true,
	}
}

// newStoreServer starts a server over an EXISTING store, so a test can stand a
// second instance with a different render configuration in front of the same
// artifacts — which is what upgrading an instance actually is.
func newStoreServer(t *testing.T, st *store.Store, cfg Config) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(New(st, cfg).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func newSharedStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// postJSON POSTs with an Accept: application/json header and decodes the reply.
func postJSON(t *testing.T, url string, out any) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("decode %s response %q: %v", url, body, err)
		}
	}
	return resp
}

// TestPublishRetainsMarkdownSource is half of the regression: the markdown a
// ?render=md publish was given must still be in the store afterwards. It was
// not, and that is how five pages were lost for good on 2026-08-30.
func TestPublishRetainsMarkdownSource(t *testing.T) {
	st := newSharedStore(t)
	ts := newStoreServer(t, st, renderConfig("https://example.com/one"))

	publish(t, ts, "?render=md&slug=field-notes", rerenderSource)

	rec, err := st.Source("field-notes")
	if err != nil {
		t.Fatalf("Source after a rendered publish: %v", err)
	}
	if string(rec.Body) != rerenderSource {
		t.Errorf("retained source =\n %q\nwant\n %q", rec.Body, rerenderSource)
	}
	if rec.Spec == "" {
		t.Error("no render spec retained; a rebake could not reproduce the page")
	}

	// A publish WITHOUT ?render=md retains nothing — there is no source to keep,
	// and pretending otherwise would let a later rebake overwrite an HTML upload.
	publish(t, ts, "?slug=hand-written", "<p>hand-written</p>")
	if _, err := st.Source("hand-written"); !errors.Is(err, store.ErrNoSource) {
		t.Errorf("Source of a plain publish = %v, want ErrNoSource", err)
	}
}

// TestRerenderPicksUpRendererChange is the other half, and the point of the
// whole change: a page published by one build must be rebakeable by a later one
// with a different renderer configuration, from the store alone, with no copy of
// the markdown anywhere else.
func TestRerenderPicksUpRendererChange(t *testing.T) {
	st := newSharedStore(t)

	// Publish against the old configuration.
	before := newStoreServer(t, st, renderConfig("https://example.com/old-renderer"))
	url := publish(t, before, "?render=md&slug=field-notes", rerenderSource)

	_, page := get(t, url)
	if !strings.Contains(string(page), "https://example.com/old-renderer") {
		t.Fatalf("published page does not carry the old renderer's footer link:\n%s", page)
	}

	// Upgrade: same store, new renderer configuration. The already-published page
	// still shows the OLD rendering until it is rebaked — that is the defect this
	// endpoint exists to fix.
	after := newStoreServer(t, st, renderConfig("https://example.com/new-renderer"))
	_, stale := get(t, after.URL+"/field-notes")
	if !strings.Contains(string(stale), "https://example.com/old-renderer") {
		t.Fatalf("page changed without a rebake; the test no longer proves anything")
	}

	var res rerenderResult
	resp := postJSON(t, after.URL+"/rerender/field-notes", &res)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /rerender status = %d", resp.StatusCode)
	}
	if !res.Rerendered {
		t.Fatalf("rerendered = false (%s)", res.Reason)
	}
	if res.Slug != "field-notes" || res.Size == 0 {
		t.Errorf("result = %+v", res)
	}

	_, rebaked := get(t, after.URL+"/field-notes")
	got := string(rebaked)
	if !strings.Contains(got, "https://example.com/new-renderer") {
		t.Errorf("rebaked page did not pick the new renderer up:\n%s", got)
	}
	if strings.Contains(got, "https://example.com/old-renderer") {
		t.Errorf("rebaked page still carries the old rendering:\n%s", got)
	}
	// The document itself is unchanged: a rebake re-renders, it does not rewrite.
	if !strings.Contains(got, "A sentence that must survive every rebake.") {
		t.Errorf("rebaked page lost the document body:\n%s", got)
	}
	if !strings.Contains(got, "Field notes") {
		t.Errorf("rebaked page lost its title:\n%s", got)
	}
	// And the source is still retained, so it can be rebaked again next upgrade.
	if _, err := st.Source("field-notes"); err != nil {
		t.Errorf("source consumed by the rebake: %v", err)
	}
}

// TestRerenderLegacyArtifactDegrades: an artifact with no retained source —
// every page published before this feature existed, and every non-markdown
// upload — must be reported and left alone. Never an error, never a rewrite.
func TestRerenderLegacyArtifactDegrades(t *testing.T) {
	st := newSharedStore(t)
	ts := newStoreServer(t, st, renderConfig("https://example.com/one"))

	const raw = "<p>published before source retention</p>"
	url := publish(t, ts, "?slug=legacy", raw)

	var res rerenderResult
	resp := postJSON(t, ts.URL+"/rerender/legacy", &res)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a sourceless artifact is not an error", resp.StatusCode)
	}
	if res.Rerendered {
		t.Error("reported a rebake of an artifact with no source")
	}
	if res.Reason == "" {
		t.Error("skipped without saying why")
	}

	// Untouched, byte for byte.
	_, body := get(t, url)
	if string(body) != raw {
		t.Errorf("legacy artifact was rewritten:\n got %q\nwant %q", body, raw)
	}
}

// TestRerenderMissingSlug: an unknown slug is a 404. Only a genuinely absent
// artifact errors — the sourceless case above must stay distinguishable from it,
// or a sweep cannot tell "nothing to do" from "something is wrong".
func TestRerenderMissingSlug(t *testing.T) {
	ts := newConfiguredServer(t, renderConfig("https://example.com/one"))
	resp := postJSON(t, ts.URL+"/rerender/no-such-page", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestRerenderAllSweep exercises the bulk path an operator runs after upgrading:
// rendered pages are rebaked, sourceless ones are skipped and counted, and the
// report accounts for every artifact in the store.
func TestRerenderAllSweep(t *testing.T) {
	st := newSharedStore(t)
	before := newStoreServer(t, st, renderConfig("https://example.com/old-renderer"))
	publish(t, before, "?render=md&slug=page-one", rerenderSource)
	publish(t, before, "?render=md&slug=page-two", rerenderSource)
	publish(t, before, "?slug=legacy", "<p>no source</p>")

	after := newStoreServer(t, st, renderConfig("https://example.com/new-renderer"))
	var report struct {
		Rerendered int               `json:"rerendered"`
		Skipped    int               `json:"skipped"`
		Failed     map[string]string `json:"failed"`
		Results    []rerenderResult  `json:"results"`
	}
	resp := postJSON(t, after.URL+"/rerender", &report)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if report.Rerendered != 2 {
		t.Errorf("rerendered = %d, want 2", report.Rerendered)
	}
	if report.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the sourceless artifact)", report.Skipped)
	}
	if len(report.Failed) != 0 {
		t.Errorf("failed = %v, want none", report.Failed)
	}
	if len(report.Results) != 3 {
		t.Errorf("results cover %d artifacts, want all 3", len(report.Results))
	}

	for _, slug := range []string{"page-one", "page-two"} {
		_, body := get(t, after.URL+"/"+slug)
		if !strings.Contains(string(body), "https://example.com/new-renderer") {
			t.Errorf("%s was not rebaked by the sweep", slug)
		}
	}
	_, legacy := get(t, after.URL+"/legacy")
	if string(legacy) != "<p>no source</p>" {
		t.Errorf("sweep rewrote the sourceless artifact: %q", legacy)
	}
}

// TestRerenderRequiresAuth: rebaking rewrites artifact bytes, so it belongs
// behind the same bearer auth as publish and delete rather than being an
// unauthenticated way to overwrite every page on an instance.
func TestRerenderRequiresAuth(t *testing.T) {
	ts := newAuthTestServer(t, "", "s3cret")

	for _, path := range []string{"/rerender", "/rerender/anything"} {
		resp := postJSON(t, ts.URL+path, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("POST %s without a token = %d, want 401", path, resp.StatusCode)
		}
	}
}

// TestRerenderIsReserved: "rerender" must not be claimable as an artifact slug,
// or an artifact would shadow the route (the invariant every core route holds).
func TestRerenderIsReserved(t *testing.T) {
	ts := newConfiguredServer(t, renderConfig("https://example.com/one"))
	resp, body := post(t, ts.URL+"/publish?slug=rerender", "<p>shadow</p>")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("publishing to the reserved slug = %d, want 400 (%s)", resp.StatusCode, body)
	}
}

// TestRerenderReplaysPublishDate: a rebake is not a re-publish. The publish date
// baked into the colophon replays from the spec captured at publish, so
// upgrading an instance does not silently redate every page on it.
func TestRerenderReplaysPublishDate(t *testing.T) {
	st := newSharedStore(t)
	// Seed the store the way an older publish would have left it, with a publish
	// date that is unmistakably not today.
	if _, err := st.Put(store.PutOptions{
		Slug:       "dated",
		Filename:   "dated.html",
		Source:     []byte(rerenderSource),
		RenderSpec: `{"named_slug":"dated","published":"2020-01-02T03:04:05Z"}`,
	}, strings.NewReader("<h1>stale rendering</h1>")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ts := newStoreServer(t, st, renderConfig("https://example.com/new-renderer"))
	var res rerenderResult
	if resp := postJSON(t, ts.URL+"/rerender/dated", &res); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !res.Rerendered {
		t.Fatalf("rerendered = false (%s)", res.Reason)
	}

	_, body := get(t, ts.URL+"/dated")
	if !strings.Contains(string(body), "published 2020-01-02") {
		t.Errorf("rebaked page did not replay the original publish date:\n%s", body)
	}
}

// TestRerenderSpecFallbacks: a retained source whose spec is missing or
// unreadable still rebakes. The source is the irreplaceable part; the spec is
// recoverable defaults, and refusing to rebake would strand the page on old
// output over metadata nobody would miss.
func TestRerenderSpecFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec string
	}{
		{"empty spec", ""},
		{"unreadable spec", "{not json at all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newSharedStore(t)
			if _, err := st.Put(store.PutOptions{
				Slug:       "seeded",
				Filename:   "seeded.html",
				Source:     []byte(rerenderSource),
				RenderSpec: tc.spec,
			}, strings.NewReader("<h1>stale rendering</h1>")); err != nil {
				t.Fatalf("seed: %v", err)
			}

			ts := newStoreServer(t, st, renderConfig("https://example.com/new-renderer"))
			var res rerenderResult
			if resp := postJSON(t, ts.URL+"/rerender/seeded", &res); resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			if !res.Rerendered {
				t.Fatalf("rerendered = false (%s)", res.Reason)
			}
			_, body := get(t, ts.URL+"/seeded")
			if !strings.Contains(string(body), "A sentence that must survive every rebake.") {
				t.Errorf("rebaked page lost the document:\n%s", body)
			}
			if !strings.Contains(string(body), "https://example.com/new-renderer") {
				t.Errorf("rebaked page did not use the current renderer:\n%s", body)
			}
		})
	}
}
