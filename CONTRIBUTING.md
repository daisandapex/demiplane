# Contributing to demiplane

Thanks for your interest. demiplane is a small, dependency-light Go project with a
deliberate design (internal-first, single static binary, minimal dependencies).
Contributions that keep it that way are most welcome.

## Development setup

```sh
git clone https://github.com/daisandapex/demiplane
cd demiplane
go build ./...
go test ./...
```

Go 1.26+ is required (the project uses the stdlib `crypto/pbkdf2`).

## Where issues live

Bugs, feature requests, and questions go to **[GitHub Issues](https://github.com/daisandapex/demiplane/issues)**.
Search first; a short repro beats a long description. For anything that does not fit
an issue, email **tekton@daisandapex.com**. Security issues are the
exception — **do not** file them publicly; email **security@daisandapex.com** and see
[Reporting security issues](#reporting-security-issues) below.

## License & Contributor License Agreement (CLA)

demiplane is licensed under the **GNU AGPL-3.0-only**. By contributing you agree
that your contributions are made under that license **and** that you accept the
project's [Contributor License Agreement](./CLA.md).

The CLA grants the maintainer (Dais & Apex) the rights needed to keep
offering demiplane under AGPL-3.0 **and** to grant commercial/alternative
licenses (see [COMMERCIAL-LICENSE.md](./COMMERCIAL-LICENSE.md)) — this dual-
licensing is what funds continued development. You retain copyright in your
contributions; you grant a broad license to use and relicense them.

**Every PR must accept the CLA.** Until [CLA-assistant](https://cla-assistant.io)
is wired up on the repository, state in your first PR:

> I have read and agree to the demiplane CLA (CLA.md).

## Before you open a pull request

Run the same gates CI runs — all must be green:

```sh
gofmt -l .          # must print nothing
go vet ./...
go build ./...
go test -race ./...
# and the module-tagged builds when your change touches (or could touch) them:
go build -tags "reply tls" ./...
go test -race -tags "reply tls" ./...
```

- **Tests are required** for new behavior. Prefer table-driven tests; cover the
  changed paths and the security-relevant edge cases (auth, slug validation,
  escaping, expiry).
- **Keep the dependency set tiny.** The only dependencies are the pure-Go
  SQLite driver and `golang.org/x/crypto` (ACME support in `-tags tls` builds).
  New dependencies need a strong justification and are subject to a
  14-day supply-chain quarantine (don't add anything published in the last two
  weeks). Prefer the standard library.
- **Update the docs in the same change.** If you change a flag, query parameter,
  endpoint, or behavior, update `README.md` (and `SECURITY.md` / `CHANGELOG.md`
  where relevant) in the same PR.
- **Security-sensitive code** (auth, rendering, slug handling, anything touching
  the store path) gets extra scrutiny — explain the threat you considered.

## Commit and PR conventions

- Conventional-commit style subject lines (`feat:`, `fix:`, `docs:`, `chore:`,
  `security:`), imperative mood, ≤72 chars.
- Describe *what* changed and *why* in the body.
- One logical change per PR where practical.

## Branching and releases

- `main` is protected and always releasable; it is never committed to directly.
- Work on a short-lived branch off `main` and open a pull request. The CI gates
  above must pass and commits must be signed before a pull request can merge;
  merges preserve commit authorship (no squash).
- demiplane follows one line of development. Features and fixes both merge to
  `main`; a release is a tag on `main`, not a separate branch.
- Releases follow [Semantic Versioning](https://semver.org): a breaking change is
  a major bump, a backward-compatible feature is a minor bump, and a fix-only
  release is a patch. Releases are cut deliberately (version bump +
  `CHANGELOG.md` entry + `vX.Y.Z` tag), not on every merge.

## Architecture quick reference

| Package | Responsibility |
|---|---|
| `cmd/demiplane` | CLI: `serve`, `receive`, `version` |
| `internal/store` | Flat-file content + SQLite metadata, slugs, TTL, passwords |
| `internal/server` | HTTP handlers, auth, browse page |
| `internal/transport` | SSH ingest (`receive`) |
| `internal/render` | Markdown→HTML (dependency-free subset) |

## Reporting security issues

Do **not** open a public issue. See [SECURITY.md](./SECURITY.md).
