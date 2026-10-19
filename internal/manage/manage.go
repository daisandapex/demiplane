// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package manage defines the owner manifest protocol and shared CLI/MCP selection.
package manage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const MaxBatch = 1000

type Item struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	Revision    string `json:"revision"`
	Size        int64  `json:"size"`
	CreatedAt   string `json:"created_at,omitempty"`
	Series      string `json:"series,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Pinned      bool   `json:"pinned,omitempty"`
}
type Manifest struct {
	Instance   string    `json:"instance"`
	SelectedAt time.Time `json:"selected_at"`
	Items      []Item    `json:"items"`
}
type DeleteRequest struct {
	Manifest
	IncludePinned bool   `json:"include_pinned,omitempty"`
	ConfirmHost   string `json:"confirm_host,omitempty"`
}
type Result struct {
	Item
	Reason string `json:"reason,omitempty"`
}
type Report struct {
	Deleted []Result `json:"deleted"`
	Skipped []Result `json:"skipped"`
	Missing []Result `json:"missing"`
	Failed  []Result `json:"failed"`
}

func (r Report) Partial() bool { return len(r.Skipped)+len(r.Missing)+len(r.Failed) > 0 }

type Selection struct {
	Slugs         []string `json:"slugs,omitempty"`
	Match         string   `json:"match,omitempty"`
	OlderThan     string   `json:"older_than,omitempty"`
	Series        string   `json:"series,omitempty"`
	Type          string   `json:"type,omitempty"`
	Sites         bool     `json:"sites,omitempty"`
	All           bool     `json:"all,omitempty"`
	IncludePinned bool     `json:"include_pinned,omitempty"`
}

func (s Selection) Broad() bool {
	return s.All || s.Match != "" || s.OlderThan != "" || s.Series != "" || s.Type != "" || s.Sites
}
func Select(m Manifest, s Selection) (Manifest, error) {
	var age time.Duration
	var err error
	if s.OlderThan != "" {
		age, err = time.ParseDuration(s.OlderThan)
		if err != nil || age <= 0 {
			return Manifest{}, errors.New("older-than must be a positive Go duration")
		}
	}
	if _, err = path.Match(s.Match, ""); err != nil {
		return Manifest{}, errors.New("invalid match glob")
	}
	names := map[string]bool{}
	for _, id := range s.Slugs {
		names[id] = true
	}
	selected := make([]Item, 0)
	for _, i := range m.Items {
		if i.Pinned && !s.IncludePinned {
			continue
		}
		eligible := i.Kind == "artifact" || s.Sites || s.All
		if !eligible {
			continue
		}
		named := names[i.ID]
		filtered := s.All || s.Match != "" || s.OlderThan != "" || s.Series != "" || s.Type != ""
		if s.Match != "" {
			ok, _ := path.Match(s.Match, i.ID)
			filtered = filtered && ok
		}
		if s.Series != "" {
			filtered = filtered && i.Series == s.Series
		}
		if s.Type != "" {
			filtered = filtered && strings.HasPrefix(i.ContentType, s.Type)
		}
		if age > 0 {
			t, e := time.Parse(time.RFC3339Nano, i.CreatedAt)
			filtered = filtered && e == nil && t.Before(m.SelectedAt.Add(-age))
		}
		if named || filtered {
			selected = append(selected, i)
		}
	}
	m.Items = selected
	return m, nil
}

func Host(instance string) string {
	u, err := url.Parse(instance)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// Request never returns upstream bodies or transport errors, which may contain
// private URLs. Callers supply a client that refuses redirects.
func Request(ctx context.Context, c *http.Client, base, token, method, route string, body any, out any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return errors.New("management: invalid body")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, base+route, bytes.NewReader(data))
	if err != nil {
		return errors.New("management: invalid URL")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return errors.New("management: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("management: server returned status %d", resp.StatusCode)
	}
	if out != nil && json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out) != nil {
		return errors.New("management: invalid response")
	}
	return nil
}
func Inventory(ctx context.Context, c *http.Client, base, token string) (Manifest, error) {
	m := Manifest{SelectedAt: time.Now().UTC(), Items: make([]Item, 0)}
	seen := map[string]bool{}
	ids := map[string]bool{}
	cursor := ""
	for {
		var page struct {
			Owner     bool   `json:"owner"`
			Instance  string `json:"instance"`
			Artifacts []struct {
				Item
				Slug string `json:"slug"`
			} `json:"artifacts"`
			Count int    `json:"count"`
			Next  string `json:"next"`
		}
		route := "/list?all=1"
		if cursor != "" {
			route += "&after=" + url.QueryEscape(cursor)
		}
		if err := Request(ctx, c, base, token, "GET", route, nil, &page); err != nil {
			return Manifest{}, err
		}
		if !page.Owner || page.Instance == "" || page.Artifacts == nil || page.Count != len(page.Artifacts) || (m.Instance != "" && page.Instance != m.Instance) {
			return Manifest{}, errors.New("management: invalid owner inventory; upgrade server")
		}
		m.Instance = page.Instance
		for _, row := range page.Artifacts {
			i := row.Item
			i.ID = row.Slug
			if (i.Kind != "artifact" && i.Kind != "site") || i.ID == "" || i.Revision == "" {
				return Manifest{}, errors.New("management: invalid inventory entry")
			}
			key := i.Kind + ":" + i.ID
			if ids[key] {
				return Manifest{}, errors.New("management: inventory changed during pagination; retry")
			}
			ids[key] = true
			m.Items = append(m.Items, i)
		}
		if page.Next == "" {
			return m, nil
		}
		if seen[page.Next] {
			return Manifest{}, errors.New("management: repeated cursor")
		}
		seen[page.Next] = true
		cursor = page.Next
	}
}
func Execute(ctx context.Context, c *http.Client, base, token string, r DeleteRequest) (Report, error) {
	out := Report{Deleted: []Result{}, Skipped: []Result{}, Missing: []Result{}, Failed: []Result{}}
	for start := 0; start < len(r.Items); start += MaxBatch {
		batch := r
		batch.Items = r.Items[start:min(start+MaxBatch, len(r.Items))]
		var part Report
		if err := Request(ctx, c, base, token, "POST", "/manage/delete", batch, &part); err != nil {
			return out, err
		}
		out.Deleted = append(out.Deleted, part.Deleted...)
		out.Skipped = append(out.Skipped, part.Skipped...)
		out.Missing = append(out.Missing, part.Missing...)
		out.Failed = append(out.Failed, part.Failed...)
	}
	return out, nil
}
