// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestHookWebhookRefusesRedirects: the webhook body is the reply itself, and
// a 307/308 makes Go's client replay that body to the Location. The client
// must not follow a redirect at all: not to another origin, not downgraded to
// plain HTTP, and not even to the same origin. The target counts every
// request it receives, so a followed redirect fails the test.
func TestHookWebhookRefusesRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
	}))
	t.Cleanup(target.Close)

	// An HTTPS hook whose redirect points at a plain-HTTP collector covers the
	// scheme downgrade; the plain-HTTP hook covers the cross-origin case.
	tlsHook := func(code int, loc string) *httptest.Server {
		return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/landed" {
				targetHits.Add(1)
				return
			}
			w.Header().Set("Location", loc)
			w.WriteHeader(code)
		}))
	}
	plainHook := func(code int, loc string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/landed" {
				targetHits.Add(1)
				return
			}
			w.Header().Set("Location", loc)
			w.WriteHeader(code)
		}))
	}

	cases := []struct {
		name string
		hook func() *httptest.Server
	}{
		{"307 cross-origin", func() *httptest.Server { return plainHook(http.StatusTemporaryRedirect, target.URL+"/collect") }},
		{"308 cross-origin", func() *httptest.Server { return plainHook(http.StatusPermanentRedirect, target.URL+"/collect") }},
		{"307 https to http", func() *httptest.Server { return tlsHook(http.StatusTemporaryRedirect, target.URL+"/collect") }},
		{"308 https to http", func() *httptest.Server { return tlsHook(http.StatusPermanentRedirect, target.URL+"/collect") }},
		{"307 same origin", func() *httptest.Server { return plainHook(http.StatusTemporaryRedirect, "/landed") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			targetHits.Store(0)
			hook := c.hook()
			t.Cleanup(hook.Close)
			// Trust the TLS test server's certificate for this case only.
			prev := hookClient.Transport
			hookClient.Transport = hook.Client().Transport
			t.Cleanup(func() { hookClient.Transport = prev })

			ts, m := contentSetup(t)
			m.hook = hookConfig{url: hook.URL + "/hook"}
			logs := captureHookLog(t)
			submitAndWait(t, ts, m)

			if n := targetHits.Load(); n != 0 {
				t.Errorf("redirect was followed: target received %d request(s)", n)
			}
			if got := logs.String(); !strings.Contains(got, "returned 30") {
				t.Errorf("refused redirect was not logged as a failure:\n%s", got)
			}
		})
	}
}
