// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package theme is the single source of demiplane's house style. It holds the
// design tokens (warm paper, a Charter/Iowan serif essay voice, mono code, an
// oxblood accent held to links) and the element-level typography that style
// every human-facing HTML surface: the / landing, the /docs pages, and via the
// markdown renderer, user content published with ?render=md.
//
// Both the page chrome (internal/server/chrome.go) and the standalone markdown
// renderer (internal/render) consume this package, so there is exactly ONE
// stylesheet to maintain. The chrome composes Tokens()+Content() with its own
// chrome-only classes (nav, cards, badges); the renderer uses CSS() whole.
package theme

import "strings"

// Default is the built-in theme used when none is selected, the house style.
const Default = "light"

// Names lists the built-in themes, for flag validation and help text. "light"
// and "dark" are the two faces of the default warm-paper palette that the
// reader's toggle flips between; the rest are pinned single-palette themes (see
// Pinned), the dark developer palettes demiplane's audience knows by name.
var Names = []string{"light", "dark", "catppuccin", "dracula", "one-dark"}

// sharedFonts is identical across themes: a theme swaps colors, not type.
// Charter (shipped on macOS) leads the serif stack. Bitstream Charter and
// Charis SIL are Charter itself under Linux names; Iowan Old Style and the
// Palatinos (Palatino Linotype on Windows, P052 and URW Palladio L from the
// URW base fonts most Linux desktops carry for printing) are close-voiced
// book faces; Noto Serif is Android's serif. A machine with none of them falls
// to its generic serif, usually DejaVu Serif, which body's font-size-adjust
// sets at Charter's x-height so the page keeps its colour and measure.
const sharedFonts = `
  --serif:"Charter","Bitstream Charter","Charis SIL","Iowan Old Style","Palatino Linotype","Palatino","P052",
    "URW Palladio L",Georgia,Cambria,"Noto Serif",serif;
  --sans:system-ui,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif;
  --mono:ui-monospace,"SF Mono","JetBrains Mono","Cascadia Code",Menlo,Consolas,"DejaVu Sans Mono",monospace;`

// The palette is OKLCH and every neutral is tinted warm (h ~ 75 to 91, chroma
// ~ 0.004 to 0.025) so the paper reads as stock, not screen grey. There is no
// pure black or white anywhere: the card surface stops just short of white and
// the darkest ink keeps a trace of warmth. The accent is an oxblood red
// (h ~ 23 to 29) held to links and small marks only; structure is carried by
// hairlines and size, never by color.
//
// --navy is the secondary-accent SURFACE color (it backs the GET badge on the
// landing chrome), so it stays dark enough for its label to read. The info-pill
// (.ethos) uses its own --info-* trio so its TEXT can go light on dark without
// dragging the badge background with it.

// lightVars is the canonical house palette, ported from the approved render=md
// design (competition entry j, 2026-08-30): warm paper #faf9f6, near-black warm
// ink, oxblood links, a light bordered code slab, white-card tables. Values are
// exact OKLCH conversions of the approved hex sheet except --panel, which stops
// at L 0.995 instead of pure #ffffff to honor the no-pure-white refusal.
// Measured AA on --bg: ink 15.8:1, muted 7.7:1, accent 7.7:1; code-ink on
// --code-bg 13.5:1; muted on --panel 7.0:1.
//
// color-scheme:light tells the UA to render form controls, scrollbars, and the
// canvas in their light variants (paired with the dark blocks in ToggleCSS).
// The --tok-* code accents are dark inks tuned for the light slab: all clear
// WCAG AA on --code-bg (worst --tok-str 5.8:1).
const lightVars = `
  color-scheme:light;
  --bg:oklch(0.982 0.004 91); --panel:oklch(0.995 0.002 95); --ink:oklch(0.237 0.009 75);
  --muted:oklch(0.430 0.011 78); --line:oklch(0.910 0.013 87); --line-strong:oklch(0.714 0.018 85);
  --accent:oklch(0.445 0.122 23);
  --code-bg:oklch(0.953 0.010 87); --code-ink:oklch(0.261 0.010 89); --code-border:oklch(0.883 0.017 88);
  --navy:oklch(0.420 0.085 245); --danger:oklch(0.520 0.165 28); --on-badge:oklch(0.985 0.004 95);
  --info-bg:oklch(0.945 0.020 235); --info-line:oklch(0.895 0.030 235); --info-ink:oklch(0.420 0.080 245);
  --shadow:oklch(0.250 0.010 80 / 0.14); --sel:oklch(0.909 0.025 43);
  --tok-key:oklch(0.445 0.122 23); --tok-fn:oklch(0.470 0.077 253); --tok-str:oklch(0.470 0.072 150); --tok-com:oklch(0.462 0.011 78);`

