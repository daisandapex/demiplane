# demiplane design system

demiplane's human-facing HTML, the `/` landing, the `/docs` pages, and rendered
`?render=md` documents, all draw from one stylesheet defined in
`internal/theme/theme.go`. There is no second source of truth: the page chrome
composes `theme.Tokens()` + `theme.Content()` with its own chrome-only classes;
the markdown renderer embeds `theme.CSS()` / `theme.ToggleCSS()` whole. Change a
token once, every surface re-skins.

## Direction

Refined editorial. The reference is a well-set private document, calm,
trustworthy, read late in the evening on a wide monitor, not a SaaS dashboard.
The identity is a cool near-neutral paper, serif headings, mono code, a
restrained true-red accent with a deeper red for link hover/active.

## Color

OKLCH throughout. Every neutral is tinted toward a cool near-neutral hue
(h ~ 250 to 262, chroma ~ 0.004 to 0.014), so nothing reads as flat grey and the
neutral does not advertise the accent. There is no pure black or white anywhere:
even the lightest surface and darkest ink keep a trace of tint, and chroma falls
as lightness approaches the extremes. The accent (a true red, h ~ 26 to 28) is
held under ~10% of the surface: rules, links, the kicker, code accents, list
markers. A second, deeper red (`--accent-hover`) is the link hover/active state.

Color strategy: **Restrained** (tinted neutrals + one accent).

### Token map

