// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/store"
)

// getWith issues a GET with extra request headers and returns the response
// (body drained and closed).
func getWith(t *testing.T, url string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

// TestDocumentsAreSandboxed pins the per-type CSP: HTML and SVG/XML run in a
// sandbox without allow-same-origin (an opaque origin), every document refuses
// workers, and non-document types keep the plain framing policy.
func TestDocumentsAreSandboxed(t *testing.T) {
	ts := newTestServer(t, "")
	cases := []struct {
		name, query, body string
		want              []string
		absent            []string
	}{
		{"html", "?slug=page&filename=page.html", "<!doctype html><p>hi</p>",
			[]string{"sandbox allow-scripts allow-forms", "worker-src 'none'", "frame-ancestors 'self'"},
			[]string{"allow-same-origin", "allow-top-navigation"}},
		{"rendered markdown", "?slug=doc&render=md", "# Title\n\nbody\n",
			[]string{"sandbox allow-scripts", "worker-src 'none'"},
			[]string{"allow-same-origin"}},
		{"svg", "?slug=pic&filename=pic.svg", `<svg xmlns="http://www.w3.org/2000/svg"></svg>`,
			[]string{"sandbox;", "script-src 'none'", "worker-src 'none'", "frame-ancestors 'self'"},
			[]string{"allow-same-origin", "allow-scripts"}},
		{"plain text", "?slug=note&filename=note.txt", "just text",
			[]string{"frame-ancestors 'self'"}, []string{"sandbox"}},
		{"javascript", "?slug=lib&filename=lib.js", "var x=1;",
			[]string{"frame-ancestors 'self'"}, []string{"sandbox"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := publish(t, ts, tc.query, tc.body)
			csp := getWith(t, url, nil).Header.Get("Content-Security-Policy")
			for _, w := range tc.want {
				if !strings.Contains(csp, w) {
					t.Errorf("CSP %q missing %q", csp, w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(csp, a) {
					t.Errorf("CSP %q must not contain %q", csp, a)
				}
			}
		})
	}
}

// TestUnsafeNoSandboxKeepsWorkerBlock checks the operator opt-out: no sandbox,
// but workers (and so service workers) stay refused.
func TestUnsafeNoSandboxKeepsWorkerBlock(t *testing.T) {
	ts := newConfiguredServer(t, Config{UnsafeNoSandbox: true})
	url := publish(t, ts, "?slug=page&filename=page.html", "<!doctype html><p>hi</p>")
	csp := getWith(t, url, nil).Header.Get("Content-Security-Policy")
	if strings.Contains(csp, "sandbox") {
		t.Errorf("CSP %q has a sandbox under UnsafeNoSandbox", csp)
	}
	if !strings.Contains(csp, "worker-src 'none'") || !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Errorf("CSP %q lost worker-src or frame-ancestors", csp)
	}
	svg := publish(t, ts, "?slug=pic&filename=pic.svg", `<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	if csp := getWith(t, svg, nil).Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'none'") {
		t.Errorf("SVG CSP %q lost script-src 'none' under UnsafeNoSandbox", csp)
	}
}

// TestServiceWorkerScriptFetchRefused: browsers mark a service-worker script
// fetch with "Service-Worker: script"; no URL on either plane may serve one.
func TestServiceWorkerScriptFetchRefused(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := New(st, Config{})
	if _, err := st.Put(store.PutOptions{Slug: "worker", Filename: "worker.js"}, strings.NewReader("self.onfetch=null;")); err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]http.Handler{
		"content": srv.ContentHandler(), "control": srv.ControlHandler(), "combined": srv.Handler(),
	} {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(h)
			defer ts.Close()
			if resp := getWith(t, ts.URL+"/worker", map[string]string{"Service-Worker": "script"}); resp.StatusCode != http.StatusForbidden {
				t.Errorf("service-worker fetch = %d, want 403", resp.StatusCode)
			}
		})
	}
	// The same URL still serves as an ordinary script.
	ts := httptest.NewServer(srv.ContentHandler())
	defer ts.Close()
	if resp := getWith(t, ts.URL+"/worker", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("plain script fetch = %d, want 200", resp.StatusCode)
	}
}

// TestProtectedResponsesNoReferrerNoStore: private and password-protected
// responses (the 401 included) send no Referer, are never stored and cannot be
// loaded as another origin's subresource; public ones keep the revalidating
// cache policy, the baseline referrer policy and no resource policy.
func TestProtectedResponsesNoReferrerNoStore(t *testing.T) {
	ts := newTestServer(t, "")
	priv := publish(t, ts, "?private=true&filename=p.html", "<!doctype html><p>cap</p>")
	locked := publishPassword(t, ts, "?slug=locked", "letmein", "secret")
	pub := publish(t, ts, "?slug=open", "open")

	check := func(name string, resp *http.Response, wantStatus int, referrer, cache string) {
		t.Helper()
		if resp.StatusCode != wantStatus {
			t.Errorf("%s: status %d, want %d", name, resp.StatusCode, wantStatus)
		}
		if got := resp.Header.Get("Referrer-Policy"); got != referrer {
			t.Errorf("%s: Referrer-Policy %q, want %q", name, got, referrer)
		}
		if got := resp.Header.Get("Cache-Control"); got != cache {
			t.Errorf("%s: Cache-Control %q, want %q", name, got, cache)
		}
		corp := ""
		if referrer == "no-referrer" {
			corp = "same-origin"
		}
		if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got != corp {
			t.Errorf("%s: Cross-Origin-Resource-Policy %q, want %q", name, got, corp)
		}
	}
	check("private", getWith(t, priv, nil), 200, "no-referrer", "private, no-store")
	check("password 401", getWith(t, locked, nil), 401, "no-referrer", "private, no-store")
	req, _ := http.NewRequest(http.MethodGet, locked, nil)
	req.SetBasicAuth("", "letmein")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	check("password 200", resp, 200, "no-referrer", "private, no-store")
	check("public", getWith(t, pub, nil), 200, "strict-origin-when-cross-origin", "no-cache")

	// The ?live wrapper names the slug in its body; never stored.
	if cc := getWith(t, priv+"?live", nil).Header.Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("live wrapper Cache-Control %q, want private, no-store", cc)
	}
}

// TestPasswordRealmPerArtifact: each artifact challenges with its own realm,
// and the realm does not echo the slug.
func TestPasswordRealmPerArtifact(t *testing.T) {
	ts := newTestServer(t, "")
	a := publishPassword(t, ts, "?slug=alpha-doc", "pw-a", "a")
	b := publishPassword(t, ts, "?slug=beta-doc", "pw-b", "b")
	ra := getWith(t, a, nil).Header.Get("WWW-Authenticate")
	rb := getWith(t, b, nil).Header.Get("WWW-Authenticate")
	if !strings.HasPrefix(ra, `Basic realm="demiplane `) || !strings.HasPrefix(rb, `Basic realm="demiplane `) {
		t.Fatalf("unexpected challenges %q / %q", ra, rb)
	}
	if ra == rb {
		t.Errorf("two artifacts share realm %q", ra)
	}
	if strings.Contains(ra, "alpha-doc") || strings.Contains(rb, "beta-doc") {
		t.Errorf("realm echoes the slug: %q / %q", ra, rb)
	}
}

// TestSandboxedSubresourceGrant pins the narrow CORS grant that lets a
// sandboxed page load its own module scripts, styles and fonts.
func TestSandboxedSubresourceGrant(t *testing.T) {
	ts := newTestServer(t, "")
	js := publish(t, ts, "?slug=app&filename=app.js", "export const x=1;")
	css := publish(t, ts, "?slug=style&filename=style.css", "p{color:red}")
	page := publish(t, ts, "?slug=page&filename=page.html", "<!doctype html><p>x</p>")
	data := publish(t, ts, "?slug=data&filename=data.json", `{"k":1}`)
	privJS := publish(t, ts, "?private=true&filename=p.js", "var p=1;")
	lockedJS := publishPassword(t, ts, "?slug=locked-js&filename=l.js", "pw", "var l=1;")

	null := func(dest string) map[string]string {
		return map[string]string{"Origin": "null", "Sec-Fetch-Dest": dest}
	}
	cases := []struct {
		name  string
		url   string
		hdr   map[string]string
		grant bool
	}{
		{"module script", js, null("script"), true},
		{"crossorigin style", css, null("style"), true},
		{"script without dest", js, map[string]string{"Origin": "null"}, false},
		{"script from a real origin", js, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Dest": "script"}, false},
		{"fetch() of a script", js, null("empty"), false},
		{"html as script", page, null("script"), false},
		{"json as script", data, null("script"), false},
		{"css as script", css, null("script"), false},
		{"private script", privJS, null("script"), false},
		{"password script", lockedJS, null("script"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := getWith(t, tc.url, tc.hdr)
			got := resp.Header.Get("Access-Control-Allow-Origin")
			if tc.grant && got != "null" {
				t.Errorf("ACAO = %q, want null", got)
			}
			if !tc.grant && got != "" {
				t.Errorf("ACAO = %q, want none", got)
			}
			if tc.grant && resp.Header.Get("Access-Control-Allow-Credentials") != "" {
				t.Error("grant must never allow credentials")
			}
		})
	}
	if v := getWith(t, js, nil).Header.Values("Vary"); !strings.Contains(strings.Join(v, ","), "Origin") {
		t.Errorf("script response Vary %q lacks Origin", v)
	}

	// With the sandbox off there is no opaque origin to serve, so no grant.
	off := newConfiguredServer(t, Config{UnsafeNoSandbox: true})
	offJS := publish(t, off, "?slug=app&filename=app.js", "export const x=1;")
	if got := getWith(t, offJS, null("script")).Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("ACAO = %q with the sandbox off, want none", got)
	}
}

// TestSiteFilesSandboxedAndGranted: site pages get the same sandbox, and a
// site's module script is granted to its sandboxed pages.
func TestSiteFilesSandboxedAndGranted(t *testing.T) {
	ts := newTestServer(t, "")
	resp, body := postSite(t, ts.URL, "demo", "", "application/x-tar", makeTar(t, []srcFile{
		{"index.html", "<!doctype html><script type=module src=app.js></script>"},
		{"app.js", "export default 1;"},
	}))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("site publish: %d %s", resp.StatusCode, body)
	}
	csp := getWith(t, ts.URL+"/demo/", nil).Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox allow-scripts") || strings.Contains(csp, "allow-same-origin") {
		t.Errorf("site index CSP %q", csp)
	}
	got := getWith(t, ts.URL+"/demo/app.js", map[string]string{"Origin": "null", "Sec-Fetch-Dest": "script"}).
		Header.Get("Access-Control-Allow-Origin")
	if got != "null" {
		t.Errorf("site module script ACAO = %q, want null", got)
	}
}
