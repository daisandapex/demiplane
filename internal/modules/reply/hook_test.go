// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hookWait blocks until the module's hook dispatch signals done (or fails the
// test after a timeout). Install the returned channel via m.hookDone BEFORE
// any request that records a reply.
func hookWait(t *testing.T, m *Module) chan Reply {
	t.Helper()
	ch := make(chan Reply, 4)
	m.hookDone = func(rep Reply) { ch <- rep }
	return ch
}

func waitHook(t *testing.T, ch chan Reply) Reply {
	t.Helper()
	select {
	case rep := <-ch:
		return rep
	case <-time.After(10 * time.Second):
		t.Fatal("hook did not dispatch within 10s")
		return Reply{}
	}
}

// TestConfigureHookValidatesURL pins the startup contract: a malformed webhook
// URL is a hard error; empty values disable cleanly.
func TestConfigureHookValidatesURL(t *testing.T) {
	t.Cleanup(func() { std.hook = hookConfig{} })
	def := DefaultHookOptions()
	for _, bad := range []string{"not a url", "ftp://x/y", "/relative/only", "http://"} {
		if err := ConfigureHook("", bad, def); err == nil {
			t.Errorf("ConfigureHook(url=%q) should error", bad)
		}
	}
	if err := ConfigureHook("touch /tmp/x", "http://127.0.0.1:9999/hook", def); err != nil {
		t.Errorf("valid hook config rejected: %v", err)
	}
	if !std.hook.enabled() {
		t.Error("ConfigureHook did not arm the registered module")
	}
	if err := ConfigureHook("", "", def); err != nil {
		t.Errorf("empty hook config should disable, not error: %v", err)
	}
	if std.hook.enabled() {
		t.Error("empty ConfigureHook should disable the hook")
	}
}

// TestHookExecReceivesEvent covers the exec action end to end off a real
// content-plane answer: the command gets the reply as JSON on stdin AND as
// DEMIPLANE_REPLY_* env vars.
func TestHookExecReceivesEvent(t *testing.T) {
	ts, m := contentSetup(t)
	dir := t.TempDir()
	stdinFile := filepath.Join(dir, "payload.json")
	envFile := filepath.Join(dir, "env.txt")
	m.hook = hookConfig{execCmd: `cat > "` + stdinFile + `" && ` +
		`printf '%s|%s|%s' "$DEMIPLANE_REPLY_SLUG" "$DEMIPLANE_REPLY_KIND" "$DEMIPLANE_REPLY_BODY" > "` + envFile + `"`}
	done := hookWait(t, m)

	form := url.Values{"body": {"grade me"}}.Encode()
	if code, _ := do(t, http.MethodPost, ts.URL+"/answer/report", formCT, form, false); code != http.StatusCreated {
		t.Fatalf("submit = %d, want 201", code)
	}
	rep := waitHook(t, done)
	if rep.Slug != "report" || rep.Body != "grade me" {
		t.Fatalf("hook dispatched wrong reply: %+v", rep)
	}

	// JSON stdin carries the full event.
	b, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("hook exec did not write stdin capture: %v", err)
	}
	var got Reply
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("hook stdin is not the reply JSON: %v (%s)", err, b)
	}
	if got.Slug != "report" || got.Kind != "comment" || got.Body != "grade me" || got.ID == 0 {
		t.Errorf("hook stdin payload wrong: %+v", got)
	}

	// Env vars carry the same event.
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("hook exec did not write env capture: %v", err)
	}
	if string(env) != "report|comment|grade me" {
		t.Errorf("hook env vars wrong: %q", env)
	}
}

