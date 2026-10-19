// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// TestDeletedArtifactTakesItsReplies: deleting an artifact deletes its
// replies, so a later artifact published under the same slug neither lists
// them nor starts part-way to its reply cap.
func TestDeletedArtifactTakesItsReplies(t *testing.T) {
	_, m := setup(t)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := m.rs.add("report", "comment", "old "+strconv.Itoa(i), now); err != nil {
			t.Fatal(err)
		}
	}
	st := m.host.Store()
	if err := st.Delete("report"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(store.PutOptions{Slug: "report", Replies: true}, strings.NewReader("<p>new</p>")); err != nil {
		t.Fatal(err)
	}
	if n := storedCount(t, m, "report"); n != 0 {
		t.Fatalf("the new artifact at the slug inherited %d replies", n)
	}
}

// TestRepliesCanBeDeleted: the operator frees reply capacity through the
// bearer-gated API, one reply or a slug's at a time.
func TestRepliesCanBeDeleted(t *testing.T) {
	ts, m := setup(t)
	now := time.Now()
	var ids []int64
	for i := 0; i < 3; i++ {
		r, err := m.rs.add("report", "comment", "r", now)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	if code, _ := do(t, http.MethodDelete, ts.URL+"/replies/"+strconv.FormatInt(ids[0], 10), "", "", false); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated delete = %d, want 401", code)
	}
	if code, _ := do(t, http.MethodDelete, ts.URL+"/replies/"+strconv.FormatInt(ids[0], 10), "", "", true); code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", code)
	}
	if code, _ := do(t, http.MethodDelete, ts.URL+"/replies/"+strconv.FormatInt(ids[0], 10), "", "", true); code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", code)
	}
	code, body := do(t, http.MethodDelete, ts.URL+"/replies?slug=report", "", "", true)
	var got struct{ Deleted int64 }
	if code != http.StatusOK || json.Unmarshal(body, &got) != nil || got.Deleted != 2 {
		t.Fatalf("delete by slug = %d %s; want 200 deleted 2", code, body)
	}
	var n, bytes int64
	if err := m.rs.db.QueryRow(`SELECT n, bytes FROM reply_stats WHERE id = 1`).Scan(&n, &bytes); err != nil || n != 0 || bytes != 0 {
		t.Fatalf("reply totals after deleting everything = %d, %d, %v", n, bytes, err)
	}
	if code, _ := do(t, http.MethodDelete, ts.URL+"/replies", "", "", true); code != http.StatusBadRequest {
		t.Fatalf("delete without a slug = %d, want 400", code)
	}
}
