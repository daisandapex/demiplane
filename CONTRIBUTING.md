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

Go 1.26.11 or newer is required, matching the `go` line in `go.mod`.

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

## Fuzzing

Everything that parses input demiplane did not write (markdown, slugs and
site paths, request paths and headers, archives, MCP messages, reply
submissions, TTLs, cursors, stored records) has a native Go fuzz target
(`func FuzzXxx(f *testing.F)`) in a `fuzz_test.go` beside the code. CI does
not run them; run them before you change a parser, and after:

```sh
make fuzz                     # every target, 60s each
scripts/fuzz.sh 300           # every target, 5 minutes each
scripts/fuzz.sh 120 Render    # targets whose name matches a regexp
```

The script runs one target at a time with `GOFLAGS=-p=1`, `-parallel 2` and
no `-race`, so it fits a small machine, and prints one line per target: its
name, package, executions, new corpus entries, and `PASS` or the crasher's
path. The regexp matches target names only (`scripts/fuzz.sh --list Redirect`
shows what it selects). Every go process runs under `GOMEMLIMIT`
(`FUZZ_GOMEMLIMIT`, default 1GiB) and `ulimit -v` (`FUZZ_VMEM_KB`, default
4 GiB). Each target gets a fresh `TMPDIR` and `GOTMPDIR` under `FUZZ_TMPDIR`
(default `$TMPDIR`), which the script moves into `delete-me/` beside it when
the target ends; empty that directory yourself. The script never deletes, so
it refuses to start, naming the directory, while `delete-me/` holds more than
`FUZZ_DELETE_ME_MAX_KB` KiB (default 2 GiB). Put `FUZZ_TMPDIR` on a tmpfs
if you can: the store-backed targets open SQLite per input and are slow on a
syncing disk. `scripts/test-fuzz.sh` tests the script itself.

The script sets `DEMIPLANE_FUZZ_TIMING=1`, which turns on the targets'
wall-clock bounds. Plain `go test` leaves them off, because it replays the
seeds on machines of any speed and load; where the code takes a context the
bound is a generous context deadline instead and always applies. To fuzz one
target by hand, keep to the same flags:

```sh
DEMIPLANE_FUZZ_TIMING=1 GOFLAGS=-p=1 go test -run '^$' -fuzz '^FuzzRender$' -fuzztime 60s -parallel 2 ./internal/render
```

**Adding a target.** Put it in the package's `fuzz_test.go`. Seed it with
`f.Add` from the package's own test fixtures and the inputs of earlier
fixes. Assert the promise the code makes (what `SECURITY.md` says of it: no
script in rendered HTML, a path that stays under its root, a cap that holds,
a redirect that stays on this origin, a value that reaches SQL only as a
bound parameter, a round trip), not only that it does not panic. State the
property independently of the code: do not call the function's own helpers
or copy its logic as the oracle. Where a rule refuses input, assert the
refusal as well as the acceptance. Bound the time one input may take: with
a context deadline inside the call if the code takes one, otherwise only
when `DEMIPLANE_FUZZ_TIMING` is set. Never reach the network: use the
package's in-process servers, a recording `http.RoundTripper`, and
`f.TempDir()`. `scripts/fuzz.sh` finds new targets by name; there is no
list to update.

**Crashers.** A failing input is minimised and written to
`testdata/fuzz/<Target>/<hash>` in the package, and from then on plain
`go test` replays it. Commit it with the fix as its regression test. If the
invariant was too strict rather than the code wrong, fix the target and keep
the input anyway, with a comment in the target saying why it is allowed.

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
- Pushing the tag is the whole release. `.github/workflows/release.yml` builds
  `linux/amd64`, `linux/arm64`, and `darwin/arm64` binaries, writes `SHA256SUMS`,
  records a GitHub build provenance attestation for each binary, and publishes
  the GitHub release. A tag with a pre-release suffix (`v1.2.0-rc.1`) publishes
  as a pre-release. Anyone can then check a downloaded binary against the
  attestation before running it:

  ```sh
  gh attestation verify ./demiplane_1.2.0_linux_amd64 --repo daisandapex/demiplane
  ```

  Release binaries are default builds; the optional `reply` and `tls` modules are
  compile-time and need a source build.

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
