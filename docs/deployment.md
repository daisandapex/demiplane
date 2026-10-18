# Deployment

How to run a demiplane instance: binding and exposure, the primary LXC path,
reachability tiers, the two-origin topology, the `serve` flag reference, the
config file, and the Docker image.

demiplane is **domain-agnostic**: it *binds* to an address and *advertises* a base URL
(`--base-url`); how that URL is reached is a deployment choice, not the binary's job.

**Internal-first by default:** `--bind` defaults to **`127.0.0.1:8080`** (loopback only) —
out of the box demiplane is not reachable from another machine. You **opt into** exposure
by binding a specific LAN/mesh IP (e.g. a Netbird address) or `0.0.0.0`. Binding a
wildcard/all-interfaces address logs a startup warning (and a second one if no token is
set), so an accidental public exposure is loud, not silent.

**Primary v1 path — single binary in an LXC, reached by mesh/LAN IP:**

```sh
# build the binary (plain `go build ./...` compiles but emits no binary)
go build -o demiplane ./cmd/demiplane

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

## Reachability tiers

A *deployment* decision:

| Tier | Example | DNS | TLS | Note |
|---|---|---|---|---|
| Raw IP:port | `192.168.1.50:8080` | none | none | **v1 default**; trusted LAN/mesh |
| Local/private DNS | `host.demiplane.example` | private | self-signed | **avoid for browsers** — DoH bypasses local resolvers, names often won't resolve |
| Custom domain → private IP | `reports.example.com` | public A → private IP | real (DNS-01) | browser-safe + valid cert + still private; put a reverse proxy (e.g. Caddy) in front and pass the matching `--base-url` |

demiplane never touches DNS, and terminates TLS only when you opt into the native TLS
module (build tag `tls` + `tls = on`; see [Native TLS](./tls.md)) — otherwise a custom
domain is just a reverse proxy in front plus `--base-url`. **Do not trust
`X-Forwarded-Host`/`-Proto`** for URL generation; use explicit `--base-url` when set, fall
back to `Host` only when unset.

## Two origins (origin isolation)

demiplane listens on **two** ports by default — the control plane (`--bind`, `:8080`) and
the artifact content origin (`--content-bind`, `:8081`). `POST /publish`, `GET /list`, and
`DELETE /{slug}` are **control-plane** routes; artifact URLs returned by `/publish` and
`/list` point at the content origin. Behind a reverse proxy or with TLS, route **both**
upstreams and declare the public content origin with
`--content-base-url https://content.example.com` (this is where the TLS path plugs in;
a separate content **hostname** also gains cookie isolation, which ports alone do not). To
run the old single-port topology, pass `--unsafe-same-origin`. See
[ADR 0003](./adr/0003-content-origin-isolation.md).

## Public or proxied deployments

Anything reachable by clients you do not control should not derive its URLs
from the request `Host`, which the client chooses. Set all three:

```sh
demiplane serve --store /var/lib/demiplane \
  --base-url https://demiplane.example.com \
  --content-base-url https://content.example.com \
  --allowed-hosts demiplane.example.com,content.example.com
```

`--base-url` and `--content-base-url` make every advertised URL (publish
responses, `/list`, `llms.txt`, rendered pages) authoritative. With only
`--base-url` set, the content URL is its scheme and host with the content port.
`--allowed-hosts` is a comma-separated list of `host` (any port) or `host:port`
entries; a request naming any other `Host` gets `421 Misdirected Request` on
both listeners. The base URL host names are always accepted on any port, so the
list only needs the extra names (a mesh IP, an internal hostname) you also
serve; list those without a port to accept both the control and content
ports, since a bare `Host` means the scheme's default port. Behind a
TLS-terminating proxy, `--base-url` is also what lets the cross-origin write
guard recognize the browser's `https://` origin.

## `serve` flags

