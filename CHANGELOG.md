# Changelog

All notable changes to demiplane are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and the project aims to follow
[Semantic Versioning](https://semver.org/) once it reaches a tagged release.

## [Unreleased]

## [1.0.0] - 2026-07-16

**First public release, under the GNU AGPL-3.0.** Everything in this section has
been built, tested, and dogfooded on a private instance hosting real internal
workloads. It consolidates the full v1 feature set — the core HTTP/SSH publishing
API, two-plane origin isolation, the markdown render engine, the optional reply and
TLS modules, and the cross-harness clients — together with the security and design
hardening that gated the release. Subsequent detail entries below are grouped by the
milestone or work item that delivered them.

### Summary of v1.0.0

- **Publishing API** — `POST /publish` (raw body, multipart, and tar/zip/multipart
  multi-file sites), `GET /{slug}`, `GET /list`, `DELETE /{slug}`, `/gallery`,
  `/connect`; friendly `adjective-creature` slugs with named-slug overwrite-in-place.
- **Two-plane origin isolation** — artifact bodies serve from a separate content
  origin so hosted JS cannot read the control plane or drive writes (ADR 0003).
- **Auth** — bearer-token (constant-time) or SSH public-key publish; view auth via
  network, 144-bit capability slugs (`?private=true`), or per-artifact PBKDF2
  passwords set via header.
- **Ephemerality** — `?ttl=` expiry with a background sweeper.
- **Markdown render** (`?render=md`) — house-style themes (light/dark +
  catppuccin/dracula/one-dark), syntax highlighting, heading anchors, frontmatter
  meta-header, per-document colophon.
- **Live-reload** — `?live` SSE wrapper + `demiplane publish --watch`.
- **Optional modules** — inline replies (`reply`) and native TLS (`tls`), both
  compile-time and off in the default binary.
- **Cross-harness clients** — MCP server (`demiplane mcp`), `demiplane publish`
  CLI, `/connect` config page.
- **Licensing** — relicensed to AGPL-3.0-only with a CLA and a commercial-license
  path; SPDX headers on all first-party Go source.
- **Packaging** — static binary on distroless (`Dockerfile`) + CI.
- **Brand** — project mark and favicon. The control plane serves `/favicon.svg`,
  `/favicon.ico`, and `/apple-touch-icon.png`; every rendered page (published
  artifacts, control chrome, and the 404) carries a self-contained data-URI
  favicon so a saved page keeps its tab icon offline. Full icon suite and usage
  in `assets/brand/`.

### Added — design-audit renderer additions (`demiplane-rwj`)

- **Per-document colophon** at the foot of every `?render=md` page, emitted by
  the render template (raw HTML in markdown is escaped by design, so the
  colophon must be renderer-emitted). Two rows: a nav row (`← previous` ·
  position · `next →` · the muted `demiplane` wordmark) and a meta row
  (`published <date>` · size · `all artifacts →` to the instance gallery). The
  metadata is real — the publish timestamp, the rendered document's own byte
  length (which is exactly what the store records), and the gallery link minted
  against the base-URL convention so it stays instance-portable.
- **Series prev/next** computed from the slug's prefix family (the same grouping
  the gallery uses): `dispatch-08` links `dispatch-07`/`dispatch-09` when they
  exist. A singleton prefix shows the meta row alone, no nav. Siblings are the
  already-published artifacts sharing the prefix; a page's colophon is baked at
  publish time, so re-publishing a page refreshes its neighbors.
