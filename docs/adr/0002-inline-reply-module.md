# ADR 0002 — Inline-reply module

- **Status:** Accepted
- **Date:** 2026-06-19
- **Deciders:** demiplane maintainers
- **Tracking:** `rox.2` (epic `demiplane-rox`)
- **Builds on:** [ADR 0001](./0001-module-extension-pattern.md) (module seam)

## Context

demiplane's thesis: an agent (or a person) publishes a self-contained page to
**your** mesh, and only your network can reach it. The natural next beat is a
**feedback loop** — a human reads the published page and responds *inline*
(Approve / Defer / free-text), and later an agent lists those responses and acts
on them. Publish → human replies in place → agent reads the replies.

This is the first **route module** under ADR 0001. It must:

- add a reply-submission endpoint and an agent-facing read API,
- decide a submission **auth model** (the open question the brief flags),
- own its **storage** without reaching into core's DB,
- ship as an **opt-in** capability (build tag) so core stays tiny,
- **not** change the existing API or security model of core.

## Decision

### Auth model — asymmetric, reusing core's existing posture

A reply is a **write** by a *viewer*; reading the collected replies is a
privileged act by the *publisher*. Those are different trust levels, so they get
different gates — but **no new auth primitive is introduced**; we reuse exactly
what core already has.

| Action | Endpoint | Gate | Why |
|---|---|---|---|
| Submit a reply | `POST /reply/{slug}` | **mesh-only** (open at transport, like `GET /{slug}`) | A replier is a viewer. demiplane's settled model is *view auth = network reachability; the mesh is the trust boundary.* Gating replies with a token/password would contradict that and add friction to the exact people you already trust to read the page. |
| Read / triage replies | `GET /replies`, `POST /replies/{id}/ack` | **bearer publish-auth** (`host.RequireAuth`) | Consuming feedback is a publisher action, same privilege as `GET /list` and `POST /publish`. It rides core's existing bearer token — when none is configured (open instance), it's open too, exactly like `/list`. |

We considered the brief's three options:

