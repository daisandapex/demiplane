# API reference

demiplane exposes a small REST API: you POST a file and get back a URL that only
your own network can reach. This page is the complete surface, with worked
examples in curl, Python, JavaScript, and Go. It is rendered by demiplane's own
markdown engine from the reference embedded in the binary, so it always matches
the build you are running.

For a machine-readable version, fetch [/help.json](/help.json) (self-describing
JSON) or [/llms.txt](/llms.txt) (one-fetch plain text). To wire demiplane into a
coding harness, see [/connect](/connect).

## The two planes

demiplane serves on two separate origins (ADR 0003, origin isolation). Keeping
artifact bytes on a different origin from the write API means a published page's
JavaScript cannot drive `/publish`, `/list`, or `DELETE` at your control plane.

| Plane | What lives here | Default bind |
|---|---|---|
| Control plane | `POST /publish`, `GET /list`, `DELETE /{slug}`, `POST /rerender`, `/docs`, `/help`, `/connect`, `/gallery`, reply admin | `127.0.0.1:8080` |
| Content origin | `GET /{slug}` artifact bytes, sites, SSE reload stream, inline answer submit | control port + 1, e.g. `:8081` |

`POST /publish` runs on the control plane and returns a URL on the content
origin. You publish to one origin and read back from the other. The examples
below use two variables so you can paste them against any instance:

```sh
CTRL=https://demiplane.example         # control plane (publish, list, delete)
CONTENT=https://demiplane.example:8081  # content origin (artifact URLs)
TOKEN=$(cat ~/.config/demiplane/token)  # bearer token, if this instance requires one
```

A single-origin mode exists (`serve --unsafe-same-origin`) but re-opens the
stored-XSS footgun the split closes; do not use it on a shared network.

## Authentication

Two independent gates, by design:

- **Write auth (who can publish).** When the operator sets a bearer token
  (`serve --token-file` or `DEMIPLANE_TOKEN`), `POST /publish`, `GET /list`,
  `DELETE /{slug}`, and the reply-admin endpoints require
  `Authorization: Bearer <token>`. With no token configured, those endpoints are
  open to anything that can reach the control plane. `GET /{slug}` is always open.
- **Read auth (who can view a URL).** By default a URL is guarded only by network
  reachability. A `private` artifact adds an unguessable capability slug as its
  secret. An optional per-artifact password gates reads over HTTP Basic (any
  username, the password as the password).

Never put the token or a view password in a URL query: query strings leak into
access logs, proxy logs, and browser history. The token rides the
`Authorization` header; the view password is set on publish via the
`X-Demiplane-Password` header and supplied on read via HTTP Basic.

## POST /publish

Store the request body and return the artifact URL. Auth: bearer (when
configured). Body: raw bytes of any type, or `multipart/form-data` (the first
file part is taken). The body streams to disk, capped at `--max-upload` (default
100 MiB; `0` lifts the cap). It must also arrive at `--upload-min-rate` or faster
after a `--body-timeout` grace, or the request gets `408`.

### Query parameters

| Parameter | Meaning |
|---|---|
| `slug=<name>` | Named, stable URL that overwrites in place on re-publish. Charset `[A-Za-z0-9][A-Za-z0-9._-]{0,127}`. Omit for an auto friendly slug like `shadow-specter`. |
| `private=true` | Mint a high-entropy, unguessable capability slug. Cannot be combined with `slug` (a named slug is guessable). |
| `ttl=<dur>` | Auto-expire: `30m`, `2h`, `7d` (days), or any Go duration. Days are a plain decimal (`7d`, `0.5d`); exponents, signs, `NaN` and `Inf` are refused, and so is any value longer than 64 bytes. The minimum is `1s` and the maximum about 292 years; anything outside that range, or a value of only whitespace, is a `400`, never a page that does not expire. An empty or omitted `ttl` means no expiry. After it lapses the slug returns 404. |
| `render=md` | Render a markdown body (at most 8 MiB) to a styled HTML page on publish (house theme, sticky title header, optional light/dark toggle). The body is received into a scratch file in the store, charged to `--store-quota`, and rendered once it is complete; a render that waits more than 5 seconds for one of the `--render-concurrency` slots gets `503`. |
| `reply=question` | Bake a first-class inline reply box into the rendered page and accept replies. Requires `render=md` and a named `slug`; not valid with `private`. Needs the `reply` build tag. |
| `reply=form` | Accept replies through the reply module's form at `/reply/{slug}`, without changing the page. Not valid with `private`. Without either `reply` value, a publish turns replies off for the slug. |
| `next=<slug>` | Forward flow: names the page that follows this one's answer. Requires `reply`. Must differ from `slug`; need not exist yet. |
| `filename=<name>` | Content-type hint for raw-body uploads (the extension informs the served type). At most 255 bytes; longer is a `400`. |
| `site=<name>` | Ingest a tar or zip body as a path-structured multi-file site (see Sites). Needs the site feature wired in. |

