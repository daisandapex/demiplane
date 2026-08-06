// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// syncBuffer is a concurrency-safe io.Writer + String(), used to capture a
// subprocess's stderr/stdout so a test failure can print what the process
// actually said, instead of a bare "exit status 1".
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// freePort asks the OS for an unused TCP port on loopback and immediately
// releases it. There is an inherent (tiny) TOCTOU window between release and
// the child binding it — acceptable for a test harness, and the standard way
// stdlib itself allocates ephemeral ports in its own tests.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitTCP polls until a TCP connection to addr succeeds or the deadline
// passes, so callers never blind-sleep waiting for a listener to come up.
func waitTCP(t *testing.T, addr string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("waitTCP %s: timed out after %s: %w", addr, timeout, lastErr)
}

// waitHTTP200 polls url with GET until it returns any HTTP response (status
// doesn't matter — a response means the handler chain is live) or the
// deadline passes. Used as the serve-readiness gate: the landing page always
// answers GET / regardless of --browse, so a response there proves the
// control-plane mux is wired up, not just that the listener socket exists.
func waitHTTP200(t *testing.T, url string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("waitHTTP200 %s: timed out after %s: %w", url, timeout, lastErr)
}

// serverOpts configures a testServer.
type serverOpts struct {
	Token  string // empty = no auth token configured (open instance)
	Browse bool
	Args   []string // extra CLI args appended verbatim
}

// testServer wraps a real `demiplane serve` subprocess bound to two ephemeral
// loopback ports (control + content, per ADR 0003 origin isolation).
type testServer struct {
	t          *testing.T
	cmd        *exec.Cmd
	ControlURL string
	ContentURL string
	StoreDir   string
	TokenFile  string
	Token      string
	stderr     *syncBuffer
	client     *http.Client
	stopOnce   sync.Once
	// waitErr is fed exactly once, by the single goroutine that calls
	// cmd.Wait() (see the comment on that goroutine below). Every other
	// consumer — readiness detection, Stop() — reads THIS channel instead of
	// calling cmd.Wait() a second time: os/exec explicitly does not support
	// concurrent Wait() calls on the same *Cmd, and an earlier version of this
	// harness that called it from two goroutines (readiness racing + Stop())
	// deadlocked the suite (both callers blocked in Cmd.awaitGoroutines
	// forever) — caught by actually running the suite, not just reading it.
	waitErr chan error
}

// startServer builds the CLI invocation, starts the subprocess, and blocks
// until the control AND content planes are answering HTTP — or fails the test
// with the captured stderr attached, so a startup failure is diagnosable
// without re-running under -v.
func startServer(t *testing.T, opts serverOpts) *testServer {
	t.Helper()
	return startServerWithStoreDir(t, t.TempDir(), opts)
}

// startServerWithStoreDir is startServer with an explicit, caller-owned store
// directory instead of a fresh t.TempDir() — used by the SSH test to point a
// verification server at the same store an SSH `receive` just wrote into.
func startServerWithStoreDir(t *testing.T, storeDir string, opts serverOpts) *testServer {
	t.Helper()

	controlPort := freePort(t)
	contentPort := freePort(t)
	for contentPort == controlPort { // vanishingly unlikely, but be certain
		contentPort = freePort(t)
	}
	controlURL := "http://127.0.0.1:" + strconv.Itoa(controlPort)
	contentURL := "http://127.0.0.1:" + strconv.Itoa(contentPort)

	xdg := t.TempDir() // hermetic config: never read a developer's real ~/.config/demiplane

	args := []string{
		"serve",
		"--bind", "127.0.0.1:" + strconv.Itoa(controlPort),
		"--content-bind", "127.0.0.1:" + strconv.Itoa(contentPort),
		"--base-url", controlURL,
		"--content-base-url", contentURL,
		"--store", storeDir,
		"--sweep-interval", "200ms",
	}

	tokenFile := ""
	if opts.Token != "" {
		tokenFile = filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(tokenFile, []byte(opts.Token), 0o600); err != nil {
			t.Fatalf("write token file: %v", err)
		}
		args = append(args, "--token-file", tokenFile)
	}
	if opts.Browse {
		args = append(args, "--browse")
	}
	args = append(args, opts.Args...)

	cmd := exec.Command(binPath, args...)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+xdg,
		"DEMIPLANE_TOKEN=", // never inherit a dev's env token
	)
	stderr := &syncBuffer{}
	cmd.Stdout = stderr // startup banner + WARNING lines go to stderr, but be safe
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start demiplane serve: %v", err)
	}

	srv := &testServer{
		t:          t,
		cmd:        cmd,
		ControlURL: controlURL,
		ContentURL: contentURL,
		StoreDir:   storeDir,
		TokenFile:  tokenFile,
		Token:      opts.Token,
		stderr:     stderr,
		waitErr:    make(chan error, 1),
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}

	// The ONE and only cmd.Wait() call for this process's whole lifetime.
	// waitErr is buffered(1), so this goroutine never leaks even if nothing
	// ever reads it (a test that fails before Stop() is registered).
	go func() { srv.waitErr <- cmd.Wait() }()

	// Register cleanup BEFORE the readiness gate below, not after: a readiness
	// timeout calls dumpAndFail (t.Fatalf -> runtime.Goexit), which would skip
	// a t.Cleanup registered later in this function and leak the subprocess.
	t.Cleanup(srv.Stop)

	// Fail fast if the process exits before readiness (bad flags, port
	// collision the OS handed us anyway, etc.) instead of polling the full
	// timeout against a dead process.
	ready := make(chan error, 1)
	go func() {
		if err := waitHTTP200(t, controlURL+"/", 15*time.Second); err != nil {
			ready <- err
			return
		}
		ready <- waitTCP(t, "127.0.0.1:"+strconv.Itoa(contentPort), 5*time.Second)
	}()

	select {
	case err := <-ready:
		if err != nil {
			srv.dumpAndFail("server did not become ready: %v", err)
		}
	case err := <-srv.waitErr:
		// Put it back so Stop()/dumpAndFail's own read still finds it — this
		// IS the terminal wait result, just delivered early.
		srv.waitErr <- err
		srv.dumpAndFail("server exited during startup: %v", err)
		return srv
	case <-time.After(20 * time.Second):
		srv.dumpAndFail("server readiness timed out")
	}

	return srv
}

