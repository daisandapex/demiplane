// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// tokenlessSetup is setup on an instance with no token. Besides the public
// "report" it seeds "page", password-protected, both accepting replies.
func tokenlessSetup(t *testing.T) (*httptest.Server, *Module, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Put(store.PutOptions{Slug: "report", Replies: true}, strings.NewReader("<html>r</html>")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(store.PutOptions{Slug: "page", Replies: true, Password: "pw"}, strings.NewReader("<html>p</html>")); err != nil {
		t.Fatal(err)
	}
	h := &fakeHost{st: st, dataDir: t.TempDir(), tokenless: true}
	m := &Module{}
	m.setLimits(openLimits())
	mux := http.NewServeMux()
	m.Routes(mux, h)
	if m.rs == nil {
		t.Fatal("reply store did not open")
	}
	t.Cleanup(func() { m.rs.close() })
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, m, st
}

type listPage struct {
	Replies []Reply `json:"replies"`
	Next    string  `json:"next"`
}

func getPage(t *testing.T, u string) listPage {
	t.Helper()
	code, body := do(t, http.MethodGet, u, "", "", false)
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", u, code, body)
	}
	var p listPage
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestReplyKeepsItsArtifactsProtection: on an instance with no token, a
// reply written to a password-protected artifact stays out of the inbox
// after the artifact is replaced by an unprotected one. Replies survive a
// republish, so reading protection from the current artifact would hand
// them out. See audit/06-replies-to-password-pages-open-without-token.md.
func TestReplyKeepsItsArtifactsProtection(t *testing.T) {
	ts, m, st := tokenlessSetup(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/reply/page", strings.NewReader(`{"kind":"comment","body":"the merger closes on the 3rd"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("u", "pw")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("reply with the view password = %d, want 201", resp.StatusCode)
	}
	if _, err := st.Put(store.PutOptions{Slug: "page", Replies: true}, strings.NewReader("<html>now public</html>")); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"/replies?status=all", "/replies?slug=page&status=all"} {
		if p := getPage(t, ts.URL+u); len(p.Replies) != 0 {
			t.Errorf("GET %s after the artifact became public lists %q", u, p.Replies[0].Body)
		}
	}
	if code, body := do(t, http.MethodDelete, ts.URL+"/replies?slug=page", "", "", false); code != http.StatusOK || !strings.Contains(string(body), `"deleted":0`) {
		t.Errorf("tokenless DELETE /replies?slug=page = %d %s, want 200 deleting none", code, body)
	}
	if n, _ := m.rs.count("page"); n != 1 {
		t.Errorf("replies left to page = %d, want 1", n)
	}
}

// TestTokenlessPagingSkipsHiddenReplies: the inbox filters before it pages,
// so a page is not emptied by replies the caller may not see, and no cursor
// is taken from such a reply (its timestamp and id). See
// audit/06-replies-to-password-pages-open-without-token.md.
func TestTokenlessPagingSkipsHiddenReplies(t *testing.T) {
	ts, m, _ := tokenlessSetup(t)
	at := time.Unix(1_700_000_000, 0)
	visible, err := m.rs.add("report", "comment", "public note", at)
	if err != nil {
		t.Fatal(err)
	}
	hidden := map[int64]bool{}
	for i := 0; i < listPageSize+50; i++ {
		r, err := m.rs.add("page", "comment", "secret", at.Add(time.Duration(i+1)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		hidden[r.ID] = true
	}
	var got []int64
	after := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging does not end")
		}
		u := ts.URL + "/replies?status=all"
		if after != "" {
			u += "&after=" + url.QueryEscape(after)
		}
		p := getPage(t, u)
		for _, r := range p.Replies {
			got = append(got, r.ID)
		}
		if p.Next == "" {
			break
		}
		_, idStr, _ := strings.Cut(p.Next, ".")
		if id, _ := strconv.ParseInt(idStr, 10, 64); hidden[id] {
			t.Errorf("page %d: next cursor %q is a hidden reply's", pages, p.Next)
		}
		if pages == 0 && len(p.Replies) == 0 {
			t.Errorf("the first page is empty but has a next cursor")
		}
		after = p.Next
	}
	if len(got) != 1 || got[0] != visible.ID {
		t.Fatalf("listed %v, want only the public reply %d", got, visible.ID)
	}
}