// darkVars is the dark counterpart from the same approved sheet: warm near-black
// paper, unbleached-linen ink, the accent lifted to a dusty terracotta so the
// oxblood reads on the dark ground. Measured AA on --bg: ink 13.5:1, muted
// 7.5:1, accent 7.4:1; muted on --panel 6.8:1 (the sheet's worst pair, still
// clear of 4.5:1); code-ink on --code-bg 10.5:1. The code slab sits a step above
// --bg so it lifts off the page rather than sinking into it. The --tok-* accents
// are light inks tuned for the dark slab (worst --tok-com 6.0:1).
const darkVars = `
  color-scheme:dark;
  --bg:oklch(0.228 0.006 78); --panel:oklch(0.261 0.008 85); --ink:oklch(0.923 0.009 85);
  --muted:oklch(0.745 0.012 77); --line:oklch(0.330 0.010 81); --line-strong:oklch(0.461 0.015 87);
  --accent:oklch(0.749 0.082 29);
  --code-bg:oklch(0.262 0.008 85); --code-ink:oklch(0.871 0.015 85); --code-border:oklch(0.362 0.015 85);
  --navy:oklch(0.620 0.095 240); --danger:oklch(0.700 0.150 28); --on-badge:oklch(0.985 0.004 95);
  --info-bg:oklch(0.300 0.030 235); --info-line:oklch(0.400 0.040 235); --info-ink:oklch(0.780 0.060 235);
  --shadow:oklch(0.100 0.008 80 / 0.45); --sel:oklch(0.369 0.023 48);
  --tok-key:oklch(0.749 0.082 29); --tok-fn:oklch(0.746 0.055 253); --tok-str:oklch(0.766 0.062 140); --tok-com:oklch(0.708 0.012 77);`

// The named dark palettes below are faithful OKLCH conversions of the three
// developer color schemes demiplane's audience knows by name. Each was converted
// from its published sRGB/hex spec into OKLCH and mapped onto demiplane's token
// contract: the scheme's background→--bg, a raised surface→--panel, foreground→
// --ink, a brightened comment/subtext→--muted, the signature accent→--accent
// (dracula purple, catppuccin mauve, one-dark blue), selection→--sel. The
// palettes stay recognizable while obeying the house rules: neutrals carry the
// scheme's own hue (they already do, dracula ~278, catppuccin ~283, one-dark
// ~264), chroma stays modest at the lightness extremes, and there is no pure
// #000/#fff (none of the three specs use it).
//
// Accessibility: --ink and --muted clear WCAG AA (>=4.5:1) against BOTH --bg and
// --panel. Where a faithful value failed that bar it was lifted and the
// deviation is noted:
//   - dracula --muted: the spec comment (#6272a4) is only 3.03:1 on --bg;
//     brightened to a lighter blue-purple that clears AA on both surfaces.
//   - one-dark --ink: uses the scheme's BRIGHT foreground (#dcdfe4) rather than
//     the dim #abb2bf, so a subordinate --muted still has AA headroom; --muted
//     is a brightened comment (#5c6370 alone is 2.32:1 and fails).
//   - catppuccin --muted maps to subtext0 (#a6adc8), which already passes.
// --navy and --danger back the landing method badges (white label text) and are
// darkened enough to clear the 3:1 UI bar for that white text.

