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

## `serve` flags

`--bind <host:port>` (default `127.0.0.1:8080` — control plane; set a
mesh/LAN IP or `0.0.0.0` to expose), `--content-bind <host:port>` (artifact content origin;
default = host of `--bind` with port +1, i.e. `:8081`), `--content-base-url <url>`
(advertise the content origin for proxy/TLS deployments), `--unsafe-same-origin` (serve both
on one origin — legacy footgun), `--store <dir>` (**required**), `--base-url <url>`
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
`--write-timeout` / `--idle-timeout` (optional HTTP timeouts). Content-type
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

## See also

- [HTTP API](../API.md) — endpoints, publish parameters, auth model
- [Rendering](./rendering.md) — themes and `?render=md` chrome the config file controls
- [Native TLS](./tls.md) — terminating TLS in the binary instead of a proxy
- [SSH transport](./receive.md) — the second ingest path into the same store
- [Architecture](./architecture.md) — what the binary is made of
- [ADR 0003 — content origin isolation](./adr/0003-content-origin-isolation.md)
