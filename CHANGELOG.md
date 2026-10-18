# Changelog

All notable changes to demiplane are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and the project aims to follow
[Semantic Versioning](https://semver.org/) once it reaches a tagged release.

## [Unreleased]

### Security

- **Reply hooks no longer see the bearer token.** An exec hook inherited
  the server's whole environment, `DEMIPLANE_TOKEN` included; the server
  now drops the token from its environment once read, and hooks get none of
  its `DEMIPLANE_*` variables.
- **Published pages are isolated from each other.** HTML, SVG and XML are
  served in a CSP sandbox without `allow-same-origin`, so each page runs in an
  opaque origin and cannot read other artifacts (with or without the viewer's
  cached credentials) or register a service worker. Public module scripts,
  `crossorigin` stylesheets and fonts stay loadable from sandboxed pages.
- **Service workers registered before this release are removed after the
  first visit.** A service-worker script fetch on either plane, at any path,
  is answered with a fixed script that replaces the registered worker, clears
  Cache Storage, unregisters itself and reloads the pages it controlled. No
  URL serves any other worker script. The browser installs the replacement
  only after the old worker has handled the navigation that triggered the
  check, so on an instance where a hostile page may have registered a worker,
  the first page a viewer opens after the upgrade is still exposed to it; see
  the upgrade note.
- **Private and password-protected responses are not cached or referred.**
  They, their `401` and every `?live` wrapper carry `Cache-Control: private,
  no-store` and `Referrer-Policy: no-referrer`, and protected artifacts carry
  `Cross-Origin-Resource-Policy: same-origin`, so no other page can load one as
  a script, stylesheet or image. Each password-protected artifact has its own
  Basic realm.
- **Site uploads refuse options sites do not support.** `?site=` with
  `?private`, `?ttl`, `?password` or `X-Demiplane-Password` is a `400` before
  the archive is read; previously the options were ignored. A publish whose
  query string does not parse is a `400` too, rather than having the
  unreadable parameters silently dropped.
- **`/gallery` follows `--browse`.** Without `--browse` it needs the bearer
  token, like `/list`.
- **Live-reload streams are authorized.** `GET /_events/{slug}` requires an
  existing, unexpired artifact and its password, and re-checks on every
  republish and heartbeat. Its password checks, including the re-check after a
  republish, go through the view-password limits. Streams are shared fairly: a
  client holds at most two per artifact by default, and when an artifact or the
  instance is full a new client takes the place of the oldest stream of a
  client holding at least two more (`--live-max-*` flags).
- **`demiplane receive` refuses route names.** Route names are reserved when a
  route registers, not when the HTTP handlers are built, so SSH ingest can no
  longer create an artifact a route shadows (`gallery`, `connect`, `answer`
  and the rest).
- **Resource limits on unauthenticated work.** View-password checks run at
  bounded concurrency, and only failed attempts spend their budgets: a
  client's own budgets (per client, and per client and artifact) refuse its
  further guesses with `429` before hashing, while the artifact and instance
  budgets, once spent, refuse only clients that failed recently, so other
  clients' wrong guesses cannot lock a viewer out. A verified password stays
  remembered while it is in use (`--password-*` flags). Request bodies
  have deadlines (uploads a minimum rate), responses have write deadlines
  (downloads a minimum rate, and a reader that stops is cut),
  and requests and connections in progress are capped per listener and per
  client. Markdown rendering bounds table width, table cells and output size,
  runs in linear time, stops when the client goes away, and runs at bounded
  concurrency. Every byte of the page (body, frontmatter meta-header, outline
  and chrome) is charged to the output budget as it is produced, text is held
  to the remaining budget before it is formatted, and lines, headings, list
  nesting and frontmatter are capped before they are kept; heading ids are
  capped at 64 bytes. An optional aggregate storage quota (`--store-quota`)
  counts uploads in flight, archive scratch, extracted bytes, a fixed 4 KiB
  per artifact for its metadata and 4 KiB per site file or directory; a
  `filename` hint over 255 bytes is a `400`, and the sites hold at most
  100,000 files and directories in all, with or without a quota. Limits, defaults and
  flags: [SECURITY.md](SECURITY.md#resource-limits). Behind a reverse proxy
  listed in the new `--trusted-proxy`, per-client limits key on the client
  named in `X-Forwarded-For` instead of the proxy.
- **Replies are admitted, not just accepted.** Replies need a publish that
  enabled them, the artifact's view password when it has one, and room in
  per-client, per-artifact and global rate limits and in per-artifact and
  aggregate storage limits, checked atomically. The content listener refuses
  cross-origin browser writes, judged against the content origin. Every reply
  form demiplane serves (the baked `?reply=question` box, `GET /reply/{slug}`)
  carries a submission token bound to its artifact and current publish, signed
  with a key kept in the store, and a browser submission to `/answer/{slug}` or
  `/reply/{slug}` needs a valid token for exactly that slug, so another page on
  the instance cannot submit a reply as a viewer who holds the target's
  password. Clients that are not browsers need none. Tokens are bound to a
  random identifier each publish gets, so any republish invalidates earlier
  ones, and a reply is recorded only if its artifact's publish is still
  current when the reply store admits it, inside the reply transaction; a
  submission overtaken by a republish is refused (`409`) without storing
  anything or firing a hook. Reply retention runs in an
  hourly background sweep, never on the submit path. Reply hooks run on a
  bounded pool, in their own process group, with output capped while it
  streams.
- **New `--allowed-hosts` accepted-host policy.** When set, requests for any
  other `Host` (beyond the `--base-url` and `--content-base-url` hosts) get
  `421` on both planes. With `--base-url` set and no `--content-base-url`,
  content URLs are now derived from the base URL rather than the request
  `Host`.
- **The write guards compare full origins.** The control plane's `Origin`
  fallback now matches scheme, host and port (default ports normalized)
  against the `--base-url` origin when set, else the listener's own origin,
  instead of host and port alone. The content listener's guard does the same
  against the content origin (`--content-base-url`, else derived as for
  content URLs), including the `Referer` check for sandboxed pages.
- **Request paths are canonicalized before routing.** Non-canonical paths
  redirect to their canonical form with the original escaping (previously the
  redirect re-escaped `%` signs), and paths with encoded dot segments,
  separators inside a segment or control characters get `400`.
- **Site files are opened through `os.Root` with no symlinks followed.** A
  symlink anywhere on a served site path (the site directory, an
  intermediate directory or the file) is a 404, and the check cannot race
  the open.
- **Markdown links cannot disguise their destination.** A link destination
  containing a backslash or a control character, or an `http(s)` URL with
  userinfo, renders as plain text; the scheme allowlist is unchanged.
- **Deleted and expired files are always reclaimed.** The files a delete,
  expiry or overwrite stops referencing are recorded in the same transaction;
  a failed unlink is retried on every sweep, and an hourly reconcile removes
  unreferenced generation files. Every write records an intent before it
  creates a file, so reconcile never removes a write in flight (including
  one in a concurrent SSH receive) until its intent is 24 hours old; a write
  reclaimed then fails instead of committing.
- **The reply webhook follows no redirects.** A `3xx` from the configured
  endpoint is logged as a failed delivery instead of resending the reply to
  the `Location`.
- **Logs no longer carry capability slugs, store paths or hook secrets.**
  Errors from the store and the filesystem are logged with every path
  replaced, rerender logs a slug tag instead of the slug, webhook failures log
  only the URL's origin, and the exec hook's output is omitted unless the new
  `reply_hook_log_output = on` key is set (then truncated and escaped; it is
  captured bounded either way). The
  fatal error line of `serve` and `receive` is redacted the same way.
- **Request hosts are capped.** A `Host` header longer than 255 bytes gets
  `400` on every listener, before routing. Without `--base-url`, the request
  host appears in every advertised URL, so an unbounded one multiplied the
  size of each listing.
- **Listings are paged.** `GET /list` and `/gallery` return 200 artifacts at
  a time and `GET /replies` 100 replies, newest first. Listing queries
  (`/list`, `/gallery`, the browse index at `GET /`, series navigation) read
  a bounded number of index rows however many artifacts the instance holds:
  they seek on indexes over public artifacts, the browse index scans at most
  1,000 artifacts and counts a series only to 100, and the queries end with
  the request. Each read bounds the index entries it examines, not the rows
  it returns, so a large batch of expired artifacts awaiting the sweeper no
  longer makes each request walk it; a page that stops early may be short,
  or empty, and carries a cursor to continue. `GET /` no longer counts the
  instance's artifacts.
- **Setting a password shares the KDF limit.** Hashing a view password at
  publish takes the same bounded KDF slots as password checks: concurrent
  password publishes wait up to 5 seconds for a slot and otherwise get `503`.
  An SSH directory sync hashes its password once per session.
- **Site archives are checked before they are parsed.** A zip's central
  directory is validated against its end record (at most 4,096 entries and
  2 MiB) and read through a limit, archive/tar reads a tar through a 64 KiB
  limit on the headers in front of each entry, every entry of every kind counts
  toward the 4,096-entry cap, and two archives are parsed at a time (`503`
  with `Retry-After` beyond that). A site entry name with a backslash is
  refused (`400`) instead of being read as `/`.
- **Storage accounting follows the disk.** A failed site publish, a site
  replacement and a site delete release the quota only for what they actually
  removed; staging trees left by a failed cleanup or a crash stay counted and
  are removed at startup and by the hourly reconcile once an hour old. A
  replaced or deleted file that a download still holds open keeps its quota
  charge until the download ends.
- **TTL values are at most 64 bytes,** refused before they are parsed.
- **Temporary files stay charged until they are removed.** An upload's
  temporary file, or a scratch file used for a site archive or a render, that
  cannot be removed keeps its storage quota charge, and the failure is
  logged; such leftovers, and any left by a crash, are counted until the
  hourly reconcile removes them.
- **Generations left for the server count against the quota and are
  capped.** Each replaced or deleted version that `demiplane receive` leaves
  for the server to remove counts 4 KiB toward `--store-quota` until the
  server's sweep removes it. At most 10,000 may be waiting; past that, a
  publish through `receive` that would replace an artifact is refused until
  the server has swept. Deletes are never refused.
- **An upgraded store cannot be opened by older binaries.** A 2.0.0 writer
  left running against an upgraded store could republish a reply-enabled
  page with replies still on and its old reply tokens still valid, and
  write blob files and sites in place. The first open by this release moves
  `blobs/`, `sources/` and `sites/` under `data/` and leaves a marker file
  at each old name, so an older `serve` or `receive` stops at open before
  reading or writing anything; the database also refuses older writers' row
  changes.
- **Unnamed publishes can no longer replace each other.** Two concurrent
  publishes without `?slug=` could be given the same generated slug; the
  second then silently replaced the first, deleting its bytes and retained
  source, and both callers received the same URL. A generated slug is now
  reserved in the database before the upload is written, across processes
  (`serve` and SSH `receive`), and a generated publish only ever creates a
  new artifact; only an explicit `?slug=` (or `receive --slug`) replaces one.
  Affects 2.0.0.
- **An interrupted site replace no longer loses the site.** The replace is
  recorded before the old site is moved aside, and startup and the hourly
  reconcile restore the old site (or finish the replace) before any cleanup.
  Only the serving process recovers replaces and cleans up site trees; a
  later publish or delete of the site settles an interrupted replace first,
  so a deleted site never comes back.
- **A publish is on disk before it takes effect.** An artifact's bytes and
  retained source, and every file and directory of a staged site, are synced
  before the row or the site install that names them commits; a site install
  stays recorded until it is synced, so recovery finishes it, and a replaced
  site is removed only after that, so a power loss cannot leave a page or
  site pointing at bytes that never reached the disk with its predecessor
  already gone. A site delete likewise takes the site's name away durably
  before it removes any file, so a power loss leaves the site whole or
  gone, never partly removed.
- **The server no longer releases quota it never counted.** Removing an
  artifact, an old version or a pending deletion that `demiplane receive`
  created after the server last measured its usage no longer lowers the
  server's count below what the store holds; the rest is accounted at its
  next recount.
- **The MCP publish tool streams files published by `path`,** capped at
  100 MiB, instead of reading them whole into memory; a larger file is
  refused, not truncated.
- **Multipart publish headers are limited to 16 KiB.** At most 16 KiB may
  precede a multipart publish's file part body (preamble, earlier form
  fields and the part's headers); more is refused with `431`.
- **`POST /rerender` no longer reveals private capability slugs.** It
  reported every artifact it rebaked by slug, private ones included, so on
  an instance without a bearer token any client that could reach the control
  plane could list private artifacts' capability URLs. Private artifacts are
  still rebaked but reported as a one-way `slug#…` tag with `"private": true`,
  and the resume cursor is an opaque number. Affects 2.0.0.
- **Bulk rerender is bounded.** `POST /rerender` reads the store a page at a
  time, lists at most 100 results and 100 failures, stops when its request
  ends, and runs one sweep at a time (another gets `409`). It refuses a
  request body (`400`), writes a newline every second once it runs past a
  second and stops when a write fails or takes over 10 seconds, so a client
  that has gone or stopped reading is noticed even when the server does not
  cancel the request; and it stops after 30 minutes. A resumed
  `?after=` sweep that stops early reports a resume point at or after its
  `after`, never the start of the store.
- **Refused publishes are answered without reading the rest of the body**
  (for example a multipart upload over the 16 KiB header limit), and the
  connection is closed.
- **Live-reload streams over HTTP/2 (native TLS) no longer reset between
  heartbeats:** the per-frame write deadline is cleared once the frame is
  sent. With `--write-timeout` set, downloads keep the sendfile path.
- **`--write-timeout` bounds each whole response again,** from the start of
  the request, live-reload streams included; per-route deadlines no longer
  replace it.
- **Downloads honour a single byte range.** A request for several ranges,
  or with a `Range` header over 128 bytes, gets the whole body instead of a
  multipart response the server sizes range by range.
- **IPv6 clients with an interface zone are grouped by /64** for per-client
  limits, like other IPv6 clients.
- **Render uploads are spooled to disk.** A `?render=md` body is received
  into a scratch file charged to the store quota and rendered only once
  complete, under a render slot held through the store write, so
  `--render-concurrency` bounds the memory renders use.
- **A replace through `demiplane receive` no longer frees space a download
  still uses.** `receive` leaves a replaced or removed generation in place,
  and the server's next sweep removes it, keeping it charged until any
  download of it ends.
- **Every accepted TTL expires.** `?ttl=`, `receive --ttl`, the MCP `publish`
  tool and `demiplane publish --ttl` share one parser. Day values must be plain
  decimals and are converted exactly; non-finite, overflowing and sub-second
  values are rejected with `400` (or a client-side error) instead of being
  stored as "never expires". A blank value (only whitespace) is an error
  too; omit the TTL for no expiry.
- **MCP `publish` paths are confined to a publish root.** The tool's `path`
  argument must resolve inside `--publish-root` / `DEMIPLANE_PUBLISH_ROOT`
  (default: the working directory of `demiplane mcp`) and is opened through that
  root. Files on `/proc`, `/sys` and `/dev`, and the token file matched by file
  identity, are refused whether the token came from a file or from
  `DEMIPLANE_TOKEN`. A working directory of `/` disables `path` publishing unless
  `/` is set explicitly.
- **The store is owner-only.** The store root, `data/` with `blobs/`,
  `sources/` and `sites/` in it, the marker files, `.open.lock` and `meta.db`
  with its `-wal` and `-shm` files are created or tightened to `0700`/`0600`
  at startup, and each change is logged.
- **A republish never serves new bytes under the previous metadata.** Each
  write goes to a new file and the metadata row names the file it describes, so
  adding a password, making a page private or changing its expiry takes effect
  together with the new bytes.

### Changed

- **Upgrade note: published pages run sandboxed.** HTML, SVG and XML pages
  run in an opaque origin. A page can no longer use `localStorage`,
  `sessionStorage`, IndexedDB or cookies, `fetch` its own data files or other
  same-origin URLs, frame other pages on the instance, or run workers. Inline,
  classic and module scripts, stylesheets, images and fonts still load. An
  instance that hosts only trusted content can restore the shared origin with
  the new `serve --unsafe-no-sandbox` flag, which logs a warning at startup.
  See DESIGN.md, "Content isolation".
- **Upgrade note: replies are opt-in per artifact (`-tags reply`).**
  `/reply/{slug}` and `/answer/{slug}` now answer `404` unless the artifact's
  latest publish used `?reply=form` (new: the reply form, page unchanged) or
  `?reply=question`. Pages published with an inline reply box before this
  release stay reply-enabled (migrated from their retained render spec), but
  their baked forms carry no submission token, so browser answers to them are
  refused until the page is rebaked (`POST /rerender`) or republished (see
  "rebake reply pages" below); for any other page you collect replies on,
  republish it with `?reply=form`.
  A republish without `?reply` turns replies off. Replying to a
  password-protected page now needs its password.
- **Reply retention is off by default.** Existing replies are kept. Set
  `reply_retention` (for example `90d`) to have an hourly background sweep
  delete older replies.
- **Upgrade note: rebake reply pages.** Pages published with `?reply=question`
  before this release have no submission token, and browser answers to them
  are refused with a message saying the page must be rebuilt. Run
  `POST /rerender` after upgrading, or republish any page without retained
  source. A tool that posts replies from a browser must send the form's
  `token` field (JSON: `"token"`).
- **Upgrade note: upgrade every writer together; roll back from a backup.**
  `serve` and every `receive` (SSH forced commands, cron, CI) must run this
  release. Once this release has opened a store, a 2.0.0 binary cannot open
  it (it stops at start-up with nothing changed), so back up the store
  directory first; returning to 2.0.0 means restoring that backup, and publishes made
  since are not in it. See the deployment guide, "Upgrading".
- **Upgrade note: move the content origin if hostile pages may have been
  published.** Under an affected release, a published page could register a
  service worker on the content origin. The kill switch removes such a
  worker eventually, but not before it has handled the first navigation a
  viewer makes after the upgrade: that worker can read the URL, so a
  capability URL leaks, and with cached credentials it can read the
  response, so a password-protected artifact leaks too. For an instance that
  may have served hostile pages, serve the content plane from a new origin:
  a new port or host name, set with `--content-bind` and `--content-base-url`
  (a same-origin instance gains a separate content origin this way). Links to
  the old origin still reach the old worker first, so send viewers only
  new-origin links, and republish any private artifact whose old-origin URL
  may have been opened after the upgrade. If the origin cannot move, viewers
  must clear site data for the content origin (browser settings, site data
  for that host and port) before opening confidential pages. Instances that
  never published untrusted pages need no action.
- **Upgrade note: markdown input caps.** A `?render=md` document with more
  than 2,097,152 lines, more than 50,000 headings or lists nested deeper than
  64 levels is refused with `413`, as is, with the meta-header on, frontmatter
  of more than 256 fields or 64 KiB, or with a key over 256 bytes or a value
  over 8 KiB.
- **Upgrade note: a series of more than 1,000 artifacts renders without
  its series tree and prev/next row.** The server looks up at most that many
  members instead of listing every artifact on each publish.
- **Upgrade note: each site file and directory counts 4 KiB toward
  `--store-quota`,** and the sites hold at most 100,000 files and directories
  in all. An existing store over either limit keeps serving, but further site
  publishes get `507` until space is freed. There is no HTTP endpoint that
  deletes a site yet: stop the server, remove the site's directory under
  `<store>/data/sites/` (not the `<store>/sites` marker file), and start it
  again (usage is measured at startup).
- **Upgrade note: each artifact counts 4 KiB toward `--store-quota`,** and
  `?filename=` is capped at 255 bytes. Size the quota with that in mind; a
  store already near its quota may refuse new artifacts with `507`.
- **Upgrade note: a client gets at most two live-reload streams per
  artifact** (`--live-max-per-client-slug`). A third tab of the same `?live`
  preview gets `503` until one closes. Behind a reverse proxy set
  `--trusted-proxy` so each user is counted separately.
- **Upgrade note: heading anchors longer than 64 bytes change.** Ids built
  from very long headings are cut at 64 bytes; links to such anchors need
  updating after a rebake. Masthead outline markup changed (`data-sec` names
  a section index); a theme that styles it may need adjusting.
- **Upgrade note: reply hooks.** A hook's process group is killed when its
  shell exits (or after `reply_hook_timeout`, default 5m); a hook that leaves a
  plain background job running must detach it with `systemd-run` or `setsid`.
  A full hook pool refuses new replies with `503` instead of queueing without
  bound (`reply_hook_workers`, `reply_hook_queue`). A failing exec hook's
  output is no longer logged unless `reply_hook_log_output = on`. A webhook
  that answers with a redirect is a failed delivery; configure the final URL.
- **Upgrade note: new limits can refuse requests that used to succeed.** A
  `?render=md` document over a render limit gets `413`; a slow body `408`; a
  busy server `503`; throttled password attempts or reply submissions `429`;
  a full store or reply store `507`. Behind a reverse proxy, set the new
  `--trusted-proxy` to the proxy's addresses so per-client limits see each
  client (from `X-Forwarded-For`), and raise or disable
  `--max-conns-per-client`, which always counts the proxy's connections.
  Without `--trusted-proxy` all clients share the proxy's address.
- **Upgrade note: ambiguous paths are refused.** A path segment that decodes
  to `.` or `..`, or contains an encoded `/` or `\` or a control character,
  gets `400` on both planes; `//`, `/./` and `/../` redirect (`307`) to the
  canonical path.
- **Upgrade note: TTLs must produce a real expiry.** A `ttl` under one
  second or over about 292 years, a non-finite value, a day value with an
  exponent or sign, or a value made only of whitespace is a `400` (or a
  client-side error), where some of these used to be stored as "never
  expires". Omit the TTL for no expiry.
- **Upgrade note: site uploads reject options they ignored.** `?site=` with
  `?private`, `?ttl` or a password is a `400`, as is any publish whose query
  string does not parse.
- **Upgrade note: `--base-url` is the control plane's origin.** With it set,
  a browser write to the control plane must come from exactly that scheme,
  host and port, and content URLs are derived from it (with the content port)
  when `--content-base-url` is unset. Behind a TLS-terminating proxy set
  `--base-url` to the public `https://` origin, and set `--content-base-url`
  when the content plane has its own public host name, or sandboxed pages'
  reply forms are refused as cross-origin. The new `--allowed-hosts` refuses
  any other `Host` with `421`.
- **Upgrade note: `/gallery` needs the token unless `--browse` is set.**
- **Upgrade note: after a replace through `demiplane receive`, the old
  version's bytes stay on disk until the running server's next sweep**
  (`--sweep-interval`, a minute by default), or until a server next starts if
  none is running. This applies to private and password-protected artifacts
  too.
- **Upgrade note: `GET /replies` is paged** the same way: at most 100
  replies a response, with `next` to pass back as `?after=`.
- **The browse index shows a large series as `(100+)`,** or `(N+)` when its
  size was not fully counted, and the landing no longer states the total
  number of artifacts; the browse footer reads "More in the gallery".
- **Upgrade note: bulk rerender cursors changed.** The `next` cursor and
  `?after=` are now an opaque number, not a slug; a slug in `?after=` gets
  `400`, so start a sweep from an earlier version again without `?after=`.
- **Upgrade note: a long bulk rerender streams.** A `POST /rerender` that
  runs over a second answers `200` at once and sends blank lines until its
  report; the JSON report adds `complete`, plus `next` when the sweep stopped
  early, and `?after=<next>` (the returned cursor) resumes a sweep.
- **Upgrade note: bulk rerender reports are capped.** `POST /rerender` lists
  at most 100 results and 100 failures (new `failed_count` and `truncated`
  fields carry the rest); read the counts for totals. A second sweep while one
  runs gets `409`, and disconnecting stops a sweep partway (run it again to
  finish).
- **Upgrade note: a `/list` page can be short.** A page holds at most 200
  artifacts but can hold fewer, or none, while `next` is present; follow
  `next` until it is absent rather than stopping at a short page.
- **Upgrade note: `GET /list` is paged.** A response holds at most 200
  artifacts; a client that reads one response as the whole list must follow
  `next` (`?after=<next>`) until it is absent. `count` now counts the
  response, not the instance. The MCP `list` tool takes the cursor as `after`.
- **Upgrade note: site archives with backslash paths are refused.** Rebuild
  an archive made by a tool that writes `\` separators with `/`.
- **Upgrade note: `demiplane mcp` refuses `path` outside its publish root.** An
  MCP client that publishes files from outside the directory it starts
  `demiplane mcp` in (a scratch or temp directory, another repository) now gets
  an error. Pass `--publish-root <dir>` (or set `DEMIPLANE_PUBLISH_ROOT`) in the
  MCP server config, or send the page as `content` instead of `path`. Pick the
  narrowest directory that holds what you publish: every readable regular file
  under the root can be published.

### Fixed

- **Every generated capability slug is a valid slug.** About one in 32
  started with `-` or `_`, which the named-slug rule rejects, so it could
  not be republished to, named in `?next=` or passed to `receive --slug`.
  Such draws are now discarded and redrawn.
- **Concurrent writes to one slug no longer corrupt it.** A publish racing a
  delete, the expiry sweeper or a rebake could leave a row with no bytes (a
  permanent 500) or a page rendered from an older source. Writers to a slug are
  now serialized, and a rebake that loses to a publish starts over from the new
  source.
- **A rebake reaches browsers that cached the old page.** `GET /{slug}` now sends
  an `ETag` that changes on every write, and a revalidating browser gets the new
  rendering instead of `304`. The `ETag` alone decides conditional requests:
  `If-Modified-Since` never earns a `304` and an `If-Range` date sends the whole
  body, because a date cannot tell apart two writes in one second or a deleted
  page from its recreation. `Last-Modified` is the wall-clock write time and is
  never later than the response.
- **Rendered pages share one left edge.** The document column measure was
  `70ch` per child, and `ch` follows each element's own font-size, so headings,
  the lead and blockquotes got wider centred boxes than the body and hung left
  of it (an h2 by 140px at a 1360px window). The measure is now one
  root-relative token, `--measure: 30rem`, which also brings the body to about
  70 characters per line (it measured 83). Paragraph and list spacing, heading
  spacing and inline code chips now meet WCAG 2.2 SC 1.4.8. Pages bake at
  publish, so run `POST /rerender` after upgrading.

## [2.0.0] - 2026-09-01

### Changed

- **BREAKING: series membership is explicit.** Artifacts join a prev/next
  family only through the `series` publish parameter; slug-prefix families
  (`notes-01`, `notes-02`, …) no longer group automatically. Pages
  published before this change keep working individually, but their colophon
  prev/next links and gallery grouping are gone until each is republished with
  an explicit `?series=`.
- **Markdown render overhaul** — rendered markdown pages get a reworked house
  theme and a set of render-fidelity fixes (#12, #13). Markdown bakes at
  publish, so pages published before the overhaul keep their old rendering
  until rebaked (see source retention, below, for how that stops being true
  going forward).

### Added

- **Release workflow** (`.github/workflows/release.yml`) — pushing a `vX.Y.Z`
  tag builds `linux/amd64`, `linux/arm64`, and `darwin/arm64` binaries, writes a
  `SHA256SUMS` file, records a GitHub build provenance attestation per binary,
  and publishes the GitHub release. Every third-party action is pinned to a full
  commit SHA, permissions are least-privilege per job, and the release toolchain
  reads its Go version from `go.mod` so it cannot drift from CI's. A downloaded
  binary is verifiable with `gh attestation verify` before it is ever run.
- **Source retention and re-rendering** (#14) — a `?render=md` publish now keeps
  the markdown it was given alongside the baked HTML, and `POST /rerender/{slug}`
  (or `POST /rerender` for the whole store) rebakes stored pages with the current
  renderer and theme. Previously the source was discarded at publish, so a
  renderer change could only reach a page if its author still had a copy of the
  markdown: five pages were permanently lost during the 2026-08-30 render
  overhaul. A rebake preserves privacy, view password, TTL, series and the
  original publish date; artifacts with no retained source (anything published
  before this, and every non-markdown upload) are skipped and counted, never
  failed and never rewritten. Sources live in `<store>/sources/` at mode 0700 and
  are removed with the artifact on re-publish, delete, and expiry. The schema
  change is additive (`render_spec`), so an existing store upgrades in place.
  Deliberately not included: version history, diffing, an editor.
### Documentation

- **Install section leads with `go install`** — `go install
  github.com/daisandapex/demiplane/cmd/demiplane@latest` is now the first
  install path in the README, with the trust argument stated plainly: the
  download is verified against `sum.golang.org`, a public transparency log the
  project does not control, so the user gets cryptographic proof the source
  matches what everyone else received and compiles it themselves. Notes that
  `@latest` resolves to a pseudo-version until the first tagged release, and
  records that demiplane will not ship a piped-shell installer.
- **Digest-pinned Docker pulls** — `docs/deployment.md` now documents
  `image@sha256:…` references and how to read a digest, with the reason a
  mutable tag is not a version.

## [1.0.0] - 2026-07-16

**First public release, under the GNU AGPL-3.0.** Everything in this section has
been built, tested, and dogfooded on a private instance hosting real internal
workloads. It consolidates the full v1 feature set — the core HTTP/SSH publishing
API, two-plane origin isolation, the markdown render engine, the optional reply and
TLS modules, and the cross-harness clients — together with the security and design
hardening that gated the release. Subsequent detail entries below are grouped by the
milestone or work item that delivered them.

### Summary of v1.0.0

- **Publishing API** — `POST /publish` (raw body, multipart, and tar/zip/multipart
  multi-file sites), `GET /{slug}`, `GET /list`, `DELETE /{slug}`, `/gallery`,
  `/connect`; friendly `adjective-creature` slugs with named-slug overwrite-in-place.
- **Two-plane origin isolation** — artifact bodies serve from a separate content
  origin so hosted JS cannot read the control plane or drive writes (ADR 0003).
- **Auth** — bearer-token (constant-time) or SSH public-key publish; view auth via
  network, 144-bit capability slugs (`?private=true`), or per-artifact PBKDF2
  passwords set via header.
- **Ephemerality** — `?ttl=` expiry with a background sweeper.
- **Markdown render** (`?render=md`) — house-style themes (light/dark +
  catppuccin/dracula/one-dark), syntax highlighting, heading anchors, frontmatter
  meta-header, per-document colophon.
- **Live-reload** — `?live` SSE wrapper + `demiplane publish --watch`.
- **Optional modules** — inline replies (`reply`) and native TLS (`tls`), both
  compile-time and off in the default binary.
- **Cross-harness clients** — MCP server (`demiplane mcp`), `demiplane publish`
  CLI, `/connect` config page.
- **Licensing** — relicensed to AGPL-3.0-only with a CLA and a commercial-license
  path; SPDX headers on all first-party Go source.
- **Packaging** — static binary on distroless (`Dockerfile`) + CI.
- **Brand** — project mark and favicon. The control plane serves `/favicon.svg`,
  `/favicon.ico`, and `/apple-touch-icon.png`; every rendered page (published
  artifacts, control chrome, and the 404) carries a self-contained data-URI
  favicon so a saved page keeps its tab icon offline. Full icon suite and usage
  in `assets/brand/`.

### Added — design-audit renderer additions (`demiplane-rwj`)

- **Per-document colophon** at the foot of every `?render=md` page, emitted by
  the render template (raw HTML in markdown is escaped by design, so the
  colophon must be renderer-emitted). Two rows: a nav row (`← previous` ·
  position · `next →` · the muted `demiplane` wordmark) and a meta row
  (`published <date>` · size · `all artifacts →` to the instance gallery). The
  metadata is real — the publish timestamp, the rendered document's own byte
  length (which is exactly what the store records), and the gallery link minted
  against the base-URL convention so it stays instance-portable.
- **Series prev/next** computed from the slug's prefix family (the same grouping
  the gallery uses): `dispatch-08` links `dispatch-07`/`dispatch-09` when they
  exist. A singleton prefix shows the meta row alone, no nav. Siblings are the
  already-published artifacts sharing the prefix; a page's colophon is baked at
  publish time, so re-publishing a page refreshes its neighbors.
- **Dependency-free syntax highlighting** (stdlib only) for fenced code in `go`,
  `bash`, `json`, `yaml`, and `python`: a shared, per-language lexer emits
  classed spans (keyword / function / string / comment) colored by four new
  per-theme `--tok-*` tokens (defined for light, dark, catppuccin, dracula, and
  one-dark, mapped to each scheme's canonical syntax palette). Unknown or
  untagged languages render plain, HTML-escaped, as before; every emitted byte
  is escaped, so raw HTML in a code block still cannot inject.
- **Heading anchors** — every rendered heading (`h1`–`h6`) gets a stable, URL-safe
  `id` slugified from its text (lowercased, spaces to hyphens, non-alphanumerics
  stripped, collisions deduped with a numeric suffix; a punctuation-only heading
  falls back to `section`). A quiet hover-`#` permalink is appended to each
  heading, revealed on hover or keyboard focus and reserving its own space so it
  shifts no layout, with a `Permalink to <heading>` `aria-label` for assistive
  tech and its transition disabled under `prefers-reduced-motion`. The scroll
  root gains `scroll-padding-top` so navigating to a `#anchor` clears the sticky
  masthead instead of hiding the target beneath it. The slug alphabet is
  `[a-z0-9-]` and the aria-label is attribute-escaped, so attacker-controlled
  heading text cannot inject into either sink.

### Added — native TLS module (`demiplane-4qq`)

- **In-binary TLS termination for both planes** as an opt-in module (build tag
  `tls`; ADR 0004). Off by default twice over: the default build contains none
  of it, and a tls-tagged binary still serves plain HTTP until the config file
  sets `tls = on`. Three certificate sources, inferred from the keys set and
  refusing ambiguous mixes at startup:
  - **Self-signed (zero-config default):** ECDSA P-256 cert generated on first
    TLS start, SANs derived from the bind addresses (or `tls_hosts = a,b,…`),
    persisted under `<store>/modules/tls/` (key `0600`), reused across
    restarts, auto-regenerated near expiry or on host-set change; SHA-256
    fingerprint logged for pinning.
  - **ACME / Let's Encrypt:** `tls_acme_domains` (+ optional `tls_acme_email`,
    `tls_acme_ca` directory override) via `golang.org/x/crypto/acme/autocert`,
    TLS-ALPN-01 challenge, cache in `<store>/modules/tls/acme/`.
  - **BYO:** `tls_cert` + `tls_key` PEM paths.

  Core stays certificate-ignorant: the module installs a cmd-side listener
  hook (`moduleTLS`) and hands back a `*tls.Config`; minted URLs flip to
  `https://` via the existing `r.TLS` scheme derivation. New dependency:
  `golang.org/x/crypto` v0.53.0 (first-party Go project; used by tls-tagged
  builds only).

### Security — Shannon pass-1 low findings

- **Clickjacking (`demiplane-t0j`):** every response on both planes now sends
  `X-Frame-Options: SAMEORIGIN` and a `frame-ancestors 'self'` CSP.
  Same-origin framing (the `?live` preview wrapper) keeps working; handlers
  that set their own CSP (scriptable non-HTML types, the live wrapper)
  re-state the directive since `frame-ancestors` has no `default-src`
  fallback.
- **Host-header reflection (`demiplane-6y9`):** the base URL derived from the
  request `Host` (llms.txt, publish responses, minted links when `--base-url`
  is unset) is now sanitized through a host-grammar character allowlist, so a
  crafted Host header cannot inject content into text/plain or HTML surfaces.

### Added — reply-event hook + forward flow (`demiplane-tb7`)

- **Reply-event hook.** When a reply is durably recorded (control-plane
  `POST /reply/{slug}` or content-plane `POST /answer/{slug}`), the reply module
  fires a configurable action — an exec'd command (`/bin/sh -c`, reply JSON on
  stdin + `DEMIPLANE_REPLY_{ID,SLUG,KIND,BODY}` env vars) and/or a webhook POST
  of the same JSON. Config keys `reply_hook_exec` / `reply_hook_url` in the
  existing config file (`-tags reply` builds only; malformed values are hard
  startup errors). Dispatch is fire-and-forget strictly after the insert: a
  failing or slow hook is logged and can never fail, delay, or roll back the
  reply write. Lets an external grader react to answers with zero polling.
