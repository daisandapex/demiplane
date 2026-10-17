// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Browser-level tests drive a real headless Chromium against a real
// `demiplane serve`. They exist because the content-isolation guarantees (the
// CSP sandbox, the opaque origin it gives each document, the refusal of service
// workers) are enforced by the browser, not by the server: a Go HTTP client
// cannot observe them.
//
// Each test page reports what it saw by loading an image from a collector
// server run by the test (an image load is a no-cors request, so it works from
// an opaque origin too). The collector hands the query string back to the
// test, which asserts on it.
//
// The browser is found via DEMIPLANE_E2E_CHROME, then PATH, then a Playwright
// cache. Without one the tests skip, unless DEMIPLANE_E2E_REQUIRE_BROWSER=1
// (set in CI), where a missing browser is a failure rather than a silent pass.

// findChrome returns a Chromium-family binary, or "" if none is installed.
func findChrome() string {
	if p := os.Getenv("DEMIPLANE_E2E_CHROME"); p != "" {
		return p
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome",
		"google-chrome-stable", "chrome-headless-shell", "headless_shell"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	for _, pattern := range []string{
		"/.cache/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell",
		"/.cache/ms-playwright/chromium-*/chrome-linux64/chrome",
	} {
		if m, _ := filepath.Glob(home + pattern); len(m) > 0 {
			return m[len(m)-1]
		}
	}
	return ""
}

// requireChrome returns the browser path or skips (fails under
// DEMIPLANE_E2E_REQUIRE_BROWSER=1).
func requireChrome(t *testing.T) string {
	t.Helper()
	p := findChrome()
	if p == "" {
		if os.Getenv("DEMIPLANE_E2E_REQUIRE_BROWSER") == "1" {
			t.Fatal("no Chromium found and DEMIPLANE_E2E_REQUIRE_BROWSER=1")
		}
		t.Skip("no Chromium found; set DEMIPLANE_E2E_CHROME to run browser tests")
	}
	return p
}

// collector is a tiny HTTP server that receives page reports at /r?d=<json>.
type collector struct {
	srv     *httptest.Server
	reports chan string
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{reports: make(chan string, 16)}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/r" {
			select {
			case c.reports <- r.URL.Query().Get("d"):
			default:
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// wait returns the first report, or fails the test after timeout.
func (c *collector) wait(t *testing.T, timeout time.Duration) string {
	t.Helper()
	select {
	case d := <-c.reports:
		return d
	case <-time.After(timeout):
		t.Fatalf("no report from the browser within %s", timeout)
		return ""
	}
}

// startChrome opens url in a fresh headless browser profile and returns once it
// is running; the process group is killed at cleanup. The address-space limit
// is lifted for the child only: V8 reserves a large virtual region at start and
// aborts under a tight RLIMIT_AS (resident memory is unaffected).
func startChrome(t *testing.T, chrome, url string) {
	t.Helper()
	args := []string{
		"-c", `ulimit -v unlimited 2>/dev/null; exec "$0" "$@"`, chrome,
		"--headless", "--no-sandbox", "--disable-gpu", "--no-first-run",
		"--disable-background-networking", "--disable-dev-shm-usage",
		"--user-data-dir=" + t.TempDir(),
		url,
	}
	cmd := exec.Command("/bin/sh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logs := &syncBuffer{}
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start browser: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("browser output:\n%s", logs.String())
		}
	})
}

// publishAs publishes body at a named slug with an optional filename and
// password, failing the test on anything but 201.
func publishAs(t *testing.T, srv *testServer, slug, filename, password, body string) {
	t.Helper()
	u := srv.ControlURL + "/publish?slug=" + slug
	if filename != "" {
		u += "&filename=" + filename
	}
	h := srv.authHeader()
	if password != "" {
		h = mergeHeaders(h, map[string]string{"X-Demiplane-Password": password})
	}
	res := srv.do(t, http.MethodPost, u, strings.NewReader(body), h)
	if res.Status != http.StatusCreated {
		t.Fatalf("publish %s: status=%d body=%s", slug, res.Status, res.Body)
	}
}

// reportJS is the shared page-side helper: report(obj) sends obj to the
// collector once.
func reportJS(collectorURL string) string {
	return `var __sent=false;function report(o){if(__sent)return;__sent=true;` +
		`new Image().src=` + jsString(collectorURL+"/r?d=") + `+encodeURIComponent(JSON.stringify(o));}`
}

func jsString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "<", `\x3c`).Replace(s) + `"`
}

