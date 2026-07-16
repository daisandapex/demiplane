// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/store"
)

// slugFromURL returns the last path segment of a returned artifact URL.
func slugFromURL(u string) string {
	i := strings.LastIndexByte(u, '/')
	if i < 0 {
		return u
	}
	return u[i+1:]
}

func TestGalleryListsPublicArtifacts(t *testing.T) {
	ts := newTestServer(t, "")
	u1 := publish(t, ts, "?slug=alpha-report", "<h1>alpha</h1>")
	u2 := publish(t, ts, "?slug=beta-notes", "beta plain text")

	resp, body := get(t, ts.URL+"/gallery")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /gallery status = %d", resp.StatusCode)
	}
	page := string(body)
	for _, want := range []string{"alpha-report", "beta-notes", "Copy URL", "class=\"gcard\""} {
		if !strings.Contains(page, want) {
			t.Errorf("gallery page missing %q", want)
		}
	}
	// The card URLs must point at the content origin (here same-origin).
	if !strings.Contains(page, u1) || !strings.Contains(page, u2) {
		t.Errorf("gallery page missing artifact URLs %q / %q", u1, u2)
	}
}

func TestGalleryExcludesPrivate(t *testing.T) {
	ts := newTestServer(t, "")
	publish(t, ts, "?slug=public-one", "public body")
	privURL := publish(t, ts, "?private=true", "secret body")
	privSlug := slugFromURL(privURL)

	_, body := get(t, ts.URL+"/gallery")
	page := string(body)
	if !strings.Contains(page, "public-one") {
		t.Errorf("gallery should list the public artifact")
	}
	if strings.Contains(page, privSlug) {
		t.Errorf("gallery leaked the private capability slug %q", privSlug)
	}
}

func TestGalleryEmptyStatePointsAtConnect(t *testing.T) {
	ts := newTestServer(t, "")
	_, body := get(t, ts.URL+"/gallery")
	page := string(body)
	if !strings.Contains(page, "No artifacts yet") {
		t.Errorf("empty gallery missing empty-state heading")
	}
	if !strings.Contains(page, `href="/connect"`) {
		t.Errorf("empty gallery must link /connect")
	}
	// No grid/toolbar when there is nothing to show.
	if strings.Contains(page, `id="ggrid"`) {
		t.Errorf("empty gallery should not render the card grid")
	}
}

func TestGalleryEscapesFields(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, Config{})

	// A slug is validated on ingest and can't hold markup, but the renderer must
	// escape unconditionally — feed a hostile slug straight to the card renderer.
	a := store.Artifact{
		Slug:        `evil"><script>alert(1)</script>`,
		ContentType: "text/html; charset=utf-8",
		Size:        2048,
		CreatedAt:   time.Now(),
	}
	card := s.galleryCard("http://x", a)
	if strings.Contains(card, "<script>alert(1)</script>") {
		t.Errorf("galleryCard did not escape the slug: %s", card)
	}
	if !strings.Contains(card, "&lt;script&gt;") {
		t.Errorf("galleryCard should HTML-escape angle brackets: %s", card)
	}
}

func TestGalleryLockIconForPassword(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, Config{})

	locked := s.galleryCard("http://x", store.Artifact{Slug: "gated", ContentType: "text/html", PasswordHash: "abc"})
	if !strings.Contains(locked, "🔒") {
		t.Errorf("password-gated card should show a lock icon")
	}
	open := s.galleryCard("http://x", store.Artifact{Slug: "open", ContentType: "text/html"})
	if strings.Contains(open, "🔒") {
		t.Errorf("un-gated card should not show a lock icon")
	}
}

