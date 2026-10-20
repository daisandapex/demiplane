// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/manage"
	"github.com/daisandapex/demiplane/v3/internal/server"
	"github.com/daisandapex/demiplane/v3/internal/store"
)

type manageRoundTrip func(*http.Request) (*http.Response, error)

func (f manageRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func manageMCP(t *testing.T) (*store.Store, *Client, *int) {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err = s.Put(store.PutOptions{Slug: "page"}, strings.NewReader("bytes")); err != nil {
		t.Fatal(err)
	}
	srv := server.New(s, server.Config{AuthToken: "owner", BaseURL: "https://control.test"})
	h := srv.ControlHandler()
	c := NewClient("https://control.test", "", "owner")
	writes := 0
	c.HTTP.Transport = manageRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			writes++
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Result(), nil
	})
	return s, c, &writes
}
func manageCall(t *testing.T, c *Client, name string, body any) *toolResult {
	t.Helper()
	raw, _ := json.Marshal(body)
	r, err := callTool(context.Background(), c, name, raw)
	if err != nil {
		t.Fatalf("%s: %+v", name, err)
	}
	return r
}
func TestManageMCPDeleteMany(t *testing.T) {
	s, c, writes := manageMCP(t)
	r := manageCall(t, c, "delete_many", map[string]any{"match": "*"})
	var m manage.Manifest
	if err := json.Unmarshal([]byte(r.Content[0].Text), &m); err != nil {
		t.Fatal(err)
	}
	if *writes != 0 || len(m.Items) != 1 || m.Items[0].ID != "page" || m.Items[0].Revision == "" || m.SelectedAt.IsZero() {
		t.Fatalf("dry run %+v writes %d", m, *writes)
	}
	for _, body := range []any{map[string]any{"dry_run": false}, map[string]any{"dry_run": false, "manifest": m}, map[string]any{"dry_run": false, "manifest": m, "confirm_host": "wrong"}} {
		raw, _ := json.Marshal(body)
		if _, err := callTool(context.Background(), c, "delete_many", raw); err == nil {
			t.Fatal("unconfirmed bulk accepted")
		}
	}
	if *writes != 0 {
		t.Fatal("unconfirmed run mutated")
	}
	// Publish after the dry run: the real run submits old revisions, it cannot
	// silently reselect and delete the newly published bytes.
	if _, err := s.Put(store.PutOptions{Slug: "page"}, strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	r = manageCall(t, c, "delete_many", map[string]any{"dry_run": false, "manifest": m, "confirm_host": "control.test"})
	var report manage.Report
	json.Unmarshal([]byte(r.Content[0].Text), &report)
	if len(report.Deleted) != 0 || len(report.Skipped) != 1 {
		t.Fatalf("stale run %+v", report)
	}
	r = manageCall(t, c, "delete_many", map[string]any{"slugs": []string{"page"}})
	json.Unmarshal([]byte(r.Content[0].Text), &m)
	r = manageCall(t, c, "delete_many", map[string]any{"dry_run": false, "manifest": m, "confirm_host": "control.test"})
	json.Unmarshal([]byte(r.Content[0].Text), &report)
	if len(report.Deleted) != 1 || report.Deleted[0].Revision != m.Items[0].Revision {
		t.Fatal("manifest execution mismatch")
	}
	if _, err := s.Patch("page", nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("real run did not delete")
	}
}
func TestManageMCPLegacyDelete(t *testing.T) {
	s, c, writes := manageMCP(t)
	manageCall(t, c, "delete", map[string]any{"slug": "page"})
	if *writes != 1 {
		t.Fatal("legacy delete not immediate")
	}
	if _, err := s.Patch("page", nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("legacy delete changed")
	}
}
func TestManageMCPPatch(t *testing.T) {
	s, c, _ := manageMCP(t)
	manageCall(t, c, "set_ttl", map[string]any{"slug": "page", "ttl": "2h"})
	a, err := s.Patch("page", nil, nil)
	if err != nil || time.Until(a.ExpiresAt) < 119*time.Minute {
		t.Fatal("ttl tool failed")
	}
	manageCall(t, c, "pin", map[string]any{"slug": "page"})
	a, err = s.Patch("page", nil, nil)
	if err != nil || !a.Pinned || !a.ExpiresAt.IsZero() {
		t.Fatal("pin tool failed")
	}
	manageCall(t, c, "pin", map[string]any{"slug": "page", "pinned": false})
	a, err = s.Patch("page", nil, nil)
	if err != nil || a.Pinned {
		t.Fatal("unpin tool failed")
	}
	manageCall(t, c, "set_ttl", map[string]any{"slug": "page", "ttl": "none"})
	a, err = s.Patch("page", nil, nil)
	if err != nil || !a.ExpiresAt.IsZero() {
		t.Fatal("clear tool failed")
	}
	for _, value := range []string{"59s", "366d", "secret-capability"} {
		raw, _ := json.Marshal(map[string]any{"slug": "page", "ttl": value})
		if _, err := callTool(context.Background(), c, "set_ttl", raw); err == nil || strings.Contains(err.Message, value) {
			t.Fatal("invalid ttl or private error")
		}
	}
	names := map[string]bool{}
	for _, tool := range toolDefs() {
		names[tool.Name] = true
	}
	for _, name := range []string{"pin", "set_ttl", "delete_many", "delete"} {
		if !names[name] {
			t.Fatal("tool missing", name)
		}
	}
}
func TestManageMCPPublishTTL(t *testing.T) {
	s, c, _ := manageMCP(t)
	if err := s.SetDefaultTTL(time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		slug, ttl string
		expires   bool
	}{{"absent", "", true}, {"zero", "0", false}, {"none", "none", false}, {"explicit", "2h", true}} {
		body := map[string]any{"slug": tc.slug, "content": "new"}
		if tc.ttl != "" {
			body["ttl"] = tc.ttl
		}
		manageCall(t, c, "publish", body)
		a, err := s.Patch(tc.slug, nil, nil)
		if err != nil || a.ExpiresAt.IsZero() == tc.expires {
			t.Fatalf("publish ttl %+v %v", a, err)
		}
	}
}

func TestManageMCPSecondBatchFailure(t *testing.T) {
	c := NewClient("https://control.test", "", "owner-secret")
	m := manage.Manifest{Instance: "https://control.test:443", SelectedAt: time.Now()}
	for n := 0; n < 2*manage.MaxBatch+1; n++ {
		m.Items = append(m.Items, manage.Item{Kind: "artifact", ID: fmt.Sprintf("item-%d", n), Revision: "revision"})
	}
	var confirmed manage.Report
	calls := 0
	c.HTTP.Transport = manageRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		rec := httptest.NewRecorder()
		if calls == 2 {
			rec.WriteHeader(http.StatusServiceUnavailable)
			rec.WriteString("private-upstream-secret")
			return rec.Result(), nil
		}
		var batch manage.DeleteRequest
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Fatal(err)
		}
		for n, item := range batch.Items {
			result := manage.Result{Item: item}
			switch n {
			case 0:
				confirmed.Skipped = append(confirmed.Skipped, result)
			case 1:
				confirmed.Missing = append(confirmed.Missing, result)
			case 2:
				confirmed.Failed = append(confirmed.Failed, result)
			default:
				confirmed.Deleted = append(confirmed.Deleted, result)
			}
		}
		if err := json.NewEncoder(rec).Encode(confirmed); err != nil {
			t.Fatal(err)
		}
		return rec.Result(), nil
	})
	r := manageCall(t, c, "delete_many", map[string]any{"dry_run": false, "manifest": m, "confirm_host": "control.test"})
	if calls != 2 || !r.IsError {
		t.Fatalf("calls=%d result=%+v", calls, r)
	}
	var got struct {
		manage.Report
		Error      string          `json:"error"`
		Unresolved []manage.Result `json:"unresolved"`
	}
	if err := json.Unmarshal([]byte(r.Content[0].Text), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Report, confirmed) {
		t.Fatal("lost confirmed outcomes")
	}
	if len(got.Unresolved) != manage.MaxBatch+1 {
		t.Fatalf("unresolved=%d, want failed batch and unattempted items", len(got.Unresolved))
	}
	for n, result := range got.Unresolved {
		if result.Item != m.Items[manage.MaxBatch+n] || result.Reason != "outcome unknown" {
			t.Fatalf("unresolved item: %+v", result)
		}
	}
	if got.Error != "management: server returned status 503" || strings.Contains(r.Content[0].Text, "secret") {
		t.Fatalf("unsafe failure: %s", got.Error)
	}
}
