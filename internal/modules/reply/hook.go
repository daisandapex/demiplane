// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync"
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
// Dispatch is asynchronous through a bounded pool: hookWorkers events run at
// once and at most hookQueue more wait. A submission takes its place in the
// pool BEFORE the reply is stored, so a full pool refuses the submission (503,
// nothing recorded) instead of storing a reply whose hook would be dropped or
// queued without bound. Once admitted, a hook can never fail, slow, or roll
// back the reply write; failures are logged, never surfaced to the replier.
//
// An exec hook runs in its own process group. When the shell exits, or the
// timeout passes, the whole group is killed, so a stray background child
// cannot outlive its hook; work meant to outlive the hook must detach (for
// example with systemd-run or setsid). Hook output is read while it streams
// and only the first hookLogOutputCap bytes are kept, for the failure log.

const (
	// defaultHookTimeout bounds one exec'd hook run. Generous enough for a
	// grading run; a hook that wants to work longer should enqueue/detach and
	// exit.
	defaultHookTimeout = 5 * time.Minute
	// defaultHookWorkers is how many hook events run at once.
	defaultHookWorkers = 2
	// defaultHookQueue is how many admitted events may wait for a worker.
	defaultHookQueue = 32
	// hookHTTPTimeout bounds one webhook POST end to end.
	hookHTTPTimeout = 30 * time.Second
	// hookLogOutputCap bounds how much hook output is kept (for an error log
	// line); the rest is read and discarded.
	hookLogOutputCap = 512
	// hookPipeGrace is how long, after the hook's process group is killed, the
	// output reader waits for the pipe to close before abandoning it (a
	// descendant that left the group can hold it open).
	hookPipeGrace = 2 * time.Second
)

// hookClient is the shared webhook HTTP client (bounded, reused connections).
var hookClient = &http.Client{Timeout: hookHTTPTimeout}

// hookConfig is the resolved reply-event hook configuration.
type hookConfig struct {
	execCmd string        // command line for /bin/sh -c; "" = no exec action
	url     string        // webhook POST target; "" = no webhook action
	timeout time.Duration // exec bound; 0 = defaultHookTimeout
	workers int           // concurrent events; 0 = defaultHookWorkers
	queue   int           // admitted events waiting for a worker
}

func (h hookConfig) enabled() bool { return h.execCmd != "" || h.url != "" }

func (h hookConfig) execTimeout() time.Duration {
	if h.timeout > 0 {
		return h.timeout
	}
	return defaultHookTimeout
}

func (h hookConfig) poolSize() (workers, queue int) {
	workers, queue = h.workers, h.queue
	if workers <= 0 {
		workers = defaultHookWorkers
	}
	if queue < 0 {
		queue = 0
	}
	return workers, queue
}

// HookOptions are the reply_hook_* config keys beyond the two actions.
type HookOptions struct {
	Timeout time.Duration // reply_hook_timeout: bound on one exec run
	Workers int           // reply_hook_workers: events run at once
	Queue   int           // reply_hook_queue: admitted events waiting for a worker
}

// DefaultHookOptions returns the defaults for unset reply_hook_* keys.
func DefaultHookOptions() HookOptions {
	return HookOptions{Timeout: defaultHookTimeout, Workers: defaultHookWorkers, Queue: defaultHookQueue}
}

// ConfigureHook sets the reply-event hook on the registered module (the
// package singleton the server mounts). Called at startup by the build-tagged
// config wiring in cmd/demiplane; a bad value is a hard startup error, per the
// config file's fail-loud contract. An empty action value disables that action.
func ConfigureHook(execCmd, webhookURL string, opts HookOptions) error {
	if webhookURL != "" {
		u, err := url.Parse(webhookURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("reply_hook_url %q: must be an absolute http(s) URL", webhookURL)
		}
	}
	if opts.Timeout <= 0 {
		return errors.New("reply_hook_timeout: must be a positive duration")
	}
	if opts.Workers < 1 || opts.Workers > 64 {
		return errors.New("reply_hook_workers: must be between 1 and 64")
	}
	if opts.Queue < 0 || opts.Queue > 4096 {
		return errors.New("reply_hook_queue: must be between 0 and 4096")
	}
	std.hook = hookConfig{execCmd: execCmd, url: webhookURL,
		timeout: opts.Timeout, workers: opts.Workers, queue: opts.Queue}
	return nil
}

// hookPool runs hook events on a fixed set of workers behind a bounded queue.
// tickets holds one token per admitted, unfinished event (running or
// queued), so admission is a non-blocking send and the jobs channel, sized to
// match, never blocks.
type hookPool struct {
	tickets chan struct{}
	jobs    chan hookJob
}

type hookJob struct {
	h       hookConfig
	rep     Reply
	payload []byte
	done    func(Reply)
}

func newHookPool(workers, queue int) *hookPool {
	p := &hookPool{
		tickets: make(chan struct{}, workers+queue),
		jobs:    make(chan hookJob, workers+queue),
	}
	for i := 0; i < workers; i++ {
		go p.work()
	}
	return p
}

