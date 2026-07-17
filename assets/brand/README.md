<!--
SPDX-FileCopyrightText: 2026 Benjamin Connelly
SPDX-License-Identifier: AGPL-3.0-only
-->

# demiplane brand assets

The mark is a **pocket reality**: a bounded outer world with a gateway gap at the
bottom and a smaller pocket sealed inside. Monochrome, single-weight line art —
it reads at 16px and reduces to ASCII.

```
+-------+
|  +-+  |
|  | |  |
|  +-+  |
+--+ +--+
```

## Files

| File | Use |
|------|-----|
| `demiplane-mark.svg` | **Canonical mark.** `currentColor` stroke, transparent — recolors to context. Use this in docs, READMEs, and anywhere you control the ink color. |
| `demiplane-mark-dark.svg` | Mark with light ink (`#e7e9ec`) baked in, for dark backgrounds. |
| `demiplane-mark-light.svg` | Mark with dark ink (`#14171c`) baked in, for light backgrounds. |
| `demiplane-wordmark.svg` | Mark + `demiplane` lockup, `currentColor`. The wordmark is set in a monospace stack; see *Wordmark* below before using it as a fixed logotype. |
| `favicon.svg` | **Theme-adaptive favicon.** Transparent line art whose ink follows `prefers-color-scheme` via an internal `<style>`. This is the served `/favicon.svg` and the source of the inline data-URI favicon in every rendered page. |
| `favicon-tile.svg` | Mark on a rounded dark tile — the raster favicon source (always legible on any browser tab bar). |
| `maskable.svg` | Full-bleed dark tile with the mark inside the maskable safe zone. Source for `icon-192.png` / `icon-512.png`. |
| `apple-touch.svg` | Full-bleed tile, iOS-mask safe. Source for `apple-touch-icon.png`. |
| `favicon-16.png` `favicon-32.png` `favicon-48.png` | Raster favicons (from `favicon-tile.svg`). |
| `favicon.ico` | Multi-resolution icon (16/32/48) for legacy clients that ignore `rel="icon"`. Served at `/favicon.ico`. |
| `apple-touch-icon.png` | 180×180 iOS home-screen tile. Served at `/apple-touch-icon.png`. |
| `icon-192.png` `icon-512.png` | Maskable PNGs for a future web-app manifest / social use. Not currently served. |

## Palette

| Token | Hex | Role |
|-------|-----|------|
| ink (dark ground) | `#e7e9ec` | Mark on dark surfaces |
| ink (light ground) | `#14171c` | Mark on light surfaces |
| tile | `#14171c` | Icon-tile background |

## How it's wired into the binary

The mark is not a static file the server reads from disk — it's compiled in:

- `embed.go` embeds `favicon.svg`, `favicon.ico`, and `apple-touch-icon.png`
  (`BrandFS`) plus a raw copy of `favicon.svg` used to build `FaviconDataURI`.
- `internal/server/favicon.go` serves `/favicon.svg`, `/favicon.ico`, and
  `/apple-touch-icon.png` on the control plane (and the combined same-origin
  handler). The three names are reserved slugs so no artifact can shadow them.
- Every **rendered page** — published artifacts (`internal/render`), the control
  chrome (`/`, `/docs`, `/gallery`, `/help`, `/connect`), and the 404 — carries a
  `<link rel="icon">`. Artifacts and the 404 use the self-contained data-URI
  (`demiplane.FaviconDataURI`) so a saved page keeps its tab icon offline and on
  the isolated content origin; the chrome adds the served raster fallbacks.

## Regenerating the raster

Requires `librsvg2-bin` (`rsvg-convert`) and `icoutils` (`icotool`):

```sh
cd assets/brand
for s in 16 32 48; do rsvg-convert -w $s -h $s favicon-tile.svg -o favicon-$s.png; done
rsvg-convert -w 180 -h 180 apple-touch.svg  -o apple-touch-icon.png
rsvg-convert -w 192 -h 192 maskable.svg     -o icon-192.png
rsvg-convert -w 512 -h 512 maskable.svg     -o icon-512.png
icotool -c -o favicon.ico favicon-16.png favicon-32.png favicon-48.png
```

Then rebuild the binary so the embedded bytes update.

## Wordmark

`demiplane-wordmark.svg` sets the word in a monospace `font-family` stack, so it
renders with whatever mono the viewer has. That's fine for docs and the repo. For
a **font-locked logotype** (identical on every machine), outline the text to
paths with a font tool (e.g. `inkscape --export-text-to-path`) against a pinned
mono face and commit the outlined variant separately.
