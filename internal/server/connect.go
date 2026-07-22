// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"strings"
)

// connect.go owns feature T1.3: the GET /connect onboarding page (control plane)
// with a copy-paste config block per harness. It is the adoption ramp — a user
// lands here, copies the stanza for their coding harness, and demiplane becomes
// a first-class "publish" target in that tool.
//
// The page is fully server-composed static HTML: it reflects NO user input and
// emits NO secret. The only per-request value is the instance base URL (derived
// from --base-url or the request Host, HTML-escaped like every other surface).
// The bearer token is shown only as a PLACEHOLDER file path (--token-file) plus
// a local one-liner to read it — the live token never touches this page.
func init() {
	registerCoreControlRoute([]string{"connect"}, func(mux *http.ServeMux, s *Server) {
		mux.HandleFunc("GET /connect", s.handleConnect)
	})
}

// tokenPath is the conventional local path an operator points --token-file at.
// It is a PATH, never a token value; the page instructs harnesses to read it
// locally so the secret stays on the user's machine.
const tokenPath = "~/.config/demiplane/token"

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	base := s.requestBase(r)
	eb := html.EscapeString(base)

	// authNote adapts the copy to whether this instance requires a bearer token.
	// It never reveals the token; it only tells the user where theirs lives.
	authConfigured := s.authToken != ""

	var b strings.Builder

	b.WriteString(`<span class="kicker">Works with your harness</span>`)
	b.WriteString(`<h1>Connect demiplane</h1>`)
	b.WriteString(`<p>demiplane publishes from any coding harness, agent, or shell. ` +
		`Pick your tool below and copy the block. Every snippet targets this instance ` +
		`(<code>` + eb + `</code>) and each publish returns a URL only your network can reach.</p>`)

	if authConfigured {
		b.WriteString(`<span class="ethos">This instance requires a bearer token. ` +
			`Save yours to <code>` + html.EscapeString(tokenPath) + `</code> once; ` +
			`every snippet below reads it locally so the secret never leaves your machine.</span>`)
		b.WriteString(`<h2>One-time token setup</h2>`)
		b.WriteString(`<p>Ask the operator for the instance token, then store it once ` +
			`(the file, not this page, is where it lives):</p>`)
		b.WriteString(codeBlock("Save your token locally",
			"mkdir -p ~/.config/demiplane\n"+
				"printf %s \"$YOUR_TOKEN\" > "+tokenPath+"\n"+
				"chmod 600 "+tokenPath))
	} else {
		b.WriteString(`<span class="ethos">This instance is open (no bearer token configured). ` +
			`The snippets below work as-is; if the operator later enables auth, save your token to ` +
			`<code>` + html.EscapeString(tokenPath) + `</code> and the same blocks keep working.</span>`)
	}

	// --- Compatibility matrix ---
	b.WriteString(`<h2>Compatibility</h2>`)
	b.WriteString(harnessMatrix())
	b.WriteString(`<p class="desc">demiplane speaks plain MCP and plain HTTP, so the list of ` +
		`clients it works with is longer than the list we verify. The table says which is ` +
		`which. Got it running in another MCP client? Open an issue and we will add it.</p>`)

	// --- MCP: the native path for any MCP-capable client ---
	b.WriteString(`<h2 id="mcp">MCP clients</h2>`)
	b.WriteString(`<p><code>demiplane mcp</code> is a standard stdio JSON-RPC MCP server, so any ` +
		`MCP-capable client can drive it. Register demiplane once and <code>publish</code>, ` +
		`<code>list</code>, <code>get</code>, and <code>delete</code> become native tools. ` +
		`The stanza below is the same everywhere; only the config file location differs, and ` +
		`your client's own docs name that path.</p>`)
	b.WriteString(codeBlock("mcp.json (mcpServers stanza)", mcpStanza(base)))
	b.WriteString(`<div class="grid">` +
		mcpTile("Claude Code", "claude mcp add, or ~/.claude.json → mcpServers") +
		mcpTile("Any MCP client", "wherever that client keeps its mcpServers config") +
		`</div>`)
	b.WriteString(`<p class="desc">Claude Code one-liner (adds the stanza above for you):</p>`)
	b.WriteString(codeBlock("Claude Code",
		"claude mcp add demiplane -- demiplane mcp --url "+base+" --token-file "+tokenPath))

	// --- Claude Code capture hook (the original companion path) ---
	b.WriteString(`<h2 id="hook">Claude Code capture hook</h2>`)
	b.WriteString(`<p>Prefer a hook over MCP? The companion Stop-hook publishes the ` +
		`transcript or a named artifact automatically. See <code>companion/README.md</code> ` +
		`in the repo; the hook targets this instance with:</p>`)
	b.WriteString(codeBlock("companion hook env",
		"export DEMIPLANE_URL="+base+"\n"+
			"export DEMIPLANE_TOKEN_FILE="+tokenPath))

	// --- Bare curl: works in literally anything that can shell out ---
	b.WriteString(`<h2 id="curl">Bare curl</h2>`)
	b.WriteString(`<p>The universal fallback. Any harness, script, or terminal that can run ` +
		`<code>curl</code> can publish:</p>`)
	b.WriteString(codeBlock("Publish a page", curlPublish(base, authConfigured)))
	b.WriteString(`<p>Add query parameters to control the result:</p>`)
	b.WriteString(codeBlock("Variations", curlVariations(base)))

	// --- CLI: the demiplane publish subcommand ---
	b.WriteString(`<h2 id="cli">Command-line client</h2>`)
	b.WriteString(`<p>The <code>demiplane publish</code> subcommand wraps the same call, ` +
		`copies the URL to your clipboard, and can watch a file for live-reload:</p>`)
	b.WriteString(codeBlock("demiplane publish", cliPublish(base)))

	// --- Aider / shell-command harnesses ---
	b.WriteString(`<h2 id="aider">Aider and shell-command harnesses</h2>`)
	b.WriteString(`<p>Harnesses that expose a shell command (Aider's <code>/run</code>, ` +
		`for example) publish through the same curl. Run it and paste the returned URL back:</p>`)
	b.WriteString(codeBlock("Aider /run",
		"/run "+curlPublishOneLine(base, authConfigured)))

	// --- Read it back ---
	b.WriteString(`<h2>Fetch it back</h2>`)
	b.WriteString(`<p>Every publish returns a URL. Fetch it from anything on your network:</p>`)
	b.WriteString(codeBlock("Read", "curl "+base+"/shadow-specter"))

	b.WriteString(`<h2>Learn more</h2>`)
	b.WriteString(`<div class="grid">` +
		`<a class="tile" href="/docs"><div class="t">Docs</div><div class="d">Full guide, served by demiplane itself.</div></a>` +
		`<a class="tile" href="/docs/api"><div class="t">API reference</div><div class="d">Full REST surface with per-language examples.</div></a>` +
		`<a class="tile" href="/llms.txt"><div class="t">llms.txt</div><div class="d">One-fetch reference for agents.</div></a>` +
		`</div>`)

	writeHTML(w, s.pageHTML("connect · demiplane", connectNav(), b.String()))
}

