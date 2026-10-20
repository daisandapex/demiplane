// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/daisandapex/demiplane/v3/internal/manage"
	"github.com/daisandapex/demiplane/v3/internal/ttl"
)

func manageToolDefs() []tool {
	str := map[string]any{"type": "string"}
	boolean := map[string]any{"type": "boolean"}
	return []tool{
		{Name: "delete_many", Description: "Select owner inventory into a revision-bound manifest. Dry run defaults true. Filters intersect; named slugs union the filter set. Pins excluded unless include_pinned. Real runs require the returned manifest and confirm_host (server hostname), and never rerun selection.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"slugs": map[string]any{"type": "array", "items": str}, "match": str, "older_than": str, "series": str, "type": str, "sites": boolean, "all": boolean, "include_pinned": boolean, "dry_run": map[string]any{"type": "boolean", "default": true}, "manifest": map[string]any{"type": "object", "description": "Exact manifest from dry run, including instance, selected_at and item revisions"}, "confirm_host": str,
		}}},
		{Name: "set_ttl", Description: "Set artifact expiry to now plus 1m through 365d; none or 0 clears it. Unpin before setting ttl.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"slug": str, "ttl": str}, "required": []string{"slug", "ttl"}}},
		{Name: "pin", Description: "Pin an artifact and clear expiry. pinned defaults true; false unpins.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"slug": str, "pinned": map[string]any{"type": "boolean", "default": true}}, "required": []string{"slug"}}},
	}
}

func callManage(ctx context.Context, c *Client, name string, raw json.RawMessage) (*toolResult, *rpcError) {
	invalid := func(message string) (*toolResult, *rpcError) {
		return nil, &rpcError{Code: codeInvalidParams, Message: message}
	}
	if name == "delete_many" {
		var a struct {
			manage.Selection
			DryRun      *bool            `json:"dry_run"`
			Manifest    *manage.Manifest `json:"manifest"`
			ConfirmHost string           `json:"confirm_host"`
		}
		if json.Unmarshal(raw, &a) != nil {
			return invalid("invalid management arguments")
		}
		if a.DryRun == nil || *a.DryRun {
			m, err := manage.Inventory(ctx, c.HTTP, c.ControlURL, c.Token)
			if err != nil {
				return nil, upstreamError(err)
			}
			m, err = manage.Select(m, a.Selection)
			if err != nil {
				return invalid(err.Error())
			}
			b, _ := json.Marshal(m)
			return textResult(string(b)), nil
		}
		if a.Manifest == nil || a.Manifest.Instance == "" || a.Manifest.SelectedAt.IsZero() || a.Manifest.Items == nil {
			return invalid("real run requires the dry-run manifest")
		}
		if a.ConfirmHost == "" || a.ConfirmHost != manage.Host(a.Manifest.Instance) {
			return invalid("real run requires confirm_host matching the manifest server hostname")
		}
		for _, i := range a.Manifest.Items {
			if i.Revision == "" {
				return invalid("manifest revisions are required")
			}
		}
		report, err := manage.Execute(ctx, c.HTTP, c.ControlURL, c.Token, manage.DeleteRequest{Manifest: *a.Manifest, IncludePinned: a.IncludePinned, ConfirmHost: a.ConfirmHost})
		if err != nil {
			// Execute retains only confirmed batches. A failed request may have
			// mutated its batch, so do not label its items deleted or failed.
			var batch *manage.BatchError
			if !errors.As(err, &batch) {
				return nil, upstreamError(err)
			}
			unresolved := []manage.Result{}
			for n := batch.Start; n < len(a.Manifest.Items); n++ {
				reason := "outcome unknown"
				if n >= batch.End {
					reason = "not attempted"
				}
				unresolved = append(unresolved, manage.Result{Item: a.Manifest.Items[n], Reason: reason})
			}
			b, _ := json.Marshal(struct {
				manage.Report
				Error      string          `json:"error"`
				Unresolved []manage.Result `json:"unresolved"`
			}{report, upstreamError(err).Message, unresolved})
			result := textResult(string(b))
			result.IsError = true
			return result, nil
		}
		b, _ := json.Marshal(report)
		return textResult(string(b)), nil
	}
	var a struct {
		Slug   string `json:"slug"`
		TTL    string `json:"ttl"`
		Pinned *bool  `json:"pinned"`
	}
	if json.Unmarshal(raw, &a) != nil || a.Slug == "" || strings.ContainsAny(a.Slug, "/?#") {
		return invalid("invalid management arguments")
	}
	body := map[string]any{}
	if name == "set_ttl" {
		d, err := ttl.Management(a.TTL)
		if err != nil {
			return invalid(err.Error())
		}
		if d == 0 {
			body["ttl"] = nil
		} else {
			body["ttl"] = a.TTL
		}
	} else {
		p := true
		if a.Pinned != nil {
			p = *a.Pinned
		}
		body["pinned"] = p
	}
	var item json.RawMessage
	if err := manage.Request(ctx, c.HTTP, c.ControlURL, c.Token, "PATCH", "/"+url.PathEscape(a.Slug), body, &item); err != nil {
		return nil, upstreamError(err)
	}
	return textResult(string(item)), nil
}
