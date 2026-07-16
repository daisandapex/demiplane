# Agent Instructions

Guidance for AI coding agents working in this repository. It mirrors
[CONTRIBUTING.md](./CONTRIBUTING.md) and [CLAUDE.md](./CLAUDE.md); read those for
the full contributor workflow and architecture reference.

## Build & test

Go 1.26+ is required.

```sh
go build ./...
go test ./...
go build -tags "reply tls" ./...   # optional build-tagged modules
go test -tags "reply tls" ./...
```

Before proposing a change, run the same gates CI runs — all must be green:

```sh
gofmt -l .          # must print nothing
go vet ./...
go build ./...
go test -race ./...
go build -tags "reply tls" ./...
go test -race -tags "reply tls" ./...
```

## Conventions

- Keep the dependency set tiny; prefer the standard library.
- Tests are required for new behavior — prefer table-driven tests covering the
  changed paths and security-relevant edge cases.
- Update `README.md` / `SECURITY.md` / `CHANGELOG.md` in the same change when you
  change a flag, endpoint, query parameter, or behavior.
- Conventional-commit subject lines (`feat:`, `fix:`, `docs:`, `chore:`,
  `security:`), imperative mood, ≤72 chars.

## Non-interactive shell commands

Use non-interactive flags with file operations so an agent never hangs on a
confirmation prompt (`cp`/`mv`/`rm` may be aliased to `-i` on some systems):

```sh
cp -f source dest
mv -f source dest
rm -rf directory
```

`scp`/`ssh` accept `-o BatchMode=yes`; `apt-get` accepts `-y`.

## Issues

Bugs and feature requests go to
[GitHub Issues](https://github.com/daisandapex/demiplane/issues). Do **not**
file security issues publicly — see [SECURITY.md](./SECURITY.md).
