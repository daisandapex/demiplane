# ADR 0001 — Module extension pattern

- **Status:** Accepted (amended 2026-06-21 — see Update)
- **Date:** 2026-06-19
- **Deciders:** demiplane maintainers
- **Tracking:** epic `demiplane-rox` (artifacts-publisher); first consumers `rox.1`, `rox.2`

> **Update (2026-06-21).** The seam shipped as planned, but the **render
> retrofit was reverted**: while this branch was in flight, a substantial
> in-core render-theming feature landed on `main` (shared `--theme` styling the
> chrome *and* `?render=md` pages, YAML frontmatter, `--css/--header/--footer/
> --meta-header`). Because that theme is a deliberately *shared instance
> setting* — `--theme dark` darkens everything, chrome and rendered content
> alike — render configuration belongs in core, not siloed in a module. So
> render stayed in core, and the speculative `PublishTransform` interface (which
> only render would have used) was **removed**: a seam shape with no live
> dispatch is dead code, not an extension point. The seam now offers one shape,
> `RouteModule`, **fully exercised by the inline-reply module** (ADR 0002).
> Everything below about the registry, build-tag inclusion, `Host`, and
> `RouteModule` stands; the `PublishTransform` / render-retrofit passages are
> retained as the original record but are superseded by this note.

## Context

demiplane's wedge is that its **core is tiny**: publish a file, serve it back,
auth, TTL, slugs. That small, auditable footprint is the product — the honest
inverse of public paste/host services. As we grow capabilities (a reply
endpoint, future analytics, alternate renderers, webhook fan-out, …) the
temptation is to keep bolting features into `internal/server`. Down that road
the core stops being tiny and stops being the wedge.

We need an **extension seam**: a way to add optional capabilities without
growing the thing every operator has to trust and read. The constraints:

1. **Core stays minimal.** The default binary is publish / serve / auth / TTL /
   slugs and nothing speculative. Optional capabilities are *opt-in*.
2. **Go-appropriate.** No runtime `plugin` package — it is CGO-fragile,
   version-brittle (exact compiler + dependency match required), and unsupported
   on several targets. We compile the modules we want *into* the binary.
3. **Explicit boundary.** A module reaches core only through a narrow,
   intentional capability surface — not by importing `internal/server` internals.
4. **Two shapes of extension.** Some capabilities add HTTP routes (the reply
   endpoint). Others hook an existing request path (markdown render on publish).
   The seam must serve both.

## Decision

Adopt a **compile-time module registry**, in the style of Caddy: an interface +
a `Register()` call from each module's `init()`, with the binary's module set
selected by which module packages are imported (gated with build tags). This is
the idiomatic Go answer to "pluggable, but statically linked and type-safe."

### The seam — `internal/module`

```
type Host interface {            // capability surface core hands to a module
    Store() *store.Store
    RequireAuth(http.HandlerFunc) http.HandlerFunc
    RequestBase(*http.Request) string
    BaseURL() string
    ModuleDataDir(name string) (string, error)   // isolated, per-module storage
}

type Module interface { Name() string }          // every module

type RouteModule interface {                      // adds HTTP routes
    Module
    Reserved() []string                           // top-level slugs to reserve
    Routes(mux *http.ServeMux, host Host)
}

type PublishTransform interface {                 // hooks the publish body path
    Module
    Match(q url.Values) bool
    Transform(q url.Values, filename string, body io.Reader) (data []byte, newFilename string, err error)
}
```

A module implements `Module` plus whichever capability interface(s) apply —
exactly Caddy's "one type, opt into the sub-interfaces you need" shape. The
registry is a package-global populated by `Register()`:

```
func Register(m Module)            // called from each module's init()
func All() []Module                // core iterates this at startup
```

### Inclusion — build tags select the module set

Each module package's `init()` calls `module.Register(...)`. A module is in the
binary iff its package is imported. We centralise those blank imports in
`cmd/demiplane`:

- `modules.go` (untagged) blank-imports the **default** modules (today: render).
  These ship in every build — they are existing, expected behaviour.
