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

// TestBrowser_UpgradeRemovesPreexistingServiceWorker: a service worker that a
// page registered on the content origin before the upgrade must stop
// controlling the origin once the upgraded server answers its update check.
//
// One browser profile is kept across four browser runs on one fixed origin:
//
//  1. A stand-in for a pre-isolation server (which served pages unsandboxed
//     and any script as a worker) registers a root-scoped worker that answers
//     every navigation itself. The test confirms it intercepts.
//  2. The real demiplane takes the same port. Opening a public page there
//     triggers the browser's own update check of the registered worker; the
//     test requires the real page to load, not the worker's answer.
//  3. A password-protected canary, opened with credentials, must be the real
//     artifact.
//  4. A stand-in serving one unsandboxed page on the same origin reports the
//     origin's registrations, which must be gone.
func TestBrowser_UpgradeRemovesPreexistingServiceWorker(t *testing.T) {
	chrome := requireChrome(t)
	col := newCollector(t)
	profile := t.TempDir()
	port := freePort(t)
	origin := "http://127.0.0.1:" + strconv.Itoa(port)

	// 1. Before the upgrade.
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
  if(navigator.serviceWorker.controller){location.href='/canary';return;}
  navigator.serviceWorker.addEventListener('controllerchange',function(){location.href='/canary';});
}).catch(function(e){report({installError:String(e)});});
</script>`
	before := standIn(t, port, map[string]string{
		"/install": installPage,
		"/sw.js":   hostileWorker,
	})
	run := chromeRun(t, chrome, profile, origin+"/install")
	if got := col.wait(t, 30*time.Second); !strings.Contains(got, `"intercepted":"/canary"`) {
		t.Fatalf("pre-upgrade worker did not take over navigations, so nothing below is tested: %s", got)
	}
	run.stop(t)
	before.Close()

	// 2. The upgraded server on the same origin.
	srv := startServer(t, serverOpts{Token: "sw-token", ContentPort: port})
	publishAs(t, srv, "probe", "probe.html", "", `<!doctype html><script>`+reportJS(col.srv.URL)+
		`report({real:'probe-7d21'});</script>`)
	publishAs(t, srv, "canary", "canary.html", "pw-5150", `<!doctype html><script>`+reportJS(col.srv.URL)+
		`report({real:'canary-41ab'});</script>`)
	run = chromeRun(t, chrome, profile, origin+"/probe")
	waitReport(t, col, `"real":"probe-7d21"`, 45*time.Second)
	run.stop(t)

	// 3. The protected canary is served by the server, not a worker.
	run = chromeRun(t, chrome, profile, "http://u:pw-5150@127.0.0.1:"+strconv.Itoa(port)+"/canary")
	waitReport(t, col, `"real":"canary-41ab"`, 30*time.Second)
	run.stop(t)
	srv.Stop()

	// 4. No registration is left on the origin.
	after := standIn(t, port, map[string]string{
		"/check": `<!doctype html><script>` + reportJS(col.srv.URL) + `
navigator.serviceWorker.getRegistrations().then(function(rs){
  report({registrations:rs.length,controlled:!!navigator.serviceWorker.controller});
},function(e){report({checkError:String(e)});});
</script>`,
	})
	defer after.Close()
	run = chromeRun(t, chrome, profile, origin+"/check")
	got := waitReport(t, col, `"registrations"`, 30*time.Second)
	if !strings.Contains(got, `"registrations":0`) || !strings.Contains(got, `"controlled":false`) {
		t.Fatalf("a service worker is still registered after the upgrade: %s", got)
	}
	run.stop(t)
}

// waitReport waits for a report containing want, failing on an interception
// report or the timeout. Earlier, unrelated reports are skipped.
func waitReport(t *testing.T, col *collector, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	var seen []string
	for {
		select {
		case d := <-col.reports:
			seen = append(seen, d)
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
