# demiplane design system

demiplane's human-facing HTML, the `/` landing, the `/docs` pages, and rendered
`?render=md` documents, all draw from one stylesheet defined in
`internal/theme/theme.go`. There is no second source of truth: the page chrome
composes `theme.Tokens()` + `theme.Content()` with its own chrome-only classes;
the markdown renderer embeds `theme.CSS()` / `theme.ToggleCSS()` whole. Change a
token once, every surface re-skins.

## Direction

Refined editorial, in an essay voice. The reference is a well-set private
document, calm, trustworthy, read late in the evening on a wide monitor, not a
SaaS dashboard. The identity is warm paper, one serif family (Charter/Iowan)
for body and headings alike, mono code on a light bordered slab, and an
oxblood accent spent on links and nothing else. Hierarchy is size-led; section
structure is carried by hairlines, never by color. This system was chosen by
design competition (entry j, 2026-08-30) and supersedes the earlier
cool-neutral "rojo" palette.

## Color

OKLCH throughout. Every neutral is tinted warm (h ~ 75 to 91, chroma ~ 0.004
to 0.025), so the paper reads as stock, not screen grey. There is no pure black
or white anywhere: the card surface stops just short of white (`--panel`
L 0.995) and the darkest ink keeps a trace of warmth. The accent (an oxblood
red, h ~ 23 to 29, a dusty terracotta on the dark face) is spent on links and
the keyboard focus ring only; headings, list markers, the kicker, and inline
code are all ink or muted. Link hover deepens the underline (1px to 2px, tint
to full accent), not the color, so there is no second accent token.

Color strategy: **Restrained** (warm tinted neutrals + one accent, links only).

### Token map