- `modules_reply.go` (`//go:build reply`) blank-imports the reply module. It is
  compiled in **only** with `go build -tags reply ./cmd/demiplane`. The default
  binary does not contain its code at all — core stays tiny by construction, not
  by a runtime flag.

This is the deliberate trade: opt-in capability lives behind a build tag (zero
cost, zero surface in the default binary), not a runtime `--enable-x` flag
(which keeps the code, and its dependencies, in everyone's binary). A module
that *should* ship by default but be removable uses a negative tag
(`//go:build !no_x`) instead.

### Wiring in core

- `server.New` collects `module.All()` once into typed slices
  (`[]RouteModule`, `[]PublishTransform`). `*Server` implements `module.Host`.
- `server.Handler` mounts core routes, then calls `Routes(mux, host)` for each
  route module and registers each module's `Reserved()` names via
  `store.AddReservedSlugs` so an artifact slug can never shadow a module route
  (same invariant core already holds for `publish`, `list`, `docs`, …).
- `handlePublish` consults `PublishTransform`s: the first whose `Match(q)` is
  true rewrites the body before it is stored. A `*module.TooLargeError` (or an
  `*http.MaxBytesError` from `--max-upload`) maps to `413`; anything else `500`.

No import cycle: `module → store`; `server → module, store`; modules `→ module,
store, render`; `cmd → server` + blank-imports of modules. `module` never
imports `server`.

## The render retrofit — first module

The existing opt-in `?render=md` markdown-on-publish path was hardcoded inside
`handlePublish`. It is now `internal/modules/render`, a `PublishTransform`:

- `Match` returns true for `?render=md|markdown`.
- `Transform` reads the (8 MiB-bounded) body, runs the same dependency-free
  engine (`internal/render`), and returns the rendered HTML plus an `.html`
  filename — byte-for-byte the prior behaviour, now arriving through the seam.

Note the split: the markdown **engine** stays a plain library in
`internal/render` (also used by `/docs`); only the **publish-time hook** became a
module. That is the model — modules are thin wiring over libraries, not dumping
grounds. The render module ships in the default build (removing it would be a
regression); it proves the transform seam end-to-end before `rox.2` leans on the
route seam.

## Consequences

**Positive**

- Core stays tiny and auditable; opt-in capabilities cost nothing in the default
  binary (not compiled in).
- Type-safe, statically linked, no CGO — survives Go upgrades and cross-compiles
  cleanly, unlike `plugin`.
- One narrow `Host` interface is the entire blast radius a module can touch;
  reviewing a module means reviewing its use of those five methods.
- Both extension shapes (route, publish-hook) are covered by one registry.

**Negative / trade-offs**

- Adding a module to a build is a recompile, not a runtime toggle. Acceptable:
  demiplane is a single static binary you deploy, not a plugin host.
- The registry is a package global mutated in `init()`. Standard Go (this is how
  `database/sql` drivers, `image` decoders, and Caddy modules all work), but it
  means module *registration* is import-order-driven; module *behaviour* must not
  be. Mounting and slug reservation happen once at startup, before serving.
- `store.AddReservedSlugs` and the `module.Host` accessors widen the store/server
  API slightly. Contained: they are the seam, documented as such.

## Alternatives considered

| Option | Why not |
|---|---|
| Go runtime `plugin` (`.so`) | CGO-fragile, exact-version-locked, poor platform support, no Windows. The brief explicitly rules it out. |
| Everything in `internal/server` | Defeats the wedge — core stops being tiny. |
| Runtime `--enable-<module>` flags only | Keeps every module's code + deps in every binary; the opposite of "tiny core." Build tags remove the code entirely. |
| External process / sidecar per capability | Operational weight (extra processes, IPC, deploy) wildly out of proportion to "add a reply endpoint." |

## See also

- `docs/MODULES.md` — developer guide: how to write and wire a module.
- ADR 0002 — inline-reply module (the first `RouteModule`).
