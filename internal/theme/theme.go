// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package theme is the single source of demiplane's house style. It holds the
// design tokens (cool near-neutral background, serif headings, mono code, a
// true-red accent) and the element-level typography that style every
// human-facing HTML surface: the / landing, the /docs pages, and via the
// markdown renderer, user content published with ?render=md.
//
// Both the page chrome (internal/server/chrome.go) and the standalone markdown
// renderer (internal/render) consume this package, so there is exactly ONE
// stylesheet to maintain. The chrome composes Tokens()+Content() with its own
// chrome-only classes (nav, cards, badges); the renderer uses CSS() whole.
package theme

import "strings"

// Default is the built-in theme used when none is selected — the house style.
const Default = "light"

// Names lists the built-in themes, for flag validation and help text. "light"
// and "dark" are the two halves of the default red-on-cool-neutral palette that the
// reader's toggle flips between; the rest are pinned single-palette themes (see
// Pinned) — the dark developer palettes demiplane's audience knows by name.
var Names = []string{"light", "dark", "catppuccin", "dracula", "one-dark"}

// sharedFonts is identical across themes: a theme swaps colors, not type.
const sharedFonts = `
  --serif:Iowan Old Style,Palatino,"Palatino Linotype",Georgia,"Times New Roman",serif;
  --sans:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;
  --mono:"SFMono-Regular",Menlo,Consolas,"Liberation Mono",monospace;`

// The palette is OKLCH and every neutral is tinted toward a cool near-neutral
// hue (h~250-262, chroma ~0.004-0.014) so nothing reads as a flat grey and the
// neutral does not advertise the accent. There is no pure black or white
// anywhere: the lightest surface and the darkest ink both keep a trace of tint.
// Chroma drops as lightness nears the extremes (high chroma at the ends looks
// garish). The accent (true red, h~26-28) is held to small marks: rules, links,
// the kicker, code accents; --accent-hover is a deeper red for link hover/active.
//
// --navy is the secondary-accent SURFACE color (it backs the GET badge on the
// landing chrome), so it stays dark enough for its label to read. The info-pill
// (.ethos) uses its own --info-* trio so its TEXT can go light on dark without
// dragging the badge background with it.

// lightVars is the canonical house palette: cool near-neutral paper, dark ink,
// a true-red accent. The neutral hue is cool (h~250-262) so it no longer
// advertises the accent, and the accent is a decoupled red (h~27), not the old
// warm burnt-orange. A two-step accent (--accent → --accent-hover) gives links a
// coherent hover/active deepening. Measured AA: accent-on-bg 6.32:1, ink 14.55:1,
// muted 5.65:1 (all clear 4.5:1 on both --bg and --panel).
//
// color-scheme:light tells the UA to render form controls, scrollbars, and the
// canvas in their light variants (paired with the dark override in ToggleCSS).
// --tok-com is the code-comment ink; on the light face's --code-bg it must clear
// WCAG AA (4.5:1) at normal size — L 0.67 measures 4.91:1 (0.62 was 4.04:1 and
// failed). The dark face keeps 0.62 (5.17:1 on its darker slab), so the two are
// decoupled deliberately.
const lightVars = `
  color-scheme:light;
  --bg:oklch(0.972 0.004 250); --panel:oklch(0.988 0.003 250); --ink:oklch(0.255 0.012 262);
  --muted:oklch(0.495 0.012 258); --line:oklch(0.895 0.008 255); --line-soft:oklch(0.930 0.006 255);
  --zebra:transparent;
  --accent:oklch(0.485 0.135 27); --accent-hover:oklch(0.44 0.15 26); --accent-soft:oklch(0.952 0.020 28);
  --code-bg:oklch(0.278 0.010 258); --code-ink:oklch(0.918 0.008 255);
  --code-inline:oklch(0.455 0.130 27); --code-line:oklch(0.400 0.014 258);
  --navy:oklch(0.420 0.085 245); --danger:oklch(0.520 0.165 28);
  --info-bg:oklch(0.945 0.020 235); --info-line:oklch(0.895 0.030 235); --info-ink:oklch(0.420 0.080 245);
  --shadow:oklch(0.280 0.010 262 / 0.14); --sel:oklch(0.912 0.045 28);
  --tok-key:oklch(0.72 0.14 26); --tok-fn:oklch(0.76 0.09 245); --tok-str:oklch(0.76 0.10 150); --tok-com:oklch(0.67 0.02 258);`

