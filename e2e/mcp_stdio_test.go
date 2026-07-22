// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file is the stdio-hygiene tier of the MCP conformance suite.
//
// An MCP stdio server's stdout is a framed JSON-RPC channel and nothing else.
// A single stray byte on it — a log line, a banner, a progress indicator, a
// panic trace, a stray fmt.Println left behind in a debug session — desyncs the
// client's parser and kills the session. Clients report this as "the MCP server
// failed to start" with no useful detail, so it is expensive to diagnose in the
// field and nearly free to catch here.
//
// The tests below therefore do not check that demiplane answers correctly (the
// in-process tests in internal/mcp cover semantics, and grid_test.go covers the
// round trip). They check the far narrower property that EVERY byte the real
// subprocess writes to stdout parses as a JSON-RPC message — while the process
// is pushed into the states most likely to make a server want to talk: an
// unreachable backend, a rejected token, malformed input, an oversized body,
// and an unknown method.
//
// Diagnostics belong on stderr; these tests assert the split, not silence.

// mcpRaw runs `demiplane mcp` with the given args, feeds it the given lines on
// stdin, closes stdin, and returns everything it wrote to stdout and stderr.
//
// Unlike startMCP it performs no handshake and asserts nothing about content —
// callers inspect the raw streams. The child is given a hard deadline so a hung
// server fails the test instead of hanging the suite.
func mcpRaw(t *testing.T, args []string, stdinLines []string) (stdout, stderr string) {
	t.Helper()

	cmd := exec.Command(binPath, append([]string{"mcp"}, args...)...)
	cmd.Dir = repoRoot
	// Neutralize ambient config so the test controls the whole environment: a
	// developer's real DEMIPLANE_TOKEN/URL must not change what this asserts.
	cmd.Env = append(os.Environ(),
		"DEMIPLANE_TOKEN=",
		"DEMIPLANE_URL=",
		"XDG_CONFIG_HOME="+t.TempDir(),
	)

	cmd.Stdin = strings.NewReader(strings.Join(stdinLines, "\n") + "\n")
	var outBuf, errBuf syncBuffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		t.Fatalf("start demiplane mcp: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		// Exit status is deliberately not asserted: a server that closes on EOF
		// and one that exits non-zero after a transport error are both fine, and
		// pinning it here would make this test about lifecycle, not hygiene.
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("demiplane mcp did not exit after stdin closed\n--- stdout ---\n%s\n--- stderr ---\n%s",
			outBuf.String(), errBuf.String())
	}
	return outBuf.String(), errBuf.String()
}

// assertStdoutIsPureJSONRPC fails if any non-empty stdout line is not a
// JSON-RPC object. It reports the offending line verbatim, because the whole
// value of this check is naming the byte that broke the channel.
func assertStdoutIsPureJSONRPC(t *testing.T, stdout, context string) {
	t.Helper()

	sc := bufio.NewScanner(strings.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var seen int
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Errorf("%s: stdout line %d is not JSON (%v)\n  line: %q\n  full stdout:\n%s",
				context, lineNo, err, truncate(line, 400), stdout)
			continue
		}
		if v, ok := msg["jsonrpc"]; !ok || v != "2.0" {
			t.Errorf("%s: stdout line %d is JSON but not JSON-RPC 2.0 (jsonrpc=%v)\n  line: %q",
				context, lineNo, msg["jsonrpc"], truncate(line, 400))
			continue
		}
		seen++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("%s: scan stdout: %v", context, err)
	}
	if seen == 0 {
		t.Errorf("%s: no JSON-RPC messages on stdout at all\n  stderr:\n%s", context, stdout)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// initLine is a minimal, valid initialize request.
const initLine = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}`

// TestMCPStdoutStaysPureJSONRPC drives the real subprocess through the states
// most likely to provoke stray output and asserts stdout stays a clean
// JSON-RPC channel in every one.
func TestMCPStdoutStaysPureJSONRPC(t *testing.T) {
	// A control plane that exists, so the "healthy" leg is genuinely healthy.
	srv := startServer(t, serverOpts{Token: "s3cret-e2e-token"})

	// An address nothing is listening on, for the unreachable-backend leg.
	deadPort := freePort(t)
	deadURL := "http://127.0.0.1:" + strconv.Itoa(deadPort)

	badTokenFile := filepath.Join(t.TempDir(), "wrong-token")
	if err := os.WriteFile(badTokenFile, []byte("not-the-right-token\n"), 0o600); err != nil {
		t.Fatalf("write bad token file: %v", err)
	}

	publishCall := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":` +
		`{"name":"publish","arguments":{"content":"<h1>hi</h1>"}}}`

	cases := []struct {
		name  string
		args  []string
		lines []string
	}{
		{
			name:  "healthy instance",
			args:  []string{"--url", srv.ControlURL, "--content-url", srv.ContentURL, "--token-file", srv.TokenFile},
			lines: []string{initLine, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, publishCall},
		},
		{
			// The backend is gone. A naive implementation logs the dial error to
			// stdout and desyncs the client; the error belongs in a JSON-RPC
			// error response (or stderr), never as bare text on stdout.
			name:  "unreachable backend",
			args:  []string{"--url", deadURL, "--content-url", deadURL},
			lines: []string{initLine, publishCall},
		},
		{
			// A rejected bearer token must surface as a JSON-RPC error, and must
			// not print the token or the upstream body to stdout.
			name:  "rejected token",
			args:  []string{"--url", srv.ControlURL, "--content-url", srv.ContentURL, "--token-file", badTokenFile},
			lines: []string{initLine, publishCall},
		},
		{
			// Malformed frames must produce a JSON-RPC parse error, not a panic
			// trace and not silence-then-desync. The trailing valid call proves
			// the server is still speaking protocol afterwards.
			name: "malformed frames then a valid call",
			args: []string{"--url", srv.ControlURL, "--content-url", srv.ContentURL, "--token-file", srv.TokenFile},
			lines: []string{
				`{not json at all`,
				``,
				`[]`,
				`{"jsonrpc":"1.0","id":9,"method":"initialize"}`,
				initLine,
				publishCall,
			},
		},
		{
			name: "unknown method and unknown tool",
			args: []string{"--url", srv.ControlURL, "--content-url", srv.ContentURL, "--token-file", srv.TokenFile},
			lines: []string{
				initLine,
				`{"jsonrpc":"2.0","id":3,"method":"no/such/method"}`,
				`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope","arguments":{}}}`,
			},
		},
		{
			// A large body exercises buffered-writer flush paths, where partial
			// writes and interleaved logging tend to show up.
			name: "oversized publish body",
			args: []string{"--url", srv.ControlURL, "--content-url", srv.ContentURL, "--token-file", srv.TokenFile},
			lines: []string{
				initLine,
				`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"publish","arguments":{"content":"` +
					strings.Repeat("A", 512*1024) + `"}}}`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr := mcpRaw(t, tc.args, tc.lines)
			assertStdoutIsPureJSONRPC(t, stdout, tc.name)

			// The token must never appear on either stream. assertNoSecret is the
			// suite's existing sweep; reuse it so this stays consistent with the
			// leak checks elsewhere.
			assertNoSecret(t, srv.Token, "mcp stdout/stderr ("+tc.name+")", stdout, stderr)
		})
	}
}