| Token | Light | Dark | Role |
|---|---|---|---|
| `--bg` | `oklch(0.972 0.004 250)` | `oklch(0.225 0.008 255)` | page surface (cool near-neutral paper / dark) |
| `--panel` | `oklch(0.988 0.003 250)` | `oklch(0.263 0.009 255)` | raised surface (chrome cards; the `--zebra` source on the dark faces) |
| `--ink` | `oklch(0.255 0.012 262)` | `oklch(0.918 0.006 252)` | body text (also the blockquote's serif-italic ink) |
| `--muted` | `oklch(0.495 0.012 258)` | `oklch(0.712 0.010 255)` | secondary text, lead, captions |
| `--line` | `oklch(0.895 0.008 255)` | `oklch(0.340 0.010 255)` | hairline rules, borders |
| `--line-soft` | `oklch(0.930 0.006 255)` | `oklch(0.300 0.009 255)` | table inner rules |
| `--zebra` | `transparent` | `oklch(0.263 0.009 255)` | even-row table stripe — off in the light face (row rules carry it), a real step on the dark faces |
| `--accent` | `oklch(0.485 0.135 27)` | `oklch(0.700 0.130 26)` | links, kicker, markers, code accent |
| `--accent-hover` | `oklch(0.44 0.15 26)` | `oklch(0.660 0.135 26)` | link hover/active (deeper red) |
| `--accent-soft` | `oklch(0.952 0.020 28)` | `oklch(0.320 0.045 26)` | inline-code background |
| `--code-bg` | `oklch(0.278 0.010 258)` | `oklch(0.180 0.008 258)` | code-block surface |
| `--code-ink` | `oklch(0.918 0.008 255)` | `oklch(0.905 0.008 255)` | code-block text |
| `--code-inline` | `oklch(0.455 0.130 27)` | `oklch(0.760 0.105 27)` | inline-code text |
| `--code-line` | `oklch(0.400 0.014 258)` | `oklch(0.360 0.010 258)` | code-block border |
| `--navy` | `oklch(0.420 0.085 245)` | `oklch(0.620 0.095 240)` | secondary accent (landing GET badge) |
| `--danger` | `oklch(0.520 0.165 28)` | `oklch(0.700 0.150 28)` | destructive accent (DELETE badge) |
| `--info-bg/-line/-ink` | blue-grey trio | blue-grey trio | landing ethos pill |
| `--shadow` | `oklch(0.280 0.010 262 / 0.14)` | `oklch(0.120 0.010 258 / 0.45)` | scrolled-masthead shadow |
| `--sel` | `oklch(0.912 0.045 28)` | `oklch(0.380 0.055 26)` | text selection |

Body and secondary text clear WCAG AA against their surface in both faces
(measured, default palette): light `--ink` 14.55 : 1, `--muted` 5.65 : 1,
`--accent` 6.32 : 1 on `--bg`; dark `--ink` 13.42 : 1, `--muted` 6.70 : 1,
`--accent` 6.05 : 1 on `--bg`. All clear 4.5 : 1 on `--panel` too.

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
and emits its single `:root` palette. The toggle exists **only** for the default
palette, where `--theme light|dark` sets the initial side of a switch
the reader still controls. This is the `theme.Pinned(name)` seam: pinned themes
(`catppuccin`/`dracula`/`one-dark`) suppress the toggle; the unpinned pair keeps it.

## Typography

System stacks only, no web fonts, no CDN.

- `--serif`: Iowan Old Style, Palatino, Georgia (headings, masthead title, footer mark)
- `--sans`: system UI sans (body, kicker, table headers)
- `--mono`: system mono (code)

Editorial scale: 17px (`1.0625rem`) base on 1.7 leading; headings on a roughly
1.25 modular scale with tightened tracking (`-0.016em`) and 1.15 line-height for
a set-text feel. Vertical rhythm is varied, a heading owes more space above than
below. The document text column is capped near a 68ch measure (`43rem`).

## Rendered-document chrome (`?render=md`)

- **Masthead** (`.docbar`): sticky, page-colored, borderless until scroll, then a
  hairline plus a soft shadow fade in (ease-out-expo). Holds a small-caps accent
  kicker (the wordmark), the document title set as the single display-serif H1
  (lifted from the body so it is not duplicated), and the theme toggle.
- **Title**: every rendered document emits a `<head><title>` (the lifted H1, or
  the slug fallback, reduced to plain text), so a shared link, tab, or bookmark is
  never blank.
- **Lead**: the renderer tags the first body paragraph `class="lead"` (set quiet
  and slightly larger: muted, relaxed leading) so a dense first line reads as a
  dek. The explicit class survives a frontmatter meta-header injected ahead of the
  body, where a positional `:first-child` selector would silently stop matching.
- **Theme toggle**: an inline sun / moon SVG (currentColor, no icon dep). Click
  flips `data-theme` on `<html>` and persists to `localStorage`; the matching
  icon shows via CSS. Initial state resolves localStorage, then the server
  `--theme`/config, then OS `prefers-color-scheme`, set before paint (no FOUC).
  The button carries `aria-pressed` (true when dark is active), kept in sync on
  load and on click.
- **Footer**: a thin single-line credit, hairline top rule, muted, aligned to
  the text column.
- **Focus**: a global `:focus-visible` ring (`2px solid var(--accent)`, 2px
  offset) on every keyboard-focused surface (WCAG 2.4.7).
- **Not-found**: the content plane serves a themed 404 (chrome + "this document
  doesn't exist or has expired" + a link to the index) instead of a plaintext
  dead-end, since a stale or expired link is a reader's first impression.
- **Print** (`@media print`): a rendered artifact survives Cmd+P as a well-set
  page. The print sheet forces a page-friendly light palette regardless of the
  on-screen theme (dark and the pinned developer palettes waste toner on paper),
  re-inverts the code slab to bordered-light, unsticks the masthead, hides the
  interactive chrome (theme toggle; heading anchors are already print-hidden),
  lets code wrap instead of clipping to its scroll box, and sets `@page` margins.
  Ink is near-black, not pure `#000`; the operator's `--css` override opts out
  (their sheet owns print).

## Typographic blockquotes

Blockquotes take a purely typographic treatment — a left indent set in
serif-italic `--ink`, no box, no fill, and (per the house refusal) **no
side-stripe**. Restraint by subtraction: the indent and the italic face carry the
quotation without a bordered card.

## Motion

Ease-out-expo (`cubic-bezier(.22,1,.36,1)`) only, no bounce or elastic. Only
color, border, box-shadow, and transform are animated, never layout properties.
Everything is disabled under `prefers-reduced-motion`.

## Refusals (impeccable absolute bans, enforced here)

No side-stripe accent borders, no gradient text, no glassmorphism, no
hero-metric template, no pure `#000`/`#fff`, no em dashes in demiplane's own UI
copy. (User markdown content is rendered verbatim, including any em dashes it
contains.)