func (p *hookPool) work() {
	for j := range p.jobs {
		runHook(j.h, j.rep, j.payload)
		<-p.tickets
		if j.done != nil {
			j.done(j.rep)
		}
	}
}

// hookTicket is an admitted place in the pool. Exactly one of fire or release
// must be called.
type hookTicket struct {
	m    *Module
	pool *hookPool
	once sync.Once
}

// reserveHook takes a place in the hook pool for a reply about to be stored.
// It returns a nil ticket (and true) when no hook is configured, and false
// when the pool is full.
func (m *Module) reserveHook() (*hookTicket, bool) {
	if !m.hook.enabled() {
		return nil, true
	}
	m.poolOnce.Do(func() { m.pool = newHookPool(m.hook.poolSize()) })
	select {
	case m.pool.tickets <- struct{}{}:
		return &hookTicket{m: m, pool: m.pool}, true
	default:
		return nil, false
	}
}

// release gives back a ticket whose reply was not stored.
func (t *hookTicket) release() {
	if t == nil {
		return
	}
	t.once.Do(func() { <-t.pool.tickets })
}

// fire queues the hook for a just-recorded reply. It is called only AFTER a
// durable insert and never blocks: the ticket already holds a queue place.
func (t *hookTicket) fire(rep Reply) {
	if t == nil {
		return
	}
	t.once.Do(func() {
		payload, err := json.Marshal(rep)
		if err != nil { // structurally impossible for Reply; belt-and-braces
			log.Printf("reply hook: marshal event: %v", err)
			<-t.pool.tickets
			return
		}
		t.pool.jobs <- hookJob{h: t.m.hook, rep: rep, payload: payload, done: t.m.hookDone}
	})
}

// runHook runs the configured actions for one event, exec first.
func runHook(h hookConfig, rep Reply, payload []byte) {
	if h.execCmd != "" {
		runHookExec(h.execCmd, h.execTimeout(), rep, payload)
	}
	if h.url != "" {
		runHookWebhook(h.url, payload)
	}
}

// runHookExec runs the exec action: `/bin/sh -c <cmdline>` with the reply JSON
// on stdin and DEMIPLANE_REPLY_{ID,SLUG,KIND,BODY} in the environment, in its
// own process group. Errors (spawn failure, non-zero exit, timeout) are logged
// with truncated output.
func runHookExec(cmdline string, timeout time.Duration, rep Reply, payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdline)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = append(os.Environ(),
		"DEMIPLANE_REPLY_ID="+strconv.FormatInt(rep.ID, 10),
		"DEMIPLANE_REPLY_SLUG="+rep.Slug,
		"DEMIPLANE_REPLY_KIND="+rep.Kind,
		"DEMIPLANE_REPLY_BODY="+rep.Body,
	)
	// The child writes to a pipe this process reads while it streams, keeping
	// only the first hookLogOutputCap bytes, so a verbose hook costs no memory.
	// An *os.File (not an io.Writer) means Wait returns when the shell exits,
	// not when every descendant has closed the pipe.
	pr, pw, err := os.Pipe()
	if err != nil {
		log.Printf("reply hook exec failed (reply %d): pipe: %v", rep.ID, err)
		return
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	// Stdin is copied by an exec goroutine that Wait also waits for; a
	// descendant holding stdin open without reading it would block that copy,
	// and with it Wait, after the shell has exited. WaitDelay bounds that wait.
	cmd.WaitDelay = hookPipeGrace
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		log.Printf("reply hook exec failed (reply %d): %v", rep.ID, err)
		return
	}
	pw.Close()
	out := &capWriter{max: hookLogOutputCap}
	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, pr)
		close(copied)
	}()
	werr := cmd.Wait()
	killProcessGroup(cmd)
	if errors.Is(werr, exec.ErrWaitDelay) {
		werr = nil // the shell exited cleanly; only its leftover I/O was cut
	}
	select {
	case <-copied:
	case <-time.After(hookPipeGrace):
	}
	pr.Close()
	<-copied
	if werr != nil {
		if ctx.Err() != nil {
			werr = fmt.Errorf("timed out after %v: %w", timeout, werr)
		}
		log.Printf("reply hook exec failed (reply %d): %v; output: %s",
			rep.ID, werr, out.bytes())
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

// capWriter keeps the first max bytes written and discards the rest, always
// reporting a full write so the producer is never blocked or signalled.
type capWriter struct {
	mu        sync.Mutex
	max       int
	buf       []byte
	truncated bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := c.max - len(c.buf); room > 0 {
		if len(p) > room {
			c.buf = append(c.buf, p[:room]...)
			c.truncated = true
		} else {
			c.buf = append(c.buf, p...)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

// bytes returns the kept output, marked when more was discarded.
func (c *capWriter) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := bytes.Clone(c.buf)
	if c.truncated {
		out = append(out, []byte("…")...)
	}
	return out
}
