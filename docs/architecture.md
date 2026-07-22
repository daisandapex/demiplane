# Architecture & repo layout

The shape of the binary — storage, the tiny-core/optional-module split, TLS
posture — and a map of what lives where in the repository.

## Architecture (target)

- Single **Go** binary — embedded `net/http` file server, no runtime deps. Build it with
  `go build -o demiplane ./cmd/demiplane` (plain `go build ./...` compiles but emits no
  binary).
- **SQLite** for metadata (slug, owner, content_type, size, created_at, ttl, private, …);
  flat files for content. Uploads stream to disk.
- **Tiny core, optional modules.** Core is publish / serve / auth / TTL / slugs
  (and the markdown render-on-publish styling) — nothing speculative. Additional
  capabilities are compile-time **modules** (Caddy-style registry, no runtime
  `plugin`) behind build tags, so they aren't in the default binary at all. See
  [MODULES.md](./MODULES.md) and
  [ADR 0001](./adr/0001-module-extension-pattern.md). The inline-reply
  feature (`-tags reply`) is the first module; native TLS (`-tags tls`,
  [ADR 0004](./adr/0004-native-tls-module.md)) is the second.
- TLS: a reverse proxy (Caddy) in front, **or** the native TLS module — both
  optional, never a dependency.
- Zero coupling to pico/pgs — independent implementation.

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

## See also

- [Modules — developer guide](./MODULES.md) — writing a build-tagged module
- [HTTP API](../API.md) — the surface `internal/server/` exposes
- [Rendering](./rendering.md) — `internal/render/` and the reply module
- [SSH transport](./receive.md) — `internal/transport/`
- [Deployment](./deployment.md) — running the binary and the Docker image
- [Native TLS](./tls.md) — the `tls` module
- [ADR 0001 — module extension pattern](./adr/0001-module-extension-pattern.md)
- [CHANGELOG](../CHANGELOG.md) — what shipped, by milestone
