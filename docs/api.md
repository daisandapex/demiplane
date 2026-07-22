# HTTP API

The complete HTTP surface — endpoints, `POST /publish` query parameters, slugs,
file types, limits, ephemeral artifacts, the gallery, multi-file sites, and
live-reload — plus demiplane's two-layer auth model (publish auth vs. view auth).

## Endpoints

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

The write/list/delete/`/connect`/`/gallery` routes live on the **control plane**
(`--bind`, default `127.0.0.1:8080`); the artifact-body routes (`GET /{slug}`,
sites, and the SSE `/_events` stream) live on the separate **content origin**
(`--content-bind`, default `:8081`). See [Deployment](./deployment.md) and
[ADR 0003](./adr/0003-content-origin-isolation.md). `--unsafe-same-origin`
collapses both onto one listener for local use.

> **`POST /publish` goes to the control plane** (default port `8080`), not the
> content origin (`8081`). Posting a publish request to the content origin
> returns `405`.

## `POST /publish` query parameters

All optional:

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
Supplying `?password=` is rejected with `400`. Reads supply it via HTTP Basic (see
[Auth model](#auth-model)).

`[auth]` endpoints require the bearer token when one is configured (see
[Auth model](#auth-model)); `GET /{slug}` is always open at the transport layer — read
protection is the view-auth layer (network / capability URL / per-artifact password), not
the publish token.

## Behavior

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
  single-file pipe, and tar-stream directory sync, all into the same store (see
  [SSH transport](./receive.md)).
- **File types:** accepts *any* file. Known web types (HTML/CSS/JS/images/PDF/…) serve
  **inline**; unknown types serve as `Content-Disposition: attachment` (download). No
  type allowlist.
- **Limits:** uploads are capped at 100 MiB by default (`--max-upload <bytes>`; set
  `--max-upload=0` for unbounded streaming to disk — bounded memory either way);
  per-store/per-owner **quotas** arrive with the public-hardening scope.
- **Ephemeral:** optional `?ttl=` so artifacts (e.g. one-off reports) expire automatically;
  a background sweeper reaps past-TTL files and an expired URL 404s immediately.
- **Optional render:** `?render=md` converts a markdown body to a styled HTML page on
  publish. See [Rendering](./rendering.md) for themes, syntax highlighting, heading
  anchors, the colophon, and the frontmatter meta-header.
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

## Examples

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
> origin (the legacy footgun) if you accept it. See
> [ADR 0003](./adr/0003-content-origin-isolation.md).

## See also

- [Deployment](./deployment.md) — binding, reachability tiers, `serve` flags, config file, Docker
- [Rendering](./rendering.md) — `?render=md`, themes, inline replies
- [SSH transport](./receive.md) — the `demiplane receive` ingest path
- [Harness integration](./harness.md) — MCP, CLI, curl, capture hook
- [Native TLS](./tls.md) — the optional in-binary TLS module
- [Architecture](./architecture.md) — the binary, store, and module seam
- [ADR 0003 — content origin isolation](./adr/0003-content-origin-isolation.md)
