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
- **Denial of service / quotas.** Uploads are capped at 100 MiB by default
  (`--max-upload <bytes>` to raise/lower it; `--max-upload=0` for unbounded
  streaming). There is no global rate limiting; rely on the network boundary or a
  fronting proxy.

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
  the `publish` tool's `path` argument is guarded: it **refuses the configured
  token file** (matched on the symlink-resolved absolute path) and refuses
  non-regular files, so it cannot be steered into exfiltrating the token or
  following a symlink to a secret. `path` still reads any *other* regular file the
  process can read — scope which files the MCP process can see accordingly.
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
- **Timeouts.** `ReadHeaderTimeout` (15s) mitigates slowloris header stalls. A
  whole-request `ReadTimeout` is intentionally **not** set because demiplane
  streams arbitrarily large uploads — a global read deadline would kill
  legitimate slow transfers. `WriteTimeout` is off by default for the same reason
  on large downloads; enable it with `--write-timeout` when your artifacts are
  small. `--idle-timeout` (default 120s) bounds idle keep-alives.
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
- **Native TLS material.** With the TLS module enabled, private keys live under
  `<store>/modules/tls/` (dir `0700`, keys `0600`). The self-signed cert's
  SHA-256 fingerprint is logged at generation for out-of-band pinning; ACME mode
  stores its account key and certs in the same tree.

## Reporting

See **[Reporting a vulnerability](#reporting-a-vulnerability)** at the top of this
document for how to disclose a security issue privately.