| Option | Verdict |
|---|---|
| **mesh-only** (chosen for submit) | Matches core's view-auth model exactly; zero friction; no new surface. |
| capability token per page | Rejected as the *default*: per-page shared secret to manage, and it contradicts "the mesh is the boundary." Left as a documented future knob (a publisher who shares a page beyond the mesh can put it behind core's existing password gate, which already gates the page itself). |
| per-artifact password | Rejected: the password is a *view* gate; overloading it as a *write* gate conflates two concerns and muddies the model. |

Because the submit endpoint is open on the mesh, the module carries its own
**anti-abuse** (a write endpoint that core's read endpoints don't need): a
bounded reply body (8 KiB) and a per-artifact reply cap (1000), both rejected
with `413`/`429`. The submit endpoint validates the target slug **exists**
(via a new `store.Has`) so replies can't accrue against phantom slugs. There is
no CSRF surface: submit carries no ambient credentials, and the read API
authenticates by header (bearer), never a cookie.

### Storage — module-owned, isolated

The module owns a private SQLite database at
`host.ModuleDataDir("reply")/replies.db` — never core's `meta.db`. It reuses the
project's existing pure-Go driver (`modernc.org/sqlite`), so no new dependency.

```sql
CREATE TABLE replies (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    slug       TEXT NOT NULL,                         -- artifact replied to
    kind       TEXT NOT NULL,                         -- approve | defer | comment
    body       TEXT NOT NULL DEFAULT '',              -- free-text note
    created_at INTEGER NOT NULL,                      -- unix seconds
    status     TEXT NOT NULL DEFAULT 'pending'        -- pending | read
);
CREATE INDEX idx_replies_slug   ON replies(slug);
CREATE INDEX idx_replies_status ON replies(status);
```

The reply stores the artifact's slug as a plain string (no FK into core). Module
isolation is the point: deleting the module's data dir removes all replies and
nothing else; core has no knowledge of replies.

### Read API — for the agent

- `GET /replies` *(auth)* — JSON list, newest first. Filters: `?slug=<slug>`,
  `?status=pending|read|all` (default `pending`). Each item:
  `{id, slug, kind, body, created_at, status}`.
- `POST /replies/{id}/ack` *(auth)* — mark one reply `read`, so an agent can
  triage a pending queue idempotently. `404` if the id is unknown.

Replies are returned **only as JSON** — the module renders no admin HTML — so
untrusted reply text never enters a server-rendered HTML sink (the agent escapes
on its own surface). The one HTML the module emits is the reply *form*, which
echoes no stored reply content.

### Reply controls — renderable into a page

- `GET /reply/{slug}` — a self-contained, JS-free styled page: an optional note
  textarea plus three submit buttons (**Approve / Defer / Comment**), posting
  `application/x-www-form-urlencoded` back to `POST /reply/{slug}`. On success it
  re-renders with a confirmation. A published artifact links to it
  (`<a href="/reply/<slug>">Leave a reply</a>`), so the control is one link away
  from any page. The module README documents inlining the form markup directly
  for a fully in-page control.
- `POST /reply/{slug}` — accepts the form post **or** a JSON body
  `{"kind":"...","body":"..."}` (for programmatic/CI replies), returning the
  stored reply as JSON (or the HTML confirmation for a form post, chosen by
  `Accept`).

### Routing & inclusion

- Routes: `GET /reply/{slug}`, `POST /reply/{slug}`, `GET /replies`,
  `POST /replies/{id}/ack`. `Reserved()` returns `["reply", "replies"]` so no
  artifact slug can shadow the module's two top-level segments. Mounted ahead of
  core's catch-all `GET /{slug}` by the seam.
- **Route shape — literal-first, not wildcard-first.** The submit/form routes are
  `/reply/{slug}`, **not** `/{slug}/reply`. Go's `ServeMux` (1.22+) treats a
  wildcard-first multi-segment pattern (`GET /{slug}/reply`) as *ambiguous* with
  any literal-first one (core's `GET /docs/{page}`) — for a path like
  `/docs/reply` neither is "more specific" — and **panics at registration**.
  Putting the literal segment first (`/reply/{slug}`) removes the ambiguity. A
  `//go:build reply` integration test in `internal/server` mounts the module over
  the real core routes to guard this regression.
- Inclusion is behind build tag **`reply`**: `cmd/demiplane/modules_reply.go`
  (`//go:build reply`) blank-imports it. Default `go build ./cmd/demiplane` has
  **no** reply code or routes; `go build -tags reply ./cmd/demiplane` adds them.

## Consequences

**Positive**

- Closes the publish→respond→read loop that pairs with the rox.1 capture hook,
  without touching core's API or security model.
- No new auth primitive: submit = view-auth (mesh), read = publish-auth (bearer).
  One asymmetry, both halves already understood by operators.
- Module-owned storage proves the `ModuleDataDir` isolation surface, and the
  route + reserved-slug mounting proves the `RouteModule` half of the seam — the
  reply module is the seam's first and only live module (render stayed in core;
  see the ADR 0001 update).
- Zero cost to anyone who doesn't build `-tags reply`.

**Negative / trade-offs**

- Open submit on the mesh means a malicious mesh participant can post replies up
  to the caps. Accepted: that participant can already read every page; the caps
  + existence check + JSON-only read bound the blast radius. A future capability
  submit-token is a clean additive knob if a deployment needs it.
- `store.Has` widens core's API by one read-only method. Contained and broadly
  useful (cheap existence check without opening the blob).
- Reply data lives outside core's TTL sweeper: replies persist until acked/
  deleted or the artifact's replies are pruned. A future enhancement can expire
  replies for long-gone slugs; out of scope here.

## Addendum (2026-07-14) — same-origin inline reply box for Q&A (`demiplane-qrc`)

Dogfooding demiplane as a live classroom surfaced two gaps in the flow above:

1. The reply endpoints mount only on the **control plane** (`:8891`). A rendered
   content page served from the **content plane** (`:8890`) that tries to post a
   reply is therefore **cross-origin** (same-site, different port) — and once the
   page is https-upgraded, a plain-http cross-origin post is silently blocked
   (mixed content). A hand-rolled per-page `<form>`+timer worked around this by
   claiming success on a 250 ms `setTimeout`, **unconditionally** — a student's
   answer was lost while the page said "✓ Recorded". False success was the bug.
2. **Approve / Defer / Comment** sign-off chrome does not fit answering a
   question. Teaching wants a single answer box.

**Decision.** Add a **same-origin** submit path and a first-class rendered box:

- **`ContentRouteModule`** — a new *optional* interface in `internal/module`
  (`ContentReserved()` + `ContentRoutes(mux, host)`). A `RouteModule` may also
  implement it to mount handlers on the **content origin**, where artifact bodies
  live. `ContentHandler` and the combined same-origin `Handler` mount these before
  the `GET /{slug}` catch-all. This is the minimal seam extension that lets a
  module answer a post on the same origin that served the page.
- **`POST /{slug}` on the content plane** (reply module) records a single
  free-text answer as a **`comment`**-kind reply. `ContentReserved()` returns
  `nil`: it is a *method-distinct twin* of `GET /{slug}` (shares the flat-slug
  space, owns no literal segment). Crucially this shape — one segment — does **not**
  collide with the control plane's literal-first `POST /reply/{slug}` (two
  segments) in the combined build: no single path matches both, so no `ServeMux`
  panic (contrast the wildcard-first `/{slug}/reply`, which *would* collide and is
  the reason we avoided it — consistent with the literal-first rule above).