| Token | Light | Dark | Role |
|---|---|---|---|
| `--bg` | `oklch(0.982 0.004 91)` | `oklch(0.228 0.006 78)` | page surface (warm paper `#faf9f6` / warm near-black `#1e1c19`) |
| `--panel` | `oklch(0.995 0.002 95)` | `oklch(0.261 0.008 85)` | raised card surface (table cards, chrome cards) |
| `--ink` | `oklch(0.237 0.009 75)` | `oklch(0.923 0.009 85)` | body text (also the blockquote's serif-italic ink) |
| `--muted` | `oklch(0.430 0.011 78)` | `oklch(0.745 0.012 77)` | secondary text, byline, table headers, list markers |
| `--line` | `oklch(0.910 0.013 87)` | `oklch(0.330 0.010 81)` | hairline rules, borders, table row rules |
| `--line-strong` | `oklch(0.714 0.018 85)` | `oklch(0.461 0.015 87)` | emphatic rules: table header underline, `hr`, toggle hover ring |
| `--accent` | `oklch(0.445 0.122 23)` | `oklch(0.749 0.082 29)` | links and the focus ring, nothing else |
| `--code-bg` | `oklch(0.953 0.010 87)` | `oklch(0.262 0.008 85)` | code surface, inline and block (follows its face); in dark a step above `--bg`, so the slab lifts off the page |
| `--code-ink` | `oklch(0.261 0.010 89)` | `oklch(0.871 0.015 85)` | code text, inline and block |
| `--code-border` | `oklch(0.883 0.017 88)` | `oklch(0.362 0.015 85)` | code block hairline border (inline chips are background only) |
| `--navy` | `oklch(0.420 0.085 245)` | `oklch(0.620 0.095 240)` | secondary accent (landing GET badge) |
| `--danger` | `oklch(0.520 0.165 28)` | `oklch(0.700 0.150 28)` | destructive accent (DELETE badge) |
| `--on-badge` | `oklch(0.985 0.004 95)` | `oklch(0.985 0.004 95)` | method-badge label text, every palette |
| `--info-bg/-line/-ink` | blue-grey trio | blue-grey trio | landing ethos pill |
| `--shadow` | `oklch(0.250 0.010 80 / 0.14)` | `oklch(0.100 0.008 80 / 0.45)` | scrolled-masthead shadow |
| `--sel` | `oklch(0.909 0.025 43)` | `oklch(0.369 0.023 48)` | text selection |
| `--tok-key/-fn/-str/-com` | dark inks for the light slab | light inks for the dark slab | code syntax accents, AA on `--code-bg` per face |

The light values are exact OKLCH conversions of the approved competition sheet
(`#faf9f6` paper, `#8b3232` links, `#f2efe8` code slab, white cards), except
`--panel`, which stops at L 0.995 instead of pure `#ffffff` to honor the
no-pure-white refusal. Retired with the port: `--accent-hover`,
`--accent-soft`, `--code-inline`, `--code-line`, `--line-soft`, `--zebra`.

Body and secondary text clear WCAG AA against their surface in both faces
(measured, default palette): light `--ink` 15.8 : 1, `--muted` 7.7 : 1,
`--accent` 7.7 : 1 on `--bg`; dark `--ink` 13.5 : 1, `--muted` 7.5 : 1,
`--accent` 7.4 : 1 on `--bg`. All clear 4.5 : 1 on `--panel` too (worst pair:
dark `--muted` on `--panel`, 6.8 : 1). Code ink and all four `--tok-*` syntax
accents clear AA on `--code-bg` in both faces. These are pinned by
`TestWarmPaletteContrastAA` and `TestCodeTokenContrastAA`.

### Theme contract (light/dark)

Every rendered document that is not under a pinned theme or an operator `--css`
override ships one stylesheet in three blocks, in this order:

1. the complete light palette on bare `:root` (the default),
2. the dark palette under `@media (prefers-color-scheme: dark)`, guarded as
   `:root:not([data-theme="light"])` so an explicit light choice wins, and
3. the dark palette again under `:root[data-theme="dark"]` so an explicit dark
   choice wins in both directions.

An explicit `data-theme` on `<html>` always beats the OS preference; with no
attribute, the media query decides, so OS dark works with JavaScript disabled.
The server `--theme` stamps the attribute; the reader's toggle rewrites it.
The operator pages (home, `/docs`, `/help`, `/connect`, `/gallery`, the status
pages) ship the same contract and the same toggle as published pages, so a
reader going from a dark artifact to "All artifacts" stays dark. On the control
plane, which is a real origin, the choice persists to localStorage and a head
script re-applies it before paint. A published page runs in an opaque origin
(Content isolation below), where localStorage throws `SecurityError`
(checked in headless Chromium against a sandboxed artifact), so there the
toggle flips the open page only and the OS or server theme decides the next
one. `theme.ToggleCSS()` emits the contract and
`TestToggleCSSThreeBlockContract` pins it.

### Named themes

Beyond the default palette (its `light` and `dark` faces), demiplane
ships three **named dark themes** for the developer audience that knows them by
name: **`catppuccin`** (the Mocha flavor), **`dracula`**, and **`one-dark`**. Select
one instance-wide with `--theme <name>` or `theme = <name>` in the config file; it
re-skins every human surface (chrome, landing, `/docs`, `?render=md`) exactly like
the built-in pair, since they are palette swaps over the one token system, not
separate designs.

Each is a faithful OKLCH conversion of the scheme's published sRGB spec, mapped onto
the token contract above: `background → --bg`, a raised surface `→ --panel`,
`foreground → --ink`, a brightened comment/subtext `→ --muted`, `selection → --sel`,
and the scheme's signature accent `→ --accent` (dracula purple, catppuccin mauve,
one-dark blue). They obey the house rules — neutrals carry each scheme's own hue
(dracula ~h278, catppuccin ~h283, one-dark ~h264, so nothing reads as flat grey),
chroma stays modest near the lightness extremes, and none use pure `#000`/`#fff`
(their specs don't either: dracula base `#282a36`, catppuccin base `#1e1e2e`,
one-dark base `#282c34`).

| Theme | `--bg` | `--ink` | `--muted` | `--accent` (signature) |
|---|---|---|---|---|
| `catppuccin` | `oklch(0.243 0.030 284)` | `oklch(0.879 0.043 272)` | `oklch(0.751 0.040 274)` | `oklch(0.787 0.119 305)` mauve |
| `dracula` | `oklch(0.288 0.022 278)` | `oklch(0.977 0.008 107)` | `oklch(0.715 0.050 272)` | `oklch(0.742 0.149 302)` purple |
| `one-dark` | `oklch(0.293 0.016 264)` | `oklch(0.903 0.010 261)` | `oklch(0.705 0.022 264)` | `oklch(0.730 0.121 245)` blue |

