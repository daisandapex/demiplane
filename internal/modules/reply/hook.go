// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// The reply-event hook fires a configurable action whenever a reply is durably
// recorded (both the control-plane POST /reply/{slug} and the content-plane
// POST /answer/{slug} paths), so an external agent — e.g. a Professor that
// grades a student's answer and publishes the next lesson — reacts to the event
// with zero polling. Two actions, individually optional and both fired when
// both are set:
//
//   - exec:    reply_hook_exec — a command line run via `/bin/sh -c`, given the
//     reply as JSON on stdin and as DEMIPLANE_REPLY_* env vars.
//   - webhook: reply_hook_url — the reply POSTed as JSON to an http(s) URL.
//
// Dispatch is fire-and-forget in a goroutine: a hook can never fail, slow, or
// roll back the reply write — by the time it fires the row is already stored
// and the viewer's honest confirmation is independent of hook fate. Failures
// are logged, never surfaced to the replier.

const (
	// hookExecTimeout bounds one exec'd hook run. Generous enough for a grading
	// run; a hook that wants to work longer should enqueue/detach and exit.
	hookExecTimeout = 5 * time.Minute
	// hookHTTPTimeout bounds one webhook POST end to end.
	hookHTTPTimeout = 30 * time.Second
	// hookLogOutputCap bounds how much hook output an error log line carries.
	hookLogOutputCap = 512
)

// hookClient is the shared webhook HTTP client (bounded, reused connections).
var hookClient = &http.Client{Timeout: hookHTTPTimeout}

// hookConfig is the resolved reply-event hook configuration.
type hookConfig struct {
	execCmd string // command line for /bin/sh -c; "" = no exec action
	url     string // webhook POST target; "" = no webhook action
}

func (h hookConfig) enabled() bool { return h.execCmd != "" || h.url != "" }

// ConfigureHook sets the reply-event hook on the registered module (the
// package singleton the server mounts). Called at startup by the build-tagged
// config wiring in cmd/demiplane; a bad value is a hard startup error, per the
// config file's fail-loud contract. An empty value disables that action.
func ConfigureHook(execCmd, webhookURL string) error {
	if webhookURL != "" {
		u, err := url.Parse(webhookURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("reply_hook_url %q: must be an absolute http(s) URL", webhookURL)
		}
	}
	std.hook = hookConfig{execCmd: execCmd, url: webhookURL}
	return nil
}

// fireHook dispatches the configured hook for a just-recorded reply,
// asynchronously. It is called only AFTER a durable insert; it never blocks or
// fails the reply write. m.hookDone is a test seam invoked when dispatch
// finishes (nil in production).
func (m *Module) fireHook(rep Reply) {
	h := m.hook
	if !h.enabled() {
		return
	}
	payload, err := json.Marshal(rep)
	if err != nil { // structurally impossible for Reply; belt-and-braces
		log.Printf("reply hook: marshal event: %v", err)
		return
	}
	done := m.hookDone
	go func() {
		if h.execCmd != "" {
			runHookExec(h.execCmd, rep, payload)
		}
		if h.url != "" {
			runHookWebhook(h.url, payload)
		}
		if done != nil {
			done(rep)
		}
	}()
}

// runHookExec runs the exec action: `/bin/sh -c <cmdline>` with the reply JSON
// on stdin and DEMIPLANE_REPLY_{ID,SLUG,KIND,BODY} in the environment. Errors
// (spawn failure, non-zero exit, timeout) are logged with truncated output.
func runHookExec(cmdline string, rep Reply, payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), hookExecTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdline)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = append(os.Environ(),
		"DEMIPLANE_REPLY_ID="+strconv.FormatInt(rep.ID, 10),
		"DEMIPLANE_REPLY_SLUG="+rep.Slug,
		"DEMIPLANE_REPLY_KIND="+rep.Kind,
		"DEMIPLANE_REPLY_BODY="+rep.Body,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("reply hook exec failed (reply %d): %v; output: %s",
			rep.ID, err, truncateForLog(out))
	}
}

// runHookWebhook runs the webhook action: POST the reply JSON to url. A
// transport error or non-2xx status is logged; the response body is discarded.
func runHookWebhook(url string, payload []byte) {
	resp, err := hookClient.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("reply hook webhook failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		log.Printf("reply hook webhook: %s returned %d", url, resp.StatusCode)
	}
}

// truncateForLog bounds hook output for an error log line.
func truncateForLog(b []byte) []byte {
	if len(b) > hookLogOutputCap {
		return append(bytes.Clone(b[:hookLogOutputCap]), []byte("…")...)
	}
	return b
}
