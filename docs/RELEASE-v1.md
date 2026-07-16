# demiplane v1

What v1 actually is, why it exists, and what it does and does not promise. Honest,
dogfood-first — not a marketing page.

## What v1 is

demiplane v1 is a **single Go binary** that publishes HTML and files to your own
infrastructure and hands back a link only your network can reach. No database
server, no runtime dependencies, no external services. Drop it on a box, point it
at a store directory, bind an address, and `POST` files to it.

It is **internal-first**: out of the box it binds `127.0.0.1` and is reachable by
nothing but the local host. You opt into exposure by binding a LAN or mesh IP. The
design assumption is a trusted network (a home lab, a Netbird/Tailscale mesh, a
handful of colleagues on a shared host), not the open internet.

## The dogfood story

v1 was not built to a spec and then released. It has been **hosting real internal
workloads throughout its own development**:

- Recurring long-form documents published to a stable slug and re-read over a mesh
  — the exact "send a link, not a file" workflow that motivated the project.
- Agent-generated HTML artifacts (reports, dashboards, research) captured straight
  off a coding-agent harness via MCP and the capture hook, landing on a private
  instance instead of a public host.
- Rendered markdown lesson pages with the inline reply box, used as a lightweight
  question-and-answer surface with the forward-flow (`?next=`) auto-advance.

Every feature in v1 shipped because it removed real friction in one of those
loops, then stayed because it kept working. Nothing here is speculative surface.

## v1 scope

Built, tested, and in daily use:

- **HTTP API** — `POST /publish`, `GET /{slug}`, `GET /list`, `DELETE /{slug}`,
  plus `/gallery` and `/connect`. Raw-body, multipart, and multi-file
  (tar/zip/multipart) site uploads.
- **Two-plane origin isolation** — the control plane and the artifact content
  origin are separate origins by default, so hosted JS cannot read the API or
  drive writes (ADR 0003).
- **Publish auth** — bearer token (HTTP) or SSH public key (`demiplane receive`
  via the host's `sshd`), constant-time comparison.
- **View auth** — network reachability, unguessable 144-bit capability slugs
  (`?private=true`), and optional per-artifact passwords (PBKDF2-HMAC-SHA256, set
  via header).
- **TTL / expiry** — `?ttl=` with a background sweeper; expired URLs 404
  immediately.
- **Markdown render on publish** (`?render=md`) — a dependency-free CommonMark
  subset in demiplane's house style: named themes (`light`/`dark` plus pinned
  `catppuccin`/`dracula`/`one-dark`), syntax highlighting, heading anchors, a
  frontmatter meta-header, and a per-document colophon.
- **Live-reload** — `?live` wraps an artifact in an SSE shell that reloads on
  re-publish; pairs with `demiplane publish --watch`.
- **Inline replies** (optional `reply` module) — JS-free reply boxes on rendered
  pages, an admin read API, a reply-event hook, and `?next=` forward flow.
- **Native TLS** (optional `tls` module) — in-binary termination with self-signed,
  ACME, or bring-your-own certificates; plain HTTP until explicitly enabled.
- **Cross-harness clients** — a stdio MCP server (`demiplane mcp`), a
  `demiplane publish` CLI, and a live `/connect` config page.
- **Packaging** — a static binary on a distroless base (`Dockerfile`) and CI
  (build/vet/gofmt/test).

## Out of scope for v1 (non-goals)

- **Multi-tenant access control.** v1 is single-operator: one credential, one
  `owner`. The schema carries an `owner` column so multi-user is additive later
  without a migration, but there is no per-user isolation today.
- **Rate limiting / quotas.** There is an upload size cap (`--max-upload`) but no
  global rate limiting or per-owner storage quota. Rely on the network boundary or
  a fronting proxy.
- **A public, multi-tenant host.** demiplane is deliberately the *inverse* of a
  public paste service. It is not designed to be exposed as an open host for
  untrusted publishers.
- **A managed cloud.** Everything here is self-host, free, and fully featured
  under AGPL-3.0. A managed cloud (team/SSO/compliance) is a possible future
  funding path; it does not exist yet and nothing in v1 depends on it.

## Known limitations

- **Plain-HTTP confidentiality.** Without the TLS module or a fronting proxy,
  bearer tokens, HTTP Basic passwords, and capability slugs travel in the clear.
  Passwords and capability URLs are only meaningful behind TLS or on a trusted
  network — treat them as defense-in-depth, not the primary control on an exposed
  plain-HTTP port. See [SECURITY.md](../SECURITY.md).
- **Single content origin.** Origin isolation separates hosted JS from the control
  plane, but one content origin does not isolate one artifact's JS from another's.
  Per-slug subdomains are future hardening. `--unsafe-same-origin` collapses both
  planes and re-opens the stored-XSS footgun — avoid it unless you fully control
  the content.
- **Cookies are not origin-isolated by port alone.** Harmless today (demiplane is
  stateless); a future sessioned mode must move the content origin to a separate
  hostname (`--content-base-url`).
- **No built-in backup/replication.** The store is flat files plus a SQLite
  metadata DB; back it up like any other stateful directory.

## Versioning

v1.0.0 is the first tagged, public release. Changes are recorded in
[CHANGELOG.md](../CHANGELOG.md) following Keep a Changelog; the project follows
Semantic Versioning from this release forward.
