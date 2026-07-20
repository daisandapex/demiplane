// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "update golden files")

// getConnect fetches /connect from a server configured with the given base URL
// and (optional) auth token, returning the status and body.
func getConnect(t *testing.T, base, token string) (int, string, http.Header) {
	t.Helper()
	ts := newConfiguredServer(t, Config{BaseURL: base, AuthToken: token})
	resp, err := http.Get(ts.URL + "/connect")
	if err != nil {
		t.Fatalf("GET /connect: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

func TestConnectStatusAndType(t *testing.T) {
	status, body, hdr := getConnect(t, "https://demiplane.example", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", status, body)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if got := hdr.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestConnectContainsHarnessBlocks(t *testing.T) {
	_, body, _ := getConnect(t, "https://demiplane.example", "")

	// Every advertised harness must be named on the page. This list is limited to
	// paths we actually exercise (Claude Code via MCP and the hook, curl/CLI via
	// the REST and publish tests, Aider because it is only curl) plus the generic
	// MCP entry. Naming a harness here is a claim of support, so do not add one
	// without a test or a dogfooding path behind it.
	harnesses := []string{
		"Claude Code", "Any MCP client", "Aider", "Any shell / CI",
	}
	for _, h := range harnesses {
		if !strings.Contains(body, h) {
			t.Errorf("connect page missing harness %q", h)
		}
	}

	// Guard against the unverified per-harness claims creeping back in.
	for _, h := range []string{"Cursor", "Cline", "Windsurf", "Zed", "Continue"} {
		if strings.Contains(body, h) {
			t.Errorf("connect page names unverified harness %q; see the Verified column", h)
		}
	}

	// The five copy-paste mechanisms must all be present.
	wantSnippets := []string{
		"mcpServers",                     // MCP stanza (quotes are HTML-escaped in the block)
		"claude mcp add demiplane",       // Claude Code one-liner
		"companion/README.md",            // capture hook pointer
		"curl --data-binary @index.html", // bare curl
		"demiplane publish",              // CLI
		"/run curl",                      // aider shell harness
		"?render=md",                     // curl variation
		"X-Demiplane-Password",           // password recipe
	}
	for _, s := range wantSnippets {
		if !strings.Contains(body, s) {
			t.Errorf("connect page missing snippet %q", s)
		}
	}
}

func TestConnectTemplatesBaseURL(t *testing.T) {
	const base = "https://pub.internal.example"
	_, body, _ := getConnect(t, base, "")
	// The base URL must appear in the MCP stanza and the curl block.
	if !strings.Contains(body, base+"/publish") {
		t.Errorf("connect page missing publish URL %q", base+"/publish")
	}
	if strings.Count(body, base) < 3 {
		t.Errorf("expected base URL %q templated in multiple snippets, got %d occurrences",
			base, strings.Count(body, base))
	}
}

// TestConnectNeverEmitsToken is the load-bearing security assertion: the live
// bearer token MUST NOT appear anywhere in the page, only the token FILE PATH.
func TestConnectNeverEmitsToken(t *testing.T) {
	const secret = "SUPER-SECRET-BEARER-abc123XYZ"
	_, body, _ := getConnect(t, "https://demiplane.example", secret)
	if strings.Contains(body, secret) {
		t.Fatalf("connect page LEAKED the bearer token value")
	}
	// The placeholder path must be present so the user knows where the token lives.
	if !strings.Contains(body, tokenPath) {
		t.Errorf("connect page missing token file path %q", tokenPath)
	}
	// A configured instance should tell the user auth is required.
	if !strings.Contains(body, "requires a bearer token") {
		t.Errorf("auth-configured page should note the token requirement")
	}
}

// TestConnectOpenInstanceNoAuthHeader verifies the open-instance snippets do not
// inject an Authorization header (they work as-is with no token).
func TestConnectOpenInstanceNoAuthHeader(t *testing.T) {
	_, body, _ := getConnect(t, "https://demiplane.example", "")
	if strings.Contains(body, "Authorization: Bearer") {
		t.Errorf("open instance should not show a bearer header in snippets")
	}
	if !strings.Contains(body, "open (no bearer token configured)") {
		t.Errorf("open instance should state it is open")
	}
}

// TestConnectAuthInstanceShowsBearer verifies an auth instance instructs reading
// the token from the local file (never inlining a value).
func TestConnectAuthInstanceShowsBearer(t *testing.T) {
	_, body, _ := getConnect(t, "https://demiplane.example", "tok")
	if !strings.Contains(body, "Authorization: Bearer $(cat "+tokenPath+")") {
		t.Errorf("auth instance should read the bearer from the local token file")
	}
}

// TestConnectNoReflectedInput confirms the handler ignores query/params — the
// page is static server-composed content, so a reflected-XSS probe in the query
// string must not appear in the output.
func TestConnectNoReflectedInput(t *testing.T) {
	ts := newConfiguredServer(t, Config{BaseURL: "https://demiplane.example"})
	resp, err := http.Get(ts.URL + "/connect?x=<script>alert(1)</script>")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "<script>alert(1)</script>") {
		t.Fatalf("connect page reflected unescaped query input")
	}
}

// TestConnectGolden pins the full rendered HTML for a fixed base + open instance.
// Run `go test ./internal/server -run TestConnectGolden -update` to refresh. The
// package must come before -update: `go` does not know that flag, so it treats the
// next argument as its value and silently tests the current directory instead.
func TestConnectGolden(t *testing.T) {
	_, body, _ := getConnect(t, "https://demiplane.example", "")
	golden := filepath.Join("testdata", "connect_open.html")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if string(want) != body {
		t.Errorf("connect HTML drifted from golden %s; re-run with -update if intentional", golden)
	}
}