// catppuccinVars is the Catppuccin Mocha flavor: soft blue-purple neutrals,
// mauve accent, on a #1e1e2e base.
const catppuccinVars = `
  color-scheme:dark;
  --bg:oklch(0.243 0.030 284); --panel:oklch(0.324 0.032 282); --ink:oklch(0.879 0.043 272);
  --muted:oklch(0.751 0.040 274); --line:oklch(0.404 0.032 280); --line-strong:oklch(0.500 0.032 280);
  --accent:oklch(0.787 0.119 305);
  --code-bg:oklch(0.216 0.025 284); --code-ink:oklch(0.879 0.043 272); --code-border:oklch(0.324 0.032 282);
  --navy:oklch(0.500 0.100 262); --danger:oklch(0.560 0.170 10); --on-badge:oklch(0.985 0.004 95);
  --info-bg:oklch(0.320 0.045 268); --info-line:oklch(0.400 0.050 266); --info-ink:oklch(0.820 0.070 265);
  --shadow:oklch(0.120 0.020 284 / 0.5); --sel:oklch(0.477 0.034 279);
  --tok-key:oklch(0.787 0.119 305); --tok-fn:oklch(0.766 0.111 260); --tok-str:oklch(0.858 0.109 143); --tok-com:oklch(0.618 0.037 276);`

// draculaVars is the Dracula palette: #282a36 base, purple accent, pink code,
// the classic #44475a current-line as the selection tint.
const draculaVars = `
  color-scheme:dark;
  --bg:oklch(0.288 0.022 278); --panel:oklch(0.345 0.026 278); --ink:oklch(0.977 0.008 107);
  --muted:oklch(0.715 0.050 272); --line:oklch(0.400 0.030 278); --line-strong:oklch(0.500 0.030 278);
  --accent:oklch(0.742 0.149 302);
  --code-bg:oklch(0.255 0.019 280); --code-ink:oklch(0.918 0.012 280); --code-border:oklch(0.360 0.026 278);
  --navy:oklch(0.500 0.100 252); --danger:oklch(0.560 0.170 25); --on-badge:oklch(0.985 0.004 95);
  --info-bg:oklch(0.320 0.045 270); --info-line:oklch(0.400 0.050 270); --info-ink:oklch(0.820 0.070 270);
  --shadow:oklch(0.150 0.020 278 / 0.5); --sel:oklch(0.403 0.032 278);
  --tok-key:oklch(0.755 0.183 347); --tok-fn:oklch(0.871 0.220 148); --tok-str:oklch(0.955 0.134 113); --tok-com:oklch(0.612 0.073 273);`

// oneDarkVars is the Atom One Dark palette: #282c34 base, blue accent, cool
// blue-grey neutrals; --ink uses the scheme's bright foreground for AA headroom.
const oneDarkVars = `
  color-scheme:dark;
  --bg:oklch(0.293 0.016 264); --panel:oklch(0.335 0.018 262); --ink:oklch(0.903 0.010 261);
  --muted:oklch(0.705 0.022 264); --line:oklch(0.386 0.024 266); --line-strong:oklch(0.480 0.024 266);
  --accent:oklch(0.730 0.121 245);
  --code-bg:oklch(0.263 0.013 258); --code-ink:oklch(0.903 0.010 261); --code-border:oklch(0.335 0.018 262);
  --navy:oklch(0.500 0.100 250); --danger:oklch(0.560 0.150 20); --on-badge:oklch(0.985 0.004 95);
  --info-bg:oklch(0.320 0.040 255); --info-line:oklch(0.400 0.045 255); --info-ink:oklch(0.820 0.070 250);
  --shadow:oklch(0.140 0.015 264 / 0.5); --sel:oklch(0.386 0.024 266);
  --tok-key:oklch(0.694 0.164 318); --tok-fn:oklch(0.730 0.121 245); --tok-str:oklch(0.768 0.110 133); --tok-com:oklch(0.622 0.026 264);`

// rootByName maps a theme name to its :root token block.
var rootByName = map[string]string{
	"light":      ":root{" + lightVars + sharedFonts + "\n}",
	"dark":       ":root{" + darkVars + sharedFonts + "\n}",
	"catppuccin": ":root{" + catppuccinVars + sharedFonts + "\n}",
	"dracula":    ":root{" + draculaVars + sharedFonts + "\n}",
	"one-dark":   ":root{" + oneDarkVars + sharedFonts + "\n}",
}

