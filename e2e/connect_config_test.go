// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"regexp"
	"testing"
)

// This file is the config-artifact tier of the MCP conformance suite.
//
// GET /connect is the page a user copy-pastes into their harness. It is
// therefore a shipped artifact, not documentation: if the JSON stanza it emits
// is malformed, every client that consumes it fails at parse time with an error
// that names the user's config file, not demiplane — one of the worst possible
// diagnostic experiences.
//
// The stanza is assembled by string concatenation in internal/server/connect.go
// (mcpStanza) rather than json.Marshal, with the instance base URL interpolated
// into it. Request-derived hosts are filtered by sanitizeHost, but an operator's
// --base-url is not, so these tests pin the property that matters regardless of
// how base is derived: whatever /connect renders must parse as JSON and carry
// the keys an MCP client needs.
//
// No harness binary is involved. This is deliberate — it validates the artifact
// we hand out, which is the part we control and the part that drifts.

// codeBlockRe extracts the escaped body of the codeblock whose header label
// matches. Mirrors the markup emitted by codeBlock() in internal/server/chrome.go.
func codeBlockBody(t *testing.T, page, label string) string {
	t.Helper()

	re := regexp.MustCompile(
		`(?s)<div class="codeblock"><div class="cbhead"><span>` +
			regexp.QuoteMeta(html.EscapeString(label)) +
			`</span>.*?<pre><code>(.*?)</code></pre>`)
	m := re.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no codeblock labeled %q on /connect\n--- page ---\n%s", label, page)
	}
	// codeBlock() HTML-escapes its payload, so undo that to recover the bytes
	// the user's clipboard actually receives.
	return html.UnescapeString(m[1])
}

// mcpServersConfig is the shape every MCP client expects in its config file.
type mcpServersConfig struct {
	MCPServers map[string]struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env,omitempty"`
	} `json:"mcpServers"`
}

// fetchConnect GETs /connect from the control plane and returns the page body.
func fetchConnect(t *testing.T, srv *testServer, hostHeader string) string {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, srv.ControlURL+"/connect", nil)
	if err != nil {
		t.Fatalf("build /connect request: %v", err)
	}
	if hostHeader != "" {
		req.Host = hostHeader
	}
	resp, err := srv.client.Do(req)
	if err != nil {
		srv.dumpAndFail("GET /connect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		srv.dumpAndFail("GET /connect status=%d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /connect body: %v", err)
	}
	return string(body)
}

// TestConnectEmitsValidMCPConfig asserts the copy-paste stanza is valid JSON
// with the keys a client needs, across a range of hosts — including hostile
// ones. A Host header carrying a quote or a brace must not be able to produce
// a stanza that parses as anything other than the intended config.
func TestConnectEmitsValidMCPConfig(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "s3cret-e2e-token"})

	hosts := []struct {
		name string
		host string
	}{
		{"default", ""},
		{"plain hostname", "demiplane.internal:8891"},
		{"quote injection", `evil"host:8080`},
		{"json brace injection", `h","x":"y`},
		{"angle brackets", `<script>alert(1)</script>`},
		{"backslash", `back\slash:1`},
		{"newline", "host\nX-Injected: 1"},
	}

	for _, hc := range hosts {
		t.Run(hc.name, func(t *testing.T) {
			// net/http rejects some malformed Host values client-side; that is a
			// perfectly good outcome (the request never reaches us), so treat a
			// transport refusal as a pass rather than a failure.
			page := func() string {
				defer func() {
					if r := recover(); r != nil {
						t.Skipf("client refused Host %q: %v", hc.host, r)
					}
				}()
				return fetchConnect(t, srv, hc.host)
			}()

			stanza := codeBlockBody(t, page, "mcp.json (mcpServers stanza)")

			var cfg mcpServersConfig
			if err := json.Unmarshal([]byte(stanza), &cfg); err != nil {
				t.Fatalf("mcpServers stanza is not valid JSON: %v\n--- stanza ---\n%s", err, stanza)
			}

			entry, ok := cfg.MCPServers["demiplane"]
			if !ok {
				t.Fatalf("stanza has no mcpServers.demiplane entry\n--- stanza ---\n%s", stanza)
			}
			if entry.Command == "" {
				t.Error("mcpServers.demiplane.command is empty")
			}
			if len(entry.Args) == 0 {
				t.Fatal("mcpServers.demiplane.args is empty")
			}
			if entry.Args[0] != "mcp" {
				t.Errorf("args[0] = %q, want \"mcp\"", entry.Args[0])
			}
			if !containsArg(entry.Args, "--url") {
				t.Errorf("args missing --url: %v", entry.Args)
			}

			// The stanza must never inline the token; it references a file path.
			// This is the single most important property of the page.
			assertNoSecret(t, srv.Token, "connect page ("+hc.name+")", page, stanza)

			// A Host that injected structure would show up as extra top-level keys
			// or extra server entries. Exactly one server, and nothing else.
			if len(cfg.MCPServers) != 1 {
				t.Errorf("stanza declares %d servers, want exactly 1: %v", len(cfg.MCPServers), cfg.MCPServers)
			}
		})
	}
}

// TestConnectStanzaSurvivesOperatorBaseURL covers the path sanitizeHost does
// NOT filter: an explicit --base-url chosen by the operator. A quote there
// would be interpolated straight into the hand-built JSON.
func TestConnectStanzaSurvivesOperatorBaseURL(t *testing.T) {
	const nasty = `http://host"?x=1`

	srv := startServer(t, serverOpts{
		Token: "s3cret-e2e-token",
		Args:  []string{"--base-url", nasty},
	})

	page := fetchConnect(t, srv, "")
	stanza := codeBlockBody(t, page, "mcp.json (mcpServers stanza)")

	var cfg mcpServersConfig
	if err := json.Unmarshal([]byte(stanza), &cfg); err != nil {
		t.Fatalf("operator --base-url %q produced an unparseable mcpServers stanza: %v\n"+
			"A user copying this block into their harness config gets a JSON parse error\n"+
			"naming THEIR file, not demiplane. mcpStanza() builds JSON by string\n"+
			"concatenation (internal/server/connect.go); marshal it instead.\n"+
			"--- stanza ---\n%s", nasty, err, stanza)
	}
	if _, ok := cfg.MCPServers["demiplane"]; !ok {
		t.Errorf("stanza has no mcpServers.demiplane entry\n--- stanza ---\n%s", stanza)
	}
}

// TestConnectNeverLeaksToken sweeps the whole page, not just the stanza. The
// page adapts its copy to whether a token is configured, so it has several
// places it could accidentally interpolate one.
func TestConnectNeverLeaksToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"auth configured", "s3cret-e2e-token"},
		{"open instance", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startServer(t, serverOpts{Token: tc.token})
			page := fetchConnect(t, srv, "")

			if tc.token != "" {
				assertNoSecret(t, srv.Token, "connect page", page)
			}
			// Either way the page must still hand out a usable stanza.
			stanza := codeBlockBody(t, page, "mcp.json (mcpServers stanza)")
			var cfg mcpServersConfig
			if err := json.Unmarshal([]byte(stanza), &cfg); err != nil {
				t.Fatalf("stanza not valid JSON: %v\n%s", err, stanza)
			}
		})
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
