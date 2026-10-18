// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestBrowser_PreexistingServiceWorkerUpgrade covers a service worker that a
// page registered on the content origin under an affected release, before
// content isolation. Each subtest keeps one browser profile across several
// browser runs, so the registration survives the server swap as it would for
// a real viewer.
//
// The kill switch removes such a worker, but only through the browser's update
// check, and the browser runs that check after the active worker has handled
// the navigation that triggered it. So the first navigation after the upgrade
// is still the old worker's to read. "residual" pins that limitation and the
// cleanup after it; "fresh content origin" tests the migration procedure that
// avoids it.
func TestBrowser_PreexistingServiceWorkerUpgrade(t *testing.T) {
	chrome := requireChrome(t)

	// Known limitation: the first navigation after the upgrade is answered by
	// the old worker, and that navigation's URL (here a capability URL) is
	// disclosed to it. The browser updates the worker as part of it; the kill
	// switch then unregisters itself and reloads the page from the server, and
	// later navigations and the registration list are clean.
	t.Run("residual", func(t *testing.T) {
		col := newCollector(t)
		profile := t.TempDir()
		port := freePort(t)
		origin := "http://127.0.0.1:" + strconv.Itoa(port)
		installHostileWorker(t, chrome, col, profile, port)

		srv := startServer(t, serverOpts{Token: "sw-token", ContentPort: port})
		capPath := publishCanary(t, srv, col, "canary-41ab")

		run := chromeRun(t, chrome, profile, origin+capPath)
		first := col.wait(t, 30*time.Second)
		if !strings.Contains(first, `"intercepted":`+jsString(capPath)) {
			t.Fatalf("expected the old worker to answer the first navigation after the upgrade "+
				"(the documented limitation); if this changed, update the upgrade notes: %s", first)
		}
		t.Logf("known limitation reproduced: the first navigation after the upgrade went to the old worker: %s", first)
		// The kill switch reloads the page it took over; that load is the server's.
		waitReport(t, col, `"real":"canary-41ab"`, 30*time.Second)
		run.stop(t)

		run = chromeRun(t, chrome, profile, origin+capPath)
		waitReport(t, col, `"real":"canary-41ab"`, 30*time.Second)
		run.stop(t)
		srv.Stop()

		assertNoRegistrations(t, chrome, col, profile, port)
	})

	// The migration procedure: serve the upgraded instance's content plane on
	// a new origin. The old worker is bound to the old origin and never sees a
	// navigation on the new one, so the very first visit is the server's.
	t.Run("fresh content origin", func(t *testing.T) {
		col := newCollector(t)
		profile := t.TempDir()
		installHostileWorker(t, chrome, col, profile, freePort(t))

		srv := startServer(t, serverOpts{Token: "sw-token"}) // a new content port
		capPath := publishCanary(t, srv, col, "canary-58d0")
		run := chromeRun(t, chrome, profile, srv.ContentURL+capPath)
		waitReport(t, col, `"real":"canary-58d0"`, 30*time.Second)
		run.stop(t)
	})
}

// installHostileWorker runs a stand-in for an affected pre-isolation server
// (pages unsandboxed, any script servable as a worker) on 127.0.0.1:port and
// has the browser profile register a root-scoped worker there that answers
// every navigation itself with a page reporting {"intercepted": <path>}. It
// returns once the worker has been seen intercepting, with the stand-in and
// the browser stopped.
func installHostileWorker(t *testing.T, chrome string, col *collector, profile string, port int) {
	t.Helper()
	interceptedPage := `<!doctype html><title>intercepted</title><script>new Image().src=` +
		jsString(col.srv.URL+"/r?d=") + `+encodeURIComponent(JSON.stringify({intercepted:location.pathname}));</script>`
	hostileWorker := `var page=` + jsString(interceptedPage) + `;
self.addEventListener('install',function(){self.skipWaiting();});
self.addEventListener('activate',function(e){e.waitUntil(self.clients.claim());});
self.addEventListener('fetch',function(e){
  if(e.request.mode!=='navigate')return;
  e.respondWith(new Response(page,{headers:{'Content-Type':'text/html'}}));
});`
	installPage := `<!doctype html><script>` + reportJS(col.srv.URL) + `
navigator.serviceWorker.register('/sw.js',{scope:'/'}).then(function(){
  return navigator.serviceWorker.ready;
}).then(function(){
  if(navigator.serviceWorker.controller){location.href='/probe';return;}
  navigator.serviceWorker.addEventListener('controllerchange',function(){location.href='/probe';});
}).catch(function(e){report({installError:String(e)});});
</script>`
	before := standIn(t, port, map[string]string{
		"/install": installPage,
		"/sw.js":   hostileWorker,
	})
	run := chromeRun(t, chrome, profile, "http://127.0.0.1:"+strconv.Itoa(port)+"/install")
	if got := col.wait(t, 30*time.Second); !strings.Contains(got, `"intercepted":"/probe"`) {
		t.Fatalf("the pre-upgrade worker did not take over navigations, so nothing below is tested: %s", got)
	}
	run.stop(t)
	before.Close()
}