- **Dependency-free syntax highlighting** (stdlib only) for fenced code in `go`,
  `bash`, `json`, `yaml`, and `python`: a shared, per-language lexer emits
  classed spans (keyword / function / string / comment) colored by four new
  per-theme `--tok-*` tokens (defined for light, dark, catppuccin, dracula, and
  one-dark, mapped to each scheme's canonical syntax palette). Unknown or
  untagged languages render plain, HTML-escaped, as before; every emitted byte
  is escaped, so raw HTML in a code block still cannot inject.
- **Heading anchors** — every rendered heading (`h1`–`h6`) gets a stable, URL-safe
  `id` slugified from its text (lowercased, spaces to hyphens, non-alphanumerics
  stripped, collisions deduped with a numeric suffix; a punctuation-only heading
  falls back to `section`). A quiet hover-`#` permalink is appended to each
  heading, revealed on hover or keyboard focus and reserving its own space so it
  shifts no layout, with a `Permalink to <heading>` `aria-label` for assistive
  tech and its transition disabled under `prefers-reduced-motion`. The scroll
  root gains `scroll-padding-top` so navigating to a `#anchor` clears the sticky
  masthead instead of hiding the target beneath it. The slug alphabet is
  `[a-z0-9-]` and the aria-label is attribute-escaped, so attacker-controlled
  heading text cannot inject into either sink.

### Added — native TLS module (`demiplane-4qq`)

- **In-binary TLS termination for both planes** as an opt-in module (build tag
  `tls`; ADR 0004). Off by default twice over: the default build contains none
  of it, and a tls-tagged binary still serves plain HTTP until the config file
  sets `tls = on`. Three certificate sources, inferred from the keys set and
  refusing ambiguous mixes at startup:
  - **Self-signed (zero-config default):** ECDSA P-256 cert generated on first
    TLS start, SANs derived from the bind addresses (or `tls_hosts = a,b,…`),
    persisted under `<store>/modules/tls/` (key `0600`), reused across
    restarts, auto-regenerated near expiry or on host-set change; SHA-256
    fingerprint logged for pinning.
  - **ACME / Let's Encrypt:** `tls_acme_domains` (+ optional `tls_acme_email`,
    `tls_acme_ca` directory override) via `golang.org/x/crypto/acme/autocert`,
    TLS-ALPN-01 challenge, cache in `<store>/modules/tls/acme/`.
  - **BYO:** `tls_cert` + `tls_key` PEM paths.

  Core stays certificate-ignorant: the module installs a cmd-side listener
  hook (`moduleTLS`) and hands back a `*tls.Config`; minted URLs flip to
  `https://` via the existing `r.TLS` scheme derivation. New dependency:
  `golang.org/x/crypto` v0.53.0 (first-party Go project; used by tls-tagged
  builds only).

### Security — Shannon pass-1 low findings

- **Clickjacking (`demiplane-t0j`):** every response on both planes now sends
  `X-Frame-Options: SAMEORIGIN` and a `frame-ancestors 'self'` CSP.
  Same-origin framing (the `?live` preview wrapper) keeps working; handlers
  that set their own CSP (scriptable non-HTML types, the live wrapper)
  re-state the directive since `frame-ancestors` has no `default-src`
  fallback.
- **Host-header reflection (`demiplane-6y9`):** the base URL derived from the
  request `Host` (llms.txt, publish responses, minted links when `--base-url`
  is unset) is now sanitized through a host-grammar character allowlist, so a
  crafted Host header cannot inject content into text/plain or HTML surfaces.

### Added — reply-event hook + forward flow (`demiplane-tb7`)

- **Reply-event hook.** When a reply is durably recorded (control-plane
  `POST /reply/{slug}` or content-plane `POST /answer/{slug}`), the reply module
  fires a configurable action — an exec'd command (`/bin/sh -c`, reply JSON on
  stdin + `DEMIPLANE_REPLY_{ID,SLUG,KIND,BODY}` env vars) and/or a webhook POST
  of the same JSON. Config keys `reply_hook_exec` / `reply_hook_url` in the
  existing config file (`-tags reply` builds only; malformed values are hard
  startup errors). Dispatch is fire-and-forget strictly after the insert: a
  failing or slow hook is logged and can never fail, delay, or roll back the
  reply write. Lets an external grader react to answers with zero polling.
- **`?next=<slug>` forward flow.** Publish a lesson with
  `?render=md&reply=question&next=<sequel>` and, after a recorded answer, the
  confirmation page carries the student to the sequel once it is published:
  an honest "your next lesson is being prepared" note plus a JS-free
  meta-refresh into the new `GET /answer/{slug}/next?to=<next>` wait endpoint,
  which holds (self-refreshing, back link always present, never claiming
  readiness) until the target slug resolves publicly, then redirects to it.
  The sequel need not exist at publish/answer time; `?next` requires `?reply`,
  must be slug-shaped, and must differ from `?slug=`. Without `?next` the
  confirmation is unchanged.
- **Module config seam (cmd).** Build-tagged module wiring files can register
  config-file keys + a validating applier (`registerModuleConfig`), so
  module-owned keys are recognized exactly when the module is compiled in and
  fail loudly otherwise. See `docs/MODULES.md`.