// dumpAndFail fails the test with the subprocess's captured stderr attached —
// the "diagnostic failures" requirement: a contributor should never have to
// re-run with extra flags to see why the server didn't come up.
func (s *testServer) dumpAndFail(format string, args ...any) {
	s.t.Helper()
	msg := fmt.Sprintf(format, args...)
	s.t.Fatalf("%s\n--- demiplane serve stderr ---\n%s\n--- end stderr ---", msg, s.stderr.String())
}

// Stop terminates the server gracefully (SIGTERM, matching the CLI's own
// signal.NotifyContext handling) and force-kills it if it doesn't exit
// promptly. Safe to call multiple times: after the first call drains
// s.waitErr, every subsequent call's read on the (now-empty, unbuffered-in-
// effect) channel would block forever, so a sync.Once guards the body.
func (s *testServer) Stop() {
	s.stopOnce.Do(func() {
		if s.cmd == nil || s.cmd.Process == nil {
			return
		}
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-s.waitErr:
		case <-time.After(5 * time.Second):
			_ = s.cmd.Process.Kill()
			<-s.waitErr
		}
	})
}

// Stderr returns everything the server has logged so far — for tests that
// want to assert on (or, critically, assert the ABSENCE of the token in)
// server-side diagnostics.
func (s *testServer) Stderr() string { return s.stderr.String() }

// --- small REST helpers used across the test files ---

type httpResult struct {
	Status int
	Header http.Header
	Body   []byte
}

func (s *testServer) do(t *testing.T, method, url string, body io.Reader, headers map[string]string) httpResult {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, url, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, url, err)
	}
	return httpResult{Status: resp.StatusCode, Header: resp.Header, Body: b}
}

// authHeader returns the Authorization header map for the server's configured
// token, or an empty map if the server is unauthenticated.
func (s *testServer) authHeader() map[string]string {
	if s.Token == "" {
		return map[string]string{}
	}
	return map[string]string{"Authorization": "Bearer " + s.Token}
}

// ============================================================================
// MCP subprocess driver: a real `demiplane mcp` process, spoken to over real
// stdin/stdout pipes with newline-delimited JSON-RPC 2.0. This is the seam the
// mission calls out as untested anywhere in the unit suite.
// ============================================================================

type mcpProc struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *syncBuffer
	nextID int
	mu     sync.Mutex // serializes request/response round trips (one in flight at a time)

	// transcript accumulates every raw line written to the child's stdin and
	// every raw line read from its stdout, for the token-leak sweep.
	transcript   syncBuffer
	transcriptMu sync.Mutex
}

// startMCP launches `demiplane mcp` pointed at the given server and performs
// the initialize handshake. content may be empty (defaults to the control
// origin, matching --unsafe-same-origin deployments); tests pass it
// explicitly since the harness always runs split-origin.
func startMCP(t *testing.T, controlURL, contentURL, tokenFile string) *mcpProc {
	t.Helper()

	args := []string{"mcp", "--url", controlURL, "--content-url", contentURL}
	if tokenFile != "" {
		args = append(args, "--token-file", tokenFile)
	}
	cmd := exec.Command(binPath, args...)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "DEMIPLANE_TOKEN=", "DEMIPLANE_URL=")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("mcp stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("mcp stdout pipe: %v", err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start demiplane mcp: %v", err)
	}

	m := &mcpProc{
		t:      t,
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReaderSize(stdout, 1<<20),
		stderr: stderr,
	}
	t.Cleanup(m.Stop)

	// initialize / notifications/initialized handshake, per MCP.
	initRes, err := m.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "demiplane-e2e", "version": "0"},
	})
	if err != nil {
		m.dumpAndFail("mcp initialize failed: %v", err)
	}
	var initResult struct {
		ServerInfo struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(initRes, &initResult); err != nil {
		m.dumpAndFail("mcp initialize: decode result: %v", err)
	}
	if initResult.ServerInfo.Name != "demiplane" {
		m.dumpAndFail("mcp initialize: unexpected serverInfo.name %q", initResult.ServerInfo.Name)
	}
	if err := m.notify("notifications/initialized", nil); err != nil {
		m.dumpAndFail("mcp notifications/initialized: %v", err)
	}
	return m
}