// TestBrowser_HostileHTMLCannotReachOtherArtifacts publishes a hostile page next
// to a public artifact, a password-protected artifact and a script suitable as
// a root-scoped service worker. Opened in a real browser, the hostile page must
// not be able to read any of them (by fetch, credentialed fetch, or by framing
// and reaching into the frame) and must not be able to register the worker.
func TestBrowser_HostileHTMLCannotReachOtherArtifacts(t *testing.T) {
	chrome := requireChrome(t)
	srv := startServer(t, serverOpts{Token: "browser-token"})
	col := newCollector(t)

	publishAs(t, srv, "target", "target.txt", "", "public-canary-7f3a")
	publishAs(t, srv, "locked", "locked.txt", "pw-4521", "locked-canary-9c1e")
	publishAs(t, srv, "worker", "worker.js", "", "self.addEventListener('fetch',function(e){});")

	hostile := `<!doctype html><html><body><script>` + reportJS(col.srv.URL) + `
var out={js:true},pending=4;
function step(k,v){out[k]=v;if(--pending===0)report(out);}
function err(e){return 'ERR:'+(e&&e.name?e.name:String(e));}
fetch('/target').then(function(r){return r.text();}).then(function(t){step('read',t);},function(e){step('read',err(e));});
fetch('/locked',{credentials:'include',headers:{'Authorization':'Basic '+btoa('u:pw-4521')}})
  .then(function(r){return r.text();}).then(function(t){step('locked',t);},function(e){step('locked',err(e));});
try{
  navigator.serviceWorker.register('/worker',{scope:'/'})
    .then(function(){step('sw','registered');},function(e){step('sw',err(e));});
}catch(e){step('sw',err(e));}
var f=document.createElement('iframe'),framed=false;
function readFrame(){if(framed)return;framed=true;
  try{step('frame',f.contentDocument.body.textContent);}catch(e){step('frame',err(e));}}
f.onload=readFrame;setTimeout(readFrame,4000);
f.src='/target';document.body.appendChild(f);
try{localStorage.setItem('k','v');out.storage='ok';}catch(e){out.storage=err(e);}
out.origin=self.origin;
</script></body></html>`
	publishAs(t, srv, "hostile", "hostile.html", "", hostile)

	startChrome(t, chrome, srv.ContentURL+"/hostile")
	got := col.wait(t, 30*time.Second)
	t.Logf("hostile page report: %s", got)

	if !strings.Contains(got, `"js":true`) {
		t.Fatalf("hostile page script did not run, so nothing below was tested: %s", got)
	}
	for _, canary := range []string{"public-canary-7f3a", "locked-canary-9c1e"} {
		if strings.Contains(got, canary) {
			t.Errorf("hostile page read another artifact (%s): %s", canary, got)
		}
	}
	if strings.Contains(got, `"sw":"registered"`) {
		t.Errorf("hostile page registered a service worker: %s", got)
	}
	if !strings.Contains(got, `"origin":"null"`) {
		t.Errorf("hostile page is not running in an opaque origin: %s", got)
	}
}

// TestBrowser_ProtectedScriptsCannotBeIncluded: a hostile page must not be
// able to run a protected script through a plain <script src> (a no-cors load,
// which carries the viewer's cached credentials and needs no CORS grant) and
// read what it defines. The browser is given real cached Basic credentials by
// opening the hostile page, itself protected with the same password, through a
// URL with credentials; the browser then attaches them to later requests on
// the origin, as it would for a viewer who had typed the password. The script
// URLs are absolute: a relative one would inherit the page URL's credentials,
// and the browser refuses subresource URLs that carry credentials.
func TestBrowser_ProtectedScriptsCannotBeIncluded(t *testing.T) {
	chrome := requireChrome(t)
	srv := startServer(t, serverOpts{Token: "browser-token"})
	col := newCollector(t)

	publishAs(t, srv, "locked-js", "locked.js", "pw-777", "window.lockedLeak='locked-js-canary';")
	res := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?private=true&filename=cap.js",
		strings.NewReader("window.capLeak='cap-js-canary';"), srv.authHeader())
	if res.Status != http.StatusCreated {
		t.Fatalf("publish private script: %d %s", res.Status, res.Body)
	}
	capPath := strings.TrimPrefix(strings.TrimSpace(string(res.Body)), srv.ContentURL)

	hostile := `<!doctype html><html><head>
<script src="` + srv.ContentURL + `/locked-js"></script>
<script src="` + srv.ContentURL + capPath + `"></script>
</head><body><script>` + reportJS(col.srv.URL) + `
report({js:true,locked:window.lockedLeak||null,cap:window.capLeak||null,origin:self.origin});
</script></body></html>`
	publishAs(t, srv, "hostile-incl", "hostile.html", "pw-777", hostile)

	u := strings.Replace(srv.ContentURL, "http://", "http://viewer:pw-777@", 1) + "/hostile-incl"
	startChrome(t, chrome, u)
	got := col.wait(t, 30*time.Second)
	t.Logf("inclusion report: %s", got)

	if !strings.Contains(got, `"js":true`) || !strings.Contains(got, `"origin":"null"`) {
		t.Fatalf("hostile page did not run sandboxed, so nothing was tested: %s", got)
	}
	for _, canary := range []string{"locked-js-canary", "cap-js-canary"} {
		if strings.Contains(got, canary) {
			t.Errorf("hostile page ran a protected script (%s): %s", canary, got)
		}
	}
}