// TestTokenlessChangeOfHiddenReplyLooksMissing: without a token, acking or
// deleting a reply the inbox hides answers exactly as for an id that does
// not exist, so ids cannot be probed for replies to protected pages. See
// audit/06-replies-to-password-pages-open-without-token.md.
func TestTokenlessChangeOfHiddenReplyLooksMissing(t *testing.T) {
	ts, m, _ := tokenlessSetup(t)
	r, err := m.rs.add("page", "comment", "secret", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	hidden, missing := strconv.FormatInt(r.ID, 10), strconv.FormatInt(r.ID+1000, 10)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/replies/%s/ack"},
		{http.MethodDelete, "/replies/%s"},
	} {
		hc, hb := do(t, c.method, ts.URL+strings.Replace(c.path, "%s", hidden, 1), "", "", false)
		mc, mb := do(t, c.method, ts.URL+strings.Replace(c.path, "%s", missing, 1), "", "", false)
		if hc != mc || string(hb) != string(mb) {
			t.Errorf("%s %s: hidden reply %d %q, missing reply %d %q; want the same", c.method, c.path, hc, hb, mc, mb)
		}
	}
	if n, _ := m.rs.count("page"); n != 1 {
		t.Errorf("replies left to page = %d, want 1", n)
	}
}

// TestTokenHolderManagesRepliesOfAGoneArtifact: with a token configured,
// the token holder can ack and delete replies to an artifact that no longer
// exists, by id and by slug, as the removal hook's log message advises when
// it could not remove them.
func TestTokenHolderManagesRepliesOfAGoneArtifact(t *testing.T) {
	ts, m := setup(t)
	var ids []string
	for i := 0; i < 3; i++ {
		r, err := m.rs.add("gone", "comment", "orphan", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, strconv.FormatInt(r.ID, 10))
	}
	if code, b := do(t, http.MethodPost, ts.URL+"/replies/"+ids[0]+"/ack", "", "", true); code != http.StatusNoContent {
		t.Errorf("ack = %d %s, want 204", code, b)
	}
	if code, b := do(t, http.MethodDelete, ts.URL+"/replies/"+ids[1], "", "", true); code != http.StatusNoContent {
		t.Errorf("delete by id = %d %s, want 204", code, b)
	}
	if p := func() listPage {
		code, body := do(t, http.MethodGet, ts.URL+"/replies?slug=gone&status=all", "", "", true)
		if code != http.StatusOK {
			t.Fatalf("list = %d %s", code, body)
		}
		var p listPage
		json.Unmarshal(body, &p)
		return p
	}(); len(p.Replies) != 2 {
		t.Errorf("token holder lists %d replies to the gone artifact, want 2", len(p.Replies))
	}
	if code, b := do(t, http.MethodDelete, ts.URL+"/replies?slug=gone", "", "", true); code != http.StatusOK || !strings.Contains(string(b), `"deleted":2`) {
		t.Errorf("delete by slug = %d %s, want 200 deleting 2", code, b)
	}
}