### Headers

| Header | Meaning |
|---|---|
| `Authorization: Bearer <token>` | Write auth, when the instance requires it. |
| `X-Demiplane-Password: <pw>` | Set a view password (never via URL). Reads then need HTTP Basic. |
| `Accept: application/json` | Return a JSON object instead of a plain-text URL. |
| `Content-Type: multipart/form-data` | Upload the first file part instead of a raw body. |

### Response

`201 Created`. With no `Accept: application/json`, the body is the artifact URL
as `text/plain` followed by a newline. With `Accept: application/json`:

```json
{
  "url": "https://demiplane.example:8081/reports",
  "slug": "reports",
  "content_type": "text/html; charset=utf-8",
  "size": 4096,
  "private": false,
  "password": false,
  "expires_at": ""
}
```

`expires_at` is an RFC3339 timestamp when a `ttl` was set, otherwise an empty
string. `password` reports whether a view password gate exists (never the value).

### Publish: worked examples

```bash
# raw body, auto slug -> prints https://demiplane.example:8081/shadow-specter
curl -H "Authorization: Bearer $TOKEN" --data-binary @index.html "$CTRL/publish"

# named slug that overwrites in place, JSON response
curl -H "Authorization: Bearer $TOKEN" -H "Accept: application/json" \
     --data-binary @report.html "$CTRL/publish?slug=reports"

# private capability URL that expires in a day
curl -H "Authorization: Bearer $TOKEN" \
     --data-binary @secret.html "$CTRL/publish?private=true&ttl=24h"

# password-gated (password via header, never the URL)
curl -H "Authorization: Bearer $TOKEN" -H "X-Demiplane-Password: hunter2" \
     --data-binary @q.html "$CTRL/publish?slug=q3"

# render markdown to a styled page
curl -H "Authorization: Bearer $TOKEN" \
     --data-binary @notes.md "$CTRL/publish?render=md&slug=notes"

# multipart upload (extension informs the content-type)
curl -H "Authorization: Bearer $TOKEN" -F file=@style.css "$CTRL/publish"
```

```python
import os
import requests

CTRL = "https://demiplane.example"
token = os.environ["DEMIPLANE_TOKEN"]

with open("report.html", "rb") as f:
    body = f.read()

resp = requests.post(
    f"{CTRL}/publish",
    params={"slug": "reports"},            # add "private": "true", "ttl": "24h", "render": "md" as needed
    data=body,                             # raw bytes; use files={"file": ...} for multipart
    headers={
        "Authorization": f"Bearer {token}",
        "Accept": "application/json",
        # "X-Demiplane-Password": "hunter2",  # optional view password
    },
)
resp.raise_for_status()                    # 201 on success
print(resp.json()["url"])                  # the content-origin URL to share
```

```javascript
import { readFile } from "node:fs/promises";

const CTRL = "https://demiplane.example";
const token = process.env.DEMIPLANE_TOKEN;

const body = await readFile("report.html");
const url = new URL(`${CTRL}/publish`);
url.searchParams.set("slug", "reports");   // private=true, ttl=24h, render=md, ...

const res = await fetch(url, {
  method: "POST",
  headers: {
    Authorization: `Bearer ${token}`,
    Accept: "application/json",
    // "X-Demiplane-Password": "hunter2",
  },
  body,                                     // a Buffer/Uint8Array of the raw bytes
});
if (!res.ok) throw new Error(`publish failed: ${res.status}`);
const { url: artifactURL } = await res.json();
console.log(artifactURL);
```