// connectNav returns the shared top nav with the Connect entry marked active.
func connectNav() []navLink {
	return topNav("/connect")
}

// mcpStanza is the mcpServers JSON block a user pastes into their harness config.
// The token is referenced by FILE PATH (--token-file), never inlined.
//
// This marshals rather than concatenating strings. base is not always filtered:
// a request-derived host passes through sanitizeHost, but an explicit
// --base-url is operator input and reaches here verbatim. Interpolating it into
// hand-built JSON let a single quote emit a stanza no MCP client can parse —
// and the resulting error names the USER's config file, not demiplane. Marshal
// escapes it correctly by construction. Covered by e2e/connect_config_test.go.
func mcpStanza(base string) string {
	type serverEntry struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	stanza := struct {
		MCPServers map[string]serverEntry `json:"mcpServers"`
	}{
		MCPServers: map[string]serverEntry{
			"demiplane": {
				Command: "demiplane",
				Args:    []string{"mcp", "--url", base, "--token-file", tokenPath},
			},
		},
	}
	out, err := json.MarshalIndent(stanza, "", "  ")
	if err != nil {
		// Unreachable: the value is plain strings and a string map. Fall back to
		// an empty object rather than serving a half-written block.
		log.Printf("demiplane: mcp stanza marshal: %v", err)
		return "{}"
	}
	return string(out)
}

