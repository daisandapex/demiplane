// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// TestUpgradeListTraversal is the upgrade acceptance check for /list paging:
// 201 live artifacts (published in bursts, so many share a second), with
// 1,150 expired ones the sweeper has not removed between the newest 100 and
// the oldest 101. The first page examines its budget of entries and returns
// the 100 newest with `next` (a short, non-empty page); following `next`
// until it is absent lists every live artifact exactly once and no expired
// one.
func TestUpgradeListTraversal(t *testing.T) {
	if testing.Short() {
		t.Skip("publishes 1,500 artifacts")
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	const old, expired, recent = 101, 1150, 100
	const live = old + recent
	put := func(slug string, ttl time.Duration) {
		if _, err := st.Put(store.PutOptions{Slug: slug, TTL: ttl}, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < old; i++ {
		put(fmt.Sprintf("live-old-%04d", i), 0)
	}
	for i := 0; i < expired; i++ {
		put(fmt.Sprintf("gone-%04d", i), time.Second)
	}
	time.Sleep(1100 * time.Millisecond) // the expired batch lapses
	for i := 0; i < recent; i++ {
		put(fmt.Sprintf("live-new-%04d", i), 0)
	}
	h := New(st, Config{}).Handler()
	seen := map[string]int{}
	short, empty := 0, 0
	target := "/list"
	for pages := 0; ; pages++ {
		if pages > 40 {
			t.Fatal("paging does not end")
		}
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
		if out.Count != len(out.Artifacts) || len(out.Artifacts) > 200 {
			t.Fatalf("page %d: %d artifacts, count %d", pages, len(out.Artifacts), out.Count)
		}
		for _, a := range out.Artifacts {
			if strings.HasPrefix(a.Slug, "gone-") {
				t.Fatalf("expired artifact %s listed", a.Slug)
			}
			seen[a.Slug]++
		}
		if out.Next == "" {
			break
		}
		if len(out.Artifacts) == 0 {
			empty++
		} else if len(out.Artifacts) < 200 {
			short++
		}
		target = "/list?after=" + out.Next
	}
	if len(seen) != live {
		t.Fatalf("traversal listed %d of %d live artifacts", len(seen), live)
	}
	for slug, c := range seen {
		if c != 1 {
			t.Fatalf("%s listed %d times", slug, c)
		}
	}
	if short == 0 {
		t.Error("no short, non-empty page carried next; the fixture did not exercise it")
	}
	t.Logf("%d short and %d empty pages with next", short, empty)
}

// TestUpgradeSiteMultiRangeServedWhole: a site file answers a single range
// with 206 and a multi-range or over-long Range header with the whole body.
func TestUpgradeSiteMultiRangeServedWhole(t *testing.T) {
	ts := newTestServer(t, "")
	body := strings.Repeat("y", 4096)
	if resp, b := postSite(t, ts.URL, "manual", "fmt=tar", "application/x-tar", makeTar(t, []srcFile{{name: "index.html", body: body}})); resp.StatusCode != http.StatusCreated {
		t.Fatalf("site publish = %d %s", resp.StatusCode, b)
	}
	get := func(rng string) (int, int) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/manual/index.html", nil)
		req.Header.Set("Range", rng)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		n, _ := io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, int(n)
	}
	if code, n := get("bytes=0-9"); code != http.StatusPartialContent || n != 10 {
		t.Errorf("single range: %d, %d bytes; want 206, 10", code, n)
	}
	if code, n := get("bytes=0-0,2-2"); code != http.StatusOK || n != len(body) {
		t.Errorf("two ranges: %d, %d bytes; want 200 with the whole body", code, n)
	}
	// A valid single range spelled with leading zeros, longer than the
	// 128-byte limit (trailing padding would be trimmed in transit).
	if code, n := get("bytes=" + strings.Repeat("0", 150) + "-1"); code != http.StatusOK || n != len(body) {
		t.Errorf("over-long Range: %d, %d bytes; want 200 with the whole body", code, n)
	}
}
