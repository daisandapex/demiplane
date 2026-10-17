// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/store"
)

// TestLongHostIsRefusedBeforeRouting: a Host longer than maxHostLen gets 400
// on every listener's handler before any route runs; one at the limit is
// routed.
func TestLongHostIsRefusedBeforeRouting(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, Config{Browse: true, ContentPort: "8081"})
	for name, h := range map[string]http.Handler{
		"combined": s.Handler(),
		"control":  s.ControlHandler(),
		"content":  s.ContentHandler(),
	} {
		for _, tc := range []struct {
			host string
			want bool // refused
		}{
			{strings.Repeat("a", 300), true},
			{strings.Repeat("a", 250) + ":8080", false},
		} {
			r := httptest.NewRequest(http.MethodGet, "/gallery", nil)
			r.Host = tc.host
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if refused := w.Code == http.StatusBadRequest; refused != tc.want {
				t.Errorf("%s: Host of %d bytes = %d, refused=%v want %v", name, len(tc.host), w.Code, refused, tc.want)
			}
		}
	}
	if n := len(sanitizeHost(strings.Repeat("a", 1000))); n > maxHostLen {
		t.Errorf("sanitizeHost produced %d bytes, cap %d", n, maxHostLen)
	}
}

// browseStore publishes n public artifacts.
func browseStore(t *testing.T, n int) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for i := 0; i < n; i++ {
		if _, err := st.Put(store.PutOptions{Slug: fmt.Sprintf("art-%04d", i)}, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

var galleryHref = regexp.MustCompile(`class="gtitle" href="[^"]*/(art-\d{4})"`)
var galleryMore = regexp.MustCompile(`href="(/gallery\?after=[^"]+)"`)

// TestGalleryIsPagedAndBounded: with many artifacts and the longest accepted
// Host, one /gallery response holds at most listPageSize cards and stays
// small, and following its links lists every artifact exactly once.
func TestGalleryIsPagedAndBounded(t *testing.T) {
	const n = 450
	s := New(browseStore(t, n), Config{Browse: true})
	h := s.Handler()
	host := strings.Repeat("h", 250) + ":80"
	get := func(target string) string {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", target, w.Code)
		}
		return w.Body.String()
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	first := get("/gallery")
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 16<<20 {
		t.Errorf("one gallery page allocated %d bytes", alloc)
	}
	if size := len(first); size > 1<<20 {
		t.Errorf("one gallery page is %d bytes", size)
	}

	seen := map[string]int{}
	page, target := first, "/gallery"
	for pages := 0; ; pages++ {
		cards := galleryHref.FindAllStringSubmatch(page, -1)
		if len(cards) > 200 {
			t.Fatalf("%s holds %d cards, page size 200", target, len(cards))
		}
		for _, c := range cards {
			seen[c[1]]++
		}
		m := galleryMore.FindStringSubmatch(page)
		if m == nil {
			break
		}
		if pages > 10 {
			t.Fatal("gallery pages never end")
		}
		target = html.UnescapeString(m[1])
		page = get(target)
	}
	if len(seen) != n {
		t.Errorf("gallery pages listed %d distinct artifacts, want %d", len(seen), n)
	}
	for slug, c := range seen {
		if c != 1 {
			t.Errorf("%s listed %d times", slug, c)
		}
	}
}

// TestListIsPaged: GET /list returns at most listPageSize artifacts with a
// next cursor, and following the cursors lists every artifact exactly once.
func TestListIsPaged(t *testing.T) {
	const n = 450
	h := New(browseStore(t, n), Config{}).Handler()
	seen := map[string]int{}
	target := "/list"
	for pages := 0; ; pages++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", target, w.Code)
		}
		var out struct {
			Artifacts []listItem `json:"artifacts"`
			Count     int        `json:"count"`
			Next      string     `json:"next"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Artifacts) > 200 || out.Count != len(out.Artifacts) {
			t.Fatalf("%s: %d artifacts, count %d; want at most 200 and equal", target, len(out.Artifacts), out.Count)
		}
		for _, a := range out.Artifacts {
			seen[a.Slug]++
		}
		if out.Next == "" {
			break
		}
		if pages > 10 {
			t.Fatal("list pages never end")
		}
		target = "/list?after=" + out.Next
	}
	if len(seen) != n {
		t.Errorf("list pages listed %d distinct artifacts, want %d", len(seen), n)
	}
	for slug, c := range seen {
		if c != 1 {
			t.Errorf("%s listed %d times", slug, c)
		}
	}
}

func TestListingRejectsABadCursor(t *testing.T) {
	h := New(browseStore(t, 1), Config{Browse: true}).Handler()
	for _, target := range []string{"/list?after=nope", "/gallery?after=12"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", target, w.Code)
		}
	}
}