`--bind <host:port>` (default `127.0.0.1:8080` — control plane; set a
mesh/LAN IP or `0.0.0.0` to expose), `--content-bind <host:port>` (artifact content origin;
default = host of `--bind` with port +1, i.e. `:8081`), `--content-base-url <url>`
(advertise the content origin for proxy/TLS deployments), `--unsafe-same-origin` (serve both
on one origin — legacy footgun), `--unsafe-no-sandbox` (serve HTML/SVG/XML without the
per-document sandbox, so every page can read every artifact a viewer can open; trusted content
only, see [DESIGN.md](../DESIGN.md#content-isolation)), `--store <dir>` (**required**), `--base-url <url>`
(optional; advertise a canonical URL instead of deriving it from the request `Host`),
`--allowed-hosts <host[:port],...>` (optional; serve only these `Host` values plus
the base URL hosts, `421` for anything else; see above),
`--token-file <path>` (optional; bearer token for write/list — `DEMIPLANE_TOKEN` is the
env-var alternative), `--sweep-interval <dur>` (optional; how often to reap past-TTL
artifacts, default `1m`), `--browse` (optional; serve an HTML index of non-private
artifacts at `GET /` and make `GET /gallery` public; without it `/gallery` needs the bearer
token), `--max-upload <bytes>` (optional; cap publish size, default
100 MiB, `0` = unlimited), `--theme <name>` (optional; skins the whole instance — chrome and
`?render=md` pages — one of `light` (default house style), `dark`, or the pinned dark
developer palettes `catppuccin` / `dracula` / `one-dark`), `--css <file>` (optional; a custom
stylesheet for `?render=md` pages that replaces the built-in theme), `--header` /
`--footer` (optional; default on — the rendered-page title bar and vanity footer),
`--footer-link <url>` (optional; the footer link target, default the demiplane repo),
`--meta-header` (optional; default on — render a frontmatter-driven meta-header on
`?render=md` pages, with `--meta-header=false` stripping frontmatter instead),
`--idle-timeout` (idle keep-alive timeout, default `120s`), `--write-timeout` (legacy
whole-response timeout, default off; it also ends live-reload streams). Resource limits
(defaults in brackets; `0` disables each of these): `--body-timeout` [`30s`] and
`--upload-min-rate` [16 KiB/s] for request bodies, `--response-timeout` [`60s`] and
`--download-min-rate` [16 KiB/s] for responses, `--max-requests` [256] and
`--max-requests-per-client` [32] in progress per listener, `--max-conns` [1024] and
`--max-conns-per-client` [64] open per listener, and `--store-quota` [0 = unlimited] for the
store as a whole. For `?render=md`, `--render-max-output` [32 MiB] must be positive and
`--render-concurrency` [GOMAXPROCS, at most 4] takes its default at `0`. Behind a
reverse proxy, set `--trusted-proxy <ip-or-cidr,...>` to the proxy's addresses so
per-client limits are keyed by the client in `X-Forwarded-For` (read from the right, past
trusted hops); without it every client shares the proxy's address. The proxy must set or
append `X-Forwarded-For`, and `--max-conns-per-client` still counts the proxy's
connections, so raise it or set it to `0`. View-password limits: `--password-concurrency`
[0 = half the cores, 1 to 4] and failure rates written `N` or `N:BURST` a minute (`0` turns
one off): `--password-rate-client-target` [`10:10`], `--password-rate-client` [`20:10`],
`--password-rate-target` [`10:10`], `--password-rate-global` [`120:30`]. Live-reload stream
caps (each at least 1): `--live-max-per-client-slug` [2], `--live-max-per-client` [16],
`--live-max-per-slug` [16], `--live-max-total` [256], and requests waiting for a freed
stream slot: `--live-max-pending` [32], `--live-max-pending-per-client` [1]. See [SECURITY.md](../SECURITY.md#resource-limits). Content-type
is inferred from the filename extension, then sniffed from the bytes; known web types
serve **inline**, everything else as a download.

## Config file

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
module, see [Rendering & replies](./rendering.md)) and `tls`/`tls_*` (native TLS
module, see [Native TLS](./tls.md)). A key whose module is **not** in the binary is
treated as unknown and fails startup loudly, like any typo.

The render theme system (OKLCH tokens, type scale, the rendered-page chrome) is documented
in [DESIGN.md](../DESIGN.md).

**Precedence: CLI flag > config file > built-in default.** A flag you pass on the command
line always wins over the file; the file wins over the defaults. The `theme` here also sets
the initial light/dark state of the per-page toggle (which viewers can then override on
their own device; the choice persists in `localStorage`). With no `theme` set anywhere, a
rendered page defaults to light but the toggle's initial state follows the viewer's OS
`prefers-color-scheme`.

## Docker

```sh
docker build -t demiplane .
# map BOTH ports: 8080 control plane, 8081 isolated artifact content origin (ADR 0003)
docker run --rm -p 8080:8080 -p 8081:8081 -v demiplane-data:/var/lib/demiplane demiplane
```

The image is a static binary on a distroless base (no shell, non-root). The container binds
`0.0.0.0:8080` (control) and `0.0.0.0:8081` (content) internally; control exposure with how
you map the ports. Pass extra flags by overriding the command, e.g.
`docker run ... demiplane serve --bind 0.0.0.0:8080 --store /var/lib/demiplane --browse`.

### Pin the image by digest

A tag is a mutable pointer, not a version: whoever controls the registry
namespace can move `:1.2` or `:latest` to different bytes at any time, and every
later `docker pull` silently picks the new content up. A digest is the SHA-256 of
the image manifest, so `image@sha256:…` names exactly one set of bytes forever
and the daemon verifies what it received against that hash. Pin the digest
anywhere the reference is meant to be reproducible — a Compose file, a Kubernetes
manifest, a CI job, a base image in your own `Dockerfile`:

```sh
# pull an exact, immutable image
docker pull demiplane@sha256:0123…            # 64 hex chars, no tag

docker run --rm -p 8080:8080 -p 8081:8081 \
  -v demiplane-data:/var/lib/demiplane \
  demiplane@sha256:0123…
```

Read the digest of an image you already have, or of one in a registry, and paste
that value into the reference you keep:

```sh
docker image inspect --format '{{index .RepoDigests 0}}' demiplane:latest
docker buildx imagetools inspect demiplane:latest    # registry-side, no pull
```

demiplane's own `Dockerfile` follows the same rule: the builder and distroless
base images are pinned by digest with the human-readable tag left in a comment,
so a rebuild months later starts from the bytes the build was tested against.

## Upgrading

Every program that writes to a store is a demiplane binary: `serve`, and
`receive` wherever it runs (an SSH forced command, a cron job, a CI step). Upgrade
all of them together, and stop the old ones before the new `serve` first opens
the store.

The first time a binary of this release opens a store, it moves the content
directories `blobs/`, `sources/` and `sites/` into `data/` and leaves a small
text file at each old name saying so. A binary from 2.0.0 or earlier then cannot
open the store at all: `serve` and `receive` stop at start-up (`create blob dir:
… not a directory`) before reading or writing anything, so they can neither
publish, republish, rebake, publish a site nor delete. (Before this check, such
a binary could republish a page and leave replies on and its reply tokens
valid.) The database refuses older writers' row changes too, as a second line.

The move is a rename within the store directory, so it is quick whatever the
store holds. Each directory's move is synced to disk before its marker is
written, so a crash or power loss at any point leaves either the old directory
or the new one, and an interrupted move finishes at the next open. Processes
starting together (`serve` and a `receive`) take turns through it, holding
`.open.lock` in the store directory. If an older binary recreated an old
directory in the meantime, an empty one is removed; one that holds files stops
the open with a message naming both directories, and its files have to be
moved into `data/` by hand. A marker whose directory under `data/` is missing
also stops the open: restore that directory from a backup, or create it empty
to start without its content.

Rolling back is therefore a restore: back up the store directory before
upgrading, and to return to an older release, stop demiplane and restore that
backup. Publishes made after the upgrade are not in it.

The store directory holds `meta.db` (with its `-wal` and `-shm` files), `data/`
(`blobs/` for artifact bytes, `sources/` for retained markdown, `sites/` for
site trees), `modules/` (module storage, such as replies), the lock files
`.open.lock` and `.serve.lock` (held by the running `serve`), and the three
marker files. All of it is owner-only.

## See also

- [HTTP API](../API.md) — endpoints, publish parameters, auth model
- [Rendering](./rendering.md) — themes and `?render=md` chrome the config file controls
- [Native TLS](./tls.md) — terminating TLS in the binary instead of a proxy
- [SSH transport](./receive.md) — the second ingest path into the same store
- [Architecture](./architecture.md) — what the binary is made of
- [ADR 0003 — content origin isolation](./adr/0003-content-origin-isolation.md)