// TestHookWebhookPosts covers the webhook action: the reply is POSTed as JSON
// to the configured URL.
func TestHookWebhookPosts(t *testing.T) {
	ts, m := contentSetup(t)

	type hit struct {
		ct   string
		body []byte
	}
	hits := make(chan hit, 1)
	hookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		hits <- hit{ct: r.Header.Get("Content-Type"), body: b[:n]}
	}))
	t.Cleanup(hookSrv.Close)

	m.hook = hookConfig{url: hookSrv.URL}
	done := hookWait(t, m)

	form := url.Values{"body": {"webhook me"}}.Encode()
	if code, _ := do(t, http.MethodPost, ts.URL+"/answer/report", formCT, form, false); code != http.StatusCreated {
		t.Fatalf("submit = %d, want 201", code)
	}
	waitHook(t, done)

	select {
	case h := <-hits:
		if !strings.HasPrefix(h.ct, "application/json") {
			t.Errorf("webhook Content-Type = %q, want application/json", h.ct)
		}
		var got Reply
		if err := json.Unmarshal(h.body, &got); err != nil {
			t.Fatalf("webhook body is not reply JSON: %v (%s)", err, h.body)
		}
		if got.Slug != "report" || got.Kind != "comment" || got.Body != "webhook me" {
			t.Errorf("webhook payload wrong: %+v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("webhook was never called")
	}
}

// TestHookFailureNeverBlocksReply is the isolation guarantee: a failing exec
// AND an unreachable webhook must not fail, slow, or roll back the reply write
// — the viewer still gets the honest "Recorded" confirmation.
func TestHookFailureNeverBlocksReply(t *testing.T) {
	ts, m := contentSetup(t)
	m.hook = hookConfig{execCmd: "exit 1", url: "http://127.0.0.1:1/unreachable"}
	done := hookWait(t, m)

	form := url.Values{"body": {"still recorded"}}.Encode()
	code, body := do(t, http.MethodPost, ts.URL+"/answer/report", formCT, form, false)
	if code != http.StatusCreated {
		t.Fatalf("submit with failing hooks = %d, want 201", code)
	}
	if !strings.Contains(string(body), "✓ Recorded") {
		t.Errorf("failing hooks must not break the honest confirmation:\n%s", body)
	}
	if n, _ := m.rs.count("report"); n != 1 {
		t.Errorf("reply not stored despite failing hooks: count=%d", n)
	}
	waitHook(t, done) // dispatch completed (and logged) without panicking
}

// TestControlPlaneSubmitFiresHook pins that the hook fires on the control-plane
// submit path too, not just the content-plane answer box.
func TestControlPlaneSubmitFiresHook(t *testing.T) {
	ts, m := controlSetup(t)
	m.hook = hookConfig{execCmd: "true"}
	done := hookWait(t, m)

	code, _ := do(t, http.MethodPost, ts.URL+"/reply/report", "application/json",
		`{"kind":"approve","body":"ship it"}`, false)
	if code != http.StatusCreated {
		t.Fatalf("submit = %d, want 201", code)
	}
	rep := waitHook(t, done)
	if rep.Slug != "report" || rep.Kind != "approve" {
		t.Errorf("hook dispatched wrong reply: %+v", rep)
	}
}

// TestHookPoolBoundsConcurrencyAndQueue floods a blocking hook: at most
// workers run at once, at most queue more are admitted, and every further
// submission is refused with 503 and stores nothing.
func TestHookPoolBoundsConcurrencyAndQueue(t *testing.T) {
	ts, m := contentSetup(t)
	dir := t.TempDir()
	gate := filepath.Join(dir, "go")
	// Each run marks itself started, then blocks until the gate file exists
	// (giving up after about 30s, and the cleanup opens the gate, so a failed
	// assertion cannot leave shells behind).
	m.hook = hookConfig{
		execCmd: `touch "` + dir + `/run-$DEMIPLANE_REPLY_ID"; i=0; ` +
			`while [ ! -e "` + gate + `" ] && [ $i -lt 1500 ]; do sleep 0.02; i=$((i+1)); done`,
		workers: 2, queue: 3,
	}
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o600) })
	done := make(chan Reply, 64)
	m.hookDone = func(rep Reply) { done <- rep }

	var created, busy int
	for i := 0; i < 20; i++ {
		form := url.Values{"body": {"answer"}}.Encode()
		switch code, _ := do(t, http.MethodPost, ts.URL+"/answer/report", formCT, form, false); code {
		case http.StatusCreated:
			created++
		case http.StatusServiceUnavailable:
			busy++
		default:
			t.Fatalf("submission %d: status %d", i, code)
		}
	}
	if created != 5 || busy != 15 {
		t.Fatalf("created %d, refused %d; want 5 admitted (2 running + 3 queued) and 15 refused", created, busy)
	}
	if n, _ := m.rs.count("report"); n != 5 {
		t.Fatalf("stored %d replies, want 5: a refused submission must not be recorded", n)
	}
	// Exactly the two workers are running; the queued three have not started.
	deadline := time.Now().Add(10 * time.Second)
	for {
		runs, _ := filepath.Glob(filepath.Join(dir, "run-*"))
		if len(runs) == 2 {
			break
		}
		if len(runs) > 2 {
			t.Fatalf("%d hooks running at once, want 2", len(runs))
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d hooks started", len(runs))
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if runs, _ := filepath.Glob(filepath.Join(dir, "run-*")); len(runs) != 2 {
		t.Fatalf("%d hooks running at once, want 2", len(runs))
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		waitHook(t, done)
	}
	// Capacity is back once the backlog drains.
	form := url.Values{"body": {"after"}}.Encode()
	if code, _ := do(t, http.MethodPost, ts.URL+"/answer/report", formCT, form, false); code != http.StatusCreated {
		t.Fatalf("submission after drain = %d, want 201", code)
	}
	waitHook(t, done)
}

// TestHookTimeoutKillsProcessGroup runs a hook whose shell starts a
// background child and then outlives the timeout: the timeout must kill the
// child too.
func TestHookTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	cmd := `sleep 20 & echo $! > "` + pidFile + `"; sleep 20`
	start := time.Now()
	runHookExec(cmd, 300*time.Millisecond, Reply{ID: 1, Slug: "s", Kind: "comment"}, []byte("{}"), true)
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("hook ran %v past a 300ms timeout", el)
	}
	assertProcessGone(t, pidFile)
}

