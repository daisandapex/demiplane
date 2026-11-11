// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// nulSubmission posts one submission carrying a NUL byte in one field and
// returns the status. Each case names the Reply field the NUL would land in.
type nulSubmission struct {
	name  string
	field string // the Reply field the NUL would reach
	path  string
	ctype string
	body  string
}

func nulSubmissions() []nulSubmission {
	return []nulSubmission{
		{"control JSON body", "Body", "/reply/on", "application/json", `{"kind":"comment","body":"evil\u0000hidden"}`},
		{"control JSON approve body", "Body", "/reply/on", "application/json", `{"kind":"approve","body":"\u0000"}`},
		{"control form body", "Body", "/reply/on", formCT, "kind=comment&body=evil%00hidden"},
		{"content form body", "Body", "/answer/on", formCT, "body=evil%00hidden"},
		{"control JSON kind", "Kind", "/reply/on", "application/json", `{"kind":"approve\u0000x"}`},
		{"control form kind", "Kind", "/reply/on", formCT, "kind=approve%00x"},
		{"control slug", "Slug", "/reply/o%00n", "application/json", `{"kind":"approve"}`},
		{"content slug", "Slug", "/answer/o%00n", formCT, "body=hi"},
	}
}

// TestReplyWithNULIsRefused is the audit's reproduction: a reply whose body
// held a NUL was stored (201) and the exec hook then failed with "environment
// variable contains NUL". Every submit path must refuse it with 400 and store
// nothing, so the hook is never handed a value exec cannot pass.
func TestReplyWithNULIsRefused(t *testing.T) {
	ts, m := admissionSetup(t, openLimits())
	for _, c := range nulSubmissions() {
		code, _ := do(t, http.MethodPost, ts.URL+c.path, c.ctype, c.body, false)
		want := http.StatusBadRequest
		if c.field == "Slug" {
			want = http.StatusNotFound // no artifact can be named with a NUL
		}
		if code != want {
			t.Errorf("%s: status = %d, want %d", c.name, code, want)
		}
	}
	if n := storedCount(t, m, "on"); n != 0 {
		t.Errorf("stored %d replies from NUL submissions, want 0", n)
	}
}

// TestNULReplyNeverReachesHookButNormalOneDoes: with an exec hook armed, the
// NUL submission is refused before the hook, and a normal reply right after
// it still runs the hook with its body in the environment.
func TestNULReplyNeverReachesHookButNormalOneDoes(t *testing.T) {
	ts, m := admissionSetup(t, openLimits())
	out := filepath.Join(t.TempDir(), "body.txt")
	m.hook = hookConfig{execCmd: `printf '%s' "$DEMIPLANE_REPLY_BODY" > "` + out + `"`}
	done := hookWait(t, m)

	if code, _ := do(t, http.MethodPost, ts.URL+"/reply/on", "application/json",
		`{"kind":"comment","body":"evil\u0000hidden"}`, false); code != http.StatusBadRequest {
		t.Fatalf("NUL reply = %d, want 400", code)
	}
	select {
	case rep := <-done:
		t.Fatalf("hook dispatched for a refused reply: %+v", rep)
	case <-time.After(100 * time.Millisecond):
	}

	if code, _ := do(t, http.MethodPost, ts.URL+"/answer/on", formCT,
		url.Values{"body": {"plain answer"}}.Encode(), false); code != http.StatusCreated {
		t.Fatalf("normal reply = %d, want 201", code)
	}
	waitHook(t, done)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("hook did not run for a normal reply: %v", err)
	}
	if string(b) != "plain answer" {
		t.Errorf("hook saw body %q, want %q", b, "plain answer")
	}
}

// TestHookEnvValuesCannotCarryNUL ties the hook environment to the submit
// validation: every Reply string field that reaches hookEnv must have a NUL
// case in nulSubmissions (which TestReplyWithNULIsRefused proves is refused).
// A new field or variable added to the hook environment without a refusal
// fails here.
func TestHookEnvValuesCannotCarryNUL(t *testing.T) {
	covered := map[string]bool{}
	for _, c := range nulSubmissions() {
		covered[c.field] = true
	}
	var rep Reply
	rv := reflect.ValueOf(&rep).Elem()
	sentinels := map[string]string{}
	for i := 0; i < rv.NumField(); i++ {
		if rv.Field(i).Kind() != reflect.String {
			continue
		}
		name := rv.Type().Field(i).Name
		s := "sentinel-" + name + "-"
		rv.Field(i).SetString(s)
		sentinels[name] = s
	}
	for _, kv := range hookEnv(rep) {
		reached := false
		for name, s := range sentinels {
			if strings.Contains(kv, s) {
				reached = true
				if !covered[name] {
					t.Errorf("Reply.%s reaches the hook env (%s) but no NUL submission case covers it", name, kv)
				}
			}
		}
		val := kv[strings.IndexByte(kv, '=')+1:]
		if !reached && val != "0" {
			t.Errorf("hook env %q is not traced to a Reply field; add it to this test", kv)
		}
	}
}