// darkVars is the dark counterpart: a cool near-neutral dark base (not black, not
// warm parchment), light cool ink, and the accent lifted to L~0.70 so the red
// reads on the dark ground without going salmon. Same two-step accent. Measured
// AA: accent-on-bg 6.05:1, ink 13.42:1, muted 6.70:1, accent-hover-on-panel
// 4.66:1 (all clear 4.5:1 on both surfaces).
const darkVars = `
  color-scheme:dark;
  --bg:oklch(0.225 0.008 255); --panel:oklch(0.263 0.009 255); --ink:oklch(0.918 0.006 252);
  --muted:oklch(0.712 0.010 255); --line:oklch(0.340 0.010 255); --line-soft:oklch(0.300 0.009 255);
  --zebra:oklch(0.263 0.009 255);
  --accent:oklch(0.700 0.130 26); --accent-hover:oklch(0.660 0.135 26); --accent-soft:oklch(0.320 0.045 26);
  --code-bg:oklch(0.180 0.008 258); --code-ink:oklch(0.905 0.008 255);
  --code-inline:oklch(0.760 0.105 27); --code-line:oklch(0.360 0.010 258);
  --navy:oklch(0.620 0.095 240); --danger:oklch(0.700 0.150 28);
  --info-bg:oklch(0.300 0.030 235); --info-line:oklch(0.400 0.040 235); --info-ink:oklch(0.780 0.060 235);
  --shadow:oklch(0.120 0.010 258 / 0.45); --sel:oklch(0.380 0.055 26);
  --tok-key:oklch(0.72 0.14 26); --tok-fn:oklch(0.76 0.09 245); --tok-str:oklch(0.76 0.10 150); --tok-com:oklch(0.62 0.02 258);`

