# ADR 0003 — Content origin isolation (closing the stored-XSS class)

- **Status:** Accepted
- **Date:** 2026-06-26
- **Deciders:** demiplane maintainers
- **Tracking:** `demiplane-een` (v1 release blocker); gates `demiplane-8xf` pentest. See `docs/RELEASE-v1.md`.
- **Builds on:** the same-origin footgun documented in `SECURITY.md` ("The same-origin hosted-JS footgun")

## Context

demiplane's wedge is hosting **arbitrary HTML/JS inline** from its own origin —
publish a self-contained page, get a URL, the page just works. That is the
product. It is also a stored-XSS engine the moment the control plane shares an
origin with the content.

The Shannon white-box pentest (`~/.shannon/workspaces/demiplane-verify/`)
demonstrated **Level 4 impact** against the live instance:

1. **XSS-VULN-01** — POST any `text/html` body, serve it back at `/{slug}`. By
   design, `text/html` is excluded from the SVG/XML no-script CSP gate
   (`store.IsScriptableNonHTML`, `store.go:656`), so the page's JS runs at our
   origin with no CSP.
2. **XSS-VULN-02** — `?filename=page.html` forces `ContentType=text/html` for
   **any** body (`inferContentType`, `store.go:617`: the extension hint wins over
   the magic-byte sniff unconditionally). An SVG-with-`<script>` uploaded as
   `image/svg+xml` — which *would* get `script-src 'none'` — is relabeled
   `text/html` and loses the CSP. Combined with named-slug overwrite (no per-slug
   ownership), this **retroactively poisons** a previously-safe artifact.

In both cases the hosted JS, running same-origin with the control plane, did:
enumerate every artifact via `GET /list` (slugs, capability URLs, the
`password:true` flag), `POST /publish` a worm artifact (201), and `DELETE`
another artifact (204) — all with the visitor's ambient authority, because the
default single-operator deployment runs with no bearer token.

**The root truth (from the issue):** *a page host on a shared origin cannot
prevent same-origin XSS via CSP-by-content-type.* You cannot CSP your way out of
this without breaking the feature — `text/html` script execution **is** the
product. The only structural fix is to stop serving user content from the origin
that hosts the control plane. This is the **githubusercontent.com model**:
github.com renders the app; `raw.githubusercontent.com` (a cookieless,
API-less, separate origin) serves user bytes.

