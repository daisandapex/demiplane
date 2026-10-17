// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// lockedBuffer is a log sink safe for the hook goroutine's writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureHookLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

const answerCanary = "answer-canary-5f19"

// submitAndWait records one answer carrying the canary and waits for the hook.
func submitAndWait(t *testing.T, ts *httptest.Server, m *Module) {
	t.Helper()
	done := hookWait(t, m)
	form := url.Values{"body": {answerCanary}}.Encode()
	if code, _ := do(t, http.MethodPost, ts.URL+"/answer/report", formCT, form, false); code != http.StatusCreated {
		t.Fatalf("submit = %d, want 201", code)
	}
	waitHook(t, done)
}

// echoingHook prints the answer and a terminal escape, then fails.
const echoingHook = `printf 'hook says \033[31m%s\n' "$DEMIPLANE_REPLY_BODY"; exit 3`

func TestHookExecFailureOmitsOutputByDefault(t *testing.T) {
	ts, m := contentSetup(t)
	m.hook = hookConfig{execCmd: echoingHook}
	logs := captureHookLog(t)
	submitAndWait(t, ts, m)

	got := logs.String()
	if !strings.Contains(got, "exit status 3") {
		t.Fatalf("hook failure not logged; log:\n%s", got)
	}
	if strings.Contains(got, answerCanary) || strings.Contains(got, "hook says") {
		t.Errorf("hook output logged without opt-in:\n%s", got)
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("raw ESC byte reached the log:\n%q", got)
	}
}

func TestHookExecDebugOutputIsEscaped(t *testing.T) {
	ts, m := contentSetup(t)
	m.hook = hookConfig{execCmd: echoingHook}
	m.hook.logOutput = true
	logs := captureHookLog(t)
	submitAndWait(t, ts, m)

	got := logs.String()
	if !strings.Contains(got, "hook says") {
		t.Fatalf("opted-in hook output missing; log:\n%s", got)
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("raw ESC byte reached the log:\n%q", got)
	}
	if !strings.Contains(got, `\x1b[31m`) {
		t.Errorf("ESC not shown escaped:\n%s", got)
	}
	// One hook failure is one log line: the output's newline is escaped too.
	if n := strings.Count(strings.TrimRight(got, "\n"), "\n"); n != 0 {
		t.Errorf("hook output split the log entry into %d extra lines:\n%s", n, got)
	}
}

// webhookSecrets are planted in every part of the configured URL a hook
// service might use for its credential.
var webhookSecrets = []string{"hookuser", "hookpass", "pathsecret", "querysecret"}

func secretHookURL(base string) string {
	u, _ := url.Parse(base)
	u.User = url.UserPassword("hookuser", "hookpass")
	u.Path = "/services/pathsecret"
	u.RawQuery = "token=querysecret"
	return u.String()
}

func TestHookWebhookStatusLogRedactsURL(t *testing.T) {
	ts, m := contentSetup(t)
	hookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(hookSrv.Close)
	m.hook = hookConfig{url: secretHookURL(hookSrv.URL)}
	logs := captureHookLog(t)
	submitAndWait(t, ts, m)

	got := logs.String()
	if !strings.Contains(got, "500") {
		t.Fatalf("webhook failure not logged; log:\n%s", got)
	}
	for _, s := range webhookSecrets {
		if strings.Contains(got, s) {
			t.Errorf("webhook secret %q leaked:\n%s", s, got)
		}
	}
	if strings.Contains(got, answerCanary) {
		t.Errorf("answer leaked into the webhook log:\n%s", got)
	}
}

func TestHookWebhookTransportErrorRedactsURL(t *testing.T) {
	ts, m := contentSetup(t)
	m.hook = hookConfig{url: secretHookURL("http://127.0.0.1:1")}
	logs := captureHookLog(t)
	submitAndWait(t, ts, m)

	got := logs.String()
	if !strings.Contains(got, "webhook failed") {
		t.Fatalf("webhook transport failure not logged; log:\n%s", got)
	}
	for _, s := range webhookSecrets {
		if strings.Contains(got, s) {
			t.Errorf("webhook secret %q leaked:\n%s", s, got)
		}
	}
}
