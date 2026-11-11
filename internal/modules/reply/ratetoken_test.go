// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/limit"
)

// TestTokenlessSubmissionsDoNotSpendReplyBudget: a browser submission
// without a valid form token is refused with 403 and must not spend the
// per-artifact or global reply budget, or junk posts could lock real
// repliers out with 429.
func TestTokenlessSubmissionsDoNotSpendReplyBudget(t *testing.T) {
	for _, budget := range []string{"target", "global"} {
		l := openLimits()
		if budget == "target" {
			l.RateTarget = limit.Rate{Every: time.Hour, Burst: 3}
		} else {
			l.RateGlobal = limit.Rate{Every: time.Hour, Burst: 3}
		}
		ts, m := admissionSetup(t, l)
		browser := map[string]string{"Origin": "https://evil.example"}
		refused := map[int]int{}
		for i := 0; i < 10; i++ {
			for _, path := range []string{"/reply/on", "/answer/on"} {
				for _, tok := range []string{"", "junk"} {
					fields := url.Values{"kind": {"comment"}, "body": {"junk"}, "token": {tok}}
					code, _ := postAnswer(t, ts, path, browser, fields)
					refused[code]++
				}
			}
		}
		if refused[http.StatusForbidden] != 40 {
			t.Errorf("%s budget: 40 tokenless posts got %v, want all 403", budget, refused)
		}
		art, err := m.host.Store().LookupPublic("on")
		if err != nil {
			t.Fatal(err)
		}
		fields := url.Values{"kind": {"comment"}, "body": {"real"}, "token": {m.host.ReplyFormToken(art)}}
		hdr := map[string]string{"Origin": ts.URL}
		for _, path := range []string{"/reply/on", "/answer/on"} {
			if code, _ := postAnswer(t, ts, path, hdr, fields); code != http.StatusCreated {
				t.Errorf("%s budget: valid reply to %s after junk = %d, want 201", budget, path, code)
			}
		}
		if n := storedCount(t, m, "on"); n != 2 {
			t.Errorf("%s budget: stored %d, want 2", budget, n)
		}
	}
}
