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
- **Isolation between artifacts.** A published page cannot read another
  artifact through the viewer's browser, cannot use the viewer's cached
  credentials for one, and cannot install a service worker. See
  [Content isolation between artifacts](#content-isolation-between-artifacts)
  for what that covers and what it does not.

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
(harmless today, since demiplane is stateless; a future sessioned mode must move
to a separate content **hostname** via `--content-base-url`). Artifacts share one
content origin; the per-document sandbox below is what keeps them apart.
`--unsafe-same-origin` collapses both planes onto one origin. Sandboxed pages
still cannot read control-plane responses there, but the combined handler has no
cross-origin write guard, so on an instance with no token a hosted page can
submit a form to `POST /publish` or `DELETE`. Use it only for a single-operator,
trusted-content instance where you accept that.

## Content isolation between artifacts

Every artifact and site is served from the one content origin, so the origin
alone does not keep a hostile page away from its neighbours. Each document is
therefore served in a browser sandbox of its own (the reasoning, and the
alternative of an origin per artifact, are in [DESIGN.md](DESIGN.md#content-isolation)).

**What is isolated:**

- **Every HTML document** (uploaded pages, `?render=md` pages, site pages) is
  served with `Content-Security-Policy: sandbox allow-scripts allow-forms
  allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads`,
  without `allow-same-origin`. The page runs in an opaque origin of its own, so
  every other URL on the instance is cross-origin to it: `fetch`/XHR responses
  from other artifacts are unreadable (with or without the viewer's cached HTTP
  Basic credentials, which a cross-origin request only carries when the page
  asks, and whose response it still cannot read), frames and windows holding
  other artifacts cannot be scripted, and storage (`localStorage`, IndexedDB,
  cookies) is unavailable. **SVG, XML and XHTML** get a bare `sandbox` plus
  `script-src 'none'`.
- **Service workers.** An opaque origin cannot register one. Independently,
  every HTML, SVG and XML document carries `worker-src 'none'`, and any request
  carrying a `Service-Worker` header (the browser's marker for a worker script
  fetch) is refused with `403` on both planes, so no URL can become a worker
  even with the sandbox off.
- **Framing.** `frame-ancestors 'self'` never matches an opaque parent, so a
  sandboxed page cannot embed another artifact at all.
- **Confidential responses.** Responses for private (capability URL) and
  password-protected artifacts, their `401` and every `?live` wrapper carry
  `Referrer-Policy: no-referrer` and `Cache-Control: private, no-store`, so a
  capability URL does not leave in a `Referer` (or `document.referrer`) and no
  copy outlives expiry or deletion in a browser or shared cache. Artifact
  responses also carry `Cross-Origin-Resource-Policy: same-origin`, so another
  page cannot run a protected script, apply a protected stylesheet or draw a
  protected image through a plain `<script>`, `<link>` or `<img>`, which the
  browser would otherwise load with the viewer's cached credentials. The cost:
  a protected artifact cannot be a subresource of any page (every sandboxed
  page is another origin); it can only be opened or framed.
- **Password realms.** Each artifact challenges with its own Basic realm (a
  short hash of the slug, not the slug), so a browser does not answer one
  artifact's challenge with credentials it cached for another.
- **Live-reload streams** are authorized like the artifact (see below).

**What remains:**

- **The sandbox is enforced by the browser.** A browser without CSP `sandbox`
  support (every current major browser has it) gets no isolation.
- **`--unsafe-no-sandbox`** turns the sandbox off: every page shares the origin
  again and can read any artifact the viewer can open. Service workers stay
  refused.
- **Side channels and blind requests.** A hostile page can still make the
  browser request other artifacts (links, forms, images, scripts) and observe
  coarse signals such as load or error events and timing, so it may learn
  whether a URL exists or whether the viewer is authorized for it. Browsers may
  attach cached Basic credentials to those requests, because for top-level slugs
  the credential scope is the whole origin. The server checks them against each
  artifact's own password; the page never sees the response, and a protected
  response refuses to be used as its subresource.
- **Public scripts, stylesheets and fonts are readable cross-origin.** So that a
  sandboxed page can load its own module scripts, `crossorigin` stylesheets and
  fonts (which browsers fetch in CORS mode, from origin `null`), a public
  artifact or site file of a matching type answers such a request with
  `Access-Control-Allow-Origin: null`. Only requests the browser itself labels
  `script`, `style` or `font` qualify, the stored type must match, and private
  or password-protected artifacts never get it. Any opaque-origin page,
  including another site's sandboxed frame, can therefore read public JS, CSS
  and fonts, much as any page can already run or apply them. Keep secrets out
  of public scripts and stylesheets.
- **A service worker registered before this release** is not removed. Its
  update checks now get `403`, so it is never replaced, but a browser keeps an
  installed worker until the registration is cleared. A viewer who opened a
  hostile page on an older release should clear site data for the content
  origin; an operator can also move the content origin to a new port or
  hostname.
- **Content-plane pages demiplane writes itself** (the `?live` wrapper, the
  themed 404, the reply confirmation) run unsandboxed in the content origin.
  They carry no uploaded script.
- **Writes from pages.** A sandboxed page can still submit forms, for example
  to the reply endpoint `POST /answer/{slug}`; its request arrives with
  `Origin: null`.

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
  iframe. The SSE endpoint is authorized like the artifact it watches: the slug
  must name an existing, unexpired artifact, and a password-protected one needs
  its password over HTTP Basic (a refusal is a `401` with that artifact's realm,
  and the `?live` wrapper takes the same password first, so the browser answers
  from the credentials the viewer just entered). The grant is re-checked on every republish
  and every heartbeat, so deleting or expiring the artifact, or changing its
  password, ends the stream. Subscribers are capped **per slug, per client
  address** (an IPv4 address or IPv6 /64; behind a proxy all clients share the
  proxy's address) **and globally** to bound file-descriptor/memory use; the
  stream sends periodic heartbeats and drops consumers on a write deadline /
  context cancel.
- **Multi-file archive ingest (`POST /publish?site=`).** Tar, zip, and multipart
  bundles are accepted behind the publish token. Every entry path is
  traversal-checked (`..`, absolute paths, and backslashes rejected; each segment
  re-validated as a named slug) and confined under the site prefix on both write
  and serve; symlink and other non-regular entries are skipped, never
  materialized. The archive is bounded on **both** the compressed upload
  (`--max-upload`, buffered to a scratch file) and the **total decompressed
  bytes** across all entries — enforced identically for zip **and** tar, so a
  compression- or sparse-tar bomb is rejected with `413` — plus a file-count cap.
  Served assets keep the flat-artifact posture (global `nosniff`, the
  per-document sandbox, `script-src 'none'` on SVG/XML). Sites have no privacy,
  expiry or password support: a site upload that asks for `?private`, `?ttl`,
  `?password` or an `X-Demiplane-Password` header is refused with `400` before
  the archive is read, rather than published public and permanent. A site is
  readable by anyone who can reach the content origin until it is replaced.

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
  `Referrer-Policy: strict-origin-when-cross-origin`, so a public page's links
  out cross-origin send only the origin. Private and password-protected
  artifacts and the `?live` wrapper send `no-referrer`, so a capability URL is
  not disclosed even to another page on the instance.
- **Artifact index.** `GET /gallery` lists every public artifact, including
  password-protected ones. It is public only with `--browse`; otherwise it
  takes the bearer token, like `GET /list`.
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

## Reporting

See **[Reporting a vulnerability](#reporting-a-vulnerability)** at the top of this
document for how to disclose a security issue privately.
