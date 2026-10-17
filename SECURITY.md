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
  all source and allowlists link schemes. Link destinations containing a
  backslash or a control character, and `http(s)` links with userinfo
  (`https://trusted@other`), are rendered as plain text, because a browser
  resolves them to a different host than they appear to name.
- **Log hygiene.** A private slug is a secret, so error logs never carry one:
  slugs are logged as a short hash tag (`slug#…`), and the filesystem paths
  inside wrapped I/O errors (which embed the slug) are replaced by `[path]` on
  every path that logs a store error, including rerender, expiry, the
  sweeper and the fatal error line of `serve` and `receive`. Reply-hook failures log the webhook's origin only (no userinfo,
  path or query) and omit the exec hook's output unless
  `reply_hook_log_output = on`; logged hook output is truncated and has its
  control characters escaped. The webhook client follows no redirects, so a
  reply is never replayed to another origin or over plain HTTP.
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
- **Denial of service.** demiplane bounds the work an unauthenticated
  request can cause (see [Resource limits](#resource-limits)): password
  checks, reply submissions and hooks, request bodies, responses, requests and
  connections in progress, and markdown rendering. These limits are per
  process and keyed by the client address; they make a single instance hard
  to exhaust, not impossible. Behind a reverse proxy, list it in
  `--trusted-proxy` so per-client limits see each client; otherwise every
  client shares the proxy's address. The aggregate
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
a worm. This holds on bare loopback with no DNS, certs, or proxy. When a browser
sends no `Sec-Fetch-Site`, the `Origin` fallback compares the **full origin**
(scheme, host and port, default ports normalized) with the `--base-url` origin
when one is set, else with the listener's own origin; an `http://` page cannot
write to an `https://` control plane on the same host and port, or the reverse.
Behind a TLS-terminating proxy, set `--base-url` so the browser's `https://`
origin is the one accepted.

A defense-in-depth layer also refuses to relabel a non-HTML body to `text/html`
via `?filename=` (closing the content-type-confusion vector that bypassed the
SVG/XML no-script CSP).

**Known boundaries.** Port-separation isolates *origins* but not *cookies*
(harmless today, since demiplane is stateless; a future sessioned mode must move
to a separate content **hostname** via `--content-base-url`). Artifacts share one
content origin; the per-document sandbox below is what keeps them apart.
`--unsafe-same-origin` collapses both planes onto one origin. Sandboxed pages
still cannot read control-plane responses there, but the combined handler can
only run the content listener's write guard, which admits an opaque-origin
(`Origin: null`) write that carries no other origin's `Referer` (see
[Resource limits](#resource-limits), "Replies"). On an instance with no token a
hosted page can therefore submit a form to `POST /publish`. Use it only for a
single-operator, trusted-content instance where you accept that.

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
  fetch), on either plane and at any path, is answered with one fixed script
  that removes itself (see below), so no URL can become any other worker, even
  with the sandbox off.
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
- **A service worker registered before this release** is removed the next
  time the browser checks it for an update, which it does when it loads a page
  in the worker's scope: every service-worker script fetch gets a fixed script
  that replaces the old worker, deletes Cache Storage, unregisters itself and
  reloads the pages it controlled. Until the viewer next loads a page on the
  content origin, the old worker stays installed but idle. Only Cache Storage
  is cleared; other storage a hostile page wrote under an older release is
  left in place and is unreachable from sandboxed pages. `Clear-Site-Data` is
  not sent: with `--unsafe-no-sandbox` pages use storage legitimately, and any
  page could trigger it.
- **Content-plane pages demiplane writes itself** (the `?live` wrapper, the
  themed 404, the reply confirmation) run unsandboxed in the content origin.
  They carry no uploaded script.
- **Writes from pages.** A sandboxed page can still submit forms, for example
  to the reply endpoint `POST /answer/{slug}`; its request arrives with
  `Origin: null`. A reply needs the submission token from the target's own
  form, which another page cannot read (see Resource limits, "Replies").

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
  from the credentials the viewer just entered). The grant is re-checked on
  every republish and every heartbeat, so deleting or expiring the artifact, or
  changing its password, ends the stream. Every password check here, including
  the re-check after a republish re-salts the hash, goes through the
  view-password limits ([Resource limits](#resource-limits)), so open streams
  cannot multiply KDF work; a check refused for load is a `429` or `503`, and
  ends an open stream. Subscribers are capped per client per artifact (2),
  per client (16), per artifact (16) and in all (256), with clients keyed by
  IPv4 address or IPv6 /64 (behind a `--trusted-proxy`, the forwarded client).
  When an artifact or the instance is full, a new client takes the place of
  the oldest stream of a client holding at least two more streams than it
  does; otherwise it gets `503`. An ended stream keeps its slot until its
  connection closes (at most the 10s write timeout). So no client holds more
  than its share while others wait. A request that must wait for an ended
  stream's connection to close gives up its ordinary request slot first, so
  waiting requests cannot starve the listener; at most 32 wait at once and one
  per client, each for at most 11s. The stream sends periodic heartbeats and drops consumers on a
  write deadline or context cancel.
- **Multi-file archive ingest (`POST /publish?site=`).** A tar or zip archive
  in the request body is accepted behind the publish token (multipart is
  not). Every entry path is traversal-checked (`..`, absolute paths and any
  backslash rejected; `/` is the only separator; each segment re-validated as
  a named slug) and confined under the site prefix on both write and serve;
  symlink and other non-regular entries are skipped, never materialized. The
  serve path opens every component through an `os.Root` and refuses a symlink
  anywhere on the path (site directory, intermediate directory or file),
  checking that what it opened is what it inspected, so a link planted or
  swapped in out of band is never followed. The archive is bounded on **both**
  the compressed upload (`--max-upload`, buffered to a scratch file) and the
  **total decompressed bytes** across all entries, enforced identically for
  zip **and** tar, so a compression- or sparse-tar bomb is rejected with
  `413`, plus a cap of 4,096 entries of every kind (directories and skipped
  entries included). Its structure is checked with bounded parsing before
  `archive/zip` or `archive/tar` reads it: a zip whose end record declares
  more than 4,096 entries or a central directory over 2 MiB is refused, the
  central-directory read is limited even when the end record understates it,
  and archive/tar reads a tar through a limit that allows at most 64 KiB of
  reads to reach each entry, counted from its first header block (its PAX,
  global PAX and GNU long-name or long-link headers with their data and
  padding, any sparse map, and its own header; the previous entry's padding
  is not counted, and unread entry data is skipped, not read), so every
  header it acts on is bounded, whatever sizes the archive declares. Two archives are parsed at a time;
  another waits up to five seconds and is then refused with `503`.
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
- **Timeouts.** `ReadHeaderTimeout` (15s) mitigates slowloris header stalls.
  Bodies and responses get per-route deadlines instead of a whole-request
  `ReadTimeout`/`WriteTimeout`, which would cut off large transfers and the
  live-reload stream; see [Resource limits](#resource-limits). The legacy
  `--write-timeout` (off by default) bounds each whole response from the
  start of the request: every per-route write deadline, downloads and
  live-reload streams included, is capped at it. `--idle-timeout` (default 120s) bounds idle
  keep-alives.
- **Upload cap.** `--max-upload <bytes>` (default 100 MiB; `0` = unlimited) bounds
  publish body size and returns `413` when exceeded.
- **Clickjacking.** Every response on both planes carries
  `X-Frame-Options: SAMEORIGIN` and a `frame-ancestors 'self'` CSP: an external
  site cannot frame a hosted page or the control UI. Same-origin framing stays
  allowed because the `?live` preview wrapper legitimately iframes the artifact's
  own URL.
- **Path canonicalization.** Both planes resolve path ambiguity before
  routing. A non-canonical path (`//`, `/./`, `/../`) is redirected (`307`) to
  its canonical form with its original escaping and query kept, never
  re-escaped and never to a `//host` reference. A path segment that decodes to
  `.` or `..`, contains an encoded or literal `/` or `\`, or carries a control
  character is refused with `400`.
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
  entirely: the control URL is the base URL, and the content URL is
  `--content-base-url` or, when that is unset, the base URL's scheme and host
  with the content port. For a public deployment, set both base URLs and an
  accepted-host policy, `--allowed-hosts <host[:port],...>`: a request whose
  `Host` is not listed (the base URL hosts are always included) gets
  `421 Misdirected Request` on both planes before routing.
- **Deletion is durable.** Delete, expiry and overwrite record the files they
  stop referencing in the same transaction that drops the reference. A file
  that cannot be unlinked (a read-only or failing disk) is logged and retried
  on every sweep instead of being orphaned, and an hourly reconcile removes
  orphaned generation files, so a deleted or expired page's bytes and retained
  source are always reclaimed. Every write records an intent before it creates
  a file and retires it in the commit, so reconcile never removes a write in
  flight, in this process or in SSH receive; an intent is reclaimed only after
  24 hours, and a write whose intent was reclaimed fails instead of committing.
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
IPv6 grouped by /64. Forwarding headers are ignored unless the peer is listed
in `--trusted-proxy` (IPs or CIDRs); then `X-Forwarded-For` is read from the
right, past every trusted hop, and the first other address is the client.
Entries further left are written by the client and never used. The
connection caps (`--max-conns-per-client`) are applied when a connection is
accepted, before any header is read, so they always see the proxy. Flags are on `serve`; `reply_*` keys go in the config file and exist
only in `-tags reply` builds.

| Surface | Limit | Default | Knob |
|---|---|---|---|
| View password | KDF runs at once, password checks and publish-time hashing together; more wait up to 5s, then `503` | half the cores, 1 to 4 | `--password-concurrency` |
| View password | one client's failed attempts on one artifact / on any artifact; past it `429` without hashing | 10 / 20 a minute (bursts 10, 10) | `--password-rate-client-target`, `--password-rate-client` |
| View password | all clients' failed attempts on one artifact / in all; once spent, clients that failed recently get `429` without hashing | 10 / 120 a minute (bursts 10, 30) | `--password-rate-target`, `--password-rate-global` |
| Live reload | streams per client on one artifact / per client in all; past it `503` | 2 / 16 | `--live-max-per-client-slug`, `--live-max-per-client` |
| Live reload | requests waiting for an ended stream's slot, in all / per client; they hold no request slot while waiting, wait at most 11s, and the excess gets `503` at once | 32 / 1 | `--live-max-pending`, `--live-max-pending-per-client` |
| Live reload | streams per artifact / in all; when full, a new client replaces the oldest stream of a client holding at least two more streams, otherwise `503` | 16 / 256 | `--live-max-per-slug`, `--live-max-total` |
| Request body | non-upload body must arrive within; uploads: grace and longest stall | 30s | `--body-timeout` |
| Request body | upload average rate after the grace; slower gets `408` | 16 KiB/s | `--upload-min-rate` |
| Response | write deadline per response; an upload's starts at its longest allowed body time plus this, and tightens to this once the body is read | 60s | `--response-timeout` |
| Response | extra time for an artifact or site file: size at this rate | 16 KiB/s | `--download-min-rate` |
| Request host | `Host` header length; longer gets `400` before routing | 255 bytes | (code) |
| Listings | artifacts per `/list` response or `/gallery` page / replies per `/replies` response; more via the `?after=` cursor | 200 / 100 | (code) |
| Listings | index entries one `/list` or `/gallery` page examines (expired ones skipped) | 1,201 | (code) |
| Browse index | index entries examined for families / per series counted (a series is counted to 100, shown as "100+") | 1,000 / 404 | (code) |
| Site archives | entries of every kind / zip central directory / tar headers in front of one entry / archives parsed at once (others wait 5s, then `503`) | 4,096 / 2 MiB / 64 KiB / 2 | (code) |
| TTL | `ttl` value length; longer is `400` before parsing | 64 bytes | (code) |
| Bulk rerender | sweeps at once (another gets `409`) / slugs read per page / results and failures listed | 1 / 500 / 100 each | (code) |
| Downloads | byte ranges honoured per request (more, or a `Range` header over 128 bytes, gets the whole body) | 1 | (code) |
| Admission | requests in progress per listener / per client; excess `503` | 256 / 32 | `--max-requests`, `--max-requests-per-client` |
| Admission | open connections per listener (further ones wait) / per client (further ones are closed) | 1024 / 64 | `--max-conns`, `--max-conns-per-client` |
| Rendering | table columns; table body cells per document (rows times header width) | 128; 1,000,000 | (code) |
| Rendering | rendered page size: every byte, meta-header, outline and chrome included, charged as it is produced; over it `413`, nothing stored | 32 MiB | `--render-max-output` |
| Rendering | markdown document per publish; received into a store scratch file (charged to the quota), then rendered and stored under a render slot | 8 MiB | (code) |
| Rendering | input lines / headings / list nesting depth; over it `413` | 2,097,152 / 50,000 / 64 | (code) |
| Rendering | series members a page navigates (its tree and prev/next row); a larger series renders without them | 1,000 | (code) |
| Rendering | frontmatter rendered as the meta-header: fields / block size / key / value; over it `413` | 256 / 64 KiB / 256 B / 8 KiB | (code) |
| Rendering | renders at once; one that waits 5s gets `503` | GOMAXPROCS, at most 4 | `--render-concurrency` |
| Storage | bytes of artifacts, retained sources and sites, including in-flight uploads, scratch and extracted archive bytes, removed files a download still holds open, and abandoned site staging trees, plus 4 KiB per artifact for its metadata and 4 KiB per site file or directory; over it `507` | unlimited | `--store-quota` |
| Storage | site files and directories in all, sites being published included; over it `507` | 100,000 | (code) |
| Metadata | filename hint length; longer is `400` | 255 bytes | (code) |
| Replies | submissions per client / per artifact / in all; past it `429` | 10 / 60 / 300 a minute | `reply_rate_client`, `reply_rate_target`, `reply_rate_global` |
| Replies | submit body must arrive within | 10s | (code) |
| Replies | replies per artifact (`429`); replies and reply text in all (`507`) | 1000; 100,000 and 64 MiB | `reply_max_per_artifact`, `reply_max_total`, `reply_max_bytes` |
| Replies | replies older than this are deleted, by an hourly sweep | off (kept) | `reply_retention` |
| Reply hooks | events running at once / admitted and waiting; a full pool refuses the reply with `503` | 2 / 32 | `reply_hook_workers`, `reply_hook_queue` |
| Reply hooks | exec run time; the hook's process group is killed when the shell exits or this passes | 5m | `reply_hook_timeout` |

Notes on the limits:

- **Password checks.** PBKDF2 keeps its 600,000-iteration work factor. Only
  failures spend budget. A correct password is refunded and remembered (an
  HMAC of the stored hash and the password under a per-process key) until it
  goes unused for 10 minutes. A client that keeps failing is refused before
  hashing, but a viewer with no recent failures is always verified, so other
  clients' guesses cannot lock it out. Only completed failures count; a
  client's own attempt still being checked does not. Guessing spread over many fresh
  addresses is bounded by the KDF concurrency and work factor rather than by
  a rate; use a strong password. Behind a proxy, set `--trusted-proxy`, or
  every user shares the proxy's budget. Reply routes, the `?live` wrapper and
  the live-reload stream (including its re-checks) run the same check.
  Setting a password at publish runs the same KDF under the same concurrency
  limit, so password publishes cannot take more cores than password checks;
  an SSH directory sync hashes its password once.
- **Rendering.** Every byte a render produces goes through one budgeted
  writer, charged before it is appended: the body, the frontmatter
  meta-header, the outline and the page chrome share one budget, and the page
  is assembled once at its exact size. Text that is built before it is
  written (a paragraph, quote, list item, code block, table row or heading) is
  held to the remaining budget by an upper bound on its formatted size as it
  accumulates. What the render keeps per input element that is not output is
  capped instead: lines are indexed by offset in the one copy of the input,
  and lines, headings, list depth and frontmatter fields have the limits in
  the table. Heading ids are capped at 64 bytes, so a long heading cannot make
  every reference to it long. A render's memory stays within a small multiple
  of the output budget plus the input (tested at the 8 MiB input cap). A
  `?render=md` body is received into a scratch file in the store while it
  arrives, so slow or stalled uploads hold neither memory nor a render slot;
  the document and its rendered page are held in memory only under a render
  slot, through the store write, so memory for renders is bounded by
  `--render-concurrency` × (8 MiB + `--render-max-output`).
- **Order of checks.** Body and write deadlines are set first, so they also
  bound a request refused before routing (a service-worker fetch, an unknown
  `Host`, an ambiguous path). Admission counts only requests that pass those
  checks; the cross-origin write guards and the routes come after it.
- **Replies.** A reply is accepted only for an artifact whose publish enabled
  replies (`?reply=form` or `?reply=question`); the choice is stored with the
  artifact and replaced by every publish to the slug. The content listener
  refuses cross-origin browser writes, judged against the content origin
  (`--content-base-url`, else derived as for content URLs, compared as scheme,
  host and port), except from an opaque origin (`Origin: null`, which is what
  a sandboxed artifact page sends) whose `Referer` is absent or names the
  content origin. Behind a proxy, set the base URLs so the public origin is
  the one compared. That guard cannot tell two pages on the content origin
  apart: every sandboxed page posts with `Origin: null`, a page can omit its
  Referer, and the browser may attach the viewer's cached password for the
  target. What binds a browser submission to its target is the submission
  token every reply form carries: an HMAC of the slug and the artifact's
  publish identifier under a key in the store's metadata database, required
  on every browser submission (one carrying `Origin` or `Sec-Fetch-Site`) and
  refused unless it names exactly that slug and its current publish. Every
  publish, including one that only changes the password or privacy, gets a
  new random identifier (a rebake keeps it), and a reply is recorded only if,
  inside the reply database's write transaction and immediately before the
  insert, the slug still names the publish the submission was checked against
  with replies enabled. A republish that commits before that check refuses
  the submission (`409`: nothing is stored and no hook fires), including one
  that lands while the submission waits for the reply store; one that commits
  after it is ordered after the reply, which was admitted under the publish
  still current at that point. A sandboxed
  page cannot read another artifact's page to learn its token. Admission also
  needs the per-artifact opt-in, the view password and the reply limits. Not
  covered: with `--unsafe-no-sandbox` a page can read others' tokens, and
  clients that send no browser headers are treated as non-browser clients.
  The per-artifact cap and the aggregate limits are checked and the reply
  inserted in one transaction, so concurrent submissions cannot overshoot.
- **Reply hooks.** A submission takes its place in the hook pool before the
  reply is stored, so no stored reply is ever dropped by a full pool. Hook
  output is read as it streams and only the first 512 bytes are kept; they
  reach the log only with `reply_hook_log_output = on`. Work
  meant to outlive a hook must detach (`systemd-run`, `setsid`); a plain
  background child is killed with the hook's process group.
- **Storage quota.** Not counted: the metadata database's actual size (each
  artifact instead counts a fixed 4 KiB allowance, and its per-row fields are
  bounded), the filesystem cost of a site entry beyond its 4 KiB allowance,
  module data (replies, TLS material) and filesystem block overhead.
  Temporary files that a failed cleanup or a crash left in the store are
  counted until the hourly reconcile removes them: an hour after they were
  last written, or, for the files of a write that was still in progress (its
  write intent is recorded), 24 hours after it started; a temporary file `demiplane receive` is still writing counts
  toward the server's usage too.
  An overwrite or rebake needs room for the new copy while the old one still
  exists; a site publish needs room for its archive plus the extracted files.
  A replaced or deleted file that a download still holds open stays charged
  until the download ends. `demiplane receive` runs in its own process and
  cannot see the server's downloads, so it leaves the files of a replaced
  artifact for the server's sweep to remove; until then they stay on disk and
  counted, for up to `--sweep-interval` (a minute by default), or until a
  server next starts. Leftovers of a failed site publish or a crash stay
  charged until startup or the hourly reconcile removes them, an hour after
  they were last written; a cleanup releases only what it actually removed.
  `demiplane receive` runs as a separate process and enforces the quota
  against its own writes; a running `serve` learns of them at its next
  recount (every `--sweep-interval`), so across processes the store can
  briefly exceed the quota by what the other process wrote since.

## Reporting

See **[Reporting a vulnerability](#reporting-a-vulnerability)** at the top of this
document for how to disclose a security issue privately.
