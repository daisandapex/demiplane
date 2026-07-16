# reply — inline-reply module

Viewers respond to a published page (**Approve / Defer / free-text**) and an
agent later lists and acks those replies. Closes the loop with the
[artifact-capture hook](../../../companion/capture-hook/): an agent publishes a
page to the mesh, a human replies in place, the agent reads the replies.

This is the first **route module** on the [module seam](../../../docs/MODULES.md);
design and rationale in [ADR 0002](../../../docs/adr/0002-inline-reply-module.md).
It is **opt-in** — compiled in only with the `reply` build tag.

## Build & run

```sh
go build -tags reply -o demiplane ./cmd/demiplane
demiplane serve --bind 0.0.0.0:8080 --store /var/lib/demiplane --token-file /etc/demiplane.token
```

The default build (`go build ./cmd/demiplane`) contains **none** of this — no
reply routes, no `reply`/`replies` reserved slugs.

## Auth model (asymmetric, reuses core)

| Action | Endpoint | Gate |
|---|---|---|
| Submit a reply | `POST /reply/{slug}` | **mesh-only** — open at the transport layer, exactly like `GET /{slug}`. A replier is a viewer. |
| Show the reply form | `GET /reply/{slug}` | mesh-only |
| List / triage replies | `GET /replies` | **bearer publish-auth** (same token as `POST /publish`, `GET /list`) |
| Mark a reply read | `POST /replies/{id}/ack` | bearer publish-auth |

No new auth primitive: submitting is a *view*-level act (network reachability),
reading the collected replies is a *publish*-level act (bearer token). On an
open instance (no token) the read side is open too — same as core's `/list`.

## Endpoints

### `GET /reply/{slug}` — the reply form
A self-contained, JS-free HTML page: an optional note plus **Approve / Defer /
Comment** buttons. `404` if the artifact slug doesn't exist.

### `POST /reply/{slug}` — submit
Accepts a browser form post **or** a JSON body:

```sh
curl -X POST -H 'Content-Type: application/json' \
     --data '{"kind":"approve","body":"ship it"}' \
     http://demiplane.mesh:8080/reply/proposal
```

- `kind` ∈ `approve | defer | comment` (a `comment` requires non-empty `body`).
- `201` + the stored reply as JSON (or the HTML confirmation for a form post).
- `404` unknown slug · `400` bad kind / empty comment · `413` body over 8 KiB ·
  `429` artifact past its 1000-reply cap.

### `GET /replies` — list *(auth)*
JSON, newest first. Filters: `?slug=<slug>`, `?status=pending|read|all`
(default `pending`).

```sh
curl -H "Authorization: Bearer $TOKEN" \
     "http://demiplane.mesh:8080/replies?slug=proposal&status=pending"
# {"replies":[{"id":1,"slug":"proposal","kind":"approve","body":"ship it",
#              "created_at":"…","status":"pending"}],"count":1}
```

### `POST /replies/{id}/ack` — mark read *(auth)*
`204` on success, `404` for an unknown id. Idempotent triage of a pending queue.

### `POST /{slug}` — same-origin answer submit (content plane, mesh-only)
The **content-origin** submit path for the inline reply box (below). It records a
single free-text answer as a **`comment`**-kind reply and returns a
**server-rendered** result page: `201` "✓ Recorded" only after the answer is
durably stored, or an explicit "Not recorded" page (`400`/`413`/`429`/`404`/`503`)
on any failure. Mounted via the optional `module.ContentRouteModule` seam so it
lives on the same origin that serves the page. `404` for an unknown/private slug.

## Inline reply box on a rendered page (Q&A / teaching)

For question-and-answer or LMS-style use, publish a markdown page with the box
baked in — no hand-rolled form, honest by construction:

```sh
curl --data-binary @lesson.md \
  "http://demiplane.mesh:8890/publish?render=md&slug=lesson-01&reply=question"
```

