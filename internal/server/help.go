// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"html"
	"net/http"
	"strings"
)

// help.go owns the human-facing GET /help getting-started page. The agent-native
// self-describing JSON that /help used to return now lives at /help.json
// (handleHelp, in llms.go) byte-identical; this page is the guide a person lands
// on: install, run, connect a harness, theme, and the optional modules.
//
// Like /connect it is fully server-composed static HTML: it reflects no user
// input and emits no secret. The only per-request value is the instance base URL
// (from --base-url or the request Host), HTML-escaped like every other surface.
// Deep how-to lives elsewhere and is linked, never duplicated: the REST surface
// is /docs/api, harness config is /connect, the full docs are /docs.

func (s *Server) handleHelpPage(w http.ResponseWriter, r *http.Request) {
	base := s.requestBase(r)
	eb := html.EscapeString(base)
	authConfigured := s.authToken != ""

	var b strings.Builder

	b.WriteString(`<span class="kicker">Getting started</span>`)
	b.WriteString(`<h1>demiplane help</h1>`)
	b.WriteString(`<p>demiplane is self-hosted, internal-first publishing: you POST a file ` +
		`to your own server and get back a URL only your network can reach. This page ` +
		`walks you from install to your first published page. For the full REST surface ` +
		`see the <a href="/docs/api">API reference</a>; to wire demiplane into a coding ` +
		`harness see <a href="/connect">Connect</a>.</p>`)
	b.WriteString(`<span class="ethos">Sealed by default, shared by choice.</span>`)

	// --- Install ---
	b.WriteString(`<h2 id="install">Install</h2>`)
	b.WriteString(`<p>demiplane is a single static Go binary with no runtime dependencies. ` +
		`Pick whichever path fits your setup.</p>`)

	b.WriteString(`<h3>Prebuilt binary</h3>`)
	b.WriteString(`<p>Install the latest published build straight onto your PATH with the Go ` +
		`toolchain, or drop a release binary into a directory on your PATH:</p>`)
	b.WriteString(codeBlock("go install",
		"go install github.com/daisandapex/demiplane/cmd/demiplane@latest\n"+
			"demiplane version"))

	b.WriteString(`<h3>Docker</h3>`)
	b.WriteString(`<p>Build the image and run it. Map both planes: 8080 is the control plane ` +
		`(publish, list, delete) and 8081 is the isolated content origin that serves ` +
		`artifact bytes (origin isolation, ADR 0003).</p>`)
	b.WriteString(codeBlock("docker",
		"docker build -t demiplane .\n"+
			"docker run --rm -p 8080:8080 -p 8081:8081 \\\n"+
			"  -v demiplane-data:/var/lib/demiplane demiplane"))

	b.WriteString(`<h3>Build from source</h3>`)
	b.WriteString(`<p>The default build is core only. The optional modules compile in behind ` +
		`build tags: <code>reply</code> for inline replies, <code>tls</code> for native ` +
		`HTTPS. A broken module can never break core publishing (ADR 0001), so add only ` +
		`what you need.</p>`)
	b.WriteString(codeBlock("go build",
		"# core only\n"+
			"go build -o demiplane ./cmd/demiplane\n\n"+
			"# with the reply and TLS modules\n"+
			"go build -tags \"reply tls\" -o demiplane ./cmd/demiplane"))

	// --- Run ---
	b.WriteString(`<h2 id="run">Run the server</h2>`)
	b.WriteString(`<p>Bind loopback by default; bind a mesh or LAN address to expose the ` +
		`instance. Set a bearer token to require auth on publish, list, and delete ` +
		`(omit it only on a trusted network).</p>`)
	b.WriteString(codeBlock("serve",
		"echo \"my-secret-token\" > ~/.config/demiplane/token && chmod 600 ~/.config/demiplane/token\n"+
			"demiplane serve --bind 0.0.0.0:8080 --store /var/lib/demiplane \\\n"+
			"  --token-file ~/.config/demiplane/token"))
	b.WriteString(`<p>The control plane is <code>:8080</code>; artifact bodies are served on a ` +
		`separate content origin at <code>:8081</code> by default (the control port plus one). ` +
		`A publish returns a content-origin URL you share and fetch back.</p>`)

	// --- Connect a harness ---
	b.WriteString(`<h2 id="connect">Connect a harness</h2>`)
	b.WriteString(`<p>Once an instance is running, publish from your coding harness, an agent, ` +
		`or a bare shell. The <a href="/connect">Connect</a> page has copy-paste config ` +
		`for each tool; the essentials:</p>`)
	b.WriteString(`<h3>MCP</h3>`)
	b.WriteString(`<p>Claude Code, Cursor, Cline, Windsurf, Zed, and Continue speak the Model ` +
		`Context Protocol. Register demiplane once and <code>publish</code>, ` +
		`<code>list</code>, <code>get</code>, and <code>delete</code> become native tools. ` +
		`See <a href="/connect#mcp">Connect</a> for the exact stanza and per-tool config ` +
		`paths.</p>`)
	b.WriteString(`<h3>Bare curl</h3>`)
	b.WriteString(`<p>The universal fallback: anything that can run <code>curl</code> can publish.</p>`)
	b.WriteString(codeBlock("Publish with curl", curlHelpPublish(base, authConfigured)))
	b.WriteString(`<h3>The demiplane CLI</h3>`)
	b.WriteString(`<p>The <code>demiplane publish</code> subcommand wraps the same call, prints ` +
		`and copies the URL, and can watch a file for a live edit-save-see loop:</p>`)
	b.WriteString(codeBlock("demiplane publish",
		"demiplane publish --url "+base+" --token-file "+tokenPath+" index.html\n"+
			"demiplane publish --url "+base+" --watch --slug draft notes.md"))

	// --- Theming ---
	b.WriteString(`<h2 id="theming">Theming</h2>`)
	b.WriteString(`<p>Markdown pages published with <code>?render=md</code>, and this instance's ` +
		`own chrome, share one house style. On the default theme a rendered page ships a ` +
		`client-side light/dark toggle that follows the reader's system preference. ` +
		`Pin a single palette instead with <code>--theme</code>: the built-in named ` +
		`palettes are <code>catppuccin</code>, <code>dracula</code>, and ` +
		`<code>one-dark</code> (plus plain <code>light</code> and <code>dark</code>). ` +
		`A pinned palette fixes the page's look and drops the toggle.</p>`)
	b.WriteString(codeBlock("Pick a theme",
		"# skin the whole instance (chrome + rendered pages)\n"+
			"demiplane serve --bind 0.0.0.0:8080 --store /var/lib/demiplane --theme dracula\n\n"+
			"# or set it once in the config file (CLI flag wins over the file)\n"+
			"# ${XDG_CONFIG_HOME:-~/.config}/demiplane/config\n"+
			"theme = catppuccin"))
	b.WriteString(`<p>Supply a full stylesheet with <code>--css</code> to replace the built-in ` +
		`theme, and tune the rendered-page furniture with <code>--header</code>, ` +
		`<code>--footer</code>, <code>--footer-link</code>, and <code>--meta-header</code>.</p>`)

	// --- TLS module ---
	b.WriteString(`<h2 id="tls">HTTPS (TLS module)</h2>`)
	b.WriteString(`<p>Build with <code>-tags tls</code> and set <code>tls = on</code> in the ` +
		`config file to serve HTTPS natively, with no reverse proxy. Three modes ` +
		`(ADR 0004):</p>`)
	b.WriteString(`<ul>` +
		`<li><strong>Self-signed</strong> (default): a persistent locally generated ` +
		`certificate; override its SANs with <code>tls_hosts</code>. Ideal on a mesh ` +
		`or LAN where you trust the cert yourself.</li>` +
		`<li><strong>ACME / Let's Encrypt</strong>: name public hostnames in ` +
		`<code>tls_acme_domains</code> (optionally <code>tls_acme_email</code> and ` +
		`<code>tls_acme_ca</code>) and demiplane obtains and renews certificates ` +
		`automatically.</li>` +
		`<li><strong>Bring your own</strong>: point <code>tls_cert</code> and ` +
		`<code>tls_key</code> at operator-managed PEM files.</li>` +
		`</ul>`)
	b.WriteString(codeBlock("config (self-signed on a mesh)",
		"# ${XDG_CONFIG_HOME:-~/.config}/demiplane/config  (needs -tags tls)\n"+
			"tls = on\n"+
			"tls_hosts = demiplane.example, 100.100.100.100"))

	// --- Reply module ---
	b.WriteString(`<h2 id="reply">Inline replies (reply module)</h2>`)
	b.WriteString(`<p>Build with <code>-tags reply</code> to let viewers respond to a published ` +
		`page. Publish a markdown page with <code>?render=md&amp;reply=question</code> and ` +
		`a named <code>?slug=</code> to bake a JS-free reply box into it; viewers submit ` +
		`same-origin and you read the answers back at <code>GET /replies</code> (bearer ` +
		`auth). A recorded reply can fire a server-side hook ` +
		`(<code>reply_hook_exec</code> / <code>reply_hook_url</code>) so an agent reacts ` +
		`with no polling. The forward-flow <code>?next=</code> parameter carries a reader ` +
		`to the next page once it is published. Full endpoint detail lives in the ` +
		`<a href="/docs/api#replies">API reference</a>.</p>`)

	// --- SSH ingest ---
	b.WriteString(`<h2 id="ssh">SSH ingest</h2>`)
	b.WriteString(`<p><code>demiplane receive</code> ingests an artifact from stdin, designed ` +
		`to run as an SSH forced command so a pubkey publishes without a bearer token ` +
		`(sshd does the auth; the flags bake into <code>command=</code> and ` +
		`<code>SSH_ORIGINAL_COMMAND</code> is ignored). Use <code>--untar</code> to sync a ` +
		`whole directory from a tar stream.</p>`)
	b.WriteString(codeBlock("authorized_keys forced command",
		"restrict,command=\"demiplane receive --store /var/lib/demiplane --base-url https://host\" ssh-ed25519 AAAA... you@host"))
	b.WriteString(`<p>Then publish over SSH by piping a file to the pinned command:</p>`)
	b.WriteString(codeBlock("publish over SSH",
		"cat report.html | ssh demiplane@host"))

	// --- TTL + privacy ---
	b.WriteString(`<h2 id="lifetime">Expiry and privacy</h2>`)
	b.WriteString(`<p>Two per-artifact controls set at publish time:</p>`)
	b.WriteString(`<ul>` +
		`<li><strong>TTL</strong>: <code>?ttl=30m</code>, <code>?ttl=2h</code>, or ` +
		`<code>?ttl=7d</code> auto-expires the artifact; after it lapses the URL returns ` +
		`404 and a background sweep reaps the bytes.</li>` +
		`<li><strong>Privacy</strong>: <code>?private=true</code> mints an unguessable ` +
		`capability slug (its secret is the URL itself; it cannot be combined with a ` +
		`named <code>?slug=</code>). Add a view password with the ` +
		`<code>X-Demiplane-Password</code> header (never the URL) to gate reads over ` +
		`HTTP Basic.</li>` +
		`</ul>`)

	// --- Learn more ---
	b.WriteString(`<h2>Learn more</h2>`)
	b.WriteString(`<div class="grid">` +
		`<a class="tile" href="/docs/api"><div class="t">API reference</div><div class="d">Full REST surface with curl, Python, JavaScript, and Go examples.</div></a>` +
		`<a class="tile" href="/connect"><div class="t">Connect</div><div class="d">Copy-paste config for every harness.</div></a>` +
		`<a class="tile" href="/docs"><div class="t">Docs</div><div class="d">Overview, security, and the changelog.</div></a>` +
		`<a class="tile" href="/llms.txt"><div class="t">llms.txt</div><div class="d">One-fetch reference for agents.</div></a>` +
		`</div>`)
	b.WriteString(`<p class="desc">Building an agent or script? The machine-readable API is at ` +
		`<a href="/help.json">/help.json</a> (self-describing JSON) and ` +
		`<a href="/llms.txt">/llms.txt</a> (plain text). This instance is ` + helpAuthNote(authConfigured, eb) + `</p>`)

	writeHTML(w, s.pageHTML("help · demiplane", topNav("/help"), b.String()))
}

// helpAuthNote states whether the instance requires a bearer token, without ever
// emitting the token itself (only the file path where it lives).
func helpAuthNote(auth bool, escapedBase string) string {
	if auth {
		return `open to reads on your network and requires a bearer token to publish; ` +
			`store yours at <code>` + html.EscapeString(tokenPath) + `</code>.`
	}
	return `open (no bearer token configured), so the snippets above work as-is at <code>` +
		escapedBase + `</code>.`
}

// curlHelpPublish returns the publish snippet, adding the bearer header (read
// from the local token file) only when this instance requires auth. It mirrors
// connect.go's curlPublish so the two adoption surfaces stay consistent.
func curlHelpPublish(base string, auth bool) string {
	if auth {
		return "curl --data-binary @index.html \\\n" +
			"  -H \"Authorization: Bearer $(cat " + tokenPath + ")\" \\\n" +
			"  " + base + "/publish"
	}
	return "curl --data-binary @index.html " + base + "/publish"
}
