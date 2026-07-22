<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/brand/mark-dark.png">
    <img src="assets/brand/mark-light.png" alt="demiplane" width="88" height="88">
  </picture>
</p>

<h1 align="center">demiplane</h1>

<p align="center"><strong>Your private publishing plane.</strong></p>

<p align="center">
Publish an HTML file from your shell or your coding agent and get a link back —
to a box you own, reachable only from your own network.
</p>

<p align="center">
  <code>single Go binary</code> · <code>no database server</code> · <code>AGPL-3.0-only</code>
</p>

> **🔒 Loopback by default.** A fresh instance binds `127.0.0.1` and is reachable
> from nowhere else. Nothing you publish is exposed until *you* bind a LAN or mesh
> IP. That is the product, not a limitation — if you want a URL a stranger can
> open, [use a public host instead](#when-not-to-use-it).

## 60 seconds to your first link

Requires [Go 1.26+](https://go.dev/dl/). A cold build takes about 30 seconds.

```sh
git clone https://github.com/daisandapex/demiplane && cd demiplane
go build -o demiplane ./cmd/demiplane

./demiplane serve --store ./data &
#   control   http://127.0.0.1:8080   (publish / list / delete)
#   content   http://127.0.0.1:8081   (artifacts — isolated origin)

echo '<h1>hello from my box</h1>' > report.html
curl --data-binary @report.html http://127.0.0.1:8080/publish
```

```
http://127.0.0.1:8081/abjuration-shadow
```

That URL is live. Open it, or send the link to anyone on your LAN or mesh and
they read the page instead of downloading a file.

Ask for JSON if you are scripting it:

```sh
curl -H 'Accept: application/json' --data-binary @report.html \
  http://127.0.0.1:8080/publish
```

```json
{
  "content_type": "text/html; charset=utf-8",
  "expires_at": "",
  "password": false,
  "private": false,
  "size": 27,
  "slug": "necromancy-rhinoceros",
  "url": "http://127.0.0.1:8081/necromancy-rhinoceros"
}
```

> Publish to the **control** plane (`:8080`). Artifacts are *served* from the
> separate content origin (`:8081`) — posting there returns `405`. That split is
> deliberate; see [what just happened](#what-just-happened).

## Or ask your agent

demiplane ships an MCP server, so a coding agent can publish for you. Point your
harness at `demiplane mcp` and ask for it in words:

> *"Publish this report to demiplane and give me the link."*

A running instance serves a copy-paste config block for your specific tool at
**`GET /connect`** — open it and grab the stanza. Claude Code is tested daily;
any MCP client, Aider, and any shell or CI over plain `curl` also work. See
[`docs/harness.md`](docs/harness.md).

## What just happened

Your file was stored and served by a single Go binary you control. No cloud, no
upload to anyone else's infrastructure, and no database server to run — metadata
lives in an embedded pure-Go SQLite file, so there is no `cgo`, no daemon, and
nothing to provision.

The **control plane** (`:8080`, where you publish) and the **content origin**
(`:8081`, where artifacts are served) are separate origins on purpose. HTML and
JavaScript you publish therefore run cross-origin to the API and cannot reach
your publish, delete, or list endpoints. See
[ADR 0003](docs/adr/0003-content-origin-isolation.md).

## How it compares

The axes that actually matter when choosing where internal content lives:

|  | **demiplane** | **Public paste / HTML host** | **PGS (pico.sh)** | **Just email the file** |
|---|---|---|---|---|
| Data lives on your box | Yes | No, on their server | No, on pico.sh's server | Yes, and everywhere it's forwarded |
| Single-command publish | Yes (`curl` / CLI / MCP) | Yes | Yes (`scp` / rsync) | N/A, no link |
| First-class HTTP API | Yes — publish/list/get/delete | Rarely, mostly web UI | Partial, git/ssh-oriented | No |
| Per-artifact privacy | Capability slugs, view passwords, TTL | Usually public or paid-private | Per-site privacy | Only as private as the inbox |
| Internal-first by default | Yes — loopback bind, expose deliberately | No, public by design | No, public by design | N/A |
| Cost / deps to run | One static binary, embedded SQLite | None to run (it's theirs) | Pico account, their uptime | None |
| License | AGPL-3.0-only, self-host | Proprietary SaaS | MIT client, hosted service | N/A |

**Where the alternatives genuinely win:** public hosts and PGS need *zero
infrastructure from you*, and they hand out *public URLs a stranger can open* —
which demiplane deliberately will not do. If you want the whole internet to reach
the page, use one of them.

Against PGS specifically, demiplane matches the core (static hosting, single-file
publish, per-artifact privacy) and adds a first-class HTTP API, internal-first
defaults, and TTL/expiry, with **no dependency on pico.sh**.

## What you get

|  |  |
|---|---|
| **Single-command publish** | POST a file, get an instant URL. curl, CLI, or native MCP. |
| **Internal-first** | Loopback by default; bind a LAN/mesh IP to expose. A wildcard bind logs a loud warning. |
| **Per-artifact privacy** | 144-bit unguessable capability slugs, optional view passwords, TTL/expiry with a background sweeper. |
| **Two-plane isolation** | Control plane and content origin are separate; served content cannot touch your controls. |
| **Markdown render** | Opt-in `?render=md` with themes, syntax highlighting, heading anchors, and frontmatter meta-headers. |
| **Multi-file sites** | Publish a tar/zip/multipart bundle and serve the whole tree at one slug. |
| **Live reload** | `?live` streams updates over SSE while you iterate. |
| **SSH ingest** | `demiplane receive` publishes over your existing `sshd` via an `authorized_keys` forced command. |
| **Optional TLS** | Self-signed, ACME, or bring-your-own, via `-tags tls`. |

## When to use it

- You want the send-a-link convenience of a paste service, but the data must stay
  on infrastructure you own.
- You publish reports, dashboards, or rendered docs to a LAN, homelab, or mesh.
- You want an HTTP API with per-artifact TTL and privacy, without standing up a database.
- You want your agent or CI to publish artifacts as part of a run.

## When NOT to use it

- **You need a public, internet-facing site** with a CDN and custom domains.
  demiplane is internal-first by design.
- **You want a CMS**, a WYSIWYG editor, or user accounts and roles. demiplane
  publishes files; it does not manage them.
- **You need multi-tenant isolation** or a compliance audit trail. v1 is
  single-operator and trusts your network perimeter.
- **You want managed hosting with zero ops.** You run the binary.

## Documentation

The README is the map; the full reference lives in [`docs/`](docs/):

| | |
|---|---|
| [`docs/api.md`](docs/api.md) | Every endpoint, query parameter, and the two-layer auth model |
| [`docs/deployment.md`](docs/deployment.md) | Binding, reachability tiers, config file, Docker |
| [`docs/harness.md`](docs/harness.md) | MCP, Claude Code, Aider, CI, and the capture hook |
| [`docs/rendering.md`](docs/rendering.md) | `?render=md`, themes, live-reload, inline replies |
| [`docs/tls.md`](docs/tls.md) | Self-signed, ACME, and bring-your-own certificates |
| [`docs/receive.md`](docs/receive.md) | SSH ingest via `authorized_keys` forced commands |
| [`docs/architecture.md`](docs/architecture.md) | Origin isolation, repo layout, design rationale |
| [`docs/adr/`](docs/adr/) | Architecture decision records |

Release history is in [CHANGELOG.md](CHANGELOG.md).

## Why this exists

I sent my CEO a report as an HTML file — the kind agent harnesses produce all day
now — and he had to download it, find it, and open it in a browser before he could
read a single line. He didn't want a file; he wanted a link. Markdown attachments
have the same disease with worse formatting.

The gap: rendered HTML, shareable as a URL, without handing the content to someone
else's host. So: `POST` the file to your own box, send the link, done.

## Contributing & security

Contributions are welcome under a CLA; see [CONTRIBUTING.md](CONTRIBUTING.md).
Report vulnerabilities privately per [SECURITY.md](SECURITY.md) — never in a
public issue.

## Acknowledgments

- The friendly auto-generated slugs (`adjective-creature`, e.g. `shadow-specter`) are built
  from a wordlist generated via the **[D&D 5e API](https://www.dnd5eapi.co)** — the
  open-source **5e-bits** project. Thank you for the open API. The creature and term names
  are **D&D 5e System Reference Document** content, © Wizards of the Coast, used under the
  OGL 1.0a / CC-BY-4.0.

## License

AGPL-3.0-only. A commercial license is available; see
[COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md).

<sub>Maintained by <strong>Dais &amp; Apex</strong>.</sub>