// TestGalleryCardIsSlugFirst asserts the slimmed card (demiplane-0pw): the slug
// is the card with a published-date caption, and the type-badge / size / expiry
// micro-labels are gone from the visible surface. The type/size stay only as
// data-* attributes so the client filter/sort/group still works.
func TestGalleryCardIsSlugFirst(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, Config{})

	when := time.Date(2026, 7, 15, 9, 30, 0, 0, time.UTC)
	card := s.galleryCard("http://x", store.Artifact{
		Slug: "pic", ContentType: "image/svg+xml", Size: 3 * 1024, CreatedAt: when,
	})
	// Slug-first: the slug is the title, with a published-date caption.
	if !strings.Contains(card, `>pic</a>`) {
		t.Errorf("card missing slug title, got: %s", card)
	}
	if !strings.Contains(card, `class="gdate"`) || !strings.Contains(card, "2026-07-15 09:30") {
		t.Errorf("card missing published-date caption, got: %s", card)
	}
	// Visible type badge, human size, and expiry micro-labels are gone.
	if strings.Contains(card, `class="gbadge"`) || strings.Contains(card, "3.0 KB") ||
		strings.Contains(card, `class="gmeta"`) {
		t.Errorf("card still shows type/size/meta clutter, got: %s", card)
	}
	// But the type/size survive as data-* for the filter/sort script.
	if !strings.Contains(card, `data-type="svg"`) || !strings.Contains(card, `data-size="3072"`) {
		t.Errorf("card dropped the data-* attributes the client script needs, got: %s", card)
	}
}

func TestSlugGroup(t *testing.T) {
	cases := []struct{ in, want string }{
		{"dispatch-08", "dispatch"},
		{"dispatch-01", "dispatch"},
		{"notes-reply-log", "notes"},
		{"plan-q3", "plan"},
		{"overnight-run", "overnight"},
		{"notes", "notes"}, // hyphen-free: own key (client folds singletons)
		{"docs", "docs"},   // hyphen-free
		{"ME-Notes", "me"}, // lowercased
		{"  spaced ", "spaced"},
		{"", "other"},            // empty falls back
		{"-leading", "-leading"}, // leading hyphen: not a prefix separator
	}
	for _, c := range cases {
		if got := slugGroup(c.in); got != c.want {
			t.Errorf("slugGroup(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGalleryCardHasGroupData(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, Config{})

	card := s.galleryCard("http://x", store.Artifact{Slug: "dispatch-08", ContentType: "text/html"})
	if !strings.Contains(card, `data-group="dispatch"`) {
		t.Errorf("card missing data-group derived from slug prefix: %s", card)
	}
}

func TestGalleryHasGroupControl(t *testing.T) {
	ts := newTestServer(t, "")
	publish(t, ts, "?slug=dispatch-01", "<h1>a</h1>")
	publish(t, ts, "?slug=dispatch-02", "<h1>b</h1>")

	_, body := get(t, ts.URL+"/gallery")
	page := string(body)
	for _, want := range []string{`id="ggroup"`, "Group by prefix", "No grouping", `data-group="dispatch"`} {
		if !strings.Contains(page, want) {
			t.Errorf("gallery page missing group control marker %q", want)
		}
	}
}

func TestShortTypeLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"text/html; charset=utf-8", "html"},
		{"image/svg+xml", "svg"},
		{"image/png", "png"},
		{"application/pdf", "pdf"},
		{"application/octet-stream", "bin"},
		{"text/plain; charset=utf-8", "text"},
		{"application/javascript", "js"},
		{"text/markdown", "md"},
		{"application/json", "json"},
	}
	for _, c := range cases {
		if got := shortTypeLabel(c.in); got != c.want {
			t.Errorf("shortTypeLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{5 * 1024 * 1024 * 1024, "5.0 GB"},
	}
	for _, c := range cases {
		if got := humanSize(c.in); got != c.want {
			t.Errorf("humanSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestGalleryOnContentOriginNotServed confirms the gallery is a CONTROL-plane
// route: it must NOT appear on the content origin's handler (ADR 0003). On the
// content handler, GET /gallery falls through to the artifact catch-all and 404s
// (no such slug).
func TestGalleryIsControlPlaneOnly(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, Config{})

	req, _ := http.NewRequest("GET", "http://content.example/gallery", nil)
	rec := httptest.NewRecorder()
	s.ContentHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("gallery on content origin: status = %d, want 404 (artifact catch-all)", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "class=\"gcard\"") {
		t.Errorf("gallery must not render on the content origin")
	}
}