```go
package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
)

func main() {
	const ctrl = "https://demiplane.example"
	body, err := os.ReadFile("report.html")
	if err != nil {
		panic(err)
	}

	q := url.Values{"slug": {"reports"}} // private=true, ttl=24h, render=md, ...
	req, _ := http.NewRequest(http.MethodPost, ctrl+"/publish?"+q.Encode(), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+os.Getenv("DEMIPLANE_TOKEN"))
	req.Header.Set("Accept", "application/json")
	// req.Header.Set("X-Demiplane-Password", "hunter2")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	out, _ := io.ReadAll(resp.Body)
	fmt.Printf("%d %s\n", resp.StatusCode, out) // 201 + {"url": ...}
}
```

## GET /{slug}

Fetch an artifact's bytes as-is, with the stored content-type, on the content
origin. Always open, except that a password-gated artifact requires HTTP Basic
credentials (username ignored, password is the password). Range requests and
conditional GETs are supported. Public artifacts carry `Cache-Control: no-cache`;
private and password-protected ones (and their `401`) carry
`Cache-Control: private, no-store` and `Referrer-Policy: no-referrer`. Every
response has a `Last-Modified` with one-second resolution (two writes in the
same second can share it), and, unless its bytes predate generation-tracked
storage, an `ETag` that changes on every write of the bytes, including a
re-publish and a `POST /rerender`. When there is an `ETag`, it alone decides a
conditional request: `If-None-Match` can earn a `304`, `If-Modified-Since` is
ignored, and an `If-Range` carrying a date returns the whole body. Only an
artifact without an `ETag` (written before generation-tracked storage) is
revalidated by `If-Modified-Since`. HTML, SVG and XML are
served in a CSP sandbox, so each page runs in an opaque origin of its own and
cannot read other artifacts, use storage, or register a service worker (see
[SECURITY.md](SECURITY.md#content-isolation-between-artifacts) and
[DESIGN.md](DESIGN.md#content-isolation)). Two query add-ons:

- `?live` wraps the artifact in a first-party live-reload view that refreshes the
  moment the slug is re-published. It never alters the stored bytes.
- The reload stream itself is a Server-Sent Events endpoint at
  `GET /_events/{slug}` on the content origin. It emits `event: reload` on each
  re-publish and a heartbeat comment about every 25s. Note the literal-first
  shape: it is `/_events/{slug}`, not `/{slug}/_events`. The stream is
  authorized like the artifact: an unknown or expired slug is `404`, a
  password-protected artifact needs its password over HTTP Basic (`401`
  without it; its `?live` wrapper needs it too), and the stream ends if the
  artifact is deleted, expires or changes password. Too many streams for one
  slug or one client is `503`.

### Get: worked examples

```bash
# fetch an artifact (content origin)
curl "$CONTENT/reports"

# a password-gated artifact: password over HTTP Basic
curl -u any:hunter2 "$CONTENT/q3"

# subscribe to reload events (SSE); add -u any:<pw> for a password-gated one
curl -N "$CONTENT/_events/reports"
```

```python
import requests

CONTENT = "https://demiplane.example:8081"

r = requests.get(f"{CONTENT}/reports")
r.raise_for_status()
print(r.headers["Content-Type"], len(r.content))

# password-gated:
r = requests.get(f"{CONTENT}/q3", auth=("any", "hunter2"))
```

```javascript
const CONTENT = "https://demiplane.example:8081";

const res = await fetch(`${CONTENT}/reports`);
if (res.status === 404) throw new Error("expired or unknown slug");
const html = await res.text();

// password-gated:
const auth = "Basic " + Buffer.from("any:hunter2").toString("base64");
const gated = await fetch(`${CONTENT}/q3`, { headers: { Authorization: auth } });
```

```go
package main

import (
	"fmt"
	"io"
	"net/http"
)

func main() {
	const content = "https://demiplane.example:8081"

	resp, err := http.Get(content + "/reports")
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		fmt.Println("expired or unknown slug")
		return
	}
	b, _ := io.ReadAll(resp.Body)
	fmt.Printf("%s %d bytes\n", resp.Header.Get("Content-Type"), len(b))

	// password-gated: req.SetBasicAuth("any", "hunter2") on an http.NewRequest.
}
```

## GET /list

The owner's non-private artifacts as JSON, newest first. Auth: bearer. Always
returns JSON (no `Accept` needed). Private artifacts are never listed, not even
for the owner: a capability slug is the secret, so keep the URL the publish
response returned. The response never carries a password value, only whether a
gate exists.

A response holds at most 200 artifacts. When more follow, it carries `next`, a
cursor; pass it back as `?after=<next>` for the following page. The last page
has no `next`. `count` is the number of artifacts in this response. A malformed
`after` gets `400`. A response can hold fewer than 200 artifacts, even none, and
still carry `next`: one request steps over a bounded number of expired
artifacts the sweeper has not removed yet, so keep following `next` until it is
absent.

```json
{
  "artifacts": [
    {
      "slug": "reports",
      "url": "https://demiplane.example:8081/reports",
      "filename": "report.html",
      "content_type": "text/html; charset=utf-8",
      "size": 4096,
      "created_at": "2026-07-15T10:04:00Z",
      "owner": "local",
      "private": false,
      "password": false,
      "expires_at": ""
    }
  ],
  "count": 1,
  "next": "1784109840.reports"
}
```

### List: worked examples

```bash
curl -H "Authorization: Bearer $TOKEN" "$CTRL/list"
# the following page, with the previous response's "next"
curl -H "Authorization: Bearer $TOKEN" "$CTRL/list?after=1784109840.reports"
```

```python
import os, requests
params = {}
while True:
    r = requests.get(
        "https://demiplane.example/list",
        headers={"Authorization": f"Bearer {os.environ['DEMIPLANE_TOKEN']}"},
        params=params,
    )
    r.raise_for_status()
    page = r.json()
    for a in page["artifacts"]:
        print(a["slug"], a["url"], a["size"])
    if "next" not in page:
        break
    params = {"after": page["next"]}
```

```javascript
const res = await fetch("https://demiplane.example/list", {
  headers: { Authorization: `Bearer ${process.env.DEMIPLANE_TOKEN}` },
});
const { artifacts, count } = await res.json();
console.log(`${count} artifacts`, artifacts.map((a) => a.slug));
```

```go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

type listItem struct {
	Slug string `json:"slug"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

func main() {
	req, _ := http.NewRequest(http.MethodGet, "https://demiplane.example/list", nil)
	req.Header.Set("Authorization", "Bearer "+os.Getenv("DEMIPLANE_TOKEN"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	var out struct {
		Artifacts []listItem `json:"artifacts"`
		Count     int        `json:"count"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("%d artifacts\n", out.Count)
	for _, a := range out.Artifacts {
		fmt.Println(a.Slug, a.URL, a.Size)
	}
}
```

## DELETE /{slug}

Remove an artifact (blob plus metadata) on the control plane. Auth: bearer.
Returns `204 No Content` on success, `404` for an unknown slug.

### Delete: worked examples

```bash
curl -X DELETE -H "Authorization: Bearer $TOKEN" "$CTRL/reports"
```

```python
import os, requests
r = requests.delete(
    "https://demiplane.example/reports",
    headers={"Authorization": f"Bearer {os.environ['DEMIPLANE_TOKEN']}"},
)
print(r.status_code)  # 204 deleted, 404 unknown
```

```javascript
const res = await fetch("https://demiplane.example/reports", {
  method: "DELETE",
  headers: { Authorization: `Bearer ${process.env.DEMIPLANE_TOKEN}` },
});
console.log(res.status); // 204 or 404
```

```go
req, _ := http.NewRequest(http.MethodDelete, "https://demiplane.example/reports", nil)
req.Header.Set("Authorization", "Bearer "+os.Getenv("DEMIPLANE_TOKEN"))
resp, err := http.DefaultClient.Do(req)
if err != nil {
	panic(err)
}
resp.Body.Close()
fmt.Println(resp.StatusCode) // 204 or 404
```

## POST /rerender

Rebake a `?render=md` page from the markdown the store kept when it was
published, using the instance's **current** renderer and theme. Control plane,
auth: bearer.

`POST /rerender/{slug}` rebakes one page; `POST /rerender` walks every stored
artifact and rebakes each one that has a retained source. Neither takes a body.

This exists because rendering happens once, at publish: without it, upgrading the
renderer or switching themes leaves every existing page on the old output until
someone re-publishes it from a copy of the markdown they may no longer have.

A rebake changes the rendering and nothing else. The slug, filename,
content-type, privacy, view password, TTL, series, and original publish date all
survive it, and the page keeps its URL.

An artifact with no retained source — anything published before source retention
existed, and every non-markdown upload — is **skipped, not failed**: the response
is `200` with `"rerendered": false` and a reason, and the artifact's bytes are
untouched. `404` means the slug does not exist at all. Scope: this rebakes. There
is no version history, no diff, and no editor.

```json
{
  "slug": "notes",
  "rerendered": true,
  "size": 8192
}
```

```json
{
  "rerendered": 33,
  "skipped": 5,
  "failed": {},
  "results": [
    { "slug": "notes", "rerendered": true, "size": 8192 },
    { "slug": "logo.svg", "rerendered": false, "reason": "no retained markdown source (published before source retention, or not a ?render=md page)" }
  ]
}
```

Without `Accept: application/json` both routes answer in plain text.

### Rerender: worked examples

```bash
# one page
curl -X POST -H "Authorization: Bearer $TOKEN" "$CTRL/rerender/notes"

# every page with a retained source, after upgrading the binary
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Accept: application/json" \
     "$CTRL/rerender"
```

```python
import os, requests
r = requests.post(
    "https://demiplane.example/rerender",
    headers={
        "Authorization": f"Bearer {os.environ['DEMIPLANE_TOKEN']}",
        "Accept": "application/json",
    },
)
report = r.json()
print(report["rerendered"], "rebaked,", report["skipped"], "skipped")
```

## Replies

The reply module (build tag `reply`) lets a viewer respond to a published page
and lets a publisher list and acknowledge those responses later. The endpoints
split across the two planes:

| Method + path | Plane | Auth | Purpose |
|---|---|---|---|
| `GET /reply/{slug}` | Control | viewer | JS-free reply form for an artifact that accepts replies. |
| `POST /reply/{slug}` | Control | viewer | Submit a reply (form or JSON body). A browser post must carry the form's `token` field (see below). |
| `POST /answer/{slug}` | Content | viewer | Same-origin submit target for a page's baked `?reply` box. Cross-origin browser posts get `403` (an opaque-origin post from a sandboxed page is admitted unless its Referer names another origin). A browser post must carry the box's `token` field (see below). |
| `GET /answer/{slug}/next` | Content | open (mesh) | Forward-flow wait endpoint (`?to=<next>`); 302 to the next slug once it exists. |
| `GET /replies` | Control | bearer | List replies as JSON. Filters `?slug=`, `?status=pending|read|all` (default `pending`). |
| `POST /replies/{id}/ack` | Control | bearer | Mark a reply read. `204`, or `404` for an unknown id. |

**Submission tokens.** Every reply form demiplane serves (the baked `?reply=question` box and `GET /reply/{slug}`) carries a hidden `token` field bound to that artifact and its current publish: every publish to a slug, including one that only changes its password, gets a new random publish identifier, and a rebake keeps it. A submission sent by a browser (one carrying `Origin` or `Sec-Fetch-Site`) is refused with `403` unless it carries the token for exactly the slug and publish it posts to. Clients that are not browsers (curl, agents) send neither header and need no token. A submission is recorded only if, when the reply store admits it (inside its write transaction, immediately before the insert), the slug still names the publish the submission was checked against with replies enabled; otherwise it is refused with `409`, nothing is stored and no hook fires. A republish that commits after that point is ordered after the reply. A page baked before this release has no valid token: rebake it (`POST /rerender`) or republish it.

Auth is asymmetric on purpose: submitting a reply is a viewer action, while
listing and acking are publisher actions (bearer). "Viewer" means what viewing
the artifact needs: network reachability, plus HTTP Basic with the view password
when it has one. Replies are accepted only for artifacts published with
`reply=form` or `reply=question`; other artifacts answer `404` on these routes.
Submissions are rate-limited per client, per artifact and in all, and the
stored replies are capped per artifact and in all (limits in
[SECURITY.md](SECURITY.md#resource-limits)). A reply record is:

```json
{ "id": 42, "slug": "q-01", "kind": "comment", "body": "42", "created_at": "2026-07-15T10:05:00Z", "status": "pending" }
```

`kind` is one of `approve`, `defer`, `comment`. The inline `?reply` box always
records `comment`. A recorded reply can fire a server-side hook (config keys
`reply_hook_exec` and `reply_hook_url`, plus `reply_hook_log_output` to log a
failing exec hook's output) so an agent reacts with zero polling.

### Replies: worked examples

```bash
# submit a reply as JSON (control plane)
curl -H "Content-Type: application/json" \
     -d '{"kind":"comment","body":"looks good"}' "$CTRL/reply/q-01"

# a baked ?reply box posts a form to the content origin
curl --data-urlencode "body=42" "$CONTENT/answer/q-01"

# list replies (bearer) and acknowledge one
curl -H "Authorization: Bearer $TOKEN" "$CTRL/replies?slug=q-01&status=all"
curl -X POST -H "Authorization: Bearer $TOKEN" "$CTRL/replies/42/ack"
```

```python
import os, requests
CTRL = "https://demiplane.example"
auth = {"Authorization": f"Bearer {os.environ['DEMIPLANE_TOKEN']}"}

# submit
requests.post(f"{CTRL}/reply/q-01", json={"kind": "comment", "body": "looks good"})

# list pending replies, then ack each
pending = requests.get(f"{CTRL}/replies", params={"slug": "q-01"}, headers=auth).json()
for r in pending["replies"]:
    print(r["id"], r["kind"], r["body"])
    requests.post(f"{CTRL}/replies/{r['id']}/ack", headers=auth)
```

```javascript
const CTRL = "https://demiplane.example";
const auth = { Authorization: `Bearer ${process.env.DEMIPLANE_TOKEN}` };

// submit
await fetch(`${CTRL}/reply/q-01`, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ kind: "comment", body: "looks good" }),
});

// list and ack
const { replies } = await fetch(`${CTRL}/replies?slug=q-01&status=all`, { headers: auth })
  .then((r) => r.json());
for (const r of replies) {
  await fetch(`${CTRL}/replies/${r.id}/ack`, { method: "POST", headers: auth });
}
```

```go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

func main() {
	const ctrl = "https://demiplane.example"
	bearer := "Bearer " + os.Getenv("DEMIPLANE_TOKEN")

	// submit a JSON reply (open at the mesh)
	http.Post(ctrl+"/reply/q-01", "application/json",
		strings.NewReader(`{"kind":"comment","body":"looks good"}`))

	// list pending replies (bearer)
	req, _ := http.NewRequest(http.MethodGet, ctrl+"/replies?slug=q-01", nil)
	req.Header.Set("Authorization", bearer)
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	var out struct {
		Replies []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		} `json:"replies"`
	}
	json.NewDecoder(resp.Body).Decode(&out)

	// ack each (bearer)
	for _, r := range out.Replies {
		ack, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/replies/%d/ack", ctrl, r.ID), nil)
		ack.Header.Set("Authorization", bearer)
		http.DefaultClient.Do(ack)
	}
}
```

## Sites (multi-file publish)

`POST /publish?site=<name>` ingests a tar or zip archive as a path-structured
site instead of a single artifact. It runs on the control plane and returns the
site's index URL on the content origin. A `?fmt=tar|zip` hint disambiguates the
archive; otherwise the format is sniffed. Body size is always capped for a site
publish (the archive is buffered for extraction). An archive may hold at most
4,096 entries, counting directories, links and other skipped entries as well
as files; a zip's central directory may be at most 2 MiB, and the headers in
front of any one tar entry (its PAX, global PAX and GNU long-name or long-link
headers with their data, and its own header, counted in whole 512-byte
blocks; the previous entry's padding does not count) at most 64 KiB (`413`
otherwise). Entry paths use `/`
only: a name containing a backslash, `..`, a dotfile segment or an absolute
path is refused with `400`, and symlinks and other non-regular entries are
skipped. Two archives are parsed at a time; a publish that waits more than five
seconds for a turn gets `503` with `Retry-After`. Read a site back at
`GET /{site}/{path}` on the content origin; `GET /{site}/` serves its index.
Sites are public and do not expire: a site upload that asks for `?private`,
`?ttl`, `?password` or an `X-Demiplane-Password` header is refused with `400`
before the archive is read. Site pages run in the same sandbox as other HTML;
their own scripts (classic and module), stylesheets, images and fonts load, but
`fetch` of their own data files does not (see
[DESIGN.md](DESIGN.md#content-isolation)).

```bash
# publish a directory as a zip site
( cd public && zip -r - . ) | \
  curl --data-binary @- -H "Authorization: Bearer $TOKEN" \
       -H "Accept: application/json" "$CTRL/publish?site=marketing&fmt=zip"
# -> {"url":"https://demiplane.example:8081/marketing/","site":"marketing","files":12}

curl "$CONTENT/marketing/"            # the site index
curl "$CONTENT/marketing/css/app.css" # any path within it
```

## GET /gallery

An HTML index of the instance's non-private artifacts, on the control plane. It
is a human browse surface, never a JSON API, and it never lists private
capability slugs. It follows `serve --browse`: with browsing on it is public,
like the `GET /` listing; with browsing off (the default) it needs the bearer
token, like `GET /list`. A page shows at most 200 artifacts, newest first; an
"Older artifacts" link (`/gallery?after=<cursor>`) leads to the next page, and
filtering, sorting and grouping apply to the page shown.

## Status codes

| Code | Meaning |
|---|---|
| `200 OK` | Read succeeded (`GET /{slug}`, `/list`, `/replies`). |
| `201 Created` | Publish or reply stored. |
| `204 No Content` | Delete or ack succeeded. |
| `304 Not Modified` | Conditional `GET /{slug}` whose `If-None-Match` still matches the `ETag`, or, for an artifact without an `ETag`, whose `If-Modified-Since` is not older than `Last-Modified`. |
| `302 Found` | Forward-flow redirect once a `?next` slug exists. |
| `307 Temporary Redirect` | A non-canonical path (`//`, `/./`, `/../`); `Location` is the canonical path with its escaping and query kept. |
| `400 Bad Request` | Invalid or reserved slug, `private` + named slug, password in the URL query, bad `ttl`, a malformed publish query string, a site upload asking for `?private`/`?ttl`/a password, a malformed reply/site, or an ambiguous path (a segment that decodes to `.` or `..`, or holds an encoded `/` or `\`, or a control character), a `Host` header longer than 255 bytes (on every listener, before routing), or a malformed `after` cursor (`/list`, `/gallery`). |
| `401 Unauthorized` | Missing or invalid bearer token (write/list/delete/reply admin, `/gallery` without `--browse`), or a missing or wrong view password (read, `?live`, live-reload stream, reply). |
| `403 Forbidden` | A cross-origin browser write (another site posting through a visitor's browser), or a browser reply submission without a valid form token. A service-worker script fetch is answered `200` with a fixed script that removes itself. |
| `404 Not Found` | No such artifact or reply, or it expired. |
| `405 Method Not Allowed` | Wrong method for a known path (for example `POST /list`). |
| `408 Request Timeout` | The request body arrived too slowly (`--body-timeout`, `--upload-min-rate`). |
| `413 Payload Too Large` | Upload exceeds `--max-upload`, a reply/site exceeds its cap (a site archive: 4,096 entries of any kind, a 2 MiB zip central directory, 64 KiB of tar headers in front of one entry, the decompressed-bytes budget), or a `?render=md` document exceeds a render limit (table width, table cells, `--render-max-output`). |
| `421 Misdirected Request` | `--allowed-hosts` is set and the `Host` is not one it (or a base URL) names. |
| `429 Too Many Requests` | Too many failed view-password attempts or reply submissions (with `Retry-After`), or an artifact reached its reply limit. |
| `501 Not Implemented` | A feature (for example `?site=`) is not compiled into this build. |
| `503 Service Unavailable` | A module (for example reply storage) is temporarily unavailable, or the server is at a concurrency limit (requests in progress, password checks, renders, site archive parsing, the reply hook pool); retry after `Retry-After`. |
| `507 Insufficient Storage` | The write would pass `--store-quota`, the sites would hold more than 100,000 files and directories in all, or the aggregate reply limits are reached. |

## Reserved slugs

These names collide with built-in routes and are rejected (`400`) as a `?slug=`,
so an artifact can never shadow a route: `publish`, `list`, `docs`, `help`,
`help.json`, `llms.txt`. Feature routes reserve their own top segments too
(for example `connect`, `gallery`, `rerender`, `reply`, `replies`, `answer`,
`_events`). `demiplane receive` refuses the same names.

## See also

- [/help](/help) is the human getting-started guide (install, harness setup, theming).
- [/help.json](/help.json) is this API as self-describing JSON.
- [/llms.txt](/llms.txt) is a one-fetch plain-text reference for agents.
- [/connect](/connect) has copy-paste config to wire demiplane into your harness.