`SECURITY.md` already names this as a deliberately-deferred design constraint
("isolate artifacts on a separate origin … deferred while demiplane is
single-operator and internal"). Going **public** for v1 retires that deferral —
hence `een` gates the release. This ADR decides *how*.

### Constraints

1. **Zero-config loopback must survive.** A single operator running
   `demiplane serve` on `127.0.0.1` to host their own files must still get a
   working setup with no DNS, no certificates, and no reverse proxy. A separate
   origin must not become a *hard dependency* for the simple case.
2. **Dependency-light, single static binary.** No new runtime deps, no mandatory
   sidecar. (ADR 0001's whole thesis.)
3. **Must not hard-depend on TLS (`4qq`).** `een` defines the origin model; the
   TLS work provisions certificates *for* those origins. The dependency arrow is
   `4qq → een`, never the reverse — the runbook orders them that way.
4. **Secure by default.** `een` exists *because* the default binary is the
   vulnerable surface the pentest gate audits. Shipping the fix off-by-default
   would make the gate theater. The safe topology must be the default.

## The threat, precisely (what isolation must buy)

Same-origin policy (SOP) keys on **origin = (scheme, host, port)**. Two things
must hold after the fix:

- **Hosted JS cannot *read* control-plane responses.** `GET /list`, artifact
  bodies, etc. If content is a distinct origin and the control plane sends no
  `Access-Control-Allow-Origin`, a credentialed cross-origin read is opaque —
  the worm cannot exfiltrate the artifact inventory or capability URLs.
- **Hosted JS cannot *drive* control-plane writes.** This is the subtle half:
  **SOP blocks reading responses, not sending requests.** A "simple" cross-origin
  `POST /publish` (Content-Type `text/plain`, no custom headers, no preflight)
  still executes server-side fire-and-forget — the attacker never reads the 201,
  but the worm artifact is created anyway. Origin separation alone does **not**
  close this. The control plane must additionally **reject cross-origin
  state-changing requests** (an `Origin` / `Sec-Fetch-Site` check — standard
  CSRF defense). `DELETE` is already non-simple (triggers a preflight that fails
  without CORS allow), but `POST` is not, so the header check is load-bearing.

So the fix is **two coordinated pieces**, both required:

- **(P1) Origin split** — serve `GET /{slug}` content from a content origin
  distinct from the control origin that hosts `/publish`, `/list`, `DELETE`.
- **(P2) Cross-origin write rejection** — the control plane rejects mutating
  requests whose `Sec-Fetch-Site` is `cross-site`/`same-site`, or whose `Origin`
  is present and is not the control origin. Requests with neither header (curl,
  the SSH ingest path, non-browser agents — the legitimate publish clients) are
  allowed; they are not the browser-XSS threat.

## Decision points

### D1 — Isolation mechanism (how the content origin differs)

| Option | Boundary | Zero-config / loopback | TLS + DNS burden | Isolation strength |
|---|---|---|---|---|
| **A. Separate port** (same host, e.g. control `:8080`, content `:8081`) | origin by **port** | ✅ Works on bare `127.0.0.1` with **no DNS** — `127.0.0.1:8080` and `:8081` are distinct origins | One cert covers the host; both ports share it | SOP-isolates the API. Cookies are **not** port-scoped — moot today (demiplane is stateless/cookieless), but a ceiling if sessions ever land |
| **B. Separate hostname** (subdomain, e.g. `app.host` + `content.host`, routed by Host header) | origin by **host** | ❌ Needs two resolvable names; loopback has no subdomain | SAN cert for both names (or wildcard); realistically a fronting proxy | Full origin **and** cookie isolation |
| **C. Per-slug subdomain** (`<slug>.content.host`) | origin **per artifact** | ❌ Wildcard DNS required | Wildcard cert | Strongest — isolates artifacts from **each other**, not just from the API |
| **D. Reverse-proxy only** (docs, no code: tell operators to front with Caddy) | operator's responsibility | ❌ No safe default; punts the whole fix | operator's problem | As good as the operator's config — i.e. unreliable; fails constraint #4 |

Notes:
- **A is the only option that satisfies constraint #1** (works on bare loopback
  with zero DNS/cert/proxy). Ports are a full SOP origin boundary in every modern
  browser; the only thing port-separation does *not* isolate is cookies, which
  demiplane does not use. That gap is documented, not load-bearing today.
- **A and B are not exclusive** — the binary can default to port-separation and
  *accept* a hostname content origin via config for deployments that have DNS +
  TLS. One code path (advertise+route a configured content origin), two
  topologies.
- **C** is the full githubusercontent model and the strongest, but wildcard DNS +
  wildcard cert is a heavy ask for a self-hoster and overkill for v1's
  single-operator reality. **Defer to a future ADR**; the v1 design must not
  preclude it.
- **D** alone fails "secure by default." It remains the *recommended production
  topology* (front with TLS-terminating proxy) layered **on top of** A/B.

### D2 — Default on, or opt-in?

| Option | Pro | Con |
|---|---|---|
| **On by default** (two listeners out of the box) | Secure default; the pentest gate audits the real shipped topology | Default UX changes: artifact links now live on the content port, not the bind port |
| **Opt-in** (`--content-bind` to enable) | No change for existing single-operator users | Default binary stays vulnerable → `8xf` gate is theater; violates constraint #4 |

→ **On by default.** Provide an explicit, loudly-warned escape hatch
(`--unsafe-same-origin`) for the trusted single-operator who *accepts* the
footgun and wants the old single-port behavior. The safe path is the default; the
footgun is a deliberate, logged opt-*out*.

### D3 — Single content origin, or per-artifact?

→ **Single content origin for v1** (all artifacts share one content origin).
This closes 100% of the *demonstrated* impact, which is **control-plane abuse**
(exfiltrate `/list`, worm `/publish`, `DELETE`). Per-artifact origins (D1-C)
additionally stop artifact A's JS from reading artifact B within the content
origin — a real but lesser concern for a single-operator host, and a heavy DNS
ask. Documented as future hardening, not a v1 gate.

### D4 — Core, or module?

→ **Core.** Per ADR 0001, modules are **opt-in** capabilities behind build tags;
a security default that is present in only some builds is theater (constraint
#4). Origin isolation also requires changing the **listener topology** and the
`GET /{slug}` routing — that is core's job (`Handler()`, `main.go`'s
`ListenAndServe`), and the `module.Host` surface deliberately does not expose
listener control. So `een` lands in core, not as a module. (Contrast `4qq` TLS,
scoped as a module: TLS is a transport add-on; origin topology is a serving
invariant.)

### D5 — The cheap interim `?filename=` restriction

The issue calls out a stopgap: stop letting `?filename=` relabel a sniffed
**non-HTML** body as `text/html`. This closes **XSS-VULN-02's specific vector**
(SVG→html coercion) but **not the class** — XSS-VULN-01 (an honestly-typed
`text/html` body) still runs same-origin. So it is **not** a substitute for
origin isolation.

→ **Ship it anyway, as defense-in-depth + a correctness fix.** Relabeling an SVG
as HTML is simply wrong behavior, and the restriction is a few lines in
`inferContentType`: when a `?filename=` extension maps to `text/html` (or
`application/xhtml+xml`) but the sniffed type is something else
(`image/svg+xml`, `text/xml`, …), do **not** honor the override — keep the
sniffed type (which then correctly gets the `script-src 'none'` CSP). It is cheap
belt-and-suspenders that also tightens the cross-artifact surface that the
single content origin (D3) leaves open. Independent of `een`'s big lever; can land
in the same commit.

## Decision (recommended)

Adopt **port-based single content origin, on by default, hostname-capable by
config**, plus the cross-origin write guard and the interim `?filename=`
tightening. Concretely:

1. **Two listeners (P1).** The default binary serves:
   - **Control listener** (`--bind`, default `127.0.0.1:8080`): `/publish`,
     `/list`, `DELETE /{slug}`, landing, `/docs`, modules — everything except
     artifact bodies.
   - **Content listener** (`--content-bind`, default = host of `--bind` with
     port+1, i.e. `127.0.0.1:8081`): **only** `GET /{slug}` artifact bodies (with
     password/private gates intact).
   `/publish` advertises artifact URLs on the **content origin**; `/list` URLs
   likewise. `printStartup` shows both origins.
2. **Hostname/proxy mode.** `--content-base-url https://content.host` declares a
   content origin for TLS/DNS deployments; the content listener then routes by
   Host (or sits behind a proxy that does). This is where `4qq` plugs in —
   `een` declares the origins, `4qq` provisions certs covering them. No hard
   dependency: omit it and the loopback two-port HTTP path works unchanged.
3. **Cross-origin write guard (P2).** Mutating routes (`POST /publish`,
   `DELETE /{slug}`, module writes via `RequireAuth`) reject requests whose
   `Sec-Fetch-Site ∈ {cross-site, same-site}` or whose `Origin` header is present
   and ≠ the control origin. Header-absent (non-browser) clients pass.
4. **Escape hatch.** `--unsafe-same-origin` collapses to the legacy single-port
   topology, logging a prominent warning. Off by default.
5. **Interim hardening (D5).** `inferContentType` refuses to upgrade a
   non-HTML-sniffed body to `text/html`/XHTML via the filename hint.

### Why this shape

- **Zero-config survives** (constraint #1): two ports on loopback, no DNS, no
  cert, no proxy — the single operator runs one binary and gets working,
  *isolated* artifact URLs.
- **Secure by default** (constraint #4): the vulnerable single-origin topology is
  now an explicit opt-out, so the `8xf` pentest audits the real shipped default.
- **Clean `4qq` seam** (constraint #3): the origin model is declared here;
  certificates are 4qq's job, layered on without `een` depending on TLS.
- **Dependency-light** (constraint #2): a second `http.Server` goroutine and a
  header check. No new modules, no new third-party deps.

## Consequences

**Positive**
- Closes the stored-XSS *class*, not just the two reported vectors: hosted JS can
  neither read control responses (SOP + no CORS) nor drive control writes
  (Origin guard).
- The default binary is safe to expose on a trusted mesh without a fronting proxy.
- Preserves the wedge — `text/html` artifacts still execute their JS; they just
  can no longer reach the control plane or other-artifact inventory.

**Negative / trade-offs**
- **Two ports by default** is a visible UX change. Artifact URLs move to the
  content port; anyone scripting against `:8080/{slug}` must adjust. Mitigated by
  `printStartup` output and `--unsafe-same-origin`.
- **Port-separation does not isolate cookies.** Harmless today (stateless), but a
  ceiling: if demiplane ever adds sessions, it must move to hostname separation
  (D1-B) for those to be safe. Documented as a known boundary.
- **Single content origin (D3)** still lets one artifact's JS read another's
  within the content origin. Accepted for v1 (single-operator); per-slug
  subdomains (D1-C) are the future fix.
- A reverse-proxy/TLS production deployment must now route **two** upstreams
  (control + content). Documented in `SECURITY.md` / README as the recommended
  topology.

## Alternatives considered

| Option | Why not (for v1) |
|---|---|
| Strict CSP on `text/html` (`script-src 'none'`) | Breaks the product — executable HTML hosting is the wedge. |
| Sanitize/strip script from uploaded HTML | Destroys arbitrary-HTML hosting; brittle; an arms race. |
| Per-slug subdomains now (D1-C) | Strongest, but wildcard DNS + cert is too heavy for v1 single-operator. Future ADR. |
| Hostname-only separation (D1-B) | Better isolation but breaks zero-config loopback (no subdomain on `127.0.0.1`). Offered as an *option*, not the default. |
| Reverse-proxy-only guidance (D1-D) | No safe default; fails the "secure by default" gate. Kept as layered prod guidance. |
| Interim `?filename=` fix alone | Closes one vector (VULN-02), not the class (VULN-01 survives). Ship it *with* isolation, not instead. |
| Defer origin isolation past v1 | The whole reason `een` gates the public flip — going public retires the single-operator deferral. |

## See also
- `SECURITY.md` — "The same-origin hosted-JS footgun" (the deferral this retires).
- `docs/RELEASE-v1.md` — gate sequence; `4qq` (TLS) consumes this origin model.
- ADR 0001 — module seam (why this is core, not a module).
- Evidence: `~/.shannon/workspaces/demiplane-verify/deliverables/xss_exploitation_evidence.md`.