// The named dark palettes below are faithful OKLCH conversions of the three
// developer color schemes demiplane's audience knows by name. Each was converted
// from its published sRGB/hex spec into OKLCH and mapped onto demiplane's token
// contract: the scheme's background→--bg, a raised surface→--panel, foreground→
// --ink, a brightened comment/subtext→--muted, the signature accent→--accent
// (dracula purple, catppuccin mauve, one-dark blue), selection→--sel. The
// palettes stay recognizable while obeying the house rules: neutrals carry the
// scheme's own hue (they already do — dracula ~278, catppuccin ~283, one-dark
// ~264), chroma stays modest at the lightness extremes, and there is no pure
// #000/#fff (none of the three specs use it).
//
// Accessibility: --ink and --muted clear WCAG AA (>=4.5:1) against BOTH --bg and
// --panel (dark zebra rows set body text on --panel). Where a faithful value failed
// that bar it was lifted and the deviation is noted:
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
  --muted:oklch(0.751 0.040 274); --line:oklch(0.404 0.032 280); --line-soft:oklch(0.324 0.032 282);
  --zebra:oklch(0.324 0.032 282);
  --accent:oklch(0.787 0.119 305); --accent-soft:oklch(0.345 0.050 305);
  --code-bg:oklch(0.216 0.025 284); --code-ink:oklch(0.879 0.043 272);
  --code-inline:oklch(0.787 0.119 305); --code-line:oklch(0.324 0.032 282);
  --navy:oklch(0.500 0.100 262); --danger:oklch(0.560 0.170 10);
  --info-bg:oklch(0.320 0.045 268); --info-line:oklch(0.400 0.050 266); --info-ink:oklch(0.820 0.070 265);
  --shadow:oklch(0.120 0.020 284 / 0.5); --sel:oklch(0.477 0.034 279);
  --tok-key:oklch(0.787 0.119 305); --tok-fn:oklch(0.766 0.111 260); --tok-str:oklch(0.858 0.109 143); --tok-com:oklch(0.618 0.037 276);`

// draculaVars is the Dracula palette: #282a36 base, purple accent, pink code,
// the classic #44475a current-line as the selection tint.
const draculaVars = `
  color-scheme:dark;
  --bg:oklch(0.288 0.022 278); --panel:oklch(0.345 0.026 278); --ink:oklch(0.977 0.008 107);
  --muted:oklch(0.715 0.050 272); --line:oklch(0.400 0.030 278); --line-soft:oklch(0.360 0.026 278);
  --zebra:oklch(0.345 0.026 278);
  --accent:oklch(0.742 0.149 302); --accent-soft:oklch(0.360 0.055 302);
  --code-bg:oklch(0.255 0.019 280); --code-ink:oklch(0.918 0.012 280);
  --code-inline:oklch(0.755 0.183 347); --code-line:oklch(0.360 0.026 278);
  --navy:oklch(0.500 0.100 252); --danger:oklch(0.560 0.170 25);
  --info-bg:oklch(0.320 0.045 270); --info-line:oklch(0.400 0.050 270); --info-ink:oklch(0.820 0.070 270);
  --shadow:oklch(0.150 0.020 278 / 0.5); --sel:oklch(0.403 0.032 278);
  --tok-key:oklch(0.755 0.183 347); --tok-fn:oklch(0.871 0.220 148); --tok-str:oklch(0.955 0.134 113); --tok-com:oklch(0.612 0.073 273);`

// oneDarkVars is the Atom One Dark palette: #282c34 base, blue accent, cool
// blue-grey neutrals; --ink uses the scheme's bright foreground for AA headroom.
const oneDarkVars = `
  color-scheme:dark;
  --bg:oklch(0.293 0.016 264); --panel:oklch(0.335 0.018 262); --ink:oklch(0.903 0.010 261);
  --muted:oklch(0.705 0.022 264); --line:oklch(0.386 0.024 266); --line-soft:oklch(0.335 0.018 262);
  --zebra:oklch(0.335 0.018 262);
  --accent:oklch(0.730 0.121 245); --accent-soft:oklch(0.345 0.045 245);
  --code-bg:oklch(0.263 0.013 258); --code-ink:oklch(0.903 0.010 261);
  --code-inline:oklch(0.730 0.121 245); --code-line:oklch(0.335 0.018 262);
  --navy:oklch(0.500 0.100 250); --danger:oklch(0.560 0.150 20);
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
// reader's light↔dark toggle. Only "light"/"dark" — the two faces of the default
// palette — participate in that toggle. See Pinned.
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
// scoped under `pre` so the short class names cannot collide with chrome classes,
// and the code slab is dark under every theme (--code-bg stays dark even in the
// light palette), so the bright token hues stay AA-legible everywhere.
//
// The scale is editorial: a 17px base on a 1.7 leading, headings on a roughly
// 1.25 modular scale with tightened tracking for a set-text feel, and varied
// vertical rhythm (a heading owes more space above than below). Motion is
// limited to color, border, and transform on an ease-out-expo curve; layout
// properties are never animated.
const contentCSS = `*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%;scroll-padding-top:6rem}
body{margin:0;background:var(--bg);color:var(--ink);
  font:1.0625rem/1.7 var(--sans);-webkit-font-smoothing:antialiased;text-rendering:optimizeLegibility}
::selection{background:var(--sel)}
.wrap{max-width:54rem;margin:0 auto;padding:0 1.3rem}
main{padding:2.2rem 0 3.5rem}
a{color:var(--accent);text-decoration:none;text-underline-offset:3px;
  text-decoration-thickness:1px;transition:color .2s cubic-bezier(.22,1,.36,1)}
a:hover,a:active{color:var(--accent-hover);text-decoration:underline}
:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
h1,h2,h3,h4,h5,h6{font-family:var(--serif);font-weight:600;color:var(--ink);
  line-height:1.15;letter-spacing:-.016em}
h1{font-size:2.55rem;margin:.3em 0 .35em}
h2{font-size:1.7rem;margin:2.1em 0 .55em;padding-bottom:.3em;border-bottom:1px solid var(--line)}
h3{font-size:1.32rem;margin:1.8em 0 .4em;letter-spacing:-.01em}
h4{font-size:1.15rem;margin:1.5em 0 .3em;letter-spacing:0}
h5{font-size:1.02rem;margin:1.5em 0 .3em;letter-spacing:0}
h6{font-size:.86rem;margin:1.5em 0 .3em;letter-spacing:.04em;text-transform:uppercase;color:var(--muted)}
.heading-anchor{margin-left:.35em;color:var(--muted);font-weight:400;text-decoration:none;
  opacity:0;user-select:none;transition:opacity .2s cubic-bezier(.22,1,.36,1)}
.heading-anchor:hover,.heading-anchor:active{color:var(--accent);text-decoration:none}
h1:hover .heading-anchor,h2:hover .heading-anchor,h3:hover .heading-anchor,
h4:hover .heading-anchor,h5:hover .heading-anchor,h6:hover .heading-anchor,
.heading-anchor:focus-visible{opacity:1}
@media (hover:none){.heading-anchor{opacity:.45}}
@media print{.heading-anchor{display:none}}
@media (prefers-reduced-motion:reduce){.heading-anchor{transition:none}}
p{margin:0 0 1.05em}
ul,ol{margin:0 0 1.1em;padding-left:1.45em}
li{margin:.32em 0}
li::marker{color:var(--accent)}
strong{font-weight:600}
code{font-family:var(--mono);font-size:.86em;background:var(--accent-soft);
  color:var(--code-inline);padding:.1em .38em;border-radius:4px}
pre{background:var(--code-bg);color:var(--code-ink);padding:1rem 1.15rem;border-radius:8px;
  overflow:auto;border:1px solid var(--code-line);font-size:.855rem;line-height:1.65;margin:1.4em 0}
pre code{background:none;color:inherit;padding:0;font-size:1em}
pre .k{color:var(--tok-key)}pre .fn{color:var(--tok-fn)}pre .s{color:var(--tok-str)}
pre .c{color:var(--tok-com);font-style:italic}
blockquote{margin:1.6em 0;padding:0 0 0 1.6em;border:none;background:none;
  font-family:var(--serif);font-style:italic;color:var(--ink)}
blockquote p:last-child{margin-bottom:0}
.table-wrap{overflow-x:auto;margin:1.6em 0}
.table-wrap>table{margin:0}
table{border-collapse:collapse;width:100%;margin:1.6em 0;font-size:.94rem}
thead th{border-bottom:1.5px solid var(--line);font-family:var(--sans);font-size:.72rem;
  font-weight:700;text-transform:uppercase;letter-spacing:.07em;color:var(--muted)}
th,td{text-align:left;padding:.55rem .8rem;border-bottom:1px solid var(--line-soft)}
tbody tr:nth-child(2n) td{background:var(--zebra)}
tbody tr:last-child td{border-bottom:none}
hr{border:none;border-top:1px solid var(--line);margin:2.6rem 0}
img{max-width:100%;height:auto}
@media (prefers-reduced-motion:reduce){a{transition:none}}
`

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

// ToggleCSS returns a stylesheet that ships BOTH token sets so a client can flip
// themes instantly with no server round-trip: the light tokens are the default
// (:root), and an `html[data-theme="dark"]` rule overrides them. The shared
// content typography follows. The fonts live only in :root (identical across
// themes), so the dark override carries colors alone — no token duplication.
func ToggleCSS() string {
	return ":root{" + lightVars + sharedFonts + "\n}\n" +
		`html[data-theme="dark"]{` + darkVars + "\n}\n" +
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
