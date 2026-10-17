# Security

demiplane is an **internal-first** publishing service: the intended deployment is
a single binary reachable only over a trusted LAN or mesh (e.g. Netbird), not the
public internet. The security model is built around that assumption. Read this
before exposing an instance more broadly.

## Reporting a vulnerability

**Please report security issues privately — do not open a public issue for an
unpatched vulnerability.**

Email **security@daisandapex.com** with:

- a description of the issue and its impact,
- steps to reproduce (a proof-of-concept is ideal), and
- the affected version / commit and build tags (`reply`, `tls`).

demiplane is a personal open-source project maintained by one person. There is no
formal SLA or bug-bounty program, but you can expect:

- an **acknowledgement within about 3 business days**, and
- a good-faith effort to confirm, fix, and disclose in coordination with you.

Please give a reasonable window to ship a fix before any public disclosure.
Reports are handled under coordinated disclosure; credit is given to reporters who
want it.

## Threat model

**In scope:**

- **Publish authorization.** Only holders of the bearer token (HTTP) or an
  authorized SSH public key may write, delete, or list. Tokens are compared with
  a constant-time, length-hiding HMAC; SSH auth is delegated to the host's
  `sshd` via an `authorized_keys` forced command.
- **View authorization**, in increasing strength: network reachability (the
  default), an unguessable **capability slug** (`?private=true`, 144-bit), and an
  optional **per-artifact password** (salted PBKDF2-HMAC-SHA256, supplied via the
  `X-Demiplane-Password` header on publish and HTTP Basic on read).
- **Input handling.** Uploads stream to disk (bounded memory); slugs are
  validated against path traversal; the optional markdown renderer HTML-escapes
  all source and allowlists link schemes; error logs redact slugs (a private
  slug is a secret).

**Out of scope / explicitly accepted for v1:**

- **Plain-HTTP confidentiality.** By default demiplane does not terminate TLS.
  Over plain HTTP, bearer tokens, passwords (HTTP Basic), and capability slugs
  travel in the clear. Passwords and capability URLs are only meaningful
  **behind TLS or on a trusted network** — front demiplane with a reverse proxy
  (e.g. Caddy) **or use the built-in TLS module** (`-tags tls` build +
  `tls = on`; self-signed, ACME, or BYO cert — see the README's Native TLS
  section and ADR 0004) for a public or semi-public deployment. The binary
  warns when it binds a wildcard address.
- **Multi-tenant isolation.** v1 is single-operator; all artifacts belong to one
  `owner`. There is no per-user access control yet.
