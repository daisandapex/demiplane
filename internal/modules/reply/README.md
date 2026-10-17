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
| Submit a reply | `POST /reply/{slug}` | **viewer**: open to anyone who can view the artifact, exactly like `GET /{slug}`, with the artifact's view password if it has one. Only for artifacts published with `?reply=form` or `?reply=question`. |
| Show the reply form | `GET /reply/{slug}` | viewer, same as submit |
| List / triage replies | `GET /replies` | **bearer publish-auth** (same token as `POST /publish`, `GET /list`) |
| Mark a reply read | `POST /replies/{id}/ack` | bearer publish-auth |

No new auth primitive: submitting is a *view*-level act (network reachability,
plus the view password through core's rate-limited check), reading the
collected replies is a *publish*-level act (bearer token). On an open instance
(no token) the read side is open too, same as core's `/list`.

Replies are **opt-in per artifact**: publish with `?reply=form` (this module's
form, page unchanged) or `?reply=question` (the inline box below). The choice is
stored with the artifact and replaced by every publish to the slug, so a later
publish without `?reply` turns replies off. Every other artifact answers `404`
on the reply routes.

## Endpoints

### `GET /reply/{slug}` — the reply form
A self-contained, JS-free HTML page: an optional note plus **Approve / Defer /
Comment** buttons. `404` if the artifact doesn't exist or does not accept
replies; `401` without its view password.

### `POST /reply/{slug}` — submit
Accepts a browser form post **or** a JSON body:

```sh
curl -X POST -H 'Content-Type: application/json' \
     --data '{"kind":"approve","body":"ship it"}' \
     http://demiplane.mesh:8080/reply/proposal
```

- `kind` ∈ `approve | defer | comment` (a `comment` requires non-empty `body`).
- A browser post must include the `token` field the `GET /reply/{slug}` form
  carries (or `"token"` in JSON); without it the post is a `403`. Non-browser
  clients (no `Origin` or `Sec-Fetch-Site` header) need no token.
- `201` + the stored reply as JSON (or the HTML confirmation for a form post).
- `404` unknown slug or replies not enabled · `401` view password missing or
  wrong · `400` bad kind / empty comment · `413` body over 8 KiB · `429` a rate
  limit (with `Retry-After`) or the artifact's reply cap · `503` the hook pool
  is full (with `Retry-After`) · `507` the aggregate reply limits are reached.

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

### `POST /answer/{slug}`: same-origin answer submit (content plane, viewer)
The **content-origin** submit path for the inline reply box (below). It records a
single free-text answer as a **`comment`**-kind reply and returns a
**server-rendered** result page: `201` "✓ Recorded" only after the answer is
durably stored, or an explicit "Not recorded" page
(`400`/`401`/`403`/`404`/`409`/`413`/`429`/`503`/`507`) on any failure. The content listener
refuses cross-origin browser posts (`403`), except from an opaque origin (a
sandboxed page) whose Referer is absent or on the content origin. A browser post
(one carrying `Origin` or `Sec-Fetch-Site`) must also carry the box's hidden
`token`, which is bound to the artifact and its current publish (a random
identifier every publish replaces and a rebake keeps); without a valid one the
answer is refused with `403` and not recorded. Non-browser clients need no
token. An answer is recorded only if, when the reply store admits it (inside
its write transaction, immediately before the insert), the slug still names
the publish the answer was checked against with replies enabled; otherwise it
is refused with `409`, nothing is stored and no hook fires. A republish that
commits after that point is ordered after the answer. Mounted via the optional `module.ContentRouteModule` seam so it
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
collect replies). `?reply=question` also enables replies for the artifact, so
the box and the `/reply/{slug}` form both accept them.

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
  as `DEMIPLANE_REPLY_ID/SLUG/KIND/BODY` env vars. It runs in its own process
  group, killed when the shell exits or after `reply_hook_timeout` (default
  `5m`), so long-running work must detach (as the `systemd-run` example does,
  or with `setsid`). Output is read as it streams and only the first 512 bytes
  are kept, whether or not they are logged (see below).
- **webhook** — the same JSON is POSTed (`application/json`, 30 s bound).
  Redirects are not followed (a `3xx` is logged as a failed delivery), so the
  reply is only ever sent to the configured URL; point it at the final
  endpoint.

Dispatch runs on a bounded pool: `reply_hook_workers` events at once (default
2) and at most `reply_hook_queue` more waiting (default 32). A submission takes
its place in the pool **before** the reply is stored; a full pool refuses the
submission with `503` and records nothing, so a stored reply's hook is never
dropped. Once admitted, a failing or slow hook is logged and can never fail,
delay, or roll back the reply write; the viewer's honest confirmation is
independent of hook fate. A malformed value is a hard startup error, like any
other config key.

Failure logs are kept free of reply content and hook credentials. The exec
hook's output stays out of the log by default, because a grading hook often echoes the
answer it was given; set `reply_hook_log_output = on` while debugging a hook to
log the first 512 bytes, with control characters escaped. Webhook failures log
only the URL's origin (`scheme://host`), never its userinfo, path or query.

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

Publish the artifact with `?reply=form`, then link to the form, which is
served by the control plane:

```html
<a href="http://demiplane.mesh:8080/reply/my-report">Leave a reply</a>
```

…or inline the control in a page served by the control plane (it posts
`application/x-www-form-urlencoded`). A page on the content origin cannot post
here: the control plane refuses cross-origin browser writes. Use
`?reply=question` for an answer box on the page itself.

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
- **Anti-abuse** (the submit endpoint is open to viewers): per-artifact opt-in,
  the view password, 8 KiB text cap, a 10 s body deadline, submissions
  rate-limited per client, per artifact and in all (`reply_rate_client`,
  `reply_rate_target`, `reply_rate_global`; default 10, 60 and 300 a minute),
  `reply_max_per_artifact` (1000) checked and inserted atomically,
  `reply_max_total` (100,000) and `reply_max_bytes` (64 MiB) in all, and
  `reply_retention` (unset or `0` keeps replies; set, an hourly sweep deletes
  replies older than it, never the submit path). The
  content listener refuses cross-origin browser posts except from an opaque
  origin (a sandboxed page) with no Referer or one on the content origin; it
  cannot tell apart two pages served from the same content origin, and a
  sandboxed frame elsewhere can omit Referer, which is why the opt-in and the
  password matter. Limits and defaults:
  [SECURITY.md](../../../SECURITY.md#resource-limits).
- **No HTML injection sink:** replies are returned only as JSON; the one HTML
  surface (the form) echoes no stored reply text. Reads authenticate by
  header. Submits can carry a browser's cached Basic credentials for a
  password page, which is why the content listener refuses cross-origin
  browser posts.
