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
| Control plane | `POST /publish`, `GET /list`, `DELETE /{slug}`, `/docs`, `/help`, `/connect`, `/gallery`, reply admin | `127.0.0.1:8080` |
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
file part is taken). The body streams to disk with no size limit unless the
operator set `--max-upload`.

### Query parameters

| Parameter | Meaning |
|---|---|
| `slug=<name>` | Named, stable URL that overwrites in place on re-publish. Charset `[A-Za-z0-9][A-Za-z0-9._-]{0,127}`. Omit for an auto friendly slug like `shadow-specter`. |
| `private=true` | Mint a high-entropy, unguessable capability slug. Cannot be combined with `slug` (a named slug is guessable). |
| `ttl=<dur>` | Auto-expire: `30m`, `2h`, `7d` (days), or any Go duration. After it lapses the slug returns 404. |
| `render=md` | Render a markdown body to a styled HTML page on publish (house theme, sticky title header, optional light/dark toggle). |
| `reply=question` | Bake a first-class inline reply box into the rendered page. Requires `render=md` and a named `slug`; not valid with `private`. Needs the `reply` build tag. |
| `next=<slug>` | Forward flow: names the page that follows this one's answer. Requires `reply`. Must differ from `slug`; need not exist yet. |
| `filename=<name>` | Content-type hint for raw-body uploads (the extension informs the served type). |
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
conditional GETs are supported. Two query add-ons:

- `?live` wraps the artifact in a first-party live-reload view that refreshes the
  moment the slug is re-published. It never alters the stored bytes.
- The reload stream itself is a Server-Sent Events endpoint at
  `GET /_events/{slug}` on the content origin. It emits `event: reload` on each
  re-publish and a heartbeat comment about every 25s. Note the literal-first
  shape: it is `/_events/{slug}`, not `/{slug}/_events`.

### Get: worked examples

```bash
# fetch an artifact (content origin)
curl "$CONTENT/reports"

# a password-gated artifact: password over HTTP Basic
curl -u any:hunter2 "$CONTENT/q3"

# subscribe to reload events (SSE)
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

The owner's artifacts as JSON, newest first. Auth: bearer. Always returns JSON
(no `Accept` needed). Private capability slugs are included for the owner; the
response never carries a password value, only whether a gate exists.

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
  "count": 1
}
```

### List: worked examples

```bash
curl -H "Authorization: Bearer $TOKEN" "$CTRL/list"
```

```python
import os, requests
r = requests.get(
    "https://demiplane.example/list",
    headers={"Authorization": f"Bearer {os.environ['DEMIPLANE_TOKEN']}"},
)
r.raise_for_status()
for a in r.json()["artifacts"]:
    print(a["slug"], a["url"], a["size"])
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

## Replies

The reply module (build tag `reply`) lets a viewer respond to a published page
and lets a publisher list and acknowledge those responses later. The endpoints
split across the two planes:

| Method + path | Plane | Auth | Purpose |
|---|---|---|---|
| `GET /reply/{slug}` | Control | open (mesh) | JS-free reply form for an existing artifact. |
| `POST /reply/{slug}` | Control | open (mesh) | Submit a reply (form or JSON body). |
| `POST /answer/{slug}` | Content | open (mesh) | Same-origin submit target for a page's baked `?reply` box. |
| `GET /answer/{slug}/next` | Content | open (mesh) | Forward-flow wait endpoint (`?to=<next>`); 302 to the next slug once it exists. |
| `GET /replies` | Control | bearer | List replies as JSON. Filters `?slug=`, `?status=pending|read|all` (default `pending`). |
| `POST /replies/{id}/ack` | Control | bearer | Mark a reply read. `204`, or `404` for an unknown id. |

Auth is asymmetric on purpose: submitting a reply is a viewer action (open at the
mesh), while listing and acking are publisher actions (bearer). A reply record is:

```json
{ "id": 42, "slug": "q-01", "kind": "comment", "body": "42", "created_at": "2026-07-15T10:05:00Z", "status": "pending" }
```

`kind` is one of `approve`, `defer`, `comment`. The inline `?reply` box always
records `comment`. A recorded reply can fire a server-side hook (config keys
`reply_hook_exec` and `reply_hook_url`) so an agent reacts with zero polling.

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
publish (the archive is buffered for extraction). Read a site back at
`GET /{site}/{path}` on the content origin; `GET /{site}/` serves its index.

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
is a human browse surface (like `serve --browse` for `GET /`), never a JSON API,
and it never lists private capability slugs.

## Status codes

| Code | Meaning |
|---|---|
| `200 OK` | Read succeeded (`GET /{slug}`, `/list`, `/replies`). |
| `201 Created` | Publish or reply stored. |
| `204 No Content` | Delete or ack succeeded. |
| `302 Found` | Forward-flow redirect once a `?next` slug exists. |
| `400 Bad Request` | Invalid or reserved slug, `private` + named slug, password in the URL query, bad `ttl`, or a malformed reply/site. |
| `401 Unauthorized` | Missing or invalid bearer token (write/list/delete/reply admin), or a wrong view password (read). |
| `404 Not Found` | No such artifact or reply, or it expired. |
| `405 Method Not Allowed` | Wrong method for a known path (for example `POST /list`). |
| `413 Payload Too Large` | Upload exceeds `--max-upload`, or a reply/site exceeds its cap. |
| `429 Too Many Requests` | An artifact reached its reply limit. |
| `501 Not Implemented` | A feature (for example `?site=`) is not compiled into this build. |
| `503 Service Unavailable` | A module (for example reply storage) is temporarily unavailable. |

## Reserved slugs

These names collide with built-in routes and are rejected (`400`) as a `?slug=`,
so an artifact can never shadow a route: `publish`, `list`, `docs`, `help`,
`help.json`, `llms.txt`. Feature routes reserve their own top segments too
(for example `connect`, `gallery`, `reply`, `replies`, `answer`, `_events`).

## See also

- [/help](/help) is the human getting-started guide (install, harness setup, theming).
- [/help.json](/help.json) is this API as self-describing JSON.
- [/llms.txt](/llms.txt) is a one-fetch plain-text reference for agents.
- [/connect](/connect) has copy-paste config to wire demiplane into your harness.
