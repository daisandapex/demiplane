// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// TestBulkRerenderNeverShowsAPrivateSlug: on an instance with no token, a
// bulk rerender still rebakes private artifacts, but no report or resume
// cursor carries a private capability slug, and resuming with the cursor
// still accounts for every artifact once.
func TestBulkRerenderNeverShowsAPrivateSlug(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var private []string
	for i := 0; i < 3; i++ {
		a, err := st.Put(store.PutOptions{Private: true}, strings.NewReader("secret"))
		if err != nil {
			t.Fatal(err)
		}
		private = append(private, a.Slug)
		if _, err := st.Put(store.PutOptions{Slug: fmt.Sprintf("public-%d", i)}, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	s := New(st, Config{}) // no token: anyone may call POST /rerender
	h := s.Handler()
	leaks := func(where, body string) {
		t.Helper()
		for _, p := range private {
			if strings.Contains(body, p) {
				t.Errorf("%s carries private slug %s:\n%s", where, p, body)
			}
		}
	}
	post := func(target string, asJSON bool) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, target, nil)
		if asJSON {
			r.Header.Set("Accept", "application/json")
		}
		w := serve(h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", target, w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	leaks("text report", post("/rerender", false))
	full := post("/rerender", true)
	leaks("JSON report", full)
	var whole struct {
		Skipped int              `json:"skipped"`
		Results []rerenderResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(full), &whole); err != nil || whole.Skipped != 6 {
		t.Fatalf("full sweep skipped %d (%v), want 6, private included", whole.Skipped, err)
	}
	tagged := 0
	for _, res := range whole.Results {
		if res.Private {
			tagged++
			if !strings.HasPrefix(res.Slug, "slug#") {
				t.Errorf("private result named %q, want a slug# tag", res.Slug)
			}
		}
	}
	if tagged != 3 {
		t.Errorf("%d private results, want 3", tagged)
	}
	// A slug is not a cursor.
	r := httptest.NewRequest(http.MethodPost, "/rerender?after="+private[0], nil)
	if w := serve(h, r); w.Code != http.StatusBadRequest {
		t.Errorf("after=<slug> = %d, want 400", w.Code)
	}

	// Stop early on every page and follow the cursor: no cursor is a private
	// slug, and the sweeps together cover all six artifacts once.
	savedMax, savedPage, savedWait := rerenderSweepMax, rerenderPage, renderWait
	rerenderPage, renderWait = 1, 50*time.Millisecond
	t.Cleanup(func() { rerenderSweepMax, rerenderPage, renderWait = savedMax, savedPage, savedWait })
	holdRenderSlots(t, s)
	rerenderSweepMax = 120 * time.Millisecond
	handled, after := 0, ""
	for i := 0; i < 50; i++ {
		body := post("/rerender?after="+after, true)
		leaks("resumed report", body)
		var out struct {
			FailedCount int    `json:"failed_count"`
			Complete    bool   `json:"complete"`
			Next        string `json:"next"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		handled += out.FailedCount
		if out.Complete {
			break
		}
		after = out.Next
	}
	if handled != 6 {
		t.Errorf("resumed sweeps handled %d artifacts, want 6", handled)
	}
}