// pinnedThemes are single-palette themes that OVERRIDE and FIX the palette: they
// have no light counterpart, so a surface rendered under one does not offer the
// reader's light↔dark toggle. Only "light"/"dark", the two faces of the default
// palette, participate in that toggle. See Pinned.
var pinnedThemes = map[string]bool{
	"catppuccin": true,
	"dracula":    true,
	"one-dark":   true,
}

// Pinned reports whether name is a pinned single-palette theme. When a pinned
// theme is configured, the markdown chrome emits that one palette and suppresses
// the light/dark toggle (there is nothing to toggle to); an unpinned theme
// ("" | light | dark) keeps the two-sheet toggle so the reader can flip.
func Pinned(name string) bool { return pinnedThemes[strings.TrimSpace(name)] }

// contentCSS is the element-level typography shared by the chrome and the
// markdown renderer: body, headings, links, code, pre, blockquote, tables, hr.
// Every value references a design token, so a theme swap re-skins all of it.
//
// The `pre .k/.fn/.s/.c` rules color the syntax-highlighter's token spans
// (keyword/function/string/comment) from the per-theme --tok-* tokens. They are
// scoped under `pre` so the short class names cannot collide with chrome
// classes. The code slab follows its face (light slab on the light face, dark
// slab on the dark), so each face carries its own AA-tuned token inks.
//
// The scale is the approved essay voice (entry j, 2026-08-30): a 16px serif
// body on 1.6 leading, size-led heading hierarchy (34/24/19 at weight 700), a
// hairline-topped h2 as the section scan aid, oxblood links as the only colored
// text, ink-neutral bordered inline code, and card tables set in a small sans
// with tabular numerals. Structure comes from size and hairlines; the accent
// never spends on headings, markers, or code.
const contentCSS = `*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%;scroll-padding-top:1.5rem}
html,body{overflow-x:clip}
body{margin:0;background:var(--bg);color:var(--ink);
  font:1rem/1.6 var(--serif);font-size-adjust:.48;text-rendering:optimizeLegibility}
code,pre,table,button,input,select,textarea{font-size-adjust:none}
::selection{background:var(--sel)}
.wrap{max-width:54rem;margin:0 auto;padding:0 1.3rem}
main{padding:2.2rem 0 3.5rem}
a{color:var(--accent);text-decoration:underline;text-decoration-thickness:1px;
  text-underline-offset:.18em;
  text-decoration-color:color-mix(in srgb,var(--accent) 55%,transparent);
  transition:text-decoration-color .2s cubic-bezier(.22,1,.36,1)}
a:hover,a:active{text-decoration-thickness:2px;text-decoration-color:var(--accent)}
main :is(p,li,td,dd,blockquote) a:visited{color:color-mix(in oklab,var(--accent) 55%,var(--muted));
  text-decoration-color:color-mix(in srgb,var(--muted) 55%,transparent)}
main :is(p,li,td,dd,blockquote) a:is(:hover,:active){background:color-mix(in srgb,var(--accent) 14%,transparent);
  border-radius:2px}
:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.skip{position:absolute;left:1rem;top:-4rem;z-index:20;padding:.5rem .8rem;border-radius:6px;
  background:var(--panel);color:var(--accent);border:1px solid var(--line-strong);font:600 .875rem/1.4 var(--sans)}
.skip:focus{top:.75rem}
h1,h2,h3,h4,h5,h6{font-family:var(--serif);font-weight:700;color:var(--ink);
  line-height:1.15;text-wrap:balance}
h1{font-size:2.125rem;letter-spacing:-.02em;margin:.3em 0 .35em}
h2{font-size:1.5rem;line-height:1.25;letter-spacing:-.012em;border-top:1px solid var(--line);
  padding-top:1.125rem;margin:2.5rem 0 .75rem}
h3{font-size:1.1875rem;line-height:1.3;margin:2.25rem 0 .625rem}
h4{font-size:1.0625rem;line-height:1.35;margin:2rem 0 .5rem}
h5{font-size:1rem;margin:2rem 0 .5rem}
h6{font-size:.875rem;margin:2rem 0 .5rem;color:var(--muted)}
.heading-anchor{margin-left:.35em;color:var(--muted);font-weight:400;text-decoration:none;
  opacity:0;user-select:none;transition:opacity .2s cubic-bezier(.22,1,.36,1)}
.heading-anchor:hover,.heading-anchor:active{color:var(--accent);text-decoration:none}
h1:hover .heading-anchor,h2:hover .heading-anchor,h3:hover .heading-anchor,
h4:hover .heading-anchor,h5:hover .heading-anchor,h6:hover .heading-anchor,
.heading-anchor:focus-visible{opacity:1}
@media (hover:none){:is(h2,h3,h4,h5,h6) .heading-anchor{opacity:.45}h1 .heading-anchor{display:none}}
@media print{.heading-anchor{display:none}}
@media (prefers-reduced-motion:reduce){.heading-anchor{transition:none}}
p{margin:0 0 1.5em;text-wrap:pretty}
ul,ol{margin:0 0 1.5em;padding-left:1.5em}
li{margin:.3em 0}
li::marker{color:var(--muted)}
strong{font-weight:700}
em{font-style:italic}
code{font-family:var(--mono);font-size:.875em;line-height:1;
  background:color-mix(in oklab,var(--code-border) 45%,var(--code-bg));
  color:var(--code-ink);border:0;padding:.1em .3em;border-radius:3px;
  -webkit-box-decoration-break:clone;box-decoration-break:clone;overflow-wrap:break-word}
:not(pre)>code.nw{white-space:nowrap}
@media (max-width:24.35rem){:not(pre)>code.nw{white-space:normal}}
:is(th,td) code{padding:.05em .22em}
@media (max-width:40rem){
:is(td,th) code{overflow-wrap:anywhere}
:is(td,th) code.nw{white-space:normal}}
pre{background:var(--code-bg);color:var(--code-ink);border:1px solid var(--code-border);
  background:linear-gradient(to right,var(--code-bg) 40%,transparent) left/2rem 100% no-repeat local,
  linear-gradient(to left,var(--code-bg) 40%,transparent) right/2rem 100% no-repeat local,
  radial-gradient(farthest-side at 0 50%,color-mix(in srgb,var(--ink) 35%,transparent),transparent) left/1.25rem 100% no-repeat scroll,
  radial-gradient(farthest-side at 100% 50%,color-mix(in srgb,var(--ink) 35%,transparent),transparent) right/1.25rem 100% no-repeat scroll,
  var(--code-bg);
  border-radius:8px;padding:.875rem 1rem;margin:0 0 1.125em;overflow-x:auto;
  font-size:.8125rem;line-height:1.55;tab-size:2}
pre code{background:none;border:0;color:inherit;padding:0;border-radius:0;font-size:1em;line-height:inherit}
@media (max-width:40rem){pre{white-space:pre-wrap;overflow-wrap:anywhere}}
pre .k{color:var(--tok-key)}pre .fn{color:var(--tok-fn)}pre .s{color:var(--tok-str)}
pre .c{color:var(--tok-com);font-style:italic}
blockquote{margin:1.625em 0;padding:0 1.75rem;border:none;background:none;
  font-family:var(--serif);font-style:italic;font-size:1.1875rem;line-height:1.5;color:var(--ink)}
blockquote p{margin:0}
blockquote p+p{margin-top:.625em}
.table-wrap{background:var(--panel);border:1px solid var(--line);border-radius:8px;
  overflow-x:auto;margin:.25em 0 1.125em}
.table-wrap>table{margin:0}
table{border-collapse:collapse;width:100%;margin:1.6em 0;font:.875rem/1.45 var(--sans)}
thead th{font-size:.8125rem;font-weight:600;color:var(--muted);border-bottom:1px solid var(--line-strong)}
th,td{text-align:left;vertical-align:top;padding:.45rem .875rem;font-variant-numeric:tabular-nums}
tbody tr+tr td{border-top:1px solid var(--line)}
@media (max-width:40rem){
.table-wrap:has(th:nth-child(4)):has(td[data-label]) thead{position:absolute;width:1px;height:1px;overflow:hidden;
  clip-path:inset(50%)}
.table-wrap:has(th:nth-child(4)):has(td[data-label]) :is(table,tbody,tr,td){display:block}
.table-wrap:has(th:nth-child(4)):has(td[data-label]) tr{padding:.4rem 0}
.table-wrap:has(th:nth-child(4)):has(td[data-label]) tbody tr+tr{border-top:1px solid var(--line)}
.table-wrap:has(th:nth-child(4)):has(td[data-label]) td{position:relative;padding:.15rem .875rem .15rem 6rem;
  border-top:0}
.table-wrap:has(th:nth-child(4)):has(td[data-label]) td::before{content:attr(data-label);position:absolute;
  left:.875rem;width:4.75rem;color:var(--muted);font-weight:600;font-size:.75rem;overflow-wrap:anywhere}}
hr{border:0;width:5rem;height:1px;background:var(--line-strong);margin:2.375rem auto}
img{max-width:100%;height:auto;display:block;margin:0 auto}
@media (prefers-reduced-motion:reduce){a{transition:none}}
`