// TestHookExitKillsLeftoverChildren: a hook that exits while a background
// child still runs (and holds the output pipe) returns promptly and the child
// is killed with the group.
func TestHookExitKillsLeftoverChildren(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	cmd := `sleep 20 & echo $! > "` + pidFile + `"; exit 0`
	start := time.Now()
	runHookExec(cmd, time.Minute, Reply{ID: 1, Slug: "s", Kind: "comment"}, []byte("{}"), true)
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("hook took %v to return after its shell exited", el)
	}
	assertProcessGone(t, pidFile)
}

func assertProcessGone(t *testing.T, pidFile string) {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("child pid not recorded: %v", err)
	}
	pid := strings.TrimSpace(string(b))
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := os.ReadFile("/proc/" + pid + "/stat")
		if err != nil {
			return // gone
		}
		// A zombie awaiting its (dead) parent's reaper is dead too.
		if f := strings.Fields(string(st)); len(f) > 2 && f[2] == "Z" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("background child %s survived its hook", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestHookOutputIsCappedWhileStreaming: a hook that writes far more than the
// log cap keeps memory bounded by the cap, not by the output.
func TestHookOutputIsCappedWhileStreaming(t *testing.T) {
	w := &capWriter{max: hookLogOutputCap}
	chunk := make([]byte, 64<<10)
	for i := 0; i < 1024; i++ { // 64 MiB written
		if n, err := w.Write(chunk); n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v; want a full write", n, err)
		}
	}
	if len(w.buf) != hookLogOutputCap || cap(w.buf) > 4*hookLogOutputCap {
		t.Fatalf("kept %d bytes (cap %d) of 64 MiB, want %d", len(w.buf), cap(w.buf), hookLogOutputCap)
	}
	if got := w.bytes(); !strings.HasSuffix(string(got), "…") {
		t.Fatal("truncated output is not marked")
	}

	// End to end: a hook printing 64 MiB then failing finishes and logs at
	// most the cap.
	var logged strings.Builder
	prev := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(prev)
	start := time.Now()
	runHookExec(`head -c 67108864 /dev/zero | tr '\0' x; exit 3`, time.Minute,
		Reply{ID: 7, Slug: "s", Kind: "comment"}, []byte("{}"), true)
	if el := time.Since(start); el > 30*time.Second {
		t.Fatalf("verbose hook took %v", el)
	}
	out := logged.String()
	if !strings.Contains(out, "reply hook exec failed (reply 7)") {
		t.Fatalf("failure not logged: %q", out)
	}
	if len(out) > hookLogOutputCap+512 {
		t.Fatalf("logged %d bytes of hook output, cap %d", len(out), hookLogOutputCap)
	}
}

// TestHookStdinHeldByChildDoesNotWedgeWorker: the shell exits without
// reading a payload larger than a pipe buffer while a background child keeps
// stdin open. The run must still end and the child must die.
func TestHookStdinHeldByChildDoesNotWedgeWorker(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	cmd := `sleep 20 <&0 & echo $! > "` + pidFile + `"; exit 0`
	payload := []byte(`{"body":"` + strings.Repeat("x", 1<<20) + `"}`)
	start := time.Now()
	runHookExec(cmd, time.Minute, Reply{ID: 1, Slug: "s", Kind: "comment"}, payload, true)
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("hook took %v to return while a child held stdin", el)
	}
	assertProcessGone(t, pidFile)
}