- **Denial of service.** demiplane bounds the work an unauthenticated
  request can cause (see [Resource limits](#resource-limits)): password
  checks, reply submissions and hooks, request bodies, responses, requests and
  connections in progress, and markdown rendering. These limits are per
  process and keyed by the TCP peer address; they make a single instance hard
  to exhaust, not impossible. Behind a reverse proxy every client shares the
  proxy's address, so enforce per-client limits at the proxy. The aggregate
  storage quota is off by default (`--store-quota`).

## Origin isolation — hosted JS cannot reach the control plane

demiplane serves arbitrary uploaded HTML/JS **inline**, and `text/html` script
execution is an intended feature — so it cannot be CSP'd away. Instead, artifacts
are served from a **separate origin** from the control plane (the
githubusercontent.com model; see
[ADR 0003](docs/adr/0003-content-origin-isolation.md)):

- **Control origin** (`--bind`, default `127.0.0.1:8080`) hosts `/publish`,
  `/list`, `DELETE`, and the human/agent surfaces.
- **Content origin** (`--content-bind`, default the same host with **port +1** →
  `127.0.0.1:8081`) serves **only** artifact bodies: `GET /{slug}`, published-site
  files (`GET /{site}/{path}`), and the SSE live-reload stream
  (`GET /_events/{slug}`). The `/connect` and `/gallery` human surfaces live on
  the **control** origin.

Because hosted JS runs on the content origin, the same-origin policy blocks it
from reading any control-plane response (`/list` enumeration, capability URLs),
and a **cross-origin write guard** (an `Origin`/`Sec-Fetch-Site` check on the
control plane) blocks fire-and-forget cross-origin `POST`/`DELETE` — so a
published page's script can neither exfiltrate the artifact inventory nor publish
a worm. This holds on bare loopback with no DNS, certs, or proxy.

A defense-in-depth layer also refuses to relabel a non-HTML body to `text/html`
via `?filename=` (closing the content-type-confusion vector that bypassed the
SVG/XML no-script CSP).

**Known boundaries.** Port-separation isolates *origins* but not *cookies*
(harmless today — demiplane is stateless; a future sessioned mode must move to a
separate content **hostname** via `--content-base-url`). A single content origin
does not isolate one artifact's JS from another's; per-slug subdomains are the
future hardening. `--unsafe-same-origin` collapses both planes onto one origin
and re-enables the footgun — use it only for a single-operator, trusted-content
instance where you accept that.

## New surfaces in v1.2

The cross-harness release adds three surfaces beyond the core HTTP/SSH API. Each
inherits the posture above and was reviewed adversarially before release.

- **MCP stdio server (`demiplane mcp`).** A JSON-RPC 2.0 server on stdin/stdout
  that is a **thin HTTP client** of a running instance — it has no store,
  filesystem, or server-package coupling, so it cannot bypass publish auth. The
  bearer token comes from `--token-file`/`DEMIPLANE_TOKEN` only (never argv),
  rides the `Authorization` header only, and appears in no stdout/stderr/error
  path; the view password uses the `X-Demiplane-Password` header, never the query.
  The `get` tool is bounded to the configured instance origin — matched on
  **host *and* port**, so it reaches only this instance's own control/content
  listeners and cannot pivot to another service on the same loopback host (SSRF
  guard). Because
  MCP tool arguments are **model-chosen** (a prompt-injected model can pick them),
  the `publish` tool's `path` argument is guarded the same way whether the token
  came from `--token-file` or `DEMIPLANE_TOKEN`: `path` must resolve, after
  symlink resolution, inside a **publish root** (`--publish-root` /
  `DEMIPLANE_PUBLISH_ROOT`, default the working directory; a working directory of
  `/` disables `path` publishing unless `/` is set explicitly), and is opened
  through that root, which is opened once at startup, so neither a raced-in
  symlink nor a later replacement of the root directory can move the read. The file is opened
  non-blocking, so a FIFO raced in after the checks cannot stall the server. It
  refuses symlinks and
  non-regular files, anything on the `/proc`, `/sys` or `/dev` filesystems (so the
  process environment is out of reach), and the configured token file matched by
  **file identity** (device and inode, taken at startup from the same open
  descriptor the token is read from, and re-checked on each call), which also
  catches a hardlink. `path`
  still reads any *other* regular file under the publish root that the process
  can read — choose the root accordingly.
- **SSE live-reload (`?live` + `GET /_events/{slug}`).** The `?live` wrapper is
  **opt-in** and does not mutate stored bytes — the plain `GET /{slug}` is
  byte-identical to what was published. The wrapper's inline reload script runs
  under a strict per-response CSP nonce (`default-src 'none'`, `script-src` the
  nonce only, no `unsafe-inline`/`eval`); the sole reflected value (the slug) is
  regex-constrained and HTML-escaped, and the artifact renders in a same-origin
  iframe. The SSE endpoint caps subscribers **per-slug and globally** to bound
  file-descriptor/memory use, sends periodic heartbeats, and drops consumers on a
  write deadline / context cancel.
- **Multi-file archive ingest (`POST /publish?site=`).** Tar, zip, and multipart
  bundles are accepted behind the publish token. Every entry path is
  traversal-checked (`..`, absolute paths, and backslashes rejected; each segment
  re-validated as a named slug) and confined under the site prefix on both write
  and serve; symlink and other non-regular entries are skipped, never
  materialized. The archive is bounded on **both** the compressed upload
  (`--max-upload`, buffered to a scratch file) and the **total decompressed
  bytes** across all entries — enforced identically for zip **and** tar, so a
  compression- or sparse-tar bomb is rejected with `413` — plus a file-count cap.
  Served assets keep the flat-artifact posture (global `nosniff`; `script-src
  'none'` CSP on SVG/XML).

## Hardening notes

- **Bind loopback by default.** `--bind` defaults to `127.0.0.1:8080`; expose it
  only by binding a specific mesh/LAN IP or `0.0.0.0` (which logs a warning).
- **Always set a token** (`--token-file` / `DEMIPLANE_TOKEN`) once the instance is
  reachable by anything you don't fully trust.
- **Timeouts.** `ReadHeaderTimeout` (15s) mitigates slowloris header stalls.
  Bodies and responses get per-route deadlines instead of a whole-request
  `ReadTimeout`/`WriteTimeout`, which would cut off large transfers and the
  live-reload stream; see [Resource limits](#resource-limits). The legacy
  `--write-timeout` (off by default) still sets a whole-response timeout and
  also ends live-reload streams. `--idle-timeout` (default 120s) bounds idle
  keep-alives.
- **Upload cap.** `--max-upload <bytes>` (default 100 MiB; `0` = unlimited) bounds
  publish body size and returns `413` when exceeded.
- **Clickjacking.** Every response on both planes carries
  `X-Frame-Options: SAMEORIGIN` and a `frame-ancestors 'self'` CSP: an external
  site cannot frame a hosted page or the control UI. Same-origin framing stays
  allowed because the `?live` preview wrapper legitimately iframes the artifact's
  own URL.
- **Referrer leakage.** Every response carries
  `Referrer-Policy: strict-origin-when-cross-origin`, so a capability URL
  (`?private=true` slug) is not disclosed in the `Referer` header when a hosted
  page links out cross-origin. The `?live` wrapper keeps its stronger
  `no-referrer` for the preview frame.
- **HSTS.** When native TLS terminates at demiplane (the request arrives over
  TLS), responses carry `Strict-Transport-Security: max-age=63072000`
  (two years). It is deliberately emitted **without** `includeSubDomains` and
  **without** `preload` — an instance on one subdomain must not commit sibling
  subdomains (or the apex) of its parent zone to HTTPS-only. Plain-HTTP responses
  omit HSTS entirely. Behind a TLS-terminating proxy/tunnel that forwards plain
  HTTP to demiplane, enable HSTS at that edge (demiplane sees no TLS and will not
  advertise it).
- **Archive decompression bombs.** Both multi-file ingest paths — HTTP
  `POST /publish?site=` (tar/zip) and the SSH directory-sync untar — cap the
  **total decompressed bytes** across all entries (`store.BudgetReader`,
  default 512 MiB), independent of the `--max-upload` cap on the stored/compressed
  bytes. A sparse tar declares a tiny stored payload yet expands to attacker-chosen
  zero-fill through `tar.Reader`; the budget rejects it (HTTP `413`; a hard error
  on the SSH path) rather than writing gigabytes into the store.
- **Host-header reflection.** URLs derived from the request `Host` (when
  `--base-url` is unset — `llms.txt`, publish responses, minted links) pass
  through a character allowlist first, so a crafted Host header cannot inject
  content into those surfaces. Setting `--base-url` removes the reflection
  entirely.
- **Store permissions.** The store root, `blobs/`, `sources/`, `sites/` and the
  metadata database (`meta.db` and its `-wal`/`-shm` files) are owner-only
  (directories `0700`, files `0600`). The database holds capability slugs and
  password hashes. At startup demiplane sets any of these whose mode differs to
  those modes and logs each change.
- **Native TLS material.** With the TLS module enabled, private keys live under
  `<store>/modules/tls/` (dir `0700`, keys `0600`). The self-signed cert's
  SHA-256 fingerprint is logged at generation for out-of-band pinning; ACME mode
  stores its account key and certs in the same tree.

## Resource limits

Every limit below is per process. A "client" is the TCP peer address, with
IPv6 grouped by /64; forwarding headers are ignored, as everywhere else in
demiplane. Flags are on `serve`; `reply_*` keys go in the config file and exist
only in `-tags reply` builds.

| Surface | Limit | Default | Knob |
|---|---|---|---|
| View password | KDF runs at once; more wait up to 5s, then `503` | half the cores, 1 to 4 | (code) |
| View password | failed attempts per artifact, per client, in all; past it `429` without hashing | 10, 20, 120 a minute (bursts 10, 10, 30) | (code) |
| Request body | non-upload body must arrive within; uploads: grace and longest stall | 30s | `--body-timeout` |
| Request body | upload average rate after the grace; slower gets `408` | 16 KiB/s | `--upload-min-rate` |
| Response | write deadline per response | 60s | `--response-timeout` |
| Response | extra time for an artifact or site file: size at this rate | 16 KiB/s | `--download-min-rate` |
| Admission | requests in progress per listener / per client; excess `503` | 256 / 32 | `--max-requests`, `--max-requests-per-client` |
| Admission | open connections per listener (further ones wait) / per client (further ones are closed) | 1024 / 64 | `--max-conns`, `--max-conns-per-client` |
| Rendering | table columns; table body cells per document (rows times header width) | 128; 1,000,000 | (code) |
| Rendering | rendered page size; over it `413`, nothing stored | 32 MiB | `--render-max-output` |
| Rendering | renders at once; one that waits 5s gets `503` | GOMAXPROCS, at most 4 | `--render-concurrency` |
| Storage | bytes of artifacts, retained sources and sites, including in-flight uploads, scratch and extracted archive bytes; over it `507` | unlimited | `--store-quota` |
| Replies | submissions per client / per artifact / in all; past it `429` | 10 / 60 / 300 a minute | `reply_rate_client`, `reply_rate_target`, `reply_rate_global` |
| Replies | submit body must arrive within | 10s | (code) |
| Replies | replies per artifact (`429`); replies and reply text in all (`507`) | 1000; 100,000 and 64 MiB | `reply_max_per_artifact`, `reply_max_total`, `reply_max_bytes` |
| Replies | replies older than this are deleted | 90 days | `reply_retention` (`0` keeps them) |
| Reply hooks | events running at once / admitted and waiting; a full pool refuses the reply with `503` | 2 / 32 | `reply_hook_workers`, `reply_hook_queue` |
| Reply hooks | exec run time; the hook's process group is killed when the shell exits or this passes | 5m | `reply_hook_timeout` |

Notes on the limits:

- **Password checks.** PBKDF2 keeps its 600,000-iteration work factor. A
  correct password is refunded and remembered for 10 minutes (keyed by an
  HMAC of the stored hash and the password under a per-process key), so a
  viewer who has authenticated is not affected by a flood of wrong guesses
  against the same page; a new viewer of that page may get `429` until the
  per-artifact budget refills. Reply submit and form routes run the same
  check.
- **Replies.** A reply is accepted only for an artifact whose publish enabled
  replies (`?reply=form` or `?reply=question`); the choice is stored with the
  artifact and replaced by every publish to the slug. The content listener
  refuses cross-origin browser writes, except from an opaque origin
  (`Origin: null`, which is what a sandboxed artifact page sends) whose
  `Referer` is absent or on the content origin. That check is defence in
  depth only: another site can post from a sandboxed frame with no Referer,
  and no origin check can tell apart two pages on the same content origin.
  The per-artifact opt-in, the view password and the reply limits are what
  admission rests on.
  The per-artifact cap and the aggregate limits are checked and the reply
  inserted in one transaction, so concurrent submissions cannot overshoot.
- **Reply hooks.** A submission takes its place in the hook pool before the
  reply is stored, so no stored reply is ever dropped by a full pool. Hook
  output is read as it streams and only the first 512 bytes are kept. Work
  meant to outlive a hook must detach (`systemd-run`, `setsid`); a plain
  background child is killed with the hook's process group.
- **Storage quota.** Not counted: the metadata database, module data (replies,
  TLS material), filesystem block overhead and files left behind by a crash.
  An overwrite or rebake needs room for the new copy while the old one still
  exists; a site publish needs room for its archive plus the extracted files.
  `demiplane receive` runs as a separate process and enforces the quota
  against its own writes; a running `serve` learns of them at its next
  recount (every `--sweep-interval`), so across processes the store can
  briefly exceed the quota by what the other process wrote since.

## Reporting

See **[Reporting a vulnerability](#reporting-a-vulnerability)** at the top of this
document for how to disclose a security issue privately.