// frameCSS is the page frame every human surface shares, published pages and
// operator pages alike: one centred axis with two widths on it. The text
// column holds to --measure (near 70 characters); tables, code and other wide
// blocks break out to --wide around the same centre. A page title holds to
// the text column, so it never hangs left of the text or past its edge. The margin navs (.mnav: a
// sequence on the left, the contents on the right) sit outside the wide frame
// from 78.5rem, where both gutters have room for them, and fold to one-line
// disclosures above the text below that, so on a phone the text still starts
// on the first screen. The theme toggle is styled here because both kinds of
// page carry it.
const frameCSS = `
:root{--measure:33rem;--wide:46rem;--side:13rem}
.frame{display:grid;grid-template-columns:minmax(0,1fr);grid-template-areas:"head" "seq" "toc" "body";
  padding:0 1.25rem}
.frame>*{min-width:0;width:100%;max-width:var(--wide);justify-self:center}
.frame>.dochead{grid-area:head}.frame>.seq{grid-area:seq}.frame>.toc{grid-area:toc}.frame>main{grid-area:body}
.frame>.mnav{max-width:var(--measure)}
.dochead{padding-top:3rem}
.dochead>*,.doc>h1{margin-left:max(0px,calc((100% - var(--measure)) / 2));margin-right:0}
.doc>*{max-width:var(--measure);margin-inline:auto}
.doc>:is(pre,.table-wrap,.codeblock,.wide){max-width:100%}
@media (min-width:78.5rem){
.frame{grid-template-columns:minmax(0,1fr) minmax(0,var(--wide)) minmax(0,1fr);column-gap:1.25rem;
  grid-template-areas:". head ." "seq body toc";grid-template-rows:auto 1fr}
.frame>.mnav{position:sticky;top:1.5rem;align-self:start;max-width:var(--side);
  max-height:calc(100vh - 3rem);overflow-y:auto;overscroll-behavior:contain;margin:0}
.frame>.seq{justify-self:end}.frame>.toc{justify-self:start}}
.mnav{font:.8125rem/1.45 var(--sans);font-size-adjust:none;color:var(--muted);margin:0 auto}
.mnav>summary{display:flex;align-items:baseline;gap:.45rem;padding:.3rem 0;cursor:pointer;list-style:none;
  font-weight:600;color:var(--muted)}
.mnav>summary::-webkit-details-marker{display:none}
.mnav>summary::before{content:"";flex:0 0 auto;width:.38rem;height:.38rem;border:solid currentColor;
  border-width:0 1.5px 1.5px 0;transform:translateY(-.12rem) rotate(-45deg);
  transition:transform .25s cubic-bezier(.22,1,.36,1)}
.mnav[open]>summary::before{transform:translateY(-.2rem) rotate(45deg)}
.mnav>summary:hover{color:var(--ink)}
.mnav>summary .pos{font-weight:400}
.mnav ol{list-style:none;margin:0;padding:.2rem 0 .5rem}
.mnav li{margin:0}
.mnav a,.mnav [aria-current="page"]{display:block;padding:.22rem .5rem;margin-left:-.5rem;border-radius:6px;
  color:var(--muted);text-decoration:none;overflow-wrap:anywhere;text-wrap:pretty}
.mnav a:hover{color:var(--ink);background:var(--code-bg)}
.mnav a[aria-current="true"],.mnav [aria-current="page"]{color:var(--ink);font-weight:600;background:var(--code-bg)}
.outline .l3 a{padding-left:1.25rem}
.fjs .outline .l3{display:none}
.fjs .outline .l3.open{display:block}
@media (max-width:78.49rem){.fjs .outline .l3,.fjs .outline .l3.open{display:none}}
.themetoggle{flex:0 0 auto;display:inline-flex;align-items:center;justify-content:center;
  width:2.15rem;height:2.15rem;padding:0;cursor:pointer;color:var(--muted);
  background:var(--panel);border:1px solid var(--line);border-radius:999px;
  transition:color .25s cubic-bezier(.22,1,.36,1),border-color .25s cubic-bezier(.22,1,.36,1)}
.themetoggle:hover{color:var(--ink);border-color:var(--line-strong)}
.themetoggle svg{width:1.05rem;height:1.05rem;display:block;transition:transform .35s cubic-bezier(.22,1,.36,1)}
.themetoggle:hover svg{transform:rotate(11deg) scale(1.06)}
.themetoggle .i-moon{display:none}
html[data-theme="dark"] .themetoggle .i-sun{display:none}
html[data-theme="dark"] .themetoggle .i-moon{display:block}
@media (prefers-color-scheme: dark){
html:not([data-theme="light"]) .themetoggle .i-sun{display:none}
html:not([data-theme="light"]) .themetoggle .i-moon{display:block}}
.colo{margin-top:3.5rem;padding-top:1rem;border-top:1px solid var(--line);
  font:.8rem/1.7 var(--sans);font-size-adjust:none;color:var(--muted)}
.colo a{color:var(--muted)}
.colo a:hover{color:var(--accent)}
@media (prefers-reduced-motion:reduce){.mnav>summary::before,.themetoggle,.themetoggle svg{transition:none}
  .themetoggle:hover svg{transform:none}}
@media print{.mnav,.themetoggle{display:none}}
`

