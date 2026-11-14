# demiplane security audit: findings

Audited commit: `d137eea` (`seat/demiplane-fuzz-zone/fix`). Scope: this
repository and an instance built from it and run on loopback. Twelve findings
are validated by a failing test each.

**Status: all twelve fixed** on `seat/demiplane-mythos-fixes/fix`, one commit
each (listed in the table). Each test has moved out of the `audit` tag into
the package's ordinary test files, so it runs in the usual gates; each report
says where.

Already known and not reported again: the password-verifier salt and key
lengths and the bracketed `Host` forms (both fixed on this branch), and
ops-o4db9 (a damaged closing multipart boundary answers `500`).

## Findings, by severity

| # | Severity | Finding | Needs | Test (as audited) | Fixed in |
|---|---|---|---|---|---|
| [01](01-capture-hook-imports-from-cwd.md) | High | The capture hook runs `re.py` from the project directory | hook in restricted mode; a file written into the project | `e2e/audit_capture_hook_test.go` | `4ce478d` |
| [02](02-password-cache-bypasses-lockout.md) | Medium | A locked-out client still gets an answer for the correct password | nothing; a page someone is reading | `internal/server/audit_password_cache_test.go` | `badd483` |
| [03](03-loopback-host-rebinding.md) | Medium | A loopback instance answers any `Host`, so a web page can reach it by DNS rebinding | operator's browser on a hostile page; worst with no token | `internal/server/audit_loopback_host_test.go` | `a580ee4` |
| [04](04-site-path-past-path-max-bricks-store.md) | Medium | A site with near-limit paths leaves a tree the store cannot remove, and the store stops opening | publish token (or none configured) | `internal/store/audit_site_pathmax_test.go` | `6e99382` |
| [05](05-empty-password-header.md) | Low | An empty `X-Demiplane-Password` publishes with no password | publisher's mistake | `internal/server/audit_empty_password_test.go` | `57b621b` |
| [06](06-replies-to-password-pages-open-without-token.md) | Low | With no token, replies to password-protected pages are readable by anyone | no token configured; `-tags reply` | `internal/server/audit_replies_open_test.go` | `ef1dfc7` |
| [07](07-config-path-relative-to-cwd.md) | Low | With no `HOME`, the config file is read from the working directory | bare environment; writable working directory | `cmd/demiplane/audit_startup_test.go` | `00ecfbc` |
| [08](08-blank-token-env-starts-open.md) | Low | A blank `DEMIPLANE_TOKEN` starts the server open | operator's mistake | `cmd/demiplane/audit_startup_test.go` | `8958edb` |
| [09](09-mcp-publish-path-probes-outside-root.md) | Low | MCP `publish` tells a model whether a path outside the publish root exists | a model following injected instructions | `internal/mcp/audit_path_probe_test.go` | `7a93d7a` |
| [10](10-emphasis-rewrites-link-destination.md) | Low | Emphasis markers inside a link destination rewrite the `href` after it was checked | markdown source | `internal/render/audit_inline_test.go` | `1f6e469` |
| [11](11-heading-id-shadows-reply-box.md) | Low | A heading can take the reply box's element id | markdown source | `internal/render/audit_inline_test.go` | `9f93157` |
| [12](12-site-publish-error-echoes-store-path.md) | Low | A failed site publish answers with the store's absolute path | publish token | `internal/server/audit_site_error_path_test.go` | `ae44076` |

## Running the tests

The tests ran behind the build tag `audit` while the findings were open; they
now sit in the ordinary test files, so the usual gates run them:

```sh
go test ./internal/server/ ./internal/store/ ./internal/render/ ./internal/mcp/ ./cmd/demiplane/
go test -tags reply -run TestRepliesToPasswordPage ./internal/server/
go test -tags e2e -run TestCaptureHookDoesNotImport ./e2e/
```

Two tests are Linux-only (`PATH_MAX`), in `_linux_test.go` files.

## Suspicions: not validated by a test

These were traced in the code but cannot be shown by a Go test or one
request, mostly because the failure happens in a browser or needs a specific
deployment.