### Added — first-class inline reply box for Q&A (`demiplane-qrc`)

- **`?reply=question` publish mode.** `POST /publish?render=md&reply=question`
  bakes a first-class, server-rendered reply box into the rendered page — no
  per-page hand-rolled `<form>`. It requires `?render=md` and is rejected with
  `?private` (a private capability page can't collect replies).
- **Same-origin, JS-free submission.** The baked form has no `action`, so it
  posts to the page's **own URL** on the content plane — same-origin by
  construction (no cross-origin/mixed-content failure) and correct once TLS lands.
  A single free-text answer is recorded as a `comment`-kind reply, read back via
  the existing `GET /replies?slug=<slug>` (bearer auth).
- **Honest confirmation by construction.** The content plane's new
  `POST /{slug}` handler renders "✓ Recorded" **only after** the answer is
  durably stored; every failure path renders an explicit "Not recorded" page with
  the matching status. With no client JavaScript, the browser navigates to the
  server's real response — a form post cannot claim success on failure. This
  replaces the dishonest per-page `setTimeout` pattern that lost a student's
  answer while displaying success.
- **`module.ContentRouteModule`** — a new optional module interface lets a
  `RouteModule` mount handlers on the content origin (not just the control plane),
  so a module's same-origin submit path exists wherever artifact bodies are
  served. The reply module implements it. See ADR 0002 (2026-07-14 addendum).

### Added — cross-harness release (v1.2)

The v1.2 layer makes demiplane publish from whatever coding agent, editor, or
shell you already use — native where a protocol exists, one shell command
everywhere else — and adds the iterate-in-place loop.

- **`demiplane mcp` — stdio MCP server.** A Model Context Protocol server over
  JSON-RPC 2.0 on stdin/stdout, so Claude Code, Cursor, Cline, Windsurf, Zed, and
  Continue get native `publish` / `list` / `delete` / `get` tools. It is a thin
  HTTP client of a running instance's control plane (no store/filesystem
  coupling), stdlib-only, and ships in the core build. Config: `demiplane mcp
  --url <control> --content-url <content> --token-file <path>` (or
  `DEMIPLANE_URL` / `DEMIPLANE_CONTENT_URL` / `DEMIPLANE_TOKEN`). The token is
  read from the file/env only — never argv, never echoed; stdout carries only
  JSON-RPC.
- **`demiplane publish <file>` — client CLI.** The universal fallback for any
  harness that can shell out. Posts a file (or stdin) to a running instance,
  prints the URL, and best-effort copies it to the clipboard
  (`wl-copy`/`xclip`/`xsel`/`pbcopy`/`clip.exe`). `--watch` re-publishes to a
  stable slug on every save (mtime poll, ~2/sec cap), `--open` launches a
  browser. Flags: `--url`, `--token-file`, `--slug`, `--private`, `--ttl`,
  `--render`, `--filename`, `--watch`, `--open`; the view password comes from
  `DEMIPLANE_PASSWORD` only (never a flag — argv is world-readable). Note: flags
  must precede the positional file (`publish --url X file.html`).
- **`GET /connect` — onboarding page.** A house-style, copy-paste config page
  served by the running instance, templated with its own base URL, with a block
  per harness (MCP stanza, Claude Code capture hook, bare curl, `demiplane
  publish`, Aider `/run`). The bearer token is shown only as a placeholder path
  plus a local one-liner to read it — the live token is never rendered. Linked
  from the landing page.
- **SSE live-reload (`?live`).** Append `?live` to any artifact view to get the
  artifact wrapped in a minimal first-party shell that opens an SSE stream at
  `GET /_events/{slug}` and reloads the tab when the slug is re-published. The
  wrapper is opt-in and does not mutate stored bytes (plain `GET /{slug}` is
  byte-identical); it carries its own strict CSP with a per-response nonce and
  iframes the artifact same-origin. Subscribers are capped per-slug and globally
  to bound FD/memory. Pair with `demiplane publish --watch` for an
  edit-save-see loop.
- **Multi-file site publish (`POST /publish?site=<name>`).** Ingests a tar, zip,
  or multipart bundle and serves the whole tree: `GET /{name}/` returns
  `index.html`, `GET /{name}/{path}` returns each asset with the right
  content-type, so relative links resolve. Beats a single-file host — a whole
  static site lives at one slug.