// FrameCSS returns the shared page frame (see frameCSS).
func FrameCSS() string { return frameCSS }

// Tokens returns the :root design-token block for name, falling back to the
// Default theme when name is empty or unknown.
func Tokens(name string) string {
	if r, ok := rootByName[name]; ok {
		return r
	}
	return rootByName[Default]
}

// Content returns the shared element typography (no :root tokens). The chrome
// composes this with Tokens() and its own chrome-only classes.
func Content() string { return contentCSS }

// CSS returns the complete stylesheet (tokens + content) for name, falling back
// to the Default theme. This is what the markdown renderer embeds whole.
func CSS(name string) string { return Tokens(name) + "\n" + contentCSS }

// ToggleCSS returns a stylesheet that ships BOTH token sets in the house
// three-block contract, so a client can flip themes instantly with no server
// round-trip and an OS preference applies with no JavaScript at all:
//
//  1. the complete light palette on bare :root (the default),
//  2. the dark palette under @media (prefers-color-scheme: dark), guarded as
//     :root:not([data-theme="light"]) so an explicit light choice wins, and
//  3. the dark palette again under :root[data-theme="dark"] so an explicit
//     dark choice wins in both directions.
//
// The shared content typography follows. The fonts live only in :root
// (identical across themes), so the dark blocks carry colors alone.
func ToggleCSS() string {
	return ":root{" + lightVars + sharedFonts + "\n}\n" +
		"@media (prefers-color-scheme: dark){\n:root:not([data-theme=\"light\"]){" + darkVars + "\n}\n}\n" +
		":root[data-theme=\"dark\"]{" + darkVars + "\n}\n" +
		contentCSS
}

// MetaColor returns the --bg surface color for name (falling back to Default),
// for the document's <meta name="theme-color">. It reads the value straight out
// of the resolved :root token block so it can never drift from the palette. The
// tag is static per document (it reflects the server-resolved initial theme and
// does not follow a client-side toggle), which is acceptable for the mobile
// browser-chrome tint it drives.
func MetaColor(name string) string {
	root := Tokens(name)
	const key = "--bg:"
	i := strings.Index(root, key)
	if i < 0 {
		return ""
	}
	i += len(key)
	j := strings.IndexByte(root[i:], ';')
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(root[i : i+j])
}

// Valid reports whether name is a known built-in theme.
func Valid(name string) bool {
	_, ok := rootByName[strings.TrimSpace(name)]
	return ok
}