The rendered page carries a single answer box whose **JS-free form has no
`action`**, so it posts to the page's own URL on the content plane — **same-origin**
(no cross-origin/mixed-content trap) and TLS-safe (scheme inherited). Because
there is no client JavaScript, the browser navigates to the server's real
response: the "✓ Recorded" confirmation is emitted **only after** the row is
stored, and a failed write shows an error — never a false success. Read the
answers with `GET /replies?slug=lesson-01` like any other `comment` reply.

Requires `?render=md`; rejected with `?private` (a private capability page can't
collect replies).

## Reply-event hook (zero-polling consumers)

When a reply is **durably recorded** (either submit path), the module fires a
configurable action so an external agent — e.g. a Professor that grades an
answer and publishes the next lesson — reacts without polling `GET /replies`.
Configure in `${XDG_CONFIG_HOME:-~/.config}/demiplane/config` (keys exist only
in `-tags reply` builds; set either, both, or neither):

```ini
reply_hook_exec = systemd-run --user /usr/local/bin/professor-grade
reply_hook_url  = http://127.0.0.1:9099/reply-event
```

- **exec** — run via `/bin/sh -c`; the reply
  `{id, slug, kind, body, created_at, status}` arrives as JSON on **stdin** and
  as `DEMIPLANE_REPLY_ID/SLUG/KIND/BODY` env vars. Bounded at 5 minutes —
  long-running work should enqueue/detach (as the `systemd-run` example does).
- **webhook** — the same JSON is POSTed (`application/json`, 30 s bound).

Dispatch is **fire-and-forget, strictly after the insert**: a failing or slow
hook is logged and can never fail, delay, or roll back the reply write — the
viewer's honest confirmation is independent of hook fate. A malformed
`reply_hook_url` is a hard startup error, like any other config key.

## Forward flow (`?next=` — auto-advance to the next lesson)

Publish a lesson naming its sequel and the student flows forward after
answering, instead of dead-ending on the confirmation:

```sh
curl --data-binary @lesson-01.md \
  "http://demiplane.mesh:8890/publish?render=md&slug=lesson-01&reply=question&next=lesson-02"
```

- `?next=` requires `?reply`, must be a well-formed named slug, and must differ
  from `?slug=`. **It need not exist yet** — appearing later is the point.
- The pointer rides the baked form as a hidden field; the submit handler
  re-validates it (hidden fields are client-tamperable) and anything malformed
  degrades to the plain confirmation. It only ever yields a same-origin,
  slug-shaped link.
- After "✓ Recorded", the confirmation adds "your next lesson is being
  prepared" and meta-refreshes (JS-free) into
  **`GET /answer/{slug}/next?to=<next>`**: a holding page that re-checks every
  5 s while the target is unpublished — never claiming readiness, always keeping
  the back link — and `302`s to `/<next>` the moment it resolves publicly
  (`HasPublic`, no capability-slug oracle).
- No `?next=` → prior behavior exactly (confirmation + back link).

Together with the hook: answer lands → hook spawns the grader → grader publishes
`lesson-02` → the student's confirmation page carries them onto it.

## Putting reply controls on a page

Link to the form from any published artifact:

```html
<a href="/reply/my-report">Leave a reply</a>
```

…or inline the control directly (it posts `application/x-www-form-urlencoded`):

```html
<form method="post" action="/reply/my-report">
  <textarea name="body" placeholder="Optional note…"></textarea>
  <button name="kind" value="approve">Approve</button>
  <button name="kind" value="defer">Defer</button>
  <button name="kind" value="comment">Comment</button>
</form>
```

## Storage & safety

- **Module-owned storage:** a private SQLite db at
  `<store>/modules/reply/replies.db` — never core's `meta.db`. Deleting that dir
  removes all replies and nothing else.
- **Anti-abuse** (the submit endpoint is open on the mesh): 8 KiB body cap,
  1000-reply-per-artifact cap, target-slug existence check.
- **No HTML injection sink:** replies are returned only as JSON; the one HTML
  surface (the form) echoes no stored reply text. No CSRF surface — submit
  carries no ambient credentials, reads authenticate by header.