// publishCanary publishes a private (capability URL) page that reports
// {"real": marker} and returns its path on the content origin.
func publishCanary(t *testing.T, srv *testServer, col *collector, marker string) string {
	t.Helper()
	body := `<!doctype html><script>` + reportJS(col.srv.URL) + `report({real:` + jsString(marker) + `});</script>`
	res := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?private=true&filename=canary.html",
		strings.NewReader(body), srv.authHeader())
	if res.Status != http.StatusCreated {
		t.Fatalf("publish private canary: %d %s", res.Status, res.Body)
	}
	return strings.TrimPrefix(strings.TrimSpace(string(res.Body)), srv.ContentURL)
}

// assertNoRegistrations opens an unsandboxed stand-in page on 127.0.0.1:port
// with profile and requires that the origin has no service worker
// registration and the page is not controlled.
func assertNoRegistrations(t *testing.T, chrome string, col *collector, profile string, port int) {
	t.Helper()
	after := standIn(t, port, map[string]string{
		"/check": `<!doctype html><script>` + reportJS(col.srv.URL) + `
navigator.serviceWorker.getRegistrations().then(function(rs){
  report({registrations:rs.length,controlled:!!navigator.serviceWorker.controller});
},function(e){report({checkError:String(e)});});
</script>`,
	})
	defer after.Close()
	run := chromeRun(t, chrome, profile, "http://127.0.0.1:"+strconv.Itoa(port)+"/check")
	defer run.stop(t)
	got := waitReport(t, col, `"registrations"`, 30*time.Second)
	if !strings.Contains(got, `"registrations":0`) || !strings.Contains(got, `"controlled":false`) {
		t.Fatalf("a service worker is still registered after the upgrade: %s", got)
	}
}

// waitReport waits for a report containing want. It fails at once on a report
// from the hostile worker's page (one that answered a navigation instead of
// the server), and on the timeout. Other reports are skipped.
func waitReport(t *testing.T, col *collector, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	var seen []string
	for {
		select {
		case d := <-col.reports:
			seen = append(seen, d)
			if strings.Contains(d, `"intercepted"`) {
				t.Fatalf("the old worker answered a navigation while waiting for %s: %s (reports: %v)", want, d, seen)
			}
			if strings.Contains(d, want) {
				return d
			}
		case <-deadline:
			t.Fatalf("no report with %s within %s; reports: %v", want, timeout, seen)
			return ""
		}
	}
}

// standIn serves fixed bodies on 127.0.0.1:port: .js as JavaScript, anything
// else as HTML, with no isolation headers (as servers before content isolation
// did).
func standIn(t *testing.T, port int, pages map[string]string) *httptest.Server {
	t.Helper()
	var l net.Listener
	var err error
	for i := 0; i < 50; i++ { // the previous holder of the port may still be closing
		if l, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("listen on the fixed port: %v", err)
	}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		ct := "text/html; charset=utf-8"
		if strings.HasSuffix(r.URL.Path, ".js") {
			ct = "application/javascript"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(body))
	}))
	s.Listener.Close()
	s.Listener = l
	s.Start()
	t.Cleanup(s.Close)
	return s
}

// browserRun is one headless browser process on a profile kept across runs.
type browserRun struct {
	cmd  *exec.Cmd
	logs *syncBuffer
	done chan struct{}
}

// chromeRun opens url in a headless browser using profile, which persists
// (service worker registrations included) after the run is stopped.
func chromeRun(t *testing.T, chrome, profile, url string) *browserRun {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", `ulimit -v unlimited 2>/dev/null; exec "$0" "$@"`, chrome,
		"--headless", "--no-sandbox", "--disable-gpu", "--no-first-run",
		"--disable-background-networking", "--disable-dev-shm-usage",
		"--user-data-dir="+profile, url)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	b := &browserRun{cmd: cmd, logs: &syncBuffer{}, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = b.logs, b.logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start browser: %v", err)
	}
	go func() { _ = cmd.Wait(); close(b.done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-b.done
		if t.Failed() {
			t.Logf("browser output:\n%s", b.logs.String())
		}
	})
	return b
}

// stop ends the run gracefully, so the profile is written out, and kills it
// only if it does not exit in time.
func (b *browserRun) stop(t *testing.T) {
	t.Helper()
	_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-b.done:
	case <-time.After(15 * time.Second):
		_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGKILL)
		<-b.done
	}
}