// TestRepliesFromBeforeProtectionWasRecorded: a replies database from before
// the protected column gains it on open; its replies count as protected
// until backfilled from their artifact as it is then.
func TestRepliesFromBeforeProtectionWasRecorded(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "replies.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE replies (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    slug       TEXT NOT NULL,
    kind       TEXT NOT NULL,
    body       TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending'
);
INSERT INTO replies (slug, kind, body, created_at) VALUES ('report', 'comment', 'old public', 1), ('page', 'comment', 'old secret', 2);`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	rs, err := openReplyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.close()
	for id := int64(1); id <= 2; id++ {
		if _, protected, err := rs.slugOf(id); err != nil || !protected {
			t.Fatalf("reply %d before the backfill: protected %v, %v; want protected", id, protected, err)
		}
	}
	// "report" is public and was published before its reply (created_at 1);
	// "page" is protected now.
	if err := rs.backfillProtected(func(slug string) (int64, bool) { return 0, slug == "report" }); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]bool{1: false, 2: true} {
		if _, protected, _ := rs.slugOf(id); protected != want {
			t.Errorf("reply %d after the backfill: protected %v, want %v", id, protected, want)
		}
	}
	got, _, err := rs.listOnly(context.Background(), "", "all", nil, 10, &[]string{"report", "page"})
	if err != nil || len(got) != 1 || got[0].Body != "old public" {
		t.Errorf("unprotected listing = %v, %v; want only the public reply", got, err)
	}
}

// TestTokenlessListingWorkIsBounded: without a token, a listing of every
// slug looks up whether each reply's artifact is public now. That work must
// be bounded per request, not one core lookup for every slug with a reply,
// whatever the page holds (GET /replies?status=all&after=0.0 answers an
// empty page), and must stop when the request is cancelled. Paging still
// reaches every visible reply.
func TestTokenlessListingWorkIsBounded(t *testing.T) {
	ts, m, st := tokenlessSetup(t)
	h := m.host.(*fakeHost)
	at := time.Unix(1_700_000_000, 0)
	const hiddenSlugs = 2000
	// Replies written to public artifacts since removed or made private:
	// unprotected, but not on a page anyone may read now. One transaction,
	// as rs.add commits each.
	tx, err := m.rs.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < hiddenSlugs; i++ {
		if _, err := tx.Exec(`INSERT INTO replies (slug, kind, body, created_at, status, protected) VALUES (?, 'comment', 'x', ?, 'pending', 0)`,
			"gone-"+strconv.Itoa(i), at.Add(time.Duration(i)*time.Second).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var want []int64
	for i, slug := range []string{"pub-a", "pub-b", "pub-c"} {
		if _, err := st.Put(store.PutOptions{Slug: slug, Replies: true}, strings.NewReader("<html>p</html>")); err != nil {
			t.Fatal(err)
		}
		r, err := m.rs.add(slug, "comment", "visible", at.Add(time.Duration(i*900)*time.Second+time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, r.ID)
	}
	const bound = 2 * listPageSize // far below one lookup per slug
	lookups := func(u string) int64 {
		t.Helper()
		before := h.stores.Load()
		getPage(t, ts.URL+u)
		return h.stores.Load() - before
	}
	if n := lookups("/replies?status=all&after=0.0"); n > bound {
		t.Errorf("an empty page (after=0.0) took %d core lookups, want at most %d", n, bound)
	}
	if n := lookups("/replies?status=all"); n > bound {
		t.Errorf("the first page took %d core lookups, want at most %d", n, bound)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := h.stores.Load()
	rec := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/replies?status=all", nil).WithContext(ctx))
	if n := h.stores.Load() - before; n > 1 {
		t.Errorf("a cancelled listing took %d core lookups, want none", n)
	}

	got := map[int64]bool{}
	after := ""
	for pages := 0; ; pages++ {
		if pages > hiddenSlugs {
			t.Fatal("paging does not end")
		}
		u := ts.URL + "/replies?status=all"
		if after != "" {
			u += "&after=" + url.QueryEscape(after)
		}
		p := getPage(t, u)
		for _, r := range p.Replies {
			got[r.ID] = true
		}
		if p.Next == "" {
			break
		}
		after = p.Next
	}
	if len(got) != len(want) {
		t.Errorf("paged to %d replies, want the %d visible ones", len(got), len(want))
	}
	if code, _ := do(t, http.MethodGet, ts.URL+"/replies?status=all&after=sAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "", "", false); code != http.StatusBadRequest {
		t.Errorf("a forged sealed cursor = %d, want 400", code)
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("visible reply %d never listed", id)
		}
	}
}

// TestLegacyReplyToALaterRepublishedPageStaysProtected: a reply stored before
// protection was recorded does not say whether its page had a password when
// it was written. The page being public now does not settle it: replies
// survive a republish, so a confidential reply to a password-protected page
// later republished without the password would be handed to anyone on an
// instance with no token. Only a page last published before the reply was
// the page it answered (every publish sets created_at; protection never
// changes in place), so only then does its being public now count.
func TestLegacyReplyToALaterRepublishedPageStaysProtected(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// "review": republished without its password after the reply.
	// "report": public, and unchanged since the reply.
	for _, slug := range []string{"review", "report"} {
		if _, err := st.Put(store.PutOptions{Slug: slug, Replies: true}, strings.NewReader("<html>now public</html>")); err != nil {
			t.Fatal(err)
		}
	}
	reviewPublished, err := st.LookupPublic("review")
	if err != nil {
		t.Fatal(err)
	}
	before := reviewPublished.CreatedAt.Add(-time.Hour).Unix()
	after := reviewPublished.CreatedAt.Add(time.Hour).Unix()

	dataDir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "replies.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE replies (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    slug       TEXT NOT NULL,
    kind       TEXT NOT NULL,
    body       TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending'
);
INSERT INTO replies (slug, kind, body, created_at) VALUES ('review', 'comment', 'confidential', ?), ('report', 'comment', 'public note', ?);`, before, after); err != nil {
		t.Fatal(err)
	}
	db.Close()

	h := &fakeHost{st: st, dataDir: dataDir, tokenless: true}
	m := &Module{}
	m.setLimits(openLimits())
	mux := http.NewServeMux()
	m.Routes(mux, h)
	if m.rs == nil {
		t.Fatal("reply store did not open")
	}
	t.Cleanup(func() { m.rs.close() })
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	for _, u := range []string{"/replies?slug=review&status=all", "/replies?status=all"} {
		for _, r := range getPage(t, ts.URL+u).Replies {
			if r.Body == "confidential" {
				t.Errorf("GET %s without a token lists the legacy reply to a page republished after it", u)
			}
		}
	}
	if p := getPage(t, ts.URL+"/replies?slug=report&status=all"); len(p.Replies) != 1 || p.Replies[0].Body != "public note" {
		t.Errorf("legacy reply to a public page unchanged since = %v, want it listed", p.Replies)
	}
}
