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
| `--muted` | `oklch(0.462 0.011 78)` | `oklch(0.708 0.012 77)` | secondary text, byline, table headers, list markers |
| `--line` | `oklch(0.910 0.013 87)` | `oklch(0.330 0.010 81)` | hairline rules, borders, table row rules |
| `--line-strong` | `oklch(0.714 0.018 85)` | `oklch(0.461 0.015 87)` | emphatic rules: table header underline, `hr`, toggle hover ring |
| `--accent` | `oklch(0.445 0.122 23)` | `oklch(0.749 0.082 29)` | links and the focus ring, nothing else |
| `--code-bg` | `oklch(0.953 0.010 87)` | `oklch(0.205 0.008 85)` | code surface, inline and block (follows its face) |
| `--code-ink` | `oklch(0.261 0.010 89)` | `oklch(0.871 0.015 85)` | code text, inline and block |
| `--code-border` | `oklch(0.883 0.017 88)` | `oklch(0.342 0.015 85)` | code hairline border, inline and block |
| `--navy` | `oklch(0.420 0.085 245)` | `oklch(0.620 0.095 240)` | secondary accent (landing GET badge) |
| `--danger` | `oklch(0.520 0.165 28)` | `oklch(0.700 0.150 28)` | destructive accent (DELETE badge) |
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
(measured, default palette): light `--ink` 15.8 : 1, `--muted` 6.7 : 1,
`--accent` 7.7 : 1 on `--bg`; dark `--ink` 13.5 : 1, `--muted` 6.6 : 1,
`--accent` 7.4 : 1 on `--bg`. All clear 4.5 : 1 on `--panel` too (worst pair:
dark `--muted` on `--panel`, 5.97 : 1). Code ink and all four `--tok-*` syntax
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
The server `--theme` stamps the attribute; the reader's toggle rewrites it and
persists to localStorage; a tiny head script re-applies the stored choice
before paint. `theme.ToggleCSS()` emits the contract and
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

- `--serif`: Charter, Bitstream Charter, Iowan Old Style, Palatino, Georgia
  (body AND headings; one family carries the whole document)
- `--sans`: system UI sans (byline/meta, table text, colophon)
- `--mono`: system mono (code)

Essay scale: 16px (`1rem`) serif body on 1.6 leading, compact block margins
(paragraphs owe `.875em` below). Headings are size-led at weight 700:
h1 `2.125rem` (tracked `-0.02em`), h2 `1.5rem` over a hairline top rule (the
section scan aid), h3 `1.1875rem`, then `1.0625rem`/`1rem`/`.875rem` down to
h6. The lead paragraph bumps to `1.0625rem`. Blockquotes are unboxed italic at
`1.1875rem`. Tables drop to a `.875rem` sans with tabular numerals; table
headers are muted weight-600, never uppercase (the byline keeps its
letterspaced uppercase as a one-off). `text-wrap: pretty` on paragraphs,
`balance` on headings.

The measure is `70ch` on the document column. Tables and code blocks break out
to a centred `min(62rem, 100%)` cap on viewports over `60rem`; below that they
fall back to the measure and scroll horizontally inside their own container
(`.table-wrap` / `pre`), so the body never scrolls sideways
(`overflow-x: clip` on `html, body` backstops it).

## Rendered-document chrome (`?render=md`)

- **Masthead** (`.docbar`): sticky, page-colored, borderless until scroll, then a
  hairline plus a soft shadow fade in (ease-out-expo). Holds a letterspaced
  muted kicker (the wordmark; the accent is links-only), the document title set
  as the single display-serif H1 (lifted from the body so it is not
  duplicated), and the theme toggle. The masthead and footer columns align to
  the 70ch text measure.
- **Title**: every rendered document emits a `<head><title>` (the lifted H1, or
  the slug fallback, reduced to plain text), so a shared link, tab, or bookmark is
  never blank.
- **Lead**: the renderer tags the first body paragraph `class="lead"` (bumped
  to `1.0625rem`, full ink; the size alone carries the dek). The explicit class
  survives a frontmatter meta-header injected ahead of the
  body, where a positional `:first-child` selector would silently stop matching.
- **Theme toggle**: an inline sun / moon SVG (currentColor, no icon dep). Click
  flips `data-theme` on `<html>` and persists to `localStorage`; the matching
  icon shows via CSS (keyed off `data-theme` plus the same guarded media query,
  so it is right even before any choice is stored). Initial state: a stored
  localStorage choice is re-applied before paint (no FOUC); otherwise the
  server `--theme` stamp or, with neither, the stylesheet's
  `prefers-color-scheme` block decides (see the theme contract above). The
  button carries `aria-pressed` (true when dark is effective), kept in sync on
  load and on click. Headerless renders ship no scripts at all and follow the
  OS/server theme.
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
at L 0.995 for exactly this reason), no accent spent on non-link text, no em
dashes in demiplane's own UI copy. (User markdown content is rendered
verbatim, including any em dashes it contains.)
