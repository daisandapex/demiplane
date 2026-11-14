// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
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
	if err := rs.backfillProtected(func(slug string) bool { return slug == "page" }); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]bool{1: false, 2: true} {
		if _, protected, _ := rs.slugOf(id); protected != want {
			t.Errorf("reply %d after the backfill: protected %v, want %v", id, protected, want)
		}
	}
	got, _, err := rs.listOnly("", "all", nil, 10, &[]string{"report", "page"})
	if err != nil || len(got) != 1 || got[0].Body != "old public" {
		t.Errorf("unprotected listing = %v, %v; want only the public reply", got, err)
	}
}