func (m *mcpProc) dumpAndFail(format string, args ...any) {
	m.t.Helper()
	msg := fmt.Sprintf(format, args...)
	m.t.Fatalf("%s\n--- demiplane mcp stderr ---\n%s\n--- end stderr ---\n--- transcript ---\n%s\n--- end transcript ---",
		msg, m.stderr.String(), m.Transcript())
}

func (m *mcpProc) Transcript() string {
	m.transcriptMu.Lock()
	defer m.transcriptMu.Unlock()
	return m.transcript.String()
}

func (m *mcpProc) logTranscript(dir string, line []byte) {
	m.transcriptMu.Lock()
	defer m.transcriptMu.Unlock()
	m.transcript.Write([]byte(dir + " "))
	m.transcript.Write(line)
	m.transcript.Write([]byte("\n"))
}

// rpcReq/rpcResp mirror internal/mcp/jsonrpc.go's wire shapes (unexported
// there, so redefined here — the e2e suite deliberately speaks the wire
// protocol as an outside client would, not by importing the package).
type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

// call sends a JSON-RPC request and returns the raw `result`, or an error
// wrapping the JSON-RPC error object (or a transport failure).
func (m *mcpProc) call(method string, params any) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextID++
	id := m.nextID
	req := rpcReq{JSONRPC: "2.0", ID: &id, Method: method, Params: params}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	m.logTranscript(">>>", line)
	if _, err := m.stdin.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	respLine, err := m.stdout.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read response: %w (stderr: %s)", err, m.stderr.String())
	}
	m.logTranscript("<<<", bytes.TrimRight(respLine, "\n"))

	var resp rpcResp
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return nil, fmt.Errorf("decode response %q: %w", respLine, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("jsonrpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// notify sends a JSON-RPC notification (no id — no response expected).
func (m *mcpProc) notify(method string, params any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	req := rpcReq{JSONRPC: "2.0", Method: method, Params: params}
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	m.logTranscript(">>>", line)
	_, err = m.stdin.Write(append(line, '\n'))
	return err
}

// toolResultText is the shape of a tools/call success result's content array.
type toolCallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// callTool invokes an MCP tool and returns its first text content block (every
// demiplane tool returns exactly one text block, success or error — see
// internal/mcp/tools.go's textResult).
func (m *mcpProc) callTool(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	raw, err := m.call("tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatalf("mcp tools/call %s: %v\n--- transcript ---\n%s", name, err, m.Transcript())
	}
	var res toolCallResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("mcp tools/call %s: decode result: %v", name, err)
	}
	if res.IsError {
		text := ""
		if len(res.Content) > 0 {
			text = res.Content[0].Text
		}
		t.Fatalf("mcp tools/call %s: tool reported an error: %s", name, text)
	}
	if len(res.Content) == 0 {
		t.Fatalf("mcp tools/call %s: empty content", name)
	}
	return res.Content[0].Text
}

// callToolExpectError invokes a tool expecting a JSON-RPC-level OR tool-level
// error, and returns the combined error text for assertion.
func (m *mcpProc) callToolExpectError(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	raw, err := m.call("tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return err.Error()
	}
	var res toolCallResult
	if jerr := json.Unmarshal(raw, &res); jerr != nil {
		t.Fatalf("mcp tools/call %s: decode result: %v", name, jerr)
	}
	if !res.IsError {
		t.Fatalf("mcp tools/call %s: expected an error, got success: %v", name, res)
	}
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].Text
}

func (m *mcpProc) Stop() {
	if m.cmd == nil || m.cmd.Process == nil {
		return
	}
	_ = m.stdin.Close() // EOF on stdin is the documented clean-shutdown signal
	done := make(chan struct{})
	go func() {
		m.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = m.cmd.Process.Kill()
		<-done
	}
}

// ============================================================================
// Cross-cutting assertions
// ============================================================================

// assertNoSecret fails the test if secret (the bearer token) appears anywhere
// in hay — used to check response bodies, headers, and MCP transcripts never
// leak the token, per invariant 4 (SECURITY.md) and the mission's explicit
// requirement.
func assertNoSecret(t *testing.T, secret, where string, hay ...string) {
	t.Helper()
	if secret == "" {
		return
	}
	for _, h := range hay {
		if strings.Contains(h, secret) {
			t.Fatalf("token leaked in %s", where)
		}
	}
}
