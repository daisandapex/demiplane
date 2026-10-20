<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/brand/mark-dark.png">
    <img src="assets/brand/mark-light.png" alt="demiplane" width="88" height="88">
  </picture>
</p>

# demiplane

**Your private publishing plane.**

Publish an HTML file from your shell or your coding agent and get a link back —
to a box you own, reachable only from your own network.

`single Go binary` · `no database server` · `AGPL-3.0-only`

> **🔒 Loopback by default.** A fresh instance binds `127.0.0.1` and is reachable
> from nowhere else. Nothing you publish is exposed until *you* bind a LAN or mesh
> IP. That is the product, not a limitation — if you want a URL a stranger can
> open, [use a public host instead](#when-not-to-use-it).

## Install

```sh
go install github.com/daisandapex/demiplane/v3/cmd/demiplane@latest
```

Requires [Go 1.26+](https://go.dev/dl/). The binary lands in
`$(go env GOPATH)/bin`; a cold build takes about 30 seconds.

**Why this, and why never `curl | bash`.** `go install` fetches the module
through the Go module proxy and verifies what it downloads against
[`sum.golang.org`](https://sum.golang.org) — a public transparency log that this
project does not run, does not control, and cannot rewrite after the fact. The
checksum your machine checks is the checksum every other machine checks, so a
tampered copy served to you alone fails before a single line compiles. Then your
own toolchain builds the source. That is cryptographic proof the source you are
compiling is the source everybody else received, which means you do not have to
trust the author of an unfamiliar binary — you verify instead. A piped shell
installer gives you the opposite: it asks for that trust and offers nothing to
check. demiplane will not ship one.

Until the first tagged release, `@latest` resolves to a *pseudo-version* of the
form `v0.0.0-<date>-<commit>`, built from the tip of `main`. It is verified
through the same log; a tagged release is imminent.

Prefer a clone if you want the build-tagged optional modules, or want to read
before you build:

```sh
git clone https://github.com/daisandapex/demiplane && cd demiplane
go build -o demiplane ./cmd/demiplane
# with the optional modules compiled in:
go build -tags "reply tls" -o demiplane ./cmd/demiplane
```

For the container path — including the digest-pinned pull that makes an image
reference immutable — see [`docs/deployment.md`](docs/deployment.md#docker).

## 60 seconds to your first link

```sh
demiplane serve --store ./data &
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
[ADR 0003](docs/adr/0003-content-origin-isolation.md). Each published page is
also sandboxed in an origin of its own, so one page cannot read another or
install a service worker; pages lose storage and same-origin `fetch` in
exchange (see [DESIGN.md](DESIGN.md#content-isolation)).

## List and manage publications

```sh
# Public artifacts, human table by default:
demiplane list --url http://127.0.0.1:8080
# Owner inventory, including private artifacts and sites:
demiplane list --url http://127.0.0.1:8080 --token-file ./token --all --json
# Delete a site on a running server:
demiplane delete-site --url http://127.0.0.1:8080 --token-file ./token demo
# Or maintain an offline store, with the server stopped:
demiplane delete-site --store ./store demo
```

`list` follows every API page. `--json` emits one `artifacts` array and total
`count`; the default table shows kind, slug, render mode, reply enabled, series,
expiry and URL. `--all` requires the API's `owner: true` envelope marker and
owner metadata, reporting an upgrade error even for an empty older-server
response. Artifact continuation cursors use numeric row IDs, so they contain
no capability slugs.
`--url` overrides `DEMIPLANE_URL`; `list` and `publish` default to
`http://127.0.0.1:8080`. Remote `delete-site` requires a URL supplied through
`--url` or `DEMIPLANE_URL`; without one, supply `--store` for offline deletion.
`--token-file` overrides `DEMIPLANE_TOKEN`. Explicit `--store` selects offline
site deletion and cannot be combined with `--url` or `--token-file`.
Flag parsing errors omit supplied values and URL environment defaults and exit
with status 2. Publish failures retain status 1.
The `list` and `delete-site` clients include token configuration advice on 403.

Owner listing and remote site deletion require a configured server token and a
valid bearer, even on an otherwise open server. Owner responses are private and
not cacheable, including admission and host-policy refusals. Site inspection
errors return 500 rather than reporting success or absence. The startup banner
counts unexpired artifacts including private,
plus published sites. Public `/list` and `/gallery` continue to exclude private
artifacts and sites. Sites remain public directories, without TTL or reply
metadata. Deleting a site preserves a same-named artifact, and vice versa.
See [API.md](API.md#get-list) for metadata defaults and pagination.

On the split content listener, GET and HEAD `/` redirect with `302` to an
explicit valid HTTP(S) `--base-url` on a different host or non-default port.
Unsafe or looping destinations keep the themed 404. Unknown slugs and the
combined-origin landing page keep their existing behavior.

### Expiry, pins, and bulk deletion (3.1)

```sh
demiplane ttl report 7d --url http://127.0.0.1:8080 --token-file ./token
demiplane ttl report none --token-file ./token
demiplane pin report --token-file ./token
demiplane unpin report --token-file ./token
# Preview: count, total bytes, and every selected identifier and revision.
demiplane delete --match 'draft-*' --older-than 24h --token-file ./token
# Execute exactly the manifest printed by this invocation.
demiplane delete --match 'draft-*' --older-than 24h --yes --confirm-host 127.0.0.1 --token-file ./token
demiplane delete --all --yes --confirm-host 127.0.0.1 --token-file ./token
```

Management requires a configured valid token. TTL changes set expiry to **now
plus** the duration, from `1m` through `365d`; `none` or `0` clears expiry.
Pinning clears expiry. A pinned slug refuses any nonempty explicitly supplied TTL,
including never-expire values, until unpinned. Republishing without TTL keeps
its pin. Unknown or expired unpinned slugs return 404 without changing anything.

`serve --default-ttl 7d` sets a persistent default for HTTP, SSH receive (including
untar artifact ingestion), and MCP publishes. The default is `0` (off); starting
serve with `0` clears the stored policy. Absent or empty TTL on unpinned artifacts
uses the default. Explicit `ttl=0` or `ttl=none` means never expires. Sites still
reject TTL and are outside the default policy. Pins are honored only by 3.1+:
the additive default-false database column preserves 3.0 compatibility, and a
pinned artifact has the existing zero/no-expiry sentinel so a 3.0 sweeper leaves
it alone. A 3.0 writer does not enforce pins and can set an expiry again.
In 3.1, such pins remain in owner inventory, can be patched, and are protected from sweeping.

`delete` lists all owner pages, then selects locally. `--match` matches identifiers;
`--older-than` is a positive Go duration measured from publication time;
`--series` matches exactly; `--type` matches a MIME prefix. Filters intersect.
Named slugs are unioned with the filter set. Artifacts are eligible by default;
`--sites` adds sites, and `--all` includes both kinds. `--sites` alone is not a
selector; no selector selects nothing.
Pins are excluded from every selection unless `--include-pinned` is present.
`--all` with other filters still intersects those filters. Sites have no series
or MIME type, so those filters exclude them.

Deletion is a dry run unless `--yes` is supplied. Every filter-based real run,
including `--all`, also requires typing the server hostname interactively or
passing `--confirm-host HOST` (hostname only, without a port). Named-only deletion
needs `--yes` but no host confirmation. A new invocation creates a new preview;
within one invocation the printed revision manifest is exactly what is submitted.
Changed or newly pinned items are skipped atomically, never silently replaced by
new selections. Empty selections print `nothing to delete` and exit 0. Per-item
failures, skips, or missing items exit 1; invalid flags exit 2. Retained sources
are removed with artifacts; a same-named site is independent.

MCP adds `delete_many` (dry run defaults true), `set_ttl`, and `pin`. A real
`delete_many` call requires the returned manifest, including revisions, and
`confirm_host`. `pin` takes `pinned:false` to unpin. Existing MCP `delete` remains
an immediate, single-slug operation. See [API.md](API.md#post-managedelete).

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
| **Two-plane isolation** | Control plane and content origin are separate; served content cannot touch your controls, and each page is sandboxed from the others. |
| **Markdown render** | Opt-in `?render=md` with themes, syntax highlighting, heading anchors, and frontmatter meta-headers. The markdown source is kept, so `POST /rerender` rebakes pages after a theme or renderer change. |
| **Multi-file sites** | Publish a tar/zip bundle and serve the whole tree at one slug. |
| **Live reload** | `?live` streams updates over SSE while you iterate. |
| **SSH ingest** | `demiplane receive` publishes over your existing `sshd` via an `authorized_keys` forced command. |
| **Optional TLS** | Self-signed, ACME, or bring-your-own, via `-tags tls`. |
| **Bounded by default** | Password checks, request bodies and responses, requests and connections in progress, markdown rendering, and replies all have limits; an aggregate storage quota is one flag away (`--store-quota`). See [SECURITY.md](SECURITY.md#resource-limits). |

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
| [`API.md`](API.md) | Full REST reference — every endpoint and parameter, with curl, Python, JavaScript, and Go examples |
| [`docs/deployment.md`](docs/deployment.md) | Binding, reachability tiers, public deployments (base URLs, `--allowed-hosts`), config file, Docker |
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

**Brand assets are excepted.** The marks under `assets/brand/` are Dais & Apex
identity, not software, and are not covered by the AGPL grant — see
[assets/brand/LICENSE](assets/brand/LICENSE). Fork the code freely; replace the
marks with your own.

<sub>Maintained by <strong>Dais &amp; Apex</strong>.</sub>