// TestMCPNotificationsAreSilent pins the JSON-RPC rule that notifications (no
// id) get no response. A server that answers them puts an unmatched message on
// the channel, which strict clients treat as a protocol violation.
func TestMCPNotificationsAreSilent(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "s3cret-e2e-token"})

	stdout, _ := mcpRaw(t,
		[]string{"--url", srv.ControlURL, "--content-url", srv.ContentURL, "--token-file", srv.TokenFile},
		[]string{
			initLine,
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		})

	assertStdoutIsPureJSONRPC(t, stdout, "notifications")

	// Exactly one response: the initialize reply. Both notifications are silent.
	var responses int
	sc := bufio.NewScanner(strings.NewReader(stdout))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			responses++
		}
	}
	if responses != 1 {
		t.Errorf("got %d messages on stdout, want exactly 1 (the initialize reply)\nstdout:\n%s",
			responses, stdout)
	}
}

// TestMCPToolSchemasAreValid asserts every advertised tool carries an
// inputSchema that is a JSON Schema object with a "type" — the shape strict
// clients validate before they will expose a tool. A tool whose schema is
// missing or malformed is silently dropped by some clients, which presents to
// the user as "demiplane connected but has no tools".
func TestMCPToolSchemasAreValid(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "s3cret-e2e-token"})
	mcp := startMCP(t, srv.ControlURL, srv.ContentURL, srv.TokenFile)

	raw, err := mcp.call("tools/list", nil)
	if err != nil {
		mcp.dumpAndFail("tools/list: %v", err)
	}

	var result struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		mcp.dumpAndFail("tools/list: decode: %v", err)
	}
	if len(result.Tools) == 0 {
		t.Fatal("tools/list returned no tools")
	}

	// The four tools demiplane documents as its MCP surface.
	want := map[string]bool{"publish": false, "list": false, "get": false, "delete": false}

	for _, tool := range result.Tools {
		if tool.Name == "" {
			t.Error("a tool has an empty name")
			continue
		}
		if _, known := want[tool.Name]; known {
			want[tool.Name] = true
		}
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("tool %q has an empty description (clients show this to the model)", tool.Name)
		}
		if len(tool.InputSchema) == 0 {
			t.Errorf("tool %q has no inputSchema", tool.Name)
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Errorf("tool %q inputSchema is not a JSON object: %v", tool.Name, err)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("tool %q inputSchema.type = %v, want \"object\"", tool.Name, schema["type"])
		}
		if _, ok := schema["properties"]; !ok {
			t.Errorf("tool %q inputSchema has no properties block", tool.Name)
		}
	}

	for name, found := range want {
		if !found {
			t.Errorf("documented tool %q is missing from tools/list", name)
		}
	}
}