- **`?next=<slug>` forward flow.** Publish a lesson with
  `?render=md&reply=question&next=<sequel>` and, after a recorded answer, the
  confirmation page carries the student to the sequel once it is published:
  an honest "your next lesson is being prepared" note plus a JS-free
  meta-refresh into the new `GET /answer/{slug}/next?to=<next>` wait endpoint,
  which holds (self-refreshing, back link always present, never claiming
  readiness) until the target slug resolves publicly, then redirects to it.
  The sequel need not exist at publish/answer time; `?next` requires `?reply`,
  must be slug-shaped, and must differ from `?slug=`. Without `?next` the
  confirmation is unchanged.
- **Module config seam (cmd).** Build-tagged module wiring files can register
  config-file keys + a validating applier (`registerModuleConfig`), so
  module-owned keys are recognized exactly when the module is compiled in and
  fail loudly otherwise. See `docs/MODULES.md`.

### Added — first-class inline reply box for Q&A (`demiplane-qrc`)

- **`?reply=question` publish mode.** `POST /publish?render=md&reply=question`
  bakes a first-class, server-rendered reply box into the rendered page — no
  per-page hand-rolled `<form>`. It requires `?render=md` and is rejected with
  `?private` (a private capability page can't collect replies).
- **Same-origin, JS-free submission.** The baked form has no `action`, so it
  posts to the page's **own URL** on the content plane — same-origin by
  construction (no cross-origin/mixed-content failure) and correct once TLS lands.
  A single free-text answer is recorded as a `comment`-kind reply, read back via
  the existing `GET /replies?slug=<slug>` (bearer auth).
- **Honest confirmation by construction.** The content plane's new
  `POST /{slug}` handler renders "✓ Recorded" **only after** the answer is
  durably stored; every failure path renders an explicit "Not recorded" page with
  the matching status. With no client JavaScript, the browser navigates to the
  server's real response — a form post cannot claim success on failure. This
  replaces the dishonest per-page `setTimeout` pattern that lost a student's
  answer while displaying success.
- **`module.ContentRouteModule`** — a new optional module interface lets a
  `RouteModule` mount handlers on the content origin (not just the control plane),
  so a module's same-origin submit path exists wherever artifact bodies are
  served. The reply module implements it. See ADR 0002 (2026-07-14 addendum).

### Added — cross-harness release (v1.2)

The v1.2 layer makes demiplane publish from whatever coding agent, editor, or
shell you already use — native where a protocol exists, one shell command
everywhere else — and adds the iterate-in-place loop.

- **`demiplane mcp` — stdio MCP server.** A Model Context Protocol server over
  JSON-RPC 2.0 on stdin/stdout, so any MCP-capable client gets native `publish` /
  `list` / `delete` / `get` tools — Claude Code is the one we test. It is a thin
  HTTP client of a running instance's control plane (no store/filesystem
  coupling), stdlib-only, and ships in the core build. Config: `demiplane mcp
  --url <control> --content-url <content> --token-file <path>` (or
  `DEMIPLANE_URL` / `DEMIPLANE_CONTENT_URL` / `DEMIPLANE_TOKEN`). The token is
  read from the file/env only — never argv, never echoed; stdout carries only
  JSON-RPC.
