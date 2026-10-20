// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package manage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestManageSelection(t *testing.T) {
	now := time.Now().UTC()
	items := []Item{
		{Kind: "artifact", ID: "old-html", Revision: "a", CreatedAt: now.Add(-48 * time.Hour).Format(time.RFC3339), Series: "s", ContentType: "text/html", Size: 1},
		{Kind: "artifact", ID: "new-html", Revision: "b", CreatedAt: now.Format(time.RFC3339), Series: "s", ContentType: "text/html", Size: 2},
		{Kind: "artifact", ID: "old-text", Revision: "c", CreatedAt: now.Add(-48 * time.Hour).Format(time.RFC3339), Series: "other", ContentType: "text/plain", Size: 3},
		{Kind: "site", ID: "old-html", Revision: "d", CreatedAt: now.Add(-48 * time.Hour).Format(time.RFC3339), Size: 4},
		{Kind: "artifact", ID: "pinned", Revision: "e", Pinned: true, Size: 5},
	}
	for _, tc := range []struct {
		name string
		sel  Selection
		want []string
	}{
		{"none", Selection{}, []string{}},
		{"named", Selection{Slugs: []string{"old-text"}}, []string{"artifact:old-text"}},
		{"glob", Selection{Match: "old-*"}, []string{"artifact:old-html", "artifact:old-text"}},
		{"age", Selection{OlderThan: "24h"}, []string{"artifact:old-html", "artifact:old-text"}},
		{"series", Selection{Series: "s"}, []string{"artifact:old-html", "artifact:new-html"}},
		{"type", Selection{Type: "text/h"}, []string{"artifact:old-html", "artifact:new-html"}},
		{"intersect", Selection{Match: "old-*", OlderThan: "24h", Series: "s", Type: "text/h"}, []string{"artifact:old-html"}},
		{"union", Selection{Slugs: []string{"new-html"}, Match: "old-*", Series: "other"}, []string{"artifact:new-html", "artifact:old-text"}},
		{"sites-only", Selection{Sites: true}, []string{}},
		{"named-sites", Selection{Slugs: []string{"old-html"}, Sites: true}, []string{"artifact:old-html", "site:old-html"}},
		{"sites", Selection{Match: "old-html", Sites: true}, []string{"artifact:old-html", "site:old-html"}},
		{"all", Selection{All: true}, []string{"artifact:old-html", "artifact:new-html", "artifact:old-text", "site:old-html"}},
		{"include-pinned", Selection{All: true, IncludePinned: true}, []string{"artifact:old-html", "artifact:new-html", "artifact:old-text", "site:old-html", "artifact:pinned"}},
		{"named-pin", Selection{Slugs: []string{"pinned"}}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Select(Manifest{SelectedAt: now, Items: items}, tc.sel)
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, i := range m.Items {
				got = append(got, i.Kind+":"+i.ID)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	for _, s := range []Selection{{Match: "["}, {OlderThan: "-1h"}, {OlderThan: "garbage"}} {
		if _, err := Select(Manifest{}, s); err == nil {
			t.Fatal("bad filter accepted")
		}
	}
}

// inventoryTransport keeps clock-skew tests independent of listening sockets.
type inventoryTransport func(*http.Request) (*http.Response, error)

func (f inventoryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestManageInventoryClockAhead(t *testing.T) {
	// The real client clock is two hours ahead of this simulated server clock.
	serverTime := time.Now().UTC().Add(-2 * time.Hour)
	calls := 0
	c := &http.Client{Transport: inventoryTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		id, created, now, next := "fresh", serverTime, serverTime, "page2"
		if calls == 2 {
			if r.URL.Query().Get("after") != "page2" {
				t.Fatal("missing cursor")
			}
			// Even if pagination takes two hours, use the first page's cutoff.
			id, created, now, next = "old", serverTime.Add(-2*time.Hour), serverTime.Add(2*time.Hour), ""
		}
		rec := httptest.NewRecorder()
		err := json.NewEncoder(rec).Encode(map[string]any{
			"owner": true, "instance": "https://control.test:443", "server_time": now,
			"count": 1, "next": next, "artifacts": []map[string]any{{
				"kind": "artifact", "slug": id, "revision": "rev", "created_at": created,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return rec.Result(), nil
	})}
	m, err := Inventory(context.Background(), c, "https://control.test", "owner")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(m, Selection{OlderThan: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Items) != 1 || selected.Items[0].ID != "old" {
		t.Fatalf("client two hours ahead selected fresh artifact with --older-than 1h: %+v", selected.Items)
	}
	if calls != 2 || !selected.SelectedAt.Equal(serverTime) {
		t.Fatalf("selection must retain first page server time: calls=%d selected_at=%s want=%s", calls, selected.SelectedAt, serverTime)
	}
}

func TestManageInventoryRequiresServerTime(t *testing.T) {
	for _, badTime := range []string{"", `,"server_time":null`, `,"server_time":"0001-01-01T00:00:00Z"`, `,"server_time":"invalid"`} {
		for _, badPage := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/page%d", badTime, badPage), func(t *testing.T) {
				calls := 0
				c := &http.Client{Transport: inventoryTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					stamp, next := `,"server_time":"2026-10-19T12:00:00Z"`, `,"next":"page2"`
					if calls == badPage {
						stamp, next = badTime, ""
					}
					rec := httptest.NewRecorder()
					rec.WriteString(`{"owner":true,"instance":"https://control.test:443","artifacts":[],"count":0` + stamp + next + `}`)
					return rec.Result(), nil
				})}
				if _, err := Inventory(context.Background(), c, "https://control.test", "owner"); err == nil {
					t.Fatal("accepted inventory without valid server time")
				} else if badTime == "" && !strings.Contains(err.Error(), "upgrade server") {
					t.Fatalf("missing upgrade advice: %v", err)
				}
			})
		}
	}
}