- **The rendered box self-posts.** `render.Options.Reply` bakes a JS-free
  `<form method="post">` with **no `action`** at the foot of a `?render=md` page,
  so the browser posts to the page's **own URL** on the content origin — same-origin
  by construction, and correct once TLS lands (the scheme is inherited, never
  hard-coded). `POST /publish?render=md&reply=question` is the publish trigger;
  `?reply` is rejected without `?render=md` and with `?private` (a private
  capability page can't collect replies via `HasPublic`).
- **Honesty is structural, not best-effort.** With no JavaScript, the viewer's
  browser simply navigates to the server's real response. The content handler
  renders the "✓ Recorded" confirmation **only on the success branch, after the
  row is durably inserted**; every failure (storage down, empty/oversized answer,
  cap reached, unknown/private slug) renders an explicit "Not recorded" page with
  the matching status. A form post that lands on a real server response cannot
  claim success on failure.

**Unchanged.** Auth model (submit mesh-only, read bearer-gated), module-owned
storage, the `kind` set, and the existing control-plane routes all stand. The
box is additive and opt-in per publish; TLS remains `demiplane-4qq`.

## Addendum (2026-07-14) — reply-event hook + forward flow (`demiplane-tb7`)

The classroom loop wants two more beats: an external grader should learn about a
new answer **without polling**, and the student should flow onto the next lesson
**without navigating anywhere** ("the student does not want to go anywhere, they
just want to keep learning").

**Decision — reply-event hook.**

- On every durably recorded reply (both `POST /reply/{slug}` and
  `POST /answer/{slug}`), the module fires a configurable action: an exec'd
  command (`/bin/sh -c`, reply JSON on stdin + `DEMIPLANE_REPLY_*` env vars)
  and/or a webhook POST of the same JSON. Dispatch is a goroutine launched
  strictly **after** the insert: a hook can never fail, slow, or roll back the
  reply write, and the viewer's honest confirmation is independent of hook fate.
  Failures are logged; exec is bounded (5 min), webhook bounded (30 s).
- **Configuration rides the existing config file** through a small new cmd seam:
  a build-tagged wiring file (`cmd/demiplane/modules_reply.go`) registers
  module-owned keys (`reply_hook_exec`, `reply_hook_url`) plus an applier via
  `registerModuleConfig`. A key is recognized exactly when its module is
  compiled in — a config referencing a module the build lacks fails loudly at
  startup, preserving the file's fail-loud contract. Core's config parser learns
  nothing about replies; the module validates its own values
  (`reply.ConfigureHook`, hard startup error on a malformed URL).

**Decision — forward flow (`?next=`).**

- `POST /publish?render=md&slug=A&reply=question&next=B` bakes B into the reply
  form as a **hidden field**. Publish-side validation: `?next` requires
  `?reply`, must be a well-formed named slug, and must differ from `?slug=`. B
  need not exist yet — appearing later is the point, so no server-side state is
  kept (the pointer travels with the form; a hidden field is client-tamperable,
  so the submit handler re-validates it and anything malformed degrades to the
  plain confirmation — the worst a tamperer can do is choose which same-origin
  slug *their own* page waits for).
- After a recorded answer, the confirmation page adds an honest "your next
  lesson is being prepared" note and a JS-free `meta-refresh` into the new wait
  endpoint **`GET /answer/{slug}/next?to=B`** (content plane, literal-first
  under the already-reserved `answer` segment, needs no reply storage). While B
  does not resolve publicly it renders a self-refreshing holding page (5 s) that
  never claims readiness and always keeps the back link; the moment B exists it
  302-redirects to `/B`. Existence uses `HasPublic` — same no-capability-oracle
  rule as the submit path.

**Unchanged.** The honest-confirmation property is structural as before:
"Recorded" renders only after the durable insert, on the one success branch —
hook dispatch and forward chrome are appended around it, never in place of it.
No `?next=` → prior behavior exactly.

## See also

- `docs/MODULES.md` — how to build and wire a module.
- ADR 0001 — the module extension pattern this implements.
- ADR 0003 — the two-plane origin split the same-origin submit path respects.