// mcpTile renders a compatibility tile naming where a harness keeps its MCP config.
func mcpTile(name, where string) string {
	return fmt.Sprintf(
		`<div class="tile"><div class="t">%s</div><div class="d">%s</div></div>`,
		html.EscapeString(name), html.EscapeString(where))
}

// harnessMatrix renders the "works with" table. Static content, escaped.
//
// The "Verified" column is the honest one: it says what demiplane's own tests and
// daily use actually exercise. Only move a row to "Yes" when there is a test or a
// dogfooding path behind it — not because the protocol says it ought to work.
func harnessMatrix() string {
	rows := []struct{ harness, method, verified string }{
		{"Claude Code", "MCP (native tools) or capture hook", "Yes — tested and used daily"},
		{"Any MCP client", "MCP (standard stdio JSON-RPC server)", "Not yet verified"},
		{"Aider", "shell /run + curl", "Yes — the curl path is tested"},
		{"Any shell / CI", "curl or demiplane publish", "Yes — REST and CLI tests"},
	}
	var b strings.Builder
	b.WriteString(`<table><tr><th>Harness</th><th>How it connects</th><th>Verified</th></tr>`)
	for _, r := range rows {
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%s</td></tr>`,
			html.EscapeString(r.harness), html.EscapeString(r.method),
			html.EscapeString(r.verified))
	}
	b.WriteString(`</table>`)
	return b.String()
}

// curlPublish returns the multi-line publish snippet, adding the bearer header
// (read from the local token file) only when this instance requires auth.
func curlPublish(base string, auth bool) string {
	if auth {
		return "curl --data-binary @index.html \\\n" +
			"  -H \"Authorization: Bearer $(cat " + tokenPath + ")\" \\\n" +
			"  " + base + "/publish"
	}
	return "curl --data-binary @index.html " + base + "/publish"
}

// curlPublishOneLine is the single-line variant for harnesses whose command box
// does not accept a multi-line paste (e.g. Aider /run).
func curlPublishOneLine(base string, auth bool) string {
	if auth {
		return "curl --data-binary @index.html -H \"Authorization: Bearer $(cat " +
			tokenPath + ")\" " + base + "/publish"
	}
	return "curl --data-binary @index.html " + base + "/publish"
}

// curlVariations shows the common query-parameter recipes.
func curlVariations(base string) string {
	return "# stable slug that overwrites in place\n" +
		"curl --data-binary @report.html \"" + base + "/publish?slug=reports\"\n\n" +
		"# private, unguessable URL that expires in a day\n" +
		"curl --data-binary @secret.html \"" + base + "/publish?private=true&ttl=24h\"\n\n" +
		"# render markdown to a styled page\n" +
		"curl --data-binary @notes.md \"" + base + "/publish?render=md\"\n\n" +
		"# set a view password (header, never the URL)\n" +
		"curl -H \"X-Demiplane-Password: hunter2\" --data-binary @q.html \"" + base + "/publish?slug=q\""
}

// cliPublish shows the demiplane publish subcommand recipes.
func cliPublish(base string) string {
	return "# publish a file; the URL is printed and copied to your clipboard\n" +
		"demiplane publish --url " + base + " --token-file " + tokenPath + " index.html\n\n" +
		"# watch a file and re-publish on every save (pairs with ?live for auto-reload)\n" +
		"demiplane publish --url " + base + " --token-file " + tokenPath + " --watch --slug draft notes.md\n\n" +
		"# or set the environment once and drop the flags\n" +
		"export DEMIPLANE_URL=" + base + "\n" +
		"export DEMIPLANE_TOKEN_FILE=" + tokenPath + "\n" +
		"demiplane publish index.html"
}
