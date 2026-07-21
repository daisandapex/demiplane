# demiplane

Self-hosted, internal-first static & HTML publishing with a REST API — a single
static Go binary, minimal dependencies, no upstream service.

This file is guidance for AI coding agents working in this repository. Human
contributors should read [CONTRIBUTING.md](./CONTRIBUTING.md), which this file
mirrors.

## Build & test

Go 1.26+ is required (the project uses the stdlib `crypto/pbkdf2`).

```sh
go build ./...
go test ./...

# Optional modules are behind build tags — build and test them when your change
# touches (or could touch) them:
go build -tags "reply tls" ./...
go test -tags "reply tls" ./...
```

Run the full gate set before proposing a change — all must be green:

```sh
gofmt -l .          # must print nothing
go vet ./...
go build ./...
go test -race ./...
go build -tags "reply tls" ./...
go test -race -tags "reply tls" ./...
```

## Architecture

| Package | Responsibility |
|---|---|
| `cmd/demiplane` | CLI: `serve`, `receive`, `version` |
| `internal/store` | Flat-file content + SQLite metadata, slugs, TTL, passwords |
| `internal/server` | HTTP handlers, auth, browse/gallery pages |
| `internal/transport` | SSH ingest (`receive`) |
| `internal/render` | Markdown→HTML (dependency-free subset) |
| `internal/modules` | Optional, build-tagged features (`reply`, `tls`) |

Optional features are compiled in via the module-extension pattern (see
`docs/adr/0001-module-extension-pattern.md`).

## Conventions

- **Keep the dependency set tiny.** The only dependencies are the pure-Go SQLite
  driver and `golang.org/x/crypto` (ACME support in `-tags tls` builds). New
  dependencies need strong justification; prefer the standard library.
- **Tests are required** for new behavior. Prefer table-driven tests; cover the
  changed paths and the security-relevant edge cases (auth, slug validation,
  escaping, expiry).
- **Update the docs in the same change.** If you change a flag, query parameter,
  endpoint, or behavior, update `README.md` (and `SECURITY.md` / `CHANGELOG.md`
  where relevant) in the same change.
- **Conventional-commit style** subject lines (`feat:`, `fix:`, `docs:`,
  `chore:`, `security:`), imperative mood, ≤72 chars. Describe *what* changed and
  *why* in the body. One logical change per commit where practical.
- **Security-sensitive code** (auth, rendering, slug handling, anything touching
  the store path) gets extra scrutiny — explain the threat you considered.

## Branching & releases

- `main` is protected and always releasable. Do not commit to it directly.
- Each unit of work gets its own short-lived branch off `main`; open a pull
  request back to `main`. The full gate matrix above and a verified commit
  signature are required to merge. Merges use a merge commit or rebase, never
  squash (squash rewrites authorship).
- There is one line of development. Features and fixes both land on `main`; a
  release is a tag on `main`, not a separate branch. You do not work "on" a
  version, you tag one when you ship.
- **Versioning is [SemVer](https://semver.org).** The number describes the change
  since the last release: a breaking change is a **major** bump, a
  backward-compatible feature is a **minor** bump, and a fix-only release is a
  **patch**.
- Landing work and cutting a release are separate decisions. `main` accumulates
  merges; a release is cut deliberately when there is user-meaningful value,
  bumping the version, updating `CHANGELOG.md`, and tagging `vX.Y.Z`. A
  maintenance branch (`release/N.x`) is created only if an already-released major
  needs a backport after `main` has moved on.

## Issues

Bugs, feature requests, and questions go to
[GitHub Issues](https://github.com/daisandapex/demiplane/issues). Security
issues are the exception — do **not** file them publicly; see
[SECURITY.md](./SECURITY.md).
