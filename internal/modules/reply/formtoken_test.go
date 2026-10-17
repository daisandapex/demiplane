// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/formtoken"
)

// postAnswer posts a form submission to path with the given headers and
// fields and returns the status and body.
func postAnswer(t *testing.T, ts *httptest.Server, path string, hdr map[string]string, fields url.Values) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", formCT)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestBrowserSubmissionsNeedTheTargetsToken: on both submit routes a browser
// submission (Origin or Sec-Fetch-Site present) is recorded only with a valid
// token for exactly its target; a non-browser client needs none.
func TestBrowserSubmissionsNeedTheTargetsToken(t *testing.T) {
	ts, m := admissionSetup(t, openLimits())
	key := m.host.Store().FormKey()
	good := m.host.ReplyFormToken("on")
	otherSlug := formtoken.Mint(key, "locked", time.Now())
	stale := formtoken.Mint(key, "on", time.Now().Add(-24*time.Hour)) // before the publish
	null := map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site"}
	sameOrigin := map[string]string{"Origin": ts.URL, "Sec-Fetch-Site": "same-origin"}

	for _, route := range []struct{ name, path, kind string }{
		{"content", "/answer/on", ""},
		{"control", "/reply/on", "comment"},
	} {
		fields := func(token string) url.Values {
			v := url.Values{"body": {"an answer"}}
			if route.kind != "" {
				v.Set("kind", route.kind)
			}
			if token != "" {
				v.Set("token", token)
			}
			return v
		}
		for _, tc := range []struct {
			name  string
			hdr   map[string]string
			token string
			want  int
		}{
			{"opaque origin, no token", null, "", http.StatusForbidden},
			{"opaque origin, token for another slug", null, otherSlug, http.StatusForbidden},
			{"opaque origin, token from before the publish", null, stale, http.StatusForbidden},
			{"opaque origin, tampered token", null, good[:len(good)-2] + "AA", http.StatusForbidden},
			{"same-origin browser post, no token", sameOrigin, "", http.StatusForbidden},
			{"opaque origin, the target's token", null, good, http.StatusCreated},
			{"same-origin browser post, the target's token", sameOrigin, good, http.StatusCreated},
			{"non-browser client, no token", nil, "", http.StatusCreated},
		} {
			before := storedCount(t, m, "on")
			code, body := postAnswer(t, ts, route.path, tc.hdr, fields(tc.token))
			if code != tc.want {
				t.Errorf("%s: %s: status %d, want %d (%s)", route.name, tc.name, code, tc.want, body)
			}
			stored := storedCount(t, m, "on") - before
			if want := map[bool]int{true: 1, false: 0}[tc.want == http.StatusCreated]; stored != want {
				t.Errorf("%s: %s: stored %d replies, want %d", route.name, tc.name, stored, want)
			}
		}
	}
}

// TestAnswerWithoutTokenExplainsRebuild: the inline box of a page baked before
// forms carried a token is refused with an honest "not recorded" page that
// says the page must be rebuilt.
func TestAnswerWithoutTokenExplainsRebuild(t *testing.T) {
	ts, _ := admissionSetup(t, openLimits())
	code, body := postAnswer(t, ts, "/answer/on", map[string]string{"Origin": "null"}, url.Values{"body": {"x"}})
	if code != http.StatusForbidden || !strings.Contains(body, "was not recorded") || !strings.Contains(body, "rebuild") {
		t.Fatalf("status %d, body %q", code, body)
	}
}

// TestReplyFormCarriesToken: the module's own form page carries a token that
// its submit route accepts.
func TestReplyFormCarriesToken(t *testing.T) {
	ts, m := admissionSetup(t, openLimits())
	resp, err := http.Get(ts.URL + "/reply/on")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	const marker = `name="token" value="`
	i := strings.Index(string(b), marker)
	if i < 0 {
		t.Fatalf("form page has no token field: %s", b)
	}
	rest := string(b)[i+len(marker):]
	tok := rest[:strings.IndexByte(rest, '"')]
	code, body := postAnswer(t, ts, "/reply/on", map[string]string{"Origin": ts.URL},
		url.Values{"kind": {"approve"}, "token": {tok}})
	if code != http.StatusCreated {
		t.Fatalf("submitting the served form: %d %s", code, body)
	}
	if storedCount(t, m, "on") != 1 {
		t.Fatal("reply not stored")
	}
}