**Accessibility.** `--ink` and `--muted` clear WCAG AA (≥ 4.5 : 1) against **both**
`--bg` and `--panel` (dark zebra rows set body text on `--panel`); `--accent` links clear
AA on `--bg`. Faithful values that failed the bar were lifted, and the deviations are:

- **dracula `--muted`** — the spec comment `#6272a4` is only 3.03 : 1 on `--bg`;
  brightened to a lighter blue-purple that clears AA on both surfaces.
- **one-dark `--ink`** — uses the scheme's **bright** foreground `#dcdfe4` rather than
  the dim `#abb2bf` (which leaves no headroom for a subordinate but AA-passing
  `--muted`); `--muted` is a brightened comment (`#5c6370` alone is 2.32 : 1).
- **catppuccin `--muted`** maps to subtext0 `#a6adc8`, which already passes — no lift.

The method-badge surfaces `--navy` / `--danger` (white label text) are darkened enough
to clear the 3 : 1 UI bar for that white text.

**Interaction with the light/dark toggle.** A named theme **overrides and pins** its
palette: it is dark-only, has no light counterpart, so a `?render=md` page rendered
under it drops the reader's sun/moon light↔dark toggle (there is nothing to flip to)
and emits its single `:root` palette instead of the three-block contract sheet. The
toggle exists **only** for the default palette, where `--theme light|dark` sets the
initial side of a switch the reader still controls. This is the `theme.Pinned(name)`
seam: pinned themes (`catppuccin`/`dracula`/`one-dark`) suppress the toggle; the
unpinned pair keeps it.

## Typography

System stacks only, no web fonts, no CDN.

- `--serif`: Charter, Bitstream Charter, Charis SIL, Iowan Old Style,
  Palatino Linotype, Palatino, P052, URW Palladio L, Georgia, Cambria, Noto
  Serif (body AND headings; one family carries the whole document). Charis SIL
  is Charter under its Linux name; P052 and URW Palladio L are the URW
  Palatinos most Debian and Ubuntu desktops carry for printing; Noto Serif is
  Android's serif.
- `font-size-adjust: .48` on body sets whichever serif lands at Charter's
  x-height, so a machine with none of the named faces (DejaVu Serif) keeps the
  page's measure and colour instead of setting it wide. Code, tables, form
  controls and the sans UI text reset it to `none`.
- `--sans`: system UI sans (byline/meta, table text, colophon)
- `--mono`: system mono (code)