- **`demiplane publish <file>` — client CLI.** The universal fallback for any
  harness that can shell out. Posts a file (or stdin) to a running instance,
  prints the URL, and best-effort copies it to the clipboard
  (`wl-copy`/`xclip`/`xsel`/`pbcopy`/`clip.exe`). `--watch` re-publishes to a
  stable slug on every save (mtime poll, ~2/sec cap), `--open` launches a
  browser. Flags: `--url`, `--token-file`, `--slug`, `--private`, `--ttl`,
  `--render`, `--filename`, `--watch`, `--open`; the view password comes from
  `DEMIPLANE_PASSWORD` only (never a flag — argv is world-readable). Note: flags
  must precede the positional file (`publish --url X file.html`).
- **`GET /connect` — onboarding page.** A house-style, copy-paste config page
  served by the running instance, templated with its own base URL, with a block
  per harness (MCP stanza, Claude Code capture hook, bare curl, `demiplane
  publish`, Aider `/run`). The bearer token is shown only as a placeholder path
  plus a local one-liner to read it — the live token is never rendered. Linked
  from the landing page.
- **SSE live-reload (`?live`).** Append `?live` to any artifact view to get the
  artifact wrapped in a minimal first-party shell that opens an SSE stream at
  `GET /_events/{slug}` and reloads the tab when the slug is re-published. The
  wrapper is opt-in and does not mutate stored bytes (plain `GET /{slug}` is
  byte-identical); it carries its own strict CSP with a per-response nonce and
  iframes the artifact same-origin. Subscribers are capped per-slug and globally
  to bound FD/memory. Pair with `demiplane publish --watch` for an
  edit-save-see loop.