- **First-party scripts break on the name `constructor`.** The gallery script
  groups cards in plain objects keyed by series (`internal/server/gallery.go:315-320`),
  and the rendered page's outline script looks headings up in one
  (`internal/render/flayout.go:315-316`). A series named `constructor`
  (`ValidateSeries` allows it), or a heading `## constructor`, finds
  `Object.prototype.constructor` there. Run on their own in Node, the two
  statements throw `TypeError: buckets[k].push is not a function` and
  `links[h.id].setAttribute is not a function`. For the gallery this happens
  after the grid has been emptied, so one such artifact among the newest 200
  would leave the gallery blank for everyone with scripts on. Not run in a
  browser. Fix: `Object.create(null)` or `Map`.
- **`--trusted-proxy` ranges that contain clients.** `Proxies.ClientAddr`
  (`internal/limit/limit.go`, the `X-Forwarded-For` walk from line 396) walks `X-Forwarded-For` from the right
  past every trusted address. If the trusted range also covers the clients
  (`10.0.0.0/8` with proxy and users in it), the client's own address is
  skipped as a hop and the entry it wrote itself becomes its key: a fresh key
  per request avoids every per-client limit, and naming someone else's
  address spends their password budget. This is the documented algorithm
  applied to a configuration the documentation does not warn against;
  `ParseProxies` accepts `0.0.0.0/0`.
- **Both base URLs on one origin.** `serve` compares the bind addresses
  (`cmd/demiplane/main.go:358-367`) but not `--base-url` with
  `--content-base-url`. Behind a path-routing proxy both can be the same
  browser origin without `--unsafe-same-origin` and without a warning.
- **A rebake stores the caller's `Host`.** Without base URLs,
  `POST /rerender` bakes the request's `Host` into the gallery and series
  links of every rebaked page (`internal/server/rerender.go:524-525`). It
  needs the token and is limited to host characters by `sanitizeHost`.
- **Reply pages of a password-protected artifact** (the form, its `401`, the
  confirmation that repeats the answer) do not carry the `no-store` and
  `no-referrer` headers the artifact itself gets; the module has no access to
  `setProtectedHeaders`.

## Observations on the MCP server

Asked for specifically; none of these is a defect with a test, they describe
what a model receives.

- `get` returns the page text as the tool result with nothing marking where
  it starts and ends, and the truncation notice is appended in the same text
  (`internal/mcp/tools.go:416-420`), so a page can end with that notice
  itself. A fetched page is the obvious place for injected instructions.
- `list` returns the server's JSON as is. Slugs and series are restricted to
  `[A-Za-z0-9._-]`; the one free-text field a publisher controls is
  `filename` (up to 255 bytes, JSON-encoded, so it cannot break the
  structure, but it is text the model reads).
- `delete_many` in its default dry run returns the owner inventory, which
  includes the capability slugs of private artifacts
  (`internal/mcp/manage.go:44-54`), although `list` is described as
  non-private. The real run needs `confirm_host`, which is the host of the
  manifest's own `instance` field, so a model can supply both from one dry
  run; `SECURITY.md` already calls the confirmation a client safeguard. A
  model that has read a hostile page therefore has what it needs to fetch
  private pages or delete everything unpinned. Returning opaque item ids
  instead of slugs in the manifest, and binding a real run to a nonce the
  server issued for the dry run, would change that.
- Tool arguments are decoded leniently (`json.Unmarshal`: unknown keys
  ignored, key case folded, last duplicate wins), so a misspelt `private`
  publishes public without an error.
- The publish root defaults to the working directory; a token kept in an
  `env` block of a config file under that root is not covered by the
  token-file identity check.

## Checked and found sound

Token comparison (constant-time, length-hiding, header only); capability
slug generation (144 bits from `crypto/rand`) and its absence from `/list`,
the gallery, the landing page, series navigation, logs, redirects and the
rerender report; `no-store`, `no-referrer` and CORP on private and
protected responses, their `401`s and the `?live` wrapper; the control and
content listeners' route separation; the cross-origin write guard for every
`Origin` and `Sec-Fetch-Site` combination tried; content-type inference (no
inline type renders as a scriptable document without the sandbox or
`script-src 'none'` policy, for artifacts and site files); path
canonicalisation; site archive extraction (traversal, absolute paths,
backslashes, symlinks, hard links, file-and-directory conflicts, entry and
size caps, zip and tar header bounds), tried with 27 hand-made archives; the
renderer's escaping and link allowlist; the reply module's targeting, token,
bounds and hooks; revision-bound deletion; write intents and deletion
queues across a crash. A single 8 MiB `?render=md` request of each of 33
pathological inputs took at most 2.8 s and the server's peak resident memory
stayed under 240 MiB over all of them.
