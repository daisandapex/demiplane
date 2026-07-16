# ADR 0004 — Native TLS module

- **Status:** Accepted
- **Date:** 2026-07-14
- **Deciders:** demiplane maintainers
- **Tracking:** `demiplane-4qq`
- **Builds on:** [ADR 0001](./0001-module-extension-pattern.md) (module seam),
  [ADR 0003](./0003-content-origin-isolation.md) (two-plane topology)

## Context

demiplane has always served plain HTTP and pushed transport encryption to the
deployment: a WireGuard mesh (the reference deployment) or a reverse proxy.
That posture is honest but incomplete:

- **Defense in depth.** The loopback and LAN paths are plaintext; a future
  `0.0.0.0` exposure would be too. The bearer token and reply bodies deserve
  encryption even inside a "trusted" network.
- **Browser secure contexts.** Clipboard APIs, service workers, and `Secure`
  cookies require `https://` even on private addresses. Modules will want
  these.
- **Product credibility.** Self-hosted users *without* a mesh should not need
  to stand up Caddy just to get encrypted transport for an internal tool.

The SSH ingest path needs nothing — `sshd` already encrypts it.

## Decision

### A module, not a core feature

TLS termination ships as an **opt-in module** (build tag `tls`), following the
ADR 0001 pattern: the default binary contains none of it, and even a
tls-tagged binary serves plain HTTP until the operator sets `tls = on` in the
config file. Off is the default twice over.

The module differs from the reply module in *what* it extends: it adds no HTTP
routes, so `module.RouteModule` is the wrong seam. Instead it plugs into the
listener layer through a **cmd-side nil-func hook** — the same idiom core uses
internally for `liveView` and `publishSite`:

```go
// cmd/demiplane/main.go (untagged — core)
var moduleTLS func(host module.Host, bindHosts []string) (*tls.Config, error)
```

The build-tagged wiring file (`cmd/demiplane/modules_tls.go`) installs the
hook and registers the module's config keys via the existing
`registerModuleConfig` seam. Core's entire knowledge of TLS is: *if the hook
returns a non-nil `*tls.Config`, serve both listeners with
`ListenAndServeTLS`; otherwise plain HTTP.* Certificate logic never touches
core. `module.Host.ModuleDataDir("tls")` gives the module its private storage
(`<store>/modules/tls/`, 0700), the same isolation the reply module's SQLite
db gets.

Both planes (control + content) share one `*tls.Config` — they are the same
host on different ports, so one certificate covers both (ADR 0003 unchanged).
URL minting needs no new plumbing: `requestBase` already derives `https` from
`r.TLS`.

### Three certificate sources, resolved from config

| Source | Config | Behaviour |
|---|---|---|
| **Self-signed** (default) | `tls = on` | Generate once (ECDSA P-256, 2-year validity, SANs derived from the bind addresses or `tls_hosts`), persist under `<store>/modules/tls/`, reuse across restarts; auto-regenerate near expiry or when the host set changes. Fingerprint logged for client pinning. |
| **ACME** (Let's Encrypt) | `tls_acme_domains` (+ optional `tls_acme_email`, `tls_acme_ca`) | `golang.org/x/crypto/acme/autocert`: automatic issuance + renewal via the TLS-ALPN-01 challenge, cache in `<store>/modules/tls/acme/`. Requires the domain to resolve to the host and the control listener reachable on :443. Setting domains constitutes ToS acceptance (Caddy's posture). |
| **BYO** | `tls_cert` + `tls_key` | Serve an operator-managed PEM pair (issued by the fleet CA, a DNS-01 flow outside demiplane, etc.). |

Source resolution is inferential — manual keys ⇒ manual, ACME domains ⇒ ACME,
neither ⇒ self-signed — and **mixing sources is a hard startup error**, as is
any half-configured source (cert without key, `tls_acme_email` without
domains). Fail-loud, per the config file's contract.

ACME's DNS-01 challenge (which would fit a Cloudflare-managed fleet) is
deliberately **not** included: it drags in a DNS-provider dependency tree
(lego et al.) for a flow operators can already run outside demiplane and feed
in via BYO. TLS-ALPN-01 covers the "public host, port 443" case with zero
extra dependencies beyond `golang.org/x/crypto`.

### Dependency

`golang.org/x/crypto` v0.53.0 (autocert + acme), pulling
`golang.org/x/net` v0.55.0 / `golang.org/x/text` v0.38.0 / `golang.org/x/sys`
v0.46.0 — first-party Go project modules, all published ≥ 14 days before
adoption per the supply-chain quarantine. Self-signed and BYO paths use only
the standard library.

## Consequences

- The default build and any un-configured build are byte-identical in
  behaviour to pre-module demiplane — plain HTTP, no new config keys
  recognized (an unknown `tls` key still fails loudly in a default build,
  exactly like a typo).
- A second module *shape* now exists (listener hook instead of route
  registration). If a third non-route module appears, consider promoting the
  nil-func idiom into `internal/module` as a first-class capability interface;
  two instances is not yet a pattern worth abstracting.
- The `?live` SSE preview and the reply box inherit TLS for free (same-origin
  relative URLs — no mixed-content trap).
- Migrating a deployed plain-HTTP instance is a coordinated change: published
  absolute `http://` URLs, hook scripts, and cron consumers must move
  together. The module ships dark on the reference instance until that
  migration is scheduled.