- **Multi-file site publish (`POST /publish?site=<name>`).** Ingests a tar, zip,
  or multipart bundle and serves the whole tree: `GET /{name}/` returns
  `index.html`, `GET /{name}/{path}` returns each asset with the right
  content-type, so relative links resolve. Beats a single-file host — a whole
  static site lives at one slug.
- **`GET /gallery` — artifact index.** A searchable, house-style card index of
  non-private artifacts (title/slug, type badge, size, published + expiry,
  copy-URL, password lock icon) with a dependency-free client-side filter.
  Private and expired artifacts are filtered server-side by the same `List`
  guard.

### Security — v1.2 new-surface hardening

Adversarial review of the new surfaces (MCP stdio, SSE, multi-file archive
ingest, `/connect`, publish CLI) found and fixed three issues before release:

- **Archive-bomb defense extended to tar (high).** `POST /publish?site=`'s tar
  path had no decompressed-byte budget, so a PAX/GNU **sparse** tar (regular-file
  typeflag) could expand from a ~10 KB upload to attacker-declared gigabytes of
  zero-fill, bypassing both `--max-upload` and the decompression cap (which was
  wired only into the zip path). The tar extractor now enforces the same total
  decompressed-bytes budget as zip; an oversize expansion is rejected with `413`.
- **MCP `publish` refuses the token file (token-exfil guard).** The MCP `publish`
  tool's `path` argument is model-chosen, so a prompt-injected model could point
  it at the bearer-token file and publish the token to a world-readable slug. The
  tool now refuses to read-and-publish the configured token file (compared on the
  fully symlink-resolved absolute path) and refuses non-regular files (symlinks,
  devices), so `path` cannot follow a link to a secret. The MCP HTTP client also
  no longer follows redirects (the control plane never legitimately 3xx's),
  closing any redirect-based header forwarding.
- **`demiplane publish` no longer follows redirects (password-leak guard).** The
  CLI used the default HTTP client, which follows redirects; Go strips the
  `Authorization` header across hosts but **not** the custom
  `X-Demiplane-Password` header, so a malicious/compromised target could redirect
  the request and capture the view password. The CLI client now refuses
  redirects (the publish endpoint answers `201`/`4xx`, never `3xx`).

### Security — pre-release hardening (Shannon white-box pentest)
- **Stored-XSS hardening on served artifacts.** Every response now sends
  `X-Content-Type-Options: nosniff`, and SVG/XML/XHTML artifacts are served with
  `Content-Security-Policy: script-src 'none'` so a published image/document can't
  execute embedded `<script>` at the instance origin. (Executable HTML *pages*
  remain a supported feature — demiplane is a page host.)
- **`GET /list` no longer returns private artifacts.** A private artifact's
  capability slug is its secret; `List` now filters `private` rows so the slug is
  never enumerable via the API (matching the already-non-private `--browse` page).
- **Bounded upload default.** `--max-upload` now defaults to **100 MiB** on both
  `serve` and `receive` (was unlimited), so a fresh install is not a trivial
  disk-fill target. `--max-upload=0` restores unbounded streaming as an explicit
  opt-out. Closes the manual-audit finding that the SSH forced-command and the
  no-token HTTP path had no default cap.
- **Markdown open-redirect fixed.** `safeURL()` treated protocol-relative URLs
  (`//host`) as safe same-origin relative refs, so `[text](//attacker.com)` in a
  published document produced an off-site link. Such URLs are now rejected.
- **Reply form no longer leaks private artifacts.** `GET /reply/{slug}` used a
  bare existence check that ignored `private`, so a private capability slug's
  existence was observable via a 200-vs-404 oracle. It now uses `HasPublic` — the
  same `private`+expiry filter as `GET /list` — so private and nonexistent slugs
  are indistinguishable (both `404`).
- **Private is sticky across re-publish.** In no-auth mode, `POST /publish?slug=`
  a known capability slug could upsert `private=0` and surface the artifact in
  `GET /list`. The `private` bit is now latched (`MAX(old, new)`) on same-slug
  overwrite; changing privacy requires an explicit delete + re-publish.

### Added — Modules & artifacts publisher (epic `rox`)
- **Module extension seam** (`internal/module`): a compile-time, Caddy-style
  registry (interface + `register()` from `init()`, build-tag inclusion — no
  runtime `plugin`) so capabilities are optional modules, not core bloat. The
  default core stays tiny: publish / serve / auth / TTL / slugs.
  See [ADR 0001](./docs/adr/0001-module-extension-pattern.md) and
  [docs/MODULES.md](./docs/MODULES.md).
  The seam offers one capability shape, `RouteModule` (HTTP-route modules); a
  `PublishTransform` body-rewrite shape was prototyped with markdown render but
  removed — render's theming is a shared instance setting, so it stays in core
  (see the ADR 0001 update). The inline-reply module is the seam's first module.
- **Artifact-capture hook** (`companion/capture-hook/`): a client-side Claude
  Code `PostToolUse` hook (and publish CLI) that POSTs self-contained HTML
  artifacts to your instance, so agent-generated pages land on the mesh, not a
  public host. A companion, not a server module — the API is unchanged.
- **Inline-reply module** (`internal/modules/reply`, opt-in build tag `reply`):
  viewers respond to a page (Approve / Defer / free-text) and an agent lists/acks
  replies. Submit is mesh-only; reading is bearer-gated (no new auth primitive);
  storage is module-owned and isolated. New endpoints `GET|POST /reply/{slug}`,
  `GET /replies`, `POST /replies/{id}/ack`. Core's API/security model unchanged.
  See [ADR 0002](./docs/adr/0002-inline-reply-module.md).

### Changed — Licensing
- **Relicensed from MIT to GNU AGPL-3.0-only** ahead of the public release.
  demiplane stays fully open source and self-hostable for free; the AGPL's
  network copyleft means cloud resellers must share their modifications or obtain
  a commercial license. (Sole-author relicense — no external contributors.)
- Added a **Contributor License Agreement** ([CLA.md](./CLA.md)) and a
  **commercial / dual-license** note ([COMMERCIAL-LICENSE.md](./COMMERCIAL-LICENSE.md));
  `CONTRIBUTING.md` now requires accepting the CLA.

### Changed — rendering house style (demiplane-7fp)
- `?render=md` now renders user content in demiplane's **house style by default**
  — the same design tokens and typography as the `/docs` pages — instead of the
  old bare system-sans stylesheet. The stylesheet lives in one place
  (`internal/theme`), shared by the page chrome and the markdown renderer, so the
  two can't drift.
- New `serve --theme light|dark` (built-in themes; default is the house style)
  and `serve --css <file>` (a custom stylesheet that replaces the built-in theme,
  for self-hosted branding).
- `--theme` skins the **whole instance**: the chrome (`/`, `/docs`, landing) and
  rendered `?render=md` content honor it together, so `--theme dark` darkens
  everything as one setting. (`--css` rebrands only user content; the chrome
  stays on a built-in theme.)

### Added — rendered-page chrome + config file (demiplane-umw)
- `?render=md` pages now get a **sticky title header** (the document's first
  `# H1`, lifted into a masthead — no duplicate giant title; falls back to the
  slug), an in-header **client-side light/dark toggle** (ships both token sets
  inline, persists the choice in `localStorage`, initializes from
  localStorage → server `--theme`/config → OS `prefers-color-scheme`, respects
  `prefers-reduced-motion`), and a **"Generated by demiplane"** vanity footer
  linking the repo by default.
- New `serve` flags `--header`/`--footer` (default on) and `--footer-link <url>`.
- New **config file** at `${XDG_CONFIG_HOME:-~/.config}/demiplane/config`
  (stdlib `key = value` parser, no new dependency). Keys: `footer`, `footer_link`,
  `theme`, `header`, `meta_header`. **Precedence: CLI flag > config file > built-in default.**
  Missing file → all-defaults; malformed file/unknown key → clear startup error.

### Added — frontmatter meta-header (demiplane-7fp)
- `?render=md` now lifts a leading **YAML frontmatter** block (`--- … ---` of
  minimal `key: value` lines — stdlib only, no YAML dependency) into a styled
  **meta-header** below the masthead title, replacing the old bold run-on first
  line. A `date`/`published` field renders as a localized timestamp
  (`YYYY-MM-DD · HH:MM UTC · HH:MM <localtz>`; the UTC text is server-rendered and
  a tiny client script appends the viewer's local time + zone). Every other field
  becomes its own labeled line (title-cased label, body-ink value). Frontmatter is
  consumed by the header, never shown in the body.
- New `serve --meta-header` (default on) / config `meta_header = on|off`. Off
  strips frontmatter without rendering a header. A document with no frontmatter is
  unaffected. A leading `---` with no closing fence stays a horizontal rule.
- Fixed: the masthead title clipped letter descenders (the "g" in a title like
  "Overnight"); the `.doctitle` now has a roomier line-height and bottom padding.

### Changed — refined-editorial theme pass (demiplane-7fp)
- Rebuilt both base render themes (light + dark) to a refined-editorial standard.
  Tokens are now **OKLCH**, every neutral warm-tinted toward the parchment hue,
  no pure black or white anywhere; the dark base is a warm dark parchment rather
  than near-black. Editorial type scale (17px base, ~1.25 modular headings,
  capped ~68ch measure, varied rhythm). The token system is documented in
  [DESIGN.md](./DESIGN.md).
- Rendered-page chrome refined: editorial masthead (small-caps kicker + display
  serif title + hairline) with comfortable lead-in; the first paragraph is set
  as a quiet lead so a dense metadata line reads as a subtitle; a crisp inline
  **sun/moon SVG** replaces the toggle glyph; a thin single-line footer; refined
  code blocks, tables (horizontal rules + subtle zebra), blockquotes, and links.
  Micro-interactions are ease-out-expo and never touch layout; all respect
  `prefers-reduced-motion`.

### Added — M1 (core)
- Flat-file content store + pure-Go SQLite metadata (`modernc.org/sqlite`),
  with an `owner` column from day one for additive multi-user later.
- `POST /publish` streams the body to disk (never buffered); accepts raw bodies
  and `multipart/form-data`; infers content-type by extension then sniffing,
  serving known web types inline and others as attachments.
- Friendly word-based slugs (`adjective-adjective-noun`) by default; named slugs
  via `?slug=` overwrite in place.
- `GET /{slug}` serving via `http.ServeContent`; `serve --bind/--store/--base-url`.

### Added — M2 (HTTP API)
- `DELETE /{slug}` and `GET /list` (JSON, owner-scoped).
- Bearer-token auth on publish/delete/list (`--token-file` / `DEMIPLANE_TOKEN`);
  `GET /{slug}` stays open (view-auth is a separate layer).

### Added — M3 (internal-first)
- `--bind` defaults to loopback; wildcard binds log a warning.
- Per-artifact privacy (`?private=true` → 144-bit capability slug).
- Optional view password (PBKDF2-HMAC-SHA256), set via `X-Demiplane-Password`,
  read via HTTP Basic.
- TTL/expiry (`?ttl=30m|2h|7d`) with lazy 404 + a background sweeper.

### Added — M4 (SSH transport)
- `demiplane receive` subcommand: pubkey-auth publish over the host's `sshd`
  using an `authorized_keys` forced command — single-file pipe and tar-stream
  directory sync into the same store. No new dependency.

### Added — M5 (extras)
- Opt-in markdown→HTML render on publish (`?render=md`); dependency-free
  CommonMark subset, HTML-escaped with a link-scheme allowlist.
- Opt-in browse page (`serve --browse`) listing non-private artifacts at `GET /`.

### Added — M6 (packaging + hardening)
- Dockerfile (static binary on distroless), CI workflow, `version` subcommand.
- `--max-upload` (returns `413`), `--write-timeout`, `--idle-timeout`.

### Security
- Reject the incoherent `private` + named-slug combination (store-level guard,
  covers HTTP and SSH ingest).
- View password moved out of the URL query (logged surface) to a header.
- TOCTOU-safe TTL reaping (predicate-guarded deletes) so a re-published artifact
  is never destroyed by the sweeper.
- Markdown renderer escapes all source and allowlists link schemes
  (`javascript:`/`data:`/`vbscript:` and unknown schemes dropped).
- Error logs redact slugs (a capability slug is a secret).
- Bearer-token comparison is constant-time **and** length-hiding (HMAC).

### Fixed / Hardened — pre-public review
- Docker named volume now nonroot-owned so the `docker run -v` quickstart works.
- `?render=md` returns `413` (not `500`) when `--max-upload` is exceeded.
- SSH `receive` honors `--max-upload` (fails loudly past the cap, no partial store).
- Container base images pinned by `@sha256` digest (Dockerfile + CI).
- Store directories created `0700` so other local users can't read blobs off disk.
- SSH docs corrected: removed the non-functional `--` flag passthrough (demiplane
  ignores `SSH_ORIGINAL_COMMAND` by design); `authorized_keys` example uses `restrict`.
