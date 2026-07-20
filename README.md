<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/brand/mark-dark.png">
    <img src="assets/brand/mark-light.png" alt="demiplane" width="88" height="88">
  </picture>
</p>

# demiplane

**Your private publishing plane.**

Self-hosted, internal-first HTML & file publishing with a REST API. You `POST` a file to
**your own** server and get back a link only your network (LAN/mesh) can reach. Your
content never leaves infrastructure you own — it's the honest inverse of public paste and
host services, not the predator model where *they* host *your* data.

A demiplane is your own private pocket dimension: a sealed space that's *yours*. This is
HTML/file hosting for that model — single-command publish, an instant URL, and the bytes
stay on a box you control.

> **Status: v1.0 — first public release.** A single Go binary with the full HTTP API
> (`POST /publish`, `GET /{slug}`, `GET /list`, `DELETE /{slug}`), bearer-token auth,
> loopback-by-default binding, two-plane origin isolation, per-artifact privacy
> (capability slugs), view passwords, TTL/expiry with a background sweeper, **SSH ingest**
> (`demiplane receive`), **opt-in markdown render** (`?render=md`) with named themes,
> syntax highlighting, and per-document colophons, an **MCP server** (`demiplane mcp`), a
> **`demiplane publish` CLI**, a **`/connect`** onboarding page, **SSE live-reload**
> (`?live`), **multi-file site publish** (`POST /publish?site=`), a **`/gallery`** index,
> and an optional in-binary **TLS module**. See [CHANGELOG.md](./CHANGELOG.md).

## Why

demiplane exists because of a two-step that shouldn't exist. I sent my CEO a report as
an HTML file — the kind agent harnesses produce all day now — and he had to download it,
find it, and open it in a browser before he could read a single line. He didn't want a
file; he wanted a link. Markdown attachments have the same disease with worse formatting.
The gap: rendered HTML, shareable as a URL, without handing the content to someone
else's host. So: `POST` the file to your own box, send the link, done.

Public HTML hosts ("POST your page, the world can see it") are convenient but make
*them* the host of *your* data. demiplane keeps the convenience — single-command publish,
instant URL — while keeping the data on a box you own, reachable only over your own network.
It matches the feature set of [pico.sh's `pgs`](https://pico.sh) (static hosting, single-file
publish, per-artifact privacy) and adds what `pgs` deliberately omits: a **first-class HTTP
API**, **internal-first defaults**, and **TTL/expiry** — with **zero pico dependency**.

## Works with your harness

demiplane publishes from whatever coding agent, editor, or shell you already use.
The running instance serves a live, copy-paste config page at **`GET /connect`** —
open it and grab the block for your tool. The snippets below are the same ones,
templated with the instance's own base URL and a token *file path* (never the
token itself).

| Harness | How it connects | Verified |
|---|---|---|
| Claude Code | MCP (native `publish`/`list`/`get`/`delete` tools) or the capture hook | Yes — tested and used daily |
| Any MCP client | MCP (standard stdio JSON-RPC server) | Not yet verified |
| Aider | shell `/run` + curl | Yes — the curl path is tested |
| Any shell / CI | `curl` or `demiplane publish` | Yes — REST and CLI tests |

demiplane speaks plain MCP and plain HTTP, so the set of clients it works with is
larger than the set we verify; the table says which is which. If you get it running
in another MCP client, open an issue and we will add it.

**MCP.** `demiplane mcp` is a standard stdio JSON-RPC MCP server — one stanza, and
only the config file location differs per client (its own docs name that path):

```json
{
  "mcpServers": {
    "demiplane": {
      "command": "demiplane",
      "args": ["mcp", "--url", "http://demiplane.your-mesh:8891", "--token-file", "~/.config/demiplane/token"]
    }
  }
}
```

Claude Code can add it for you:

```sh
claude mcp add demiplane -- demiplane mcp --url http://demiplane.your-mesh:8891 --token-file ~/.config/demiplane/token
```

**Bare curl** — the universal fallback, works in any harness that shells out:

```sh
curl --data-binary @index.html http://demiplane.your-mesh:8891/publish
# add -H "Authorization: Bearer $(cat ~/.config/demiplane/token)" if the instance requires auth
```

**CLI** — `demiplane publish` wraps the same call, copies the URL to your
clipboard, and can `--watch` a file for live-reload:

```sh
demiplane publish --url http://demiplane.your-mesh:8891 --token-file ~/.config/demiplane/token index.html
```

The token is read from `--token-file`/`$DEMIPLANE_TOKEN` **locally** and is never
placed on a command line, in a URL, or on the `/connect` page. See
[`companion/README.md`](./companion/README.md) for the full harness matrix and the
Claude Code capture hook.

## HTTP interface

