// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"encoding/json"
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
	for _, bad := range []string{"not a url", "ftp://x/y", "/relative/only", "http://"} {
		if err := ConfigureHook("", bad); err == nil {
			t.Errorf("ConfigureHook(url=%q) should error", bad)
		}
	}
	if err := ConfigureHook("touch /tmp/x", "http://127.0.0.1:9999/hook"); err != nil {
		t.Errorf("valid hook config rejected: %v", err)
	}
	if !std.hook.enabled() {
		t.Error("ConfigureHook did not arm the registered module")
	}
	if err := ConfigureHook("", ""); err != nil {
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
