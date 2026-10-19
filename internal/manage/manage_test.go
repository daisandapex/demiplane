// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package manage

import (
	"reflect"
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