Essay scale: 16px (`1rem`) serif body on 1.6 leading. Paragraphs and lists owe
`1.5em` below, so the baseline step across a paragraph break is about 1.9x the
line step (WCAG 2.2 SC 1.4.8 asks for at least 1.5x). Headings sit at least
twice as far from the block above as from their own text. Inline code is a
background-only chip (no border) at `line-height:1`, so it never widens a line. A short span (40 escaped bytes or fewer on one line: a port, a flag, `?slug=reports`,
`seat-land --check-authors`; 40 monospace characters still fit a phone's line) is
tagged `class="nw"` and never splits; the renderer tags it, and the operator pages get the
same tagging (`render.KeepShortCode`). A longer one breaks only where it must. On a phone a
table cell lets any code break, so the cell can shrink. Headings are size-led at weight 700:
h1 `2.125rem` (tracked `-0.02em`), h2 `1.5rem` over a hairline top rule (the
section scan aid), h3 `1.1875rem`, then `1.0625rem`/`1rem`/`.875rem` down to
h6. The lead paragraph bumps to `1.0625rem`. Blockquotes are unboxed italic at
`1.1875rem`. Tables drop to a `.875rem` sans with tabular numerals; table
headers are muted weight-600, never uppercase. Uppercase tracking is not used
anywhere: the byline is normal case, so author names keep their case. `text-wrap: pretty` on paragraphs,
`balance` on headings.

The measure is one root-relative token, `--measure: 33rem`, applied to every
block child of the document column, so headings, paragraphs, lists, the lead and
blockquotes share one left edge. The frame (`theme.FrameCSS()`) puts that column
and a `--wide: 46rem` frame on one centred axis, on published and operator pages
alike: tables and code break out to the frame, and the page title and byline
hold to the measure, so a page has one left and one right edge for its text,
its title, its byline rule and its toggle. It holds the body near 65 characters per line
(SC 1.4.8 caps it at 80). A `ch` measure is avoided on purpose: `ch` resolves
against each element's own font-size, so a larger heading got a wider centred
box and hung left of the text. Tables and code blocks break out
to the frame at every width (on a page under `--css`, to a centred
`min(62rem, 100%)` over `60rem`) and scroll horizontally inside their own
container (`.table-wrap` / `pre`), so the body never scrolls sideways. A code block that
scrolls shows a soft shadow at its hidden edge, which a covering gradient hides
again once that edge is reached (`background-attachment: local` over `scroll`)
(`overflow-x: clip` on `html, body` backstops it). Below `40rem` a table of four
or more columns sets each row as a labelled stack: every body cell carries its
header in `data-label`, the header row is visually hidden, and each cell shows
its label beside its value, so a phone reader never scrolls sideways to reach
the last column.

## Rendered-document chrome (`?render=md`)

- **Masthead**: the page opens with the document. Title, then one byline line,
  then one hairline, then the text. There is no top bar and no wordmark: once
  the bar held only the toggle it only pushed the title down, so it is gone and
  the toggle sits at the end of the byline, level with its first line when the
  byline wraps. The byline is a small muted sans in
  normal case (author names keep their case, so there is no uppercase tracking
  anywhere on a published page) carrying the frontmatter facts in a fixed
  order: the date (the frontmatter `date`/`published`, else the publish date,
  in ink weight 600), `by` the author, the status, the tags, any other field as
  `Key: value`, and the read time from two minutes. The frontmatter `title` is
  never repeated; with no H1 it becomes the title. Under a `--css` sheet the
  page keeps the plain sticky masthead (`.docbar`, the title in one row) and
  the meta-header block.
- **Margin navs** (`.mnav`): a series list (left) on a series of two or more,
  and a contents list (right) from three h2 sections; a two-section lesson
  reads top to bottom. From `78.5rem` (both gutters can hold a `13rem` list)
  they sit sticky in the gutters beside the frame, top-aligned with the text,
  the contents marking the section in view and showing only its h3s. Below
  that they fold to one-line disclosures under the byline ("Series 2 of 3",
  "Contents"), closed, so a phone opens on the text. Neither has stored state.
  The series list names the current page by its title and each sibling by what
  sets its slug apart (the family's shared prefix dropped, hyphens read as
  spaces, a bare number as "Part N"): the store keeps no sibling titles.
- **Title**: every rendered document emits a `<head><title>` (the lifted H1, or
  the slug fallback, reduced to plain text), so a shared link, tab, or bookmark is
  never blank.
- **Lead**: the renderer tags the first body paragraph `class="lead"` (bumped
  to `1.0625rem`, full ink; the size alone carries the dek). The explicit class
  survives a frontmatter meta-header injected ahead of the
  body, where a positional `:first-child` selector would silently stop matching.
- **Theme toggle**: an inline sun / moon SVG (currentColor, no icon dep). Click
  flips `data-theme` on `<html>` and tries to persist to `localStorage`; the matching
  icon shows via CSS (keyed off `data-theme` plus the same guarded media query,
  so it is right even before any choice is stored). Initial state: a stored
  localStorage choice is re-applied before paint (no FOUC); otherwise the
  server `--theme` stamp or, with neither, the stylesheet's
  `prefers-color-scheme` block decides (see the theme contract above). The
  button carries `aria-pressed` (true when dark is effective), kept in sync on
  load and on click. Headerless renders ship no scripts at all and follow the
  OS/server theme. Rendered pages run in the content sandbox (see Content
  isolation below), where localStorage is unavailable: the toggle still flips
  the open page, the storage calls fail quietly, and the choice is not carried
  to the next page. With `--unsafe-no-sandbox` it persists as described.
- **Foot**: one rule, one line, at the measure. The colophon line carries the
  publish date, the size (dropped below `40rem` so the line still fits a phone), "All artifacts" and, with `--footer` on (the
  default), the "Generated by demiplane" credit as its last item: the only
  place a published page names the tool. Series pages add the prev, position,
  next row above it, with no wordmark. `--footer=false` drops the credit. A
  page rendered without a colophon keeps the thin footer.
- **Link states** (prose links in `main`): default is oxblood with a 55%
  underline; hover thickens the underline and adds a 14% oxblood wash; visited
  is a quieter oxblood (55% accent, 45% muted) with a muted underline; focus
  is the ring below. Every state clears 4.5:1 on paper and in dark. Navs and
  the colophon keep their own muted links.
- **Focus**: a global `:focus-visible` ring (`2px solid var(--accent)`, 2px
  offset) on every keyboard-focused surface (WCAG 2.4.7).
- **Not-found**: the content plane serves a themed 404 (the operator frame with
  a site bar holding only the name and the toggle, "this document doesn't exist
  or has expired", and a link to the index) instead of a plaintext
  dead-end, since a stale or expired link is a reader's first impression.
- **Errors**: any other error a browser meets on either plane (a 404 off the
  content plane's artifact path, 400, 401, 403, 405, 413, 429, 5xx) gets the
  same themed page: the status, a one-line explanation, the server's reason
  when it adds something, and the link back. A request that does not accept
  `text/html` (curl, agents, MCP) keeps the plaintext reason.
- **Render limits**: tables over 128 columns, documents past 1,000,000 table
  body cells, and pages that would render past `--render-max-output`
  (default 32 MiB) are refused at publish with `413`, not truncated, so a
  published page is never a silently partial render. Heading ids keep their
  `-1`, `-2` suffix scheme; they are now assigned in linear time.
- **Print** (`@media print`): a rendered artifact survives Cmd+P as a well-set
  page. The print sheet forces a page-friendly light palette regardless of the
  on-screen theme (dark and the pinned developer palettes waste toner on paper),
  re-inverts the code slab to bordered-light, unsticks the masthead, hides the
  interactive chrome (theme toggle; heading anchors are already print-hidden),
  lets code wrap instead of clipping to its scroll box, and sets `@page` margins.
  Ink is near-black, not pure `#000`; the operator's `--css` override opts out
  (their sheet owns print).

## Operator pages

Home, `/docs`, `/docs/{page}`, `/help`, `/connect`, `/gallery` and the status
pages wear the published pages' frame: the same measure, the same wide blocks,
the same margin navs, the same light/dark contract and toggle. A page that opens
with an h1 lifts it into the frame's head (`.dochead`), so it runs title, navs,
text like a published page, and on a phone the folded navs follow the title.

- **Site bar**: the name "demiplane" in the serif at body weight 700, ink, then
  the nav (sans, muted, the current page in ink weight 600 with
  `aria-current`), then the toggle, over one hairline at the frame width. It is
  not sticky. On a phone the nav wraps to its own line under the name and toggle.
  The name appears once; the home H1 says what the tool is for instead.
- **No kickers**: no label sits above any heading. "For agents" and "This
  instance" are h2s.
- **Docs wayfinding**: a `/docs/{page}` carries the docs list as the left margin
  nav ("Docs 2 of 5"), the page's contents on the right, as a published series
  page does. Long pages (`/help`, `/connect`) get the contents list too.
- **Code**: the published pages' slab, under a small sans caption with a Copy
  button at its end.
- **Endpoints and the docs index**: hairline-ruled rows, not cards. Method
  labels are mono ink on the code surface, one style for every method.
- **Foot**: one rule, one line, at the measure ("Sealed by default, shared by
  choice.").

## Series grouping

A series — the prev/next colophon nav on rendered documents, the gallery's
"Group by series" view, and the landing table's folded rows — is **explicit**:
artifacts belong to the same family only when published with the same
`?series=` value (the slug alphabet: letters, digits, `.`, `_`, `-`). Slug
text never creates a family. The retired rule (family = text before the slug's
first hyphen) grouped unrelated documents the moment two slugs shared a first
word, which on an instance with prose-like slugs is nearly always; inference
from names is the same class of mistake at any pattern width, so the signal is
an author declaration, not a guess. Ordering within a series is lexical by
slug (zero-padded numeric suffixes sort correctly). Decided 2026-08-30
(render-fidelity brief, item 4, option b).

## Content isolation

Every artifact and site lives on one content origin (ADR 0003 split the
control plane off; it did not split artifacts from each other). Uploaded HTML
runs scripts by design, so without more a published page could read any other
artifact the viewer can open, ride the viewer's cached HTTP Basic
credentials, read storage written by another page, or register a root-scoped
service worker that intercepts later visits. Two designs keep artifacts apart.

**Per-artifact origins** (`<slug>.content.example`) give each artifact a real
origin of its own. That is the strongest boundary and it keeps every web
platform feature, but it needs wildcard DNS and a wildcard TLS certificate (a
DNS-01 ACME challenge, so DNS provider credentials on the host), and loopback,
bare-IP and single-hostname deployments cannot have it at all. demiplane's
default deployment is one binary on one hostname or one mesh IP with no DNS of
its own, so this cannot be the default.

**A per-document CSP sandbox** (chosen, 2026-10-17) needs nothing from the
deployment. Every HTML document (uploaded, `?render=md`, site pages) is served
with `sandbox allow-scripts allow-forms allow-popups
allow-popups-to-escape-sandbox allow-modals allow-downloads` and no
`allow-same-origin`, so the browser runs it in a fresh opaque origin and every
other URL on the instance, its own neighbours included, is cross-origin to it.
SVG, XML and XHTML get a bare `sandbox` with `script-src 'none'`. Each token is
kept because something real needs it: scripts (executable pages are the
product), forms (reply boxes and ordinary forms), popups that escape the
sandbox (`target=_blank` links behave normally), modals, downloads. The
network side refuses any request marked `Service-Worker`, and every document
carries `worker-src 'none'`, so no URL can become a service worker even if a
page runs unsandboxed.

What a page keeps: its own markup, inline and classic scripts, stylesheets,
images, media and links, and `postMessage`. A sandboxed page's module scripts,
`crossorigin` stylesheets and fonts are fetched in CORS mode from origin
`null`, which would break module-based sites (most modern bundler output). Public
artifacts of a matching type therefore answer those requests, and only those,
with `Access-Control-Allow-Origin: null` (see SECURITY.md for why that exposes
nothing a cross-origin `<script>` could not already run).

What it breaks, deliberately:

- `localStorage`, `sessionStorage`, IndexedDB and cookies throw a
  `SecurityError`. Pages that need persistence must handle that (demiplane's own
  theme and navigation toggles do).
- `fetch`/XHR from a page to its own files (a JSON data file next to a site's
  `index.html`, say) is cross-origin and refused. Data a page needs has to be
  inlined or loaded as a script.
- A page cannot frame another page on the instance, its own site included:
  `frame-ancestors 'self'` never matches an opaque parent. The `?live` wrapper
  still frames artifacts, because the wrapper is not sandboxed.
- Web workers, service workers and installable PWAs do not work.
- Inside the `?live` preview, `target=_top` links do not navigate the wrapper.
- A private or password-protected artifact cannot be loaded as a subresource
  (script, stylesheet, image) by any page, because it answers with
  `Cross-Origin-Resource-Policy: same-origin` and every sandboxed page is
  another origin. Protected artifacts are documents to open, not assets.

`--unsafe-no-sandbox` is the operator's way back to one shared origin, for an
instance that hosts only content its operator trusts; service workers stay
refused there. Per-artifact origins remain the upgrade path for a deployment
that has wildcard DNS and certificates and needs these features with
isolation: they would replace the sandbox, not sit on top of it.

## Typographic blockquotes

Blockquotes take a purely typographic treatment — an inset set in serif-italic
`--ink`, one step larger than body (`1.1875rem`), no box, no fill, and (per
the house refusal) **no side-stripe**. Restraint by subtraction: the inset,
the size, and the italic face carry the quotation without a bordered card.

## Motion

Ease-out-expo (`cubic-bezier(.22,1,.36,1)`) only, no bounce or elastic. Only
color, border, box-shadow, and transform are animated, never layout properties.
Everything is disabled under `prefers-reduced-motion`.

## Refusals (impeccable absolute bans, enforced here)

No side-stripe accent borders, no gradient text, no glassmorphism, no
hero-metric template, no pure `#000`/`#fff` (the table card's `--panel` stops
at L 0.995 for exactly this reason), no accent spent on non-link text, no kicker or
eyebrow above a heading, no em dashes in demiplane's own UI copy. (User markdown content is rendered
verbatim, including any em dashes it contains.)