- **`GET /gallery` — artifact index.** A searchable, house-style card index of
  non-private artifacts (title/slug, type badge, size, published + expiry,
  copy-URL, password lock icon) with a dependency-free client-side filter.
  Private and expired artifacts are filtered server-side by the same `List`
  guard.

### Security — v1.2 new-surface hardening

Adversarial review of the new surfaces (MCP stdio, SSE, multi-file archive
ingest, `/connect`, publish CLI) found and fixed three issues before release:

- **Archive-bomb defense extended to tar (high).** `POST /publish?site=`'s tar
  path had no decompressed-byte budget, so a PAX/GNU **sparse** tar (regular-file
  typeflag) could expand from a ~10 KB upload to attacker-declared gigabytes of
  zero-fill, bypassing both `--max-upload` and the decompression cap (which was
  wired only into the zip path). The tar extractor now enforces the same total
  decompressed-bytes budget as zip; an oversize expansion is rejected with `413`.
- **MCP `publish` refuses the token file (token-exfil guard).** The MCP `publish`
  tool's `path` argument is model-chosen, so a prompt-injected model could point
  it at the bearer-token file and publish the token to a world-readable slug. The
  tool now refuses to read-and-publish the configured token file (compared on the
  fully symlink-resolved absolute path) and refuses non-regular files (symlinks,
  devices), so `path` cannot follow a link to a secret. The MCP HTTP client also
  no longer follows redirects (the control plane never legitimately 3xx's),
  closing any redirect-based header forwarding.
- **`demiplane publish` no longer follows redirects (password-leak guard).** The
  CLI used the default HTTP client, which follows redirects; Go strips the
  `Authorization` header across hosts but **not** the custom
  `X-Demiplane-Password` header, so a malicious/compromised target could redirect
  the request and capture the view password. The CLI client now refuses
  redirects (the publish endpoint answers `201`/`4xx`, never `3xx`).

### Security — pre-release hardening (Shannon white-box pentest)
- **Stored-XSS hardening on served artifacts.** Every response now sends
  `X-Content-Type-Options: nosniff`, and SVG/XML/XHTML artifacts are served with
  `Content-Security-Policy: script-src 'none'` so a published image/document can't
  execute embedded `<script>` at the instance origin. (Executable HTML *pages*
  remain a supported feature — demiplane is a page host.)
- **`GET /list` no longer returns private artifacts.** A private artifact's
  capability slug is its secret; `List` now filters `private` rows so the slug is
  never enumerable via the API (matching the already-non-private `--browse` page).
- **Bounded upload default.** `--max-upload` now defaults to **100 MiB** on both
  `serve` and `receive` (was unlimited), so a fresh install is not a trivial
  disk-fill target. `--max-upload=0` restores unbounded streaming as an explicit
  opt-out. Closes the manual-audit finding that the SSH forced-command and the
  no-token HTTP path had no default cap.
- **Markdown open-redirect fixed.** `safeURL()` treated protocol-relative URLs
  (`//host`) as safe same-origin relative refs, so `[text](//attacker.com)` in a
  published document produced an off-site link. Such URLs are now rejected.
- **Reply form no longer leaks private artifacts.** `GET /reply/{slug}` used a
  bare existence check that ignored `private`, so a private capability slug's
  existence was observable via a 200-vs-404 oracle. It now uses `HasPublic` — the
  same `private`+expiry filter as `GET /list` — so private and nonexistent slugs
  are indistinguishable (both `404`).
- **Private is sticky across re-publish.** In no-auth mode, `POST /publish?slug=`
  a known capability slug could upsert `private=0` and surface the artifact in
  `GET /list`. The `private` bit is now latched (`MAX(old, new)`) on same-slug
  overwrite; changing privacy requires an explicit delete + re-publish.

### Added — Modules & artifacts publisher (epic `rox`)
- **Module extension seam** (`internal/module`): a compile-time, Caddy-style
  registry (interface + `register()` from `init()`, build-tag inclusion — no
  runtime `plugin`) so capabilities are optional modules, not core bloat. The
  default core stays tiny: publish / serve / auth / TTL / slugs.
  See [ADR 0001](./docs/adr/0001-module-extension-pattern.md) and
  [docs/MODULES.md](./docs/MODULES.md).
  The seam offers one capability shape, `RouteModule` (HTTP-route modules); a
  `PublishTransform` body-rewrite shape was prototyped with markdown render but
  removed — render's theming is a shared instance setting, so it stays in core
  (see the ADR 0001 update). The inline-reply module is the seam's first module.
- **Artifact-capture hook** (`companion/capture-hook/`): a client-side Claude
  Code `PostToolUse` hook (and publish CLI) that POSTs self-contained HTML
  artifacts to your instance, so agent-generated pages land on the mesh, not a
  public host. A companion, not a server module — the API is unchanged.
- **Inline-reply module** (`internal/modules/reply`, opt-in build tag `reply`):
  viewers respond to a page (Approve / Defer / free-text) and an agent lists/acks
  replies. Submit is mesh-only; reading is bearer-gated (no new auth primitive);
  storage is module-owned and isolated. New endpoints `GET|POST /reply/{slug}`,
  `GET /replies`, `POST /replies/{id}/ack`. Core's API/security model unchanged.
  See [ADR 0002](./docs/adr/0002-inline-reply-module.md).

### Changed — Licensing
- **Relicensed from MIT to GNU AGPL-3.0-only** ahead of the public release.
  demiplane stays fully open source and self-hostable for free; the AGPL's
  network copyleft means cloud resellers must share their modifications or obtain
  a commercial license. (Sole-author relicense — no external contributors.)
- Added a **Contributor License Agreement** ([CLA.md](./CLA.md)) and a
  **commercial / dual-license** note ([COMMERCIAL-LICENSE.md](./COMMERCIAL-LICENSE.md));
  `CONTRIBUTING.md` now requires accepting the CLA.

### Changed — rendering house style (demiplane-7fp)
- `?render=md` now renders user content in demiplane's **house style by default**
  — the same design tokens and typography as the `/docs` pages — instead of the
  old bare system-sans stylesheet. The stylesheet lives in one place
  (`internal/theme`), shared by the page chrome and the markdown renderer, so the
  two can't drift.
- New `serve --theme light|dark` (built-in themes; default is the house style)
  and `serve --css <file>` (a custom stylesheet that replaces the built-in theme,
  for self-hosted branding).
- `--theme` skins the **whole instance**: the chrome (`/`, `/docs`, landing) and
  rendered `?render=md` content honor it together, so `--theme dark` darkens
  everything as one setting. (`--css` rebrands only user content; the chrome
  stays on a built-in theme.)

### Added — rendered-page chrome + config file (demiplane-umw)
- `?render=md` pages now get a **sticky title header** (the document's first
  `# H1`, lifted into a masthead — no duplicate giant title; falls back to the
  slug), an in-header **client-side light/dark toggle** (ships both token sets
  inline, persists the choice in `localStorage`, initializes from
  localStorage → server `--theme`/config → OS `prefers-color-scheme`, respects
  `prefers-reduced-motion`), and a **"Generated by demiplane"** vanity footer
  linking the repo by default.
- New `serve` flags `--header`/`--footer` (default on) and `--footer-link <url>`.
- New **config file** at `${XDG_CONFIG_HOME:-~/.config}/demiplane/config`
  (stdlib `key = value` parser, no new dependency). Keys: `footer`, `footer_link`,
  `theme`, `header`, `meta_header`. **Precedence: CLI flag > config file > built-in default.**
  Missing file → all-defaults; malformed file/unknown key → clear startup error.

### Added — frontmatter meta-header (demiplane-7fp)
- `?render=md` now lifts a leading **YAML frontmatter** block (`--- … ---` of
  minimal `key: value` lines — stdlib only, no YAML dependency) into a styled
  **meta-header** below the masthead title, replacing the old bold run-on first
  line. A `date`/`published` field renders as a localized timestamp
  (`YYYY-MM-DD · HH:MM UTC · HH:MM <localtz>`; the UTC text is server-rendered and
  a tiny client script appends the viewer's local time + zone). Every other field
  becomes its own labeled line (title-cased label, body-ink value). Frontmatter is
  consumed by the header, never shown in the body.
- New `serve --meta-header` (default on) / config `meta_header = on|off`. Off
  strips frontmatter without rendering a header. A document with no frontmatter is
  unaffected. A leading `---` with no closing fence stays a horizontal rule.
- Fixed: the masthead title clipped letter descenders (the "g" in a title like
  "Overnight"); the `.doctitle` now has a roomier line-height and bottom padding.

### Changed — refined-editorial theme pass (demiplane-7fp)
- Rebuilt both base render themes (light + dark) to a refined-editorial standard.
  Tokens are now **OKLCH**, every neutral warm-tinted toward the parchment hue,
  no pure black or white anywhere; the dark base is a warm dark parchment rather
  than near-black. Editorial type scale (17px base, ~1.25 modular headings,
  capped ~68ch measure, varied rhythm). The token system is documented in
  [DESIGN.md](./DESIGN.md).
- Rendered-page chrome refined: editorial masthead (small-caps kicker + display
  serif title + hairline) with comfortable lead-in; the first paragraph is set
  as a quiet lead so a dense metadata line reads as a subtitle; a crisp inline
  **sun/moon SVG** replaces the toggle glyph; a thin single-line footer; refined
  code blocks, tables (horizontal rules + subtle zebra), blockquotes, and links.
  Micro-interactions are ease-out-expo and never touch layout; all respect
  `prefers-reduced-motion`.

### Added — M1 (core)
- Flat-file content store + pure-Go SQLite metadata (`modernc.org/sqlite`),
  with an `owner` column from day one for additive multi-user later.
- `POST /publish` streams the body to disk (never buffered); accepts raw bodies
  and `multipart/form-data`; infers content-type by extension then sniffing,
  serving known web types inline and others as attachments.
- Friendly word-based slugs (`adjective-adjective-noun`) by default; named slugs
  via `?slug=` overwrite in place.
- `GET /{slug}` serving via `http.ServeContent`; `serve --bind/--store/--base-url`.

### Added — M2 (HTTP API)
- `DELETE /{slug}` and `GET /list` (JSON, owner-scoped).
- Bearer-token auth on publish/delete/list (`--token-file` / `DEMIPLANE_TOKEN`);
  `GET /{slug}` stays open (view-auth is a separate layer).

### Added — M3 (internal-first)
- `--bind` defaults to loopback; wildcard binds log a warning.
- Per-artifact privacy (`?private=true` → 144-bit capability slug).
- Optional view password (PBKDF2-HMAC-SHA256), set via `X-Demiplane-Password`,
  read via HTTP Basic.
- TTL/expiry (`?ttl=30m|2h|7d`) with lazy 404 + a background sweeper.

### Added — M4 (SSH transport)
- `demiplane receive` subcommand: pubkey-auth publish over the host's `sshd`
  using an `authorized_keys` forced command — single-file pipe and tar-stream
  directory sync into the same store. No new dependency.

### Added — M5 (extras)
- Opt-in markdown→HTML render on publish (`?render=md`); dependency-free
  CommonMark subset, HTML-escaped with a link-scheme allowlist.
- Opt-in browse page (`serve --browse`) listing non-private artifacts at `GET /`.

### Added — M6 (packaging + hardening)
- Dockerfile (static binary on distroless), CI workflow, `version` subcommand.
- `--max-upload` (returns `413`), `--write-timeout`, `--idle-timeout`.

### Security
- Reject the incoherent `private` + named-slug combination (store-level guard,
  covers HTTP and SSH ingest).
- View password moved out of the URL query (logged surface) to a header.
- TOCTOU-safe TTL reaping (predicate-guarded deletes) so a re-published artifact
  is never destroyed by the sweeper.
- Markdown renderer escapes all source and allowlists link schemes
  (`javascript:`/`data:`/`vbscript:` and unknown schemes dropped).
- Error logs redact slugs (a capability slug is a secret).
- Bearer-token comparison is constant-time **and** length-hiding (HMAC).

### Fixed / Hardened — pre-public review
- Docker named volume now nonroot-owned so the `docker run -v` quickstart works.
- `?render=md` returns `413` (not `500`) when `--max-upload` is exceeded.
- SSH `receive` honors `--max-upload` (fails loudly past the cap, no partial store).
- Container base images pinned by `@sha256` digest (Dockerfile + CI).
- Store directories created `0700` so other local users can't read blobs off disk.
- SSH docs corrected: removed the non-functional `--` flag passthrough (demiplane
  ignores `SSH_ORIGINAL_COMMAND` by design); `authorized_keys` example uses `restrict`.