// TestBrowser_SandboxedSiteLoadsOwnAssets is the positive control for the
// sandbox: a multi-file site must still load its own stylesheet, classic
// script, module script and image while running in an opaque origin.
func TestBrowser_SandboxedSiteLoadsOwnAssets(t *testing.T) {
	chrome := requireChrome(t)
	srv := startServer(t, serverOpts{Token: "browser-token"})
	col := newCollector(t)

	index := `<!doctype html><html><head>
<link rel="stylesheet" href="style.css">
<link rel="stylesheet" crossorigin href="cors.css">
<script src="app.js"></script>
<script type="module" src="mod.js"></script>
</head><body><img id="px" src="px.png"><p id="p">x</p><script>` + reportJS(col.srv.URL) + `
window.addEventListener('load',function(){setTimeout(function(){
  report({classic:window.classicRan===1,module:window.moduleRan===1,
    css:getComputedStyle(document.body).color,cors:getComputedStyle(document.getElementById('p')).color,
    img:document.getElementById('px').naturalWidth,origin:self.origin});
},500);});
</script></body></html>`
	files := map[string]string{
		"index.html": index,
		"style.css":  "body{color:rgb(1, 2, 3)}",
		"cors.css":   "#p{color:rgb(4, 5, 6)}",
		"app.js":     "window.classicRan=1;",
		"mod.js":     "window.moduleRan=1;",
		"px.png":     onePixelPNG,
	}
	pub := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?site=assets-site",
		strings.NewReader(string(buildTar(t, files))), mergeHeaders(srv.authHeader(),
			map[string]string{"Content-Type": "application/x-tar"}))
	if pub.Status != http.StatusCreated {
		t.Fatalf("site publish: status=%d body=%s", pub.Status, pub.Body)
	}

	startChrome(t, chrome, srv.ContentURL+"/assets-site/")
	got := col.wait(t, 30*time.Second)
	t.Logf("site report: %s", got)

	for _, want := range []string{
		`"classic":true`, `"module":true`, `"css":"rgb(1, 2, 3)"`,
		`"cors":"rgb(4, 5, 6)"`, `"img":1`, `"origin":"null"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("site report missing %s: %s", want, got)
		}
	}
}

// TestBrowser_SandboxedFormStillRecordsReply checks that a sandboxed page can
// still submit the reply box: the form post arrives with an opaque Origin, and
// the answer must still be recorded.
func TestBrowser_SandboxedFormStillRecordsReply(t *testing.T) {
	chrome := requireChrome(t)
	srv := startServer(t, serverOpts{Token: "browser-token"})

	pub := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug=lesson-b&render=md&reply=question",
		strings.NewReader("# Q\n\nWhy?\n"), srv.authHeader())
	if pub.Status != http.StatusCreated {
		t.Fatalf("publish lesson: status=%d body=%s", pub.Status, pub.Body)
	}
	form := `<!doctype html><html><body><form id="f" method="post" action="/answer/lesson-b">` +
		`<textarea name="body">sandboxed-answer-31d</textarea></form>` +
		`<script>document.getElementById('f').submit();</script></body></html>`
	publishAs(t, srv, "quiz", "quiz.html", "", form)

	startChrome(t, chrome, srv.ContentURL+"/quiz")
	deadline := time.Now().Add(30 * time.Second)
	for {
		list := srv.do(t, http.MethodGet, srv.ControlURL+"/replies", nil, srv.authHeader())
		if strings.Contains(string(list.Body), "sandboxed-answer-31d") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("answer from the sandboxed form was not recorded: %s", list.Body)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestBrowser_LivePreviewReloadsSandboxedPage: the ?live wrapper frames a
// sandboxed artifact and reloads it when the artifact is republished.
func TestBrowser_LivePreviewReloadsSandboxedPage(t *testing.T) {
	chrome := requireChrome(t)
	srv := startServer(t, serverOpts{Token: "browser-token"})
	col := newCollector(t)

	page := func(version string) string {
		return `<!doctype html><html><body><script>` +
			`new Image().src=` + jsString(col.srv.URL+"/r?d=") + `+encodeURIComponent(` +
			jsString(version) + `+' '+self.origin);</script></body></html>`
	}
	publishAs(t, srv, "live-page", "live.html", "", page("v1"))
	startChrome(t, chrome, srv.ContentURL+"/live-page?live")

	if got := col.wait(t, 30*time.Second); got != "v1 null" {
		t.Fatalf("first load reported %q, want %q", got, "v1 null")
	}
	// The stream opens after the frame loads; give it a moment to connect.
	time.Sleep(time.Second)
	publishAs(t, srv, "live-page", "live.html", "", page("v2"))
	if got := col.wait(t, 30*time.Second); got != "v2 null" {
		t.Fatalf("after republish the preview reported %q, want %q", got, "v2 null")
	}
}

// onePixelPNG is a valid 1x1 PNG.
const onePixelPNG = "\x89\x50\x4e\x47\x0d\x0a\x1a\x0a\x00\x00\x00\x0d\x49\x48\x44\x52\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89" +
	"\x00\x00\x00\x0b\x49\x44\x41\x54\x78\x9c\x63\x60\x00\x02\x00\x00\x05\x00\x01\x7a\x5e\xab\x3f" +
	"\x00\x00\x00\x00\x49\x45\x4e\x44\xae\x42\x60\x82"