```
POST   /publish                 # body or multipart → writes file, returns a URL   [auth]
POST   /publish?site=<name>     # tar/zip/multipart of a multi-file site           [auth]
GET    /{slug}                  # serve the artifact as-is, correct content-type    (open)
GET    /{slug}?live             # same artifact wrapped in a live-reload shell      (open)
GET    /{site}/                 # serve a published site's index.html               (open)
GET    /{site}/{path...}        # serve an asset inside a published site            (open)
GET    /_events/{slug}          # SSE stream: emits `reload` when the slug changes  (open)
GET    /list                    # list your published artifacts (JSON)             [auth]
GET    /gallery                 # house-style card index of non-private artifacts   (open)
GET    /connect                 # copy-paste harness config for this instance       (open)
DELETE /{slug}                  # remove an artifact                               [auth]
```

The write/list/delete/`/connect`/`/gallery` routes live on the **control plane**;
the artifact-body routes (`GET /{slug}`, sites, and the SSE `/_events` stream)
live on the separate **content origin** (see **Deployment** and
[ADR 0003](./docs/adr/0003-content-origin-isolation.md)). `--unsafe-same-origin`
collapses both onto one listener for local use.

`POST /publish` query parameters (all optional):

| Param | Effect |
|---|---|
| `?slug=<name>` | Named, stable endpoint that **overwrites in place** (else a slug is generated). |
| `?private=true` | Mark private; auto-generates a **high-entropy capability slug** instead of a friendly one. Cannot be combined with `?slug=` (a named slug is guessable, so it can't be private) — that combination is rejected with `400`. |
| `?ttl=<dur>` | Auto-expire after a duration — `30m`, `2h`, `7d` (days), or any Go duration. |
| `?render=md` | Render the markdown body to a styled HTML page on publish (opt-in). |
| `?reply=question` | Bake a same-origin, JS-free inline reply box into the rendered page (needs `?render=md` and the `reply` module). |
| `?next=<slug>` | Forward flow (needs `?reply`): name the page that follows this one. After a recorded answer, the confirmation waits for `<slug>` to be published and then carries the reader there. It need not exist yet and must differ from `?slug=`. |
| `?filename=<name>` | Extension hint for content-type on raw-body uploads. |

The view **password** is set on publish via the **`X-Demiplane-Password` header**, never
a query parameter — query strings leak into access/proxy logs, history, and `Referer`.
Supplying `?password=` is rejected with `400`. Reads supply it via HTTP Basic (below).

`[auth]` endpoints require the bearer token when one is configured (see **Auth model**);
`GET /{slug}` is always open at the transport layer — read protection is the view-auth
layer (network / capability URL / per-artifact password), not the publish token.

- **Slugs:** a **friendly, word-based slug** by default — `adjective-creature`
  (D&D-themed, e.g. `shadow-specter`, `radiant-owlbear`), drawn from an embedded wordlist,
  collision-checked with a numeric-suffix fallback. Memorable and typeable for one-shot `index.html`
  drops where you want a URL you can read back over the phone. Pass `?slug=<name>` for
  a **named, stable endpoint** — re-publishing to a named slug **overwrites in place**,
  so e.g. `…/reports` always serves the latest. Ideal for auto-generated content:
  agents always publish to the same name, you bookmark it once.
  - **Entropy caveat:** friendly slugs are *low-entropy* — fine as the trusted-network
    default, but **not** a capability-URL secret. For an unguessable "anyone with the
    link" URL, publish with `?private=true`, which generates a **144-bit capability
    slug** (the view-auth tier below).
- **Transports:** HTTP API *and* SSH (a true superset of `pgs`). The SSH path reuses the
  host's `sshd` via an `authorized_keys` forced command (`demiplane receive`) — pubkey auth,
  single-file pipe, and tar-stream directory sync, all into the same store (see **SSH transport**).
- **File types:** accepts *any* file. Known web types (HTML/CSS/JS/images/PDF/…) serve
  **inline**; unknown types serve as `Content-Disposition: attachment` (download). No
  type allowlist.
- **Limits:** uploads are capped at 100 MiB by default (`--max-upload <bytes>`; set
  `--max-upload=0` for unbounded streaming to disk — bounded memory either way);
  per-store/per-owner **quotas** arrive with the public-hardening scope.
- **Ephemeral:** optional `?ttl=` so artifacts (e.g. one-off reports) expire automatically;
  a background sweeper reaps past-TTL files and an expired URL 404s immediately.
- **Optional render:** `?render=md` converts a markdown body to a styled HTML page on
  publish (a dependency-free CommonMark subset; source is HTML-escaped and dangerous link
  schemes are dropped). Rendered pages use demiplane's **house style by default** — the
  same design tokens and typography as these docs, no config needed. Rendered pages get a
  **sticky title header** (the document's first `# H1`), an in-header **light/dark toggle**
  (client-side, remembered per device), **dependency-free syntax highlighting** for fenced
  `go`/`bash`/`json`/`yaml`/`python` code, **stable heading anchors** with hover-`#`
  permalinks, and a **per-document colophon** (prev/next within a slug series, publish
  date, size, and a link to the gallery) above a small **"Generated by demiplane"** footer.
  A leading **YAML frontmatter** block (`--- … ---` of `key: value` lines) is lifted into a
  **meta-header** below the title: a `date`/`published` field renders as a localized
  timestamp (`YYYY-MM-DD · HH:MM UTC · HH:MM <localtz>`, localized client-side), and every
  other field becomes its own labeled line. `serve --meta-header=false` (or `meta_header =
  off` in the config) strips frontmatter instead.
  `serve --theme dark` switches the **whole instance** — chrome (`/`, `/docs`) and rendered
  content — to a built-in dark theme, and `--theme catppuccin|dracula|one-dark` selects one
  of the named developer palettes (dark-only, so they pin their look and drop the light/dark
  toggle); or point `serve --css <file>` at your own stylesheet to rebrand rendered content. The header/footer and the default theme are configurable via
  flags or `~/.config/demiplane/config` (see **Config file** below). An opt-in **browse
  page** (`serve --browse`) lists
  non-private artifacts at `GET /` — private/capability artifacts are never listed.
- **Gallery.** `GET /gallery` renders a searchable, house-style card index of the
  same non-private artifacts (title/slug, type badge, size, published + expiry,
  copy-URL button, and a lock icon for password-gated items). The client-side
  filter is a dependency-free inline script; private and expired artifacts are
  filtered server-side by the same `List` guard, never reflected.
- **Multi-file sites.** `POST /publish?site=<name>` ingests a **tar, zip, or
  multipart** bundle and serves the whole tree: `GET /{name}/` returns
  `index.html` and `GET /{name}/{path}` returns each asset with the right
  content-type, so relative links between pages/CSS/JS resolve. Every entry path
  is traversal-checked (`..`, absolute paths, and backslashes are rejected),
  symlink/irregular entries are skipped, and the archive is bounded on **both**
  the compressed upload (`--max-upload`) and the **total decompressed bytes**
  (archive-bomb defense) plus a file-count cap. This beats a single-file host: a
  whole static site lives at one slug.
- **Live-reload.** Append `?live` to any artifact view (`GET /{slug}?live`) to get
  the artifact wrapped in a minimal first-party shell that opens an SSE stream at
  `GET /_events/{slug}` and reloads the tab when the slug is re-published. The
  wrapper is opt-in and **does not mutate stored bytes** — the plain
  `GET /{slug}` is byte-identical to what you published. Pair it with
  `demiplane publish --watch <file>` for an edit-save-see loop with no manual
  refresh. The shell carries its own strict CSP (a per-response nonce; the
  artifact renders in a same-origin iframe).

## Auth model

Two distinct layers, deliberately separate:

- **Publish auth** (who can write): bearer token for the HTTP API; SSH public key for the
  rsync/scp transport. Not passwords.
  - **Configured via** `--token-file <path>` or the `DEMIPLANE_TOKEN` env var
    (`--token-file` wins). The token gates `POST /publish`, `DELETE /{slug}`, and
    `GET /list` — sent as `Authorization: Bearer <token>`; missing/wrong → `401`.
    Comparison is constant-time.
  - **When no token is set**, those endpoints run **open** and the server logs a loud
    startup warning. This keeps the zero-config trusted-LAN/mesh dogfood frictionless;
    set a token the moment the instance is reachable by anything you don't trust. (A
    fail-closed `--require-auth` is a candidate for a later hardening pass.)
- **View auth** (who can read an artifact), increasing strength:
  1. **Network** (default) — it's on your LAN/mesh; that's the protection.
  2. **Capability URL** (`?private=true`) — a 144-bit unguessable slug *is* the secret
     ("anyone with the link"). The friendly default slug is memorable, not unguessable;
     `?private=true` is the opt-in high-entropy mode.
  3. **Per-artifact password** — a gate on top of the link. Set on publish via the
     `X-Demiplane-Password` header (not the URL); the reader supplies it via HTTP Basic
     (`curl -u any:<pw>`; the username is ignored).

Password rules: stored **hashed** — salted **PBKDF2-HMAC-SHA256** (stdlib `crypto/pbkdf2`,
600k iterations), never plaintext. A password is only meaningful over **TLS** — in
plain-HTTP/IP-only mode, HTTP Basic is effectively cleartext, so password protection is
real only behind a TLS front or on a trusted network. Treat it as defense-in-depth on top
of network/capability protection, not as the primary control on an exposed plain-HTTP port.

**Users:** v1 is **single-operator** (one credential; all artifacts belong to the instance).
The schema carries an `owner` column from day one (defaulted to a single local owner) so
**multi-user is additive later** — per-owner listing/delete and optional URL namespaces —
without a migration.

> **Origin isolation (built in):** serving arbitrary HTML/JS means that JS runs in the
> serving origin and could touch same-origin endpoints. demiplane closes this by serving
> artifacts from a **separate origin** from the control plane (the githubusercontent.com
> model): `/publish`, `/list`, and `DELETE` live on the control listener (`--bind`, default
> `127.0.0.1:8080`); artifact bodies (`GET /{slug}`) are served on a second listener
> (`--content-bind`, default the same host with **port +1** → `127.0.0.1:8081`). Hosted JS
> therefore runs cross-origin to the API and cannot read `/list` or drive writes (a
> cross-origin write guard backs this up). `--unsafe-same-origin` collapses both onto one
> origin (the legacy footgun) if you accept it. See [ADR 0003](./docs/adr/0003-content-origin-isolation.md).

## Deployment

demiplane is **domain-agnostic**: it *binds* to an address and *advertises* a base URL
(`--base-url`); how that URL is reached is a deployment choice, not the binary's job.

**Internal-first by default:** `--bind` defaults to **`127.0.0.1:8080`** (loopback only) —
out of the box demiplane is not reachable from another machine. You **opt into** exposure
by binding a specific LAN/mesh IP (e.g. a Netbird address) or `0.0.0.0`. Binding a
wildcard/all-interfaces address logs a startup warning (and a second one if no token is
set), so an accidental public exposure is loud, not silent.

**Primary v1 path — single binary in an LXC, reached by mesh/LAN IP:**

```sh
# bind the host's mesh (or LAN) IP explicitly — loopback is the default
demiplane serve --bind 203.0.113.7:8080 --store /var/lib/demiplane
# …or all interfaces on a trusted segment:
demiplane serve --bind 0.0.0.0:8080 --store /var/lib/demiplane
# from any device on the same network/mesh:
curl --data-binary @report.html http://203.0.113.7:8080/publish    # → a URL
```

No DNS, no TLS, no proxy required. Drop the binary on any box, bind its mesh/LAN IP, hit it.
(`--base-url` is optional — the URL is derived from the request `Host`; set it only if you
want a canonical URL regardless of how the publisher connected.)

**Reachability tiers** (a *deployment* decision):

| Tier | Example | DNS | TLS | Note |
|---|---|---|---|---|
| Raw IP:port | `192.168.1.50:8080` | none | none | **v1 default**; trusted LAN/mesh |
| Local/private DNS | `host.demiplane.example` | private | self-signed | **avoid for browsers** — DoH bypasses local resolvers, names often won't resolve |
| Custom domain → private IP | `reports.example.com` | public A → private IP | real (DNS-01) | browser-safe + valid cert + still private; put a reverse proxy (e.g. Caddy) in front and pass the matching `--base-url` |

demiplane never touches DNS, and terminates TLS only when you opt into the native TLS
module (build tag `tls` + `tls = on`; see **Native TLS** below) — otherwise a custom domain
is just a reverse proxy in front plus `--base-url`. **Do not trust
`X-Forwarded-Host`/`-Proto`** for URL generation; use explicit `--base-url` when set, fall
back to `Host` only when unset.

**Two origins (origin isolation):** demiplane listens on **two** ports by default — the
control plane (`--bind`, `:8080`) and the artifact content origin (`--content-bind`,
`:8081`). Artifact URLs from `/publish` and `/list` point at the content origin. Behind a
reverse proxy or with TLS, route **both** upstreams and declare the public content origin
with `--content-base-url https://content.example.com` (this is where the TLS path plugs in;
a separate content **hostname** also gains cookie isolation, which ports alone do not). To
run the old single-port topology, pass `--unsafe-same-origin`. See
[ADR 0003](./docs/adr/0003-content-origin-isolation.md).

## SSH transport

demiplane does **not** run its own SSH server. Instead it reuses the host's `sshd`: you
pin a forced command in `~/.ssh/authorized_keys`, and `sshd` does the public-key auth.
The forced command runs `demiplane receive`, which streams the artifact from the SSH
channel (stdin) into the **same store** the HTTP server uses — no second host key, no extra
auth surface, no new dependency.

demiplane **ignores `SSH_ORIGINAL_COMMAND`** — the publisher cannot pass flags or commands
over the wire, which removes any SSH argument-injection surface. Instead, **each
`authorized_keys` entry bakes its own `receive` flags into the `command=`**, and the
`restrict` option (modern OpenSSH; implies `no-pty`, `no-port-forwarding`,
`no-agent-forwarding`, `no-X11-forwarding`, `no-user-rc`) locks the key to publishing only.
Use one key per publishing mode:

```sh
# A "drop" key — random friendly slug per publish:
restrict,command="demiplane receive --store /var/lib/demiplane --base-url https://demi.example" ssh-ed25519 AAAA... drop

# A "reports" key — always overwrites the same named slug:
restrict,command="demiplane receive --store /var/lib/demiplane --base-url https://demi.example --slug reports" ssh-ed25519 AAAA... reports

# A "sync" key — tar-stream directory sync:
restrict,command="demiplane receive --store /var/lib/demiplane --base-url https://demi.example --untar" ssh-ed25519 AAAA... sync

# then, with a matching Host alias per key in the publisher's ~/.ssh/config:
echo '<h1>hi</h1>' | ssh demi-drop                 # → prints a fresh artifact URL
ssh demi-reports < report.html                     # overwrites /reports in place
tar -C ./site -cf - . | ssh demi-sync              # directory sync (each file → an artifact)
```

`receive` flags to bake into `command=`: `--slug`, `--filename`, `--ttl`, `--private`
(single-file only), `--untar`, `--max-upload <bytes>` (cap stdin size, default 100 MiB, `0` = unlimited). A
view password comes from the `DEMIPLANE_PASSWORD` environment variable (`environment=` in
the entry, or a wrapper), never an argument. Nested tar paths flatten to hyphenated slugs
(`css/style.css` → `css-style.css`), and re-syncing overwrites in place.

## Capturing agent artifacts (Claude Code)

When an AI agent generates a self-contained HTML page, that page should land on
**your** mesh — not a public host. The
[`companion/capture-hook/`](./companion/capture-hook/) companion is a Claude Code
`PostToolUse` hook (and a standalone publish CLI) that POSTs such artifacts to
`POST /publish` automatically and hands the agent the internal URL to share.

```sh
export DEMIPLANE_URL=http://demiplane.your-mesh:8080
export DEMIPLANE_CAPTURE=1        # the hook is dormant until this is set
# wire companion/capture-hook/settings.example.json into your Claude Code settings
```

It's a *companion*, not a server module — the capture is client-side; the server
API is unchanged. The hook is one of several ways to connect; see
[`companion/README.md`](./companion/README.md) for the full harness matrix (MCP,
CLI, curl, capture hook) or the running instance's [`/connect`](./) page.

## Inline replies (optional module)

The closing half of the loop: a viewer responds to a published page —
**Approve / Defer / free-text** — and an agent later lists and acks those
replies. It's an **opt-in server module** (build tag `reply`), so the default
binary stays tiny:

```sh
go build -tags reply -o demiplane ./cmd/demiplane
# submit is mesh-only (a replier is a viewer); reading replies needs the bearer token
curl -X POST -H 'Content-Type: application/json' \
     --data '{"kind":"approve","body":"ship it"}' http://demiplane.mesh:8080/reply/proposal
curl -H "Authorization: Bearer $TOKEN" "http://demiplane.mesh:8080/replies?status=pending"
```

Storage is module-owned and isolated; core's API and security model are
untouched. See [`internal/modules/reply/`](./internal/modules/reply/) and
[ADR 0002](./docs/adr/0002-inline-reply-module.md).

### Inline reply box on a rendered page (Q&A / teaching)

For question-and-answer or lightweight LMS use, publish a markdown page with a
**first-class inline reply box** baked in — no per-page hand-rolled form:

```sh
# needs the reply module (-tags reply); box requires ?render=md, not ?private
curl --data-binary @lesson.md \
  "http://demiplane.mesh:8890/publish?render=md&slug=lesson-01&reply=question"
```

The reader gets a single answer box at the foot of the page. The form is
**JS-free and posts same-origin** to the page's own URL (the content plane), so:

- it works today over plain http on the mesh and keeps working once TLS lands
  (the form inherits the page's scheme — no cross-origin/mixed-content trap);
- the "✓ Recorded" confirmation is a **real server response rendered only after
  the answer is durably stored** — a failed write shows an honest error page, not
  a false success. There is no client-side timer deciding what to display.

Each answer is stored as a `comment`-kind reply; read them with
`GET /replies?slug=lesson-01` (bearer auth), same as any other reply. The
same-origin submit route (`POST /{slug}` on the content plane) is contributed by
the reply module via the optional `ContentRouteModule` seam (ADR 0002).

### Reply-event hook + forward flow (auto-advance lessons)

Two additions close the answer → next-lesson loop without any polling:

- **Reply-event hook.** When a reply is durably recorded, the server fires a
  configurable action — set either or both in
  `${XDG_CONFIG_HOME:-~/.config}/demiplane/config` (`-tags reply` builds only):

  ```ini
  # run a command (/bin/sh -c): reply JSON on stdin,
  # DEMIPLANE_REPLY_{ID,SLUG,KIND,BODY} in the environment
  reply_hook_exec = systemd-run --user /usr/local/bin/professor-grade
  # and/or POST the reply JSON to a webhook
  reply_hook_url = http://127.0.0.1:9099/reply-event
  ```

  Dispatch is **fire-and-forget**: a failing or slow hook is logged and can never
  fail, delay, or roll back the reply write (the exec action is bounded at 5
  minutes — long-running work should enqueue/detach). A malformed value is a
  hard startup error, like any other config key.

- **Forward flow (`?next=`).** Publish a lesson with the sequel's slug:

  ```sh
  curl --data-binary @lesson-01.md \
    "http://demiplane.mesh:8890/publish?render=md&slug=lesson-01&reply=question&next=lesson-02"
  ```

  After a recorded answer, the confirmation page says the next lesson is being
  prepared and (JS-free, via meta-refresh into `GET /answer/lesson-01/next?to=lesson-02`)
  waits for `lesson-02` to be published — then carries the student straight onto
  it. `lesson-02` need not exist yet; until it does, the holding page keeps an
  honest "being prepared" state and the way back. No `?next=` → the previous
  behavior (confirmation + back link), unchanged.

Together: student answers → hook spawns the grader → grader publishes the next
lesson → the student's page flows forward. The honest-confirmation property is
untouched — "Recorded" still renders only after the durable store, independent
of hook or forward-flow fate.

## Native TLS (optional module)

demiplane can terminate TLS itself — no reverse proxy required. It's an
**opt-in module** (build tag `tls`), and even a tls-tagged binary serves plain
HTTP until you flip it on in the config file, so the default posture is
unchanged:

```sh
go build -tags "reply tls" -o demiplane ./cmd/demiplane
```

```ini
# ${XDG_CONFIG_HOME:-~/.config}/demiplane/config  (-tags tls builds only)
tls = on
```

That alone is the **zero-config self-signed** mode: on first TLS start the
module generates an ECDSA P-256 certificate (SANs derived from your
`--bind`/`--content-bind` addresses, or set `tls_hosts = a,b,…` explicitly),
persists it under `<store>/modules/tls/`, reuses it across restarts, and
regenerates it automatically near expiry or when the bind hosts change. The
SHA-256 fingerprint is logged so clients can pin it
(`curl --cacert <store>/modules/tls/self-signed-cert.pem …`).

Two more certificate sources, picked by which keys you set (mixing them is a
hard startup error):

```ini
# ACME / Let's Encrypt — automatic issuance + renewal (TLS-ALPN-01).
# The domain must resolve to this host and the CONTROL listener must be
# reachable on :443. Setting domains constitutes ToS acceptance.
# Optional: tls_acme_email (account contact) and tls_acme_ca (directory
# override, e.g. the Let's Encrypt staging URL). Comments must be on their
# own line — the config format has no inline comments.
tls_acme_domains = demi.example.com
tls_acme_email   = ops@example.com

# — or — bring your own certificate (fleet CA, external DNS-01 flow, …):
tls_cert = /etc/demiplane/demi.crt
tls_key  = /etc/demiplane/demi.key
```

Both planes (control + content) terminate with the same configuration; minted
URLs switch to `https://` automatically. The SSH ingest path needs no
certificate (sshd already encrypts it). Full rationale — and why DNS-01 is
deliberately out — in [ADR 0004](./docs/adr/0004-native-tls-module.md).

> **Migrating a live plain-HTTP instance?** Published absolute `http://` URLs,
> hook scripts, and cron consumers don't rewrite themselves — schedule the
> scheme flip as a coordinated change, not a config toggle.

## Architecture (target)

- Single **Go** binary — embedded `net/http` file server, no runtime deps.
- **SQLite** for metadata (slug, owner, content_type, size, created_at, ttl, private, …);
  flat files for content. Uploads stream to disk.
- **Tiny core, optional modules.** Core is publish / serve / auth / TTL / slugs
  (and the markdown render-on-publish styling) — nothing speculative. Additional
  capabilities are compile-time **modules** (Caddy-style registry, no runtime
  `plugin`) behind build tags, so they aren't in the default binary at all. See
  [docs/MODULES.md](./docs/MODULES.md) and
  [ADR 0001](./docs/adr/0001-module-extension-pattern.md). The inline-reply
  feature (`-tags reply`) is the first module; native TLS (`-tags tls`,
  [ADR 0004](./docs/adr/0004-native-tls-module.md)) is the second.
- TLS: a reverse proxy (Caddy) in front, **or** the native TLS module — both
  optional, never a dependency.
- Zero coupling to pico/pgs — independent implementation.

## Quickstart

```sh
# build the single binary
go build -o demiplane ./cmd/demiplane

# run it — defaults to 127.0.0.1:8080 (loopback). Bind a mesh/LAN IP to expose it.
# Set a bearer token to protect publish/delete/list (omit it for an open trusted LAN).
echo "my-secret-token" > /etc/demiplane.token
demiplane serve --bind 0.0.0.0:8080 --store /var/lib/demiplane --token-file /etc/demiplane.token

TOKEN=my-secret-token

# control plane is :8080; artifacts are served on the SEPARATE content origin :8081
# (origin isolation, ADR 0003). /publish returns a content-origin URL.

# publish a file (raw body) → prints a friendly URL like http://host:8081/shadow-specter
curl -H "Authorization: Bearer $TOKEN" --data-binary @report.html http://127.0.0.1:8080/publish

# publish to a stable, named endpoint that overwrites in place on each push
curl -H "Authorization: Bearer $TOKEN" --data-binary @report.html "http://127.0.0.1:8080/publish?slug=reports"
curl http://127.0.0.1:8081/reports        # GET is open — always the latest (content origin)

# private (unguessable capability URL), password-gated, and auto-expiring publishes
curl -H "Authorization: Bearer $TOKEN" --data-binary @secret.html "http://127.0.0.1:8080/publish?private=true"
curl -H "Authorization: Bearer $TOKEN" -H "X-Demiplane-Password: hunter2" \
     --data-binary @report.html "http://127.0.0.1:8080/publish?slug=q3"   # password set via header, not URL
curl -u any:hunter2 http://127.0.0.1:8081/q3                       # read a password-gated artifact (content origin)
curl -H "Authorization: Bearer $TOKEN" --data-binary @temp.html "http://127.0.0.1:8080/publish?ttl=24h"

# multipart upload (the filename's extension informs the content-type)
curl -H "Authorization: Bearer $TOKEN" -F file=@style.css http://127.0.0.1:8080/publish

# list your artifacts (JSON) and delete one
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/list
curl -H "Authorization: Bearer $TOKEN" -X DELETE http://127.0.0.1:8080/reports

# ask for JSON instead of a plain-text URL on publish
curl -H "Authorization: Bearer $TOKEN" -H 'Accept: application/json' \
     --data-binary @page.html http://127.0.0.1:8080/publish
```

**`serve` flags:** `--bind <host:port>` (default `127.0.0.1:8080` — control plane; set a
mesh/LAN IP or `0.0.0.0` to expose), `--content-bind <host:port>` (artifact content origin;
default = host of `--bind` with port +1, i.e. `:8081`), `--content-base-url <url>`
(advertise the content origin for proxy/TLS deployments), `--unsafe-same-origin` (serve both
on one origin — legacy footgun), `--store <dir>` (required), `--base-url <url>`
(optional; advertise a canonical URL instead of deriving it from the request `Host`),
`--token-file <path>` (optional; bearer token for write/list — `DEMIPLANE_TOKEN` is the
env-var alternative), `--sweep-interval <dur>` (optional; how often to reap past-TTL
artifacts, default `1m`), `--browse` (optional; serve an HTML index of non-private
artifacts at `GET /`), `--max-upload <bytes>` (optional; cap publish size, default
100 MiB, `0` = unlimited), `--theme <name>` (optional; skins the whole instance — chrome and
`?render=md` pages — one of `light` (default house style), `dark`, or the pinned dark
developer palettes `catppuccin` / `dracula` / `one-dark`), `--css <file>` (optional; a custom
stylesheet for `?render=md` pages that replaces the built-in theme), `--header` /
`--footer` (optional; default on — the rendered-page title bar and vanity footer),
`--footer-link <url>` (optional; the footer link target, default the demiplane repo),
`--meta-header` (optional; default on — render a frontmatter-driven meta-header on
`?render=md` pages, with `--meta-header=false` stripping frontmatter instead),
`--write-timeout` / `--idle-timeout` (optional HTTP
timeouts). Content-type
is inferred from the filename extension, then sniffed from the bytes; known web types
serve **inline**, everything else as a download. See **Deployment** above for
reachability tiers.

### Config file

Render-chrome defaults can be set in a file at `${XDG_CONFIG_HOME:-~/.config}/demiplane/config`
so you don't repeat flags on every launch. It is a minimal `key = value` format (`#`
comments, blank lines ignored — no dependency). All keys are optional; a missing file means
all-defaults (zero-config still works perfectly). A malformed file or unknown key is a hard
startup error.

```ini
# ~/.config/demiplane/config
# Comments must be on their own line (there are no inline comments — an
# appended "# …" would become part of the value and fail loudly).
# theme: instance theme — light | dark | catppuccin | dracula | one-dark
# (the named palettes are dark-only and pin their look; light/dark keep the toggle)
theme       = dark
# header: rendered-page title bar (+ theme toggle), on | off
header      = on
# footer: "Generated by demiplane" footer, on | off
footer      = on
# footer_link: footer link target (default: the demiplane repo)
footer_link = https://example.com
# meta_header: frontmatter meta-header (localized date + fields), on | off
meta_header = on
```

Compiled-in modules add their own keys to this file — `reply_hook_*` (reply
module) and `tls`/`tls_*` (native TLS module) are documented in their sections
above. A key whose module is **not** in the binary is treated as unknown and
fails startup loudly, like any typo.

The render theme system (OKLCH tokens, type scale, the rendered-page chrome) is documented
in [DESIGN.md](./DESIGN.md).

**Precedence: CLI flag > config file > built-in default.** A flag you pass on the command
line always wins over the file; the file wins over the defaults. The `theme` here also sets
the initial light/dark state of the per-page toggle (which viewers can then override on
their own device; the choice persists in `localStorage`). With no `theme` set anywhere, a
rendered page defaults to light but the toggle's initial state follows the viewer's OS
`prefers-color-scheme`.

### Docker

```sh
docker build -t demiplane .
# map BOTH ports: 8080 control plane, 8081 isolated artifact content origin (ADR 0003)
docker run --rm -p 8080:8080 -p 8081:8081 -v demiplane-data:/var/lib/demiplane demiplane
```

The image is a static binary on a distroless base (no shell, non-root). The container binds
`0.0.0.0:8080` (control) and `0.0.0.0:8081` (content) internally; control exposure with how
you map the ports. Pass extra flags by overriding the command, e.g.
`docker run ... demiplane serve --bind 0.0.0.0:8080 --store /var/lib/demiplane --browse`.

## Repo layout

| Path | Purpose |
|---|---|
| `cmd/demiplane/` | Binary entry point: `serve`, `receive`, `version`; module wiring (`modules*.go`) |
| `internal/store/` | Flat-file content store, SQLite metadata, slug generation, TTL, passwords |
| `internal/server/` | HTTP handlers (`POST /publish`, `GET /{slug}`, `/list`, browse, auth) |
| `internal/module/` | Module seam: `Host`, `RouteModule`, compile-time registry |
| `internal/modules/` | Optional modules — `reply/` (build tag `reply`), `tls/` (build tag `tls`) |
| `companion/` | Client-side companions — `capture-hook/` publishes Claude Code artifacts to your instance |
| `internal/transport/` | SSH ingest (`demiplane receive`: single-file pipe + tar directory sync) |
| `internal/render/` | Dependency-free markdown→HTML render engine (CommonMark subset) |
| `Dockerfile` | Static-binary image on a distroless base |
| `docs/` | ADRs (`adr/`), module guide (`MODULES.md`), release notes (`RELEASE-v1.md`) |
| `CLAUDE.md` | Repo-local build/test conventions for AI agents |
| `AGENTS.md` | Agent workflow notes |
| `.github/` | CI (build/test/vet/gofmt), actionlint, dependabot |

## Contributing & security

- [CONTRIBUTING.md](./CONTRIBUTING.md) — dev setup, gates, conventions (PRs require the CLA).
- [CLA.md](./CLA.md) — Contributor License Agreement.
- [SECURITY.md](./SECURITY.md) — threat model, the same-origin hosted-JS footgun, disclosure.
- [CHANGELOG.md](./CHANGELOG.md) — what shipped, by milestone.
- [docs/RELEASE-v1.md](./docs/RELEASE-v1.md) — what v1 actually is: the dogfood story, v1 scope / non-scope, and known limitations.
- [docs/](./docs/) — ADRs (`adr/`), the module guide ([MODULES.md](./docs/MODULES.md)), and the HTTP [API reference](./API.md).

## Acknowledgments

- The friendly auto-generated slugs (`adjective-creature`, e.g. `shadow-specter`) are built
  from a wordlist generated via the **[D&D 5e API](https://www.dnd5eapi.co)** — the
  open-source **5e-bits** project. Thank you for the open API. The creature and term names
  are **D&D 5e System Reference Document** content, © Wizards of the Coast, used under the
  OGL 1.0a / CC-BY-4.0.

## License

demiplane is **free and open source under the [GNU AGPL-3.0-only](./LICENSE)**.
© 2026 Dais & Apex.

**Self-hosted, free, and fully featured under AGPL-3.0.** A managed Cloud with team/SSO/
compliance features is planned to fund development (it does not exist yet). Organizations
that cannot accept the AGPL's terms can obtain a [commercial license](./COMMERCIAL-LICENSE.md).

Contributions are accepted under a [Contributor License Agreement](./CLA.md) — see
[CONTRIBUTING.md](./CONTRIBUTING.md).

demiplane is a **Dais & Apex** project.
