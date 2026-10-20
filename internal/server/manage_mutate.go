// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/logsafe"
	"github.com/daisandapex/demiplane/v3/internal/manage"
	"github.com/daisandapex/demiplane/v3/internal/store"
	"github.com/daisandapex/demiplane/v3/internal/ttl"
)

func decodeManagement(w http.ResponseWriter, r *http.Request, v any) bool {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		http.Error(w, "invalid management JSON", 400)
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid management JSON", 400)
		return false
	}
	return true
}
func (s *Server) handlePatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TTL    json.RawMessage `json:"ttl"`
		Pinned json.RawMessage `json:"pinned"`
	}
	if !decodeManagement(w, r, &body) {
		return
	}
	var duration *time.Duration
	var pinned *bool
	if body.TTL != nil {
		var text string
		if string(body.TTL) == "null" {
			text = "0"
		} else if json.Unmarshal(body.TTL, &text) != nil {
			http.Error(w, "ttl must be a duration string or null", 400)
			return
		}
		d, err := ttl.Management(text)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		duration = &d
	}
	if body.Pinned != nil {
		var b bool
		if string(body.Pinned) == "null" || json.Unmarshal(body.Pinned, &b) != nil {
			http.Error(w, "pinned must be boolean", 400)
			return
		}
		pinned = &b
	}
	if store.ValidateNamedSlug(r.PathValue("slug")) != nil {
		http.Error(w, "invalid slug", 400)
		return
	}
	a, err := s.store.Patch(r.PathValue("slug"), duration, pinned)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "not found", 404)
	case errors.Is(err, store.ErrPinned):
		http.Error(w, store.ErrPinned.Error(), 409)
	case err != nil:
		managementFailure(w, err)
	default:
		item := artifactListItem(a, s.contentBase(r))
		item.Pinned = a.Pinned
		item.Revision = a.Revision
		writeJSON(w, 200, item)
	}
}
func (s *Server) handleBulkDelete(w http.ResponseWriter, r *http.Request) {
	var body manage.DeleteRequest
	if !decodeManagement(w, r, &body) {
		return
	}
	// Clients select against inventory server_time, not their own clocks. Allow
	// only a minute of server clock rollback; revisions still bind old manifests.
	if body.Instance != s.controlOrigin(r) || body.SelectedAt.IsZero() || body.SelectedAt.After(time.Now().Add(time.Minute)) || body.Items == nil || len(body.Items) > manage.MaxBatch {
		http.Error(w, "invalid manifest or instance", 400)
		return
	}
	if body.ConfirmHost != "" && body.ConfirmHost != manage.Host(body.Instance) {
		http.Error(w, "confirmation host mismatch", 400)
		return
	}
	seen := map[string]bool{}
	for _, i := range body.Items {
		key := i.Kind + ":" + i.ID
		if (i.Kind != "artifact" && i.Kind != "site") || store.ValidateNamedSlug(i.ID) != nil || i.Revision == "" || len(i.Revision) > 128 || seen[key] {
			http.Error(w, "invalid manifest item", 400)
			return
		}
		seen[key] = true
	}
	report := manage.Report{Deleted: []manage.Result{}, Skipped: []manage.Result{}, Missing: []manage.Result{}, Failed: []manage.Result{}}
	for _, i := range body.Items {
		var err error
		if i.Kind == "site" {
			err = s.store.DeleteSiteRevision(i.ID, i.Revision)
		} else {
			err = s.store.DeleteRevision(i.ID, i.Revision, body.IncludePinned)
		}
		result := manage.Result{Item: i}
		switch {
		case err == nil:
			report.Deleted = append(report.Deleted, result)
		case errors.Is(err, store.ErrChanged):
			result.Reason = "changed or pinned"
			report.Skipped = append(report.Skipped, result)
		case errors.Is(err, store.ErrNotFound):
			result.Reason = "missing"
			report.Missing = append(report.Missing, result)
		default:
			log.Printf("bulk delete %s failed: %s", logsafe.Slug(i.ID), logsafe.Err(err, i.ID))
			result.Reason = "delete failed"
			report.Failed = append(report.Failed, result)
		}
	}
	writeJSON(w, 200, report)
}
