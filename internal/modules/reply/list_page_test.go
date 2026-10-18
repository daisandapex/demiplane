// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestListIsPaged: GET /replies returns at most listPageSize replies, newest
// first, with a next cursor while more remain; following it lists every
// reply exactly once, across same-second ties, and a bad cursor is a 400.
func TestListIsPaged(t *testing.T) {
	ts, m := setup(t)
	const total = 250
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < total; i++ {
		// Groups of five share a second, so the cursor must order by id too.
		if _, err := m.rs.add("s1", "comment", "r", base.Add(time.Duration(i/5)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int64]bool{}
	after, pages := "", 0
	for {
		u := ts.URL + "/replies?status=all"
		if after != "" {
			u += "&after=" + url.QueryEscape(after)
		}
		code, body := do(t, http.MethodGet, u, "", "", true)
		if code != http.StatusOK {
			t.Fatalf("page %d: status %d %s", pages, code, body)
		}
		var page struct {
			Replies []Reply `json:"replies"`
			Count   int     `json:"count"`
			Next    string  `json:"next"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		pages++
		if len(page.Replies) > listPageSize || page.Count != len(page.Replies) {
			t.Fatalf("page %d: %d replies, count %d, page size %d", pages, len(page.Replies), page.Count, listPageSize)
		}
		for _, r := range page.Replies {
			if seen[r.ID] {
				t.Fatalf("reply %d listed twice", r.ID)
			}
			seen[r.ID] = true
		}
		if page.Next == "" {
			break
		}
		after = page.Next
		if pages > total {
			t.Fatal("paging does not end")
		}
	}
	if len(seen) != total {
		t.Fatalf("paging listed %d of %d replies", len(seen), total)
	}
	if code, _ := do(t, http.MethodGet, ts.URL+"/replies?after=x.y", "", "", true); code != http.StatusBadRequest {
		t.Errorf("bad cursor: status %d, want 400", code)
	}
}

// TestListQueriesUseAnIndex: every filter combination of a page reads along
// an index, so a page costs its own rows, not the store's.
func TestListQueriesUseAnIndex(t *testing.T) {
	_, m := setup(t)
	for _, c := range []struct{ name, where string }{
		{"all", ""},
		{"status", "WHERE status = 'pending' AND (created_at, id) < (1, 1)"},
		{"slug", "WHERE slug = 's' AND (created_at, id) < (1, 1)"},
		{"slug and status", "WHERE slug = 's' AND status = 'read'"},
	} {
		rows, err := m.rs.db.Query("EXPLAIN QUERY PLAN SELECT id FROM replies " + c.where + " ORDER BY created_at DESC, id DESC LIMIT 101")
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		joined := strings.Join(plan, "; ")
		if !strings.Contains(joined, "USING") || strings.Contains(joined, "TEMP B-TREE") {
			t.Errorf("%s: query plan %q does not walk an index in order", c.name, joined)
		}
	}
}

// TestListPageBoundary: 101 replies in one second page as 100 and 1, the
// first page carrying next and the last none.
func TestListPageBoundary(t *testing.T) {
	ts, m := setup(t)
	at := time.Unix(1_700_000_000, 0)
	for i := 0; i < 101; i++ {
		if _, err := m.rs.add("s1", "comment", "r", at); err != nil {
			t.Fatal(err)
		}
	}
	var pages []int
	seen := map[int64]bool{}
	after := ""
	for len(pages) < 5 {
		u := ts.URL + "/replies?status=all"
		if after != "" {
			u += "&after=" + url.QueryEscape(after)
		}
		_, body := do(t, http.MethodGet, u, "", "", true)
		var page struct {
			Replies []Reply `json:"replies"`
			Next    string  `json:"next"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		pages = append(pages, len(page.Replies))
		for _, r := range page.Replies {
			seen[r.ID] = true
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if len(pages) != 2 || pages[0] != 100 || pages[1] != 1 || len(seen) != 101 {
		t.Fatalf("pages %v, %d distinct replies; want [100 1] and 101", pages, len(seen))
	}
}
