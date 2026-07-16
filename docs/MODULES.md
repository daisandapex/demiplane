# Modules — developer guide

demiplane keeps its **core tiny** (publish / serve / auth / TTL / slugs).
Everything else is an **optional module**, compiled in by build tag. This guide
shows how to write one. For *why* it works this way, see
[ADR 0001](./adr/0001-module-extension-pattern.md).

## The shape

A module is a Go package under `internal/modules/<name>/` that:

1. registers itself from `init()` with `module.Register(...)`, and
2. implements `module.Module` (just `Name()`) plus the `RouteModule` capability
   interface.

It is included in a build **iff** something imports it. We do that with a
build-tag-gated blank import in `cmd/demiplane` — so the default binary contains
no optional modules at all.

```
internal/
  module/            # the seam: Host, Module, RouteModule, registry
  modules/
    reply/           # opt-in module — inline replies (RouteModule), //go:build reply
    tls/             # opt-in module — native TLS termination (listener seam), //go:build tls
cmd/demiplane/
  modules_reply.go   # blank-imports reply  (//go:build reply)
  modules_tls.go     # wires tls: config keys + the moduleTLS listener hook  (//go:build tls)
```

## The Host surface

Core hands every module a `module.Host` — the entire surface a module may touch:

| Method | Use |
|---|---|
| `Store() *store.Store` | the shared artifact store |
| `RequireAuth(h) h` | wrap a route in core's bearer-token publish auth |
| `RequestBase(r) string` | advertised base URL for a request |
| `BaseURL() string` | the configured `--base-url` (`""` if unset) |
| `ModuleDataDir(name) (string, error)` | an isolated `<store>/modules/<name>` dir for your own persistence |

A module never imports `internal/server`; the interface is the whole contract.

## The RouteModule interface

The seam currently offers one capability shape: a module that adds HTTP routes.
(An earlier `PublishTransform` shape, for modules that rewrite the publish body,
was removed — its only candidate, markdown render, lives in core because its
theming is a shared instance setting; see the ADR 0001 update. Reintroduce a
body-transform shape if and when a real one ships, not before.)

### RouteModule — add HTTP routes

For capabilities that add endpoints.

```go
func (Module) Reserved() []string { return []string{"reply", "replies"} }   // slugs core must protect
func (m *Module) Routes(mux *http.ServeMux, host module.Host) {
    mux.HandleFunc("POST /reply/{slug}", m.handleSubmit)              // submit — open (mesh-only)
    mux.HandleFunc("GET /replies", host.RequireAuth(m.handleList))    // read — bearer auth
}
```

`Reserved()` names are registered as reserved slugs at startup so an artifact can
never shadow your routes (the same invariant core holds for `publish`, `list`,
`docs`, …). Mount module routes **before** the catch-all `GET /{slug}` — core
does this for you by mounting modules in `Handler()` ahead of that route.

> **Route shape — put the literal segment first.** Use `/reply/{slug}`, not
> `/{slug}/reply`. Go's `ServeMux` treats a wildcard-first multi-segment pattern
> (`GET /{slug}/reply`) as *ambiguous* with any literal-first route core has
> (`GET /docs/{page}`) and **panics at registration**. Literal-first
> (`/reply/{slug}`) is unambiguous. Add a `//go:build <tag>` integration test in
> `internal/server` that mounts your module over the real core routes to catch
> this.

Worked example: `internal/modules/reply` (build tag `reply`), the inline-reply
endpoint.

### ContentRouteModule — also add routes on the content origin (optional)

`RouteModule.Routes` mounts on the **control plane** (where `/publish`, `/list`,
and the reply read API live). demiplane serves artifact **bodies** from a separate
**content origin** (ADR 0003). A module that needs a route on that origin — e.g. a
handler a *rendered page* can post to **same-origin**, avoiding the cross-origin /
mixed-content failure of posting back to the control plane — also implements the
optional `ContentRouteModule`:

```go
func (Module) ContentReserved() []string { return nil }  // POST /{slug} owns no literal segment
func (m *Module) ContentRoutes(mux *http.ServeMux, host module.Host) {
    mux.HandleFunc("POST /{slug}", m.handleContentSubmit)  // method-distinct twin of GET /{slug}
}
```

`ContentHandler()` and the combined same-origin `Handler()` mount these before the
`GET /{slug}` catch-all. `Routes` and `ContentRoutes` run on the **same module
instance**; if both need shared state (e.g. one storage handle), open it once,
idempotently (see the reply module's `ensureStore`). The literal-first rule still
holds, and note `POST /{slug}` (one segment) does **not** collide with a
control-plane `POST /reply/{slug}` (two segments) in the combined build.

Worked example: `internal/modules/reply` `POST /{slug}` — the same-origin answer
submit for the `?reply=question` inline reply box.

### Beyond routes — the cmd-side listener seam (the TLS module)

Not every capability is a set of routes. The native-TLS module
(`internal/modules/tls`, [ADR 0004](./adr/0004-native-tls-module.md)) extends
the **listener layer**: it hands `cmd/demiplane` a `*tls.Config` and core calls
`ListenAndServeTLS` instead of `ListenAndServe`. The seam is a nil-func hook in
`main.go` — the same idiom core uses internally for `liveView`/`publishSite` —
installed by the module's build-tagged wiring file:

```go
// main.go (untagged): nil = plain HTTP always.
var moduleTLS func(host module.Host, bindHosts []string) (*tls.Config, error)

// modules_tls.go (//go:build tls): installs it.
moduleTLS = func(host module.Host, bindHosts []string) (*tls.Config, error) { … }
```

The hook still receives the narrow `module.Host` (here for `ModuleDataDir`, the
module's certificate storage), so the blast-radius contract is unchanged. If a
third non-route module shape appears, promote the idiom into `internal/module`
as a first-class capability interface — two instances is not yet a pattern.

## Wiring a new module into builds

1. Write the package; `init()` calls `module.Register(YourModule{})`.
2. Add a build-tagged blank-import file under `cmd/demiplane` so it compiles in
   only under that tag (keeping the default binary tiny):

   ```go
   //go:build reply
   package main
   import _ "github.com/daisandapex/demiplane/internal/modules/reply"
   ```

   (A module you genuinely want in *every* build would instead be blank-imported
   from an untagged file; today there are none — all modules are opt-in.)
3. Build it: default `go build ./cmd/demiplane`; with opt-ins
   `go build -tags reply ./cmd/demiplane`.

## Module config keys

A module that needs operator configuration rides the existing config file
(`${XDG_CONFIG_HOME:-~/.config}/demiplane/config`) through the cmd-side seam:
its build-tagged wiring file registers the keys it owns plus an applier that
validates and consumes them —

```go
//go:build reply
func init() {
    registerModuleConfig([]string{"reply_hook_exec", "reply_hook_url"},
        func(cfg map[string]string) error {
            return reply.ConfigureHook(cfg["reply_hook_exec"], cfg["reply_hook_url"])
        })
}
```

A key is recognized exactly when its module is compiled in, so a config file
referencing a module the build lacks fails **loudly** at startup — the same
fail-loud contract as a typo'd core key. Prefix keys with the module name
(`<module>_<key>`), validate values in the applier (an error there is a hard
startup error), and keep core's parser ignorant of your module's semantics.

Worked example: the reply module's reply-event hook (`reply_hook_exec`,
`reply_hook_url`; see `internal/modules/reply/README.md`).

## Testing a module

Tests that exercise a module through the server must compile it in — add a
blank import to the test file (mirrors the `cmd/demiplane` import). The reply
module's `//go:build reply` integration test in `internal/server` does exactly
this:

```go
import _ "github.com/daisandapex/demiplane/internal/modules/reply"
```

Otherwise the registry is empty and the server runs core-only — which is exactly
correct for tests that assert core behaviour without the module.
