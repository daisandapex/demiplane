// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package theme

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// --- OKLCH → sRGB → WCAG contrast, for the accessibility assertions below. ---

// oklchToLinearSRGB converts an OKLCH color to linear-light sRGB (the standard
// OKLab matrices), clamped to gamut.
func oklchToLinearSRGB(L, C, hDeg float64) (r, g, b float64) {
	h := hDeg * math.Pi / 180
	a := C * math.Cos(h)
	bb := C * math.Sin(h)
	l_ := L + 0.3963377774*a + 0.2158037573*bb
	m_ := L - 0.1055613458*a - 0.0638541728*bb
	s_ := L - 0.0894841775*a - 1.2914855480*bb
	l, m, s := l_*l_*l_, m_*m_*m_, s_*s_*s_
	r = 4.0767416621*l - 3.3077115913*m + 0.2309699292*s
	g = -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
	b = -0.0041960863*l - 0.7034186147*m + 1.7076147010*s
	clamp := func(x float64) float64 { return math.Max(0, math.Min(1, x)) }
	return clamp(r), clamp(g), clamp(b)
}

// relLuminance is the WCAG relative luminance of a linear-sRGB triple.
func relLuminance(r, g, b float64) float64 {
	return 0.2126*r + 0.7152*g + 0.0722*b
}

// contrastRatio is the WCAG 2.x contrast ratio between two OKLCH colors.
func contrastRatio(fg, bg [3]float64) float64 {
	l1 := relLuminance(oklchToLinearSRGB(fg[0], fg[1], fg[2]))
	l2 := relLuminance(oklchToLinearSRGB(bg[0], bg[1], bg[2]))
	hi, lo := math.Max(l1, l2), math.Min(l1, l2)
	return (hi + 0.05) / (lo + 0.05)
}

var reOKLCH = regexp.MustCompile(`oklch\(([-0-9.]+)\s+([-0-9.]+)\s+([-0-9.]+)`)

// tokenColor pulls the first oklch(...) value of a named CSS custom property out
// of a token block, as an OKLCH triple.
func tokenColor(t *testing.T, css, name string) [3]float64 {
	t.Helper()
	i := strings.Index(css, name+":")
	if i < 0 {
		t.Fatalf("token %q not found in CSS", name)
	}
	seg := css[i:]
	if e := strings.IndexByte(seg, ';'); e >= 0 {
		seg = seg[:e]
	}
	m := reOKLCH.FindStringSubmatch(seg)
	if m == nil {
		t.Fatalf("token %q has no oklch() value in %q", name, seg)
	}
	var out [3]float64
	for k := 0; k < 3; k++ {
		v, err := strconv.ParseFloat(m[k+1], 64)
		if err != nil {
			t.Fatalf("token %q component %d parse: %v", name, k, err)
		}
		out[k] = v
	}
	return out
}

func TestValid(t *testing.T) {
	for _, ok := range []string{"light", "dark", " dark "} {
		if !Valid(ok) {
			t.Errorf("Valid(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "neon", "solarized"} {
		if Valid(bad) {
			t.Errorf("Valid(%q) = true, want false", bad)
		}
	}
}

func TestCSSCarriesTokensAndContent(t *testing.T) {
	css := CSS("light")
	for _, marker := range []string{"--accent:oklch(0.445", "--bg:oklch(0.982", "var(--serif)", ".wrap{"} {
		if !strings.Contains(css, marker) {
			t.Errorf("CSS(light) missing %q", marker)
		}
	}
	dark := CSS("dark")
	if !strings.Contains(dark, "--bg:oklch(0.228") || strings.Contains(dark, "--bg:oklch(0.982") {
		t.Errorf("CSS(dark) did not swap the background token:\n%s", dark)
	}
	// Typography is shared, so both carry the same content block.
	if Content() == "" || !strings.Contains(css, Content()) || !strings.Contains(dark, Content()) {
		t.Errorf("both themes must embed the shared Content() typography")
	}
}

func TestUnknownThemeFallsBackToDefault(t *testing.T) {
	if Tokens("neon") != Tokens(Default) {
		t.Errorf("unknown theme should fall back to the default tokens")
	}
}

// TestNamedThemesRegistered asserts the three named developer palettes are valid,
// enumerated for help/flag validation, and reported as pinned; the default
// warm-parchment pair is not pinned.
func TestNamedThemesRegistered(t *testing.T) {
	for _, name := range []string{"catppuccin", "dracula", "one-dark"} {
		if !Valid(name) {
			t.Errorf("Valid(%q) = false, want true", name)
		}
		if !Pinned(name) {
			t.Errorf("Pinned(%q) = false, want true (named themes fix their palette)", name)
		}
		if !contains(Names, name) {
			t.Errorf("Names is missing %q: %v", name, Names)
		}
	}
	for _, name := range []string{"light", "dark", "", "neon"} {
		if Pinned(name) {
			t.Errorf("Pinned(%q) = true, want false", name)
		}
	}
}

// TestNamedThemePalettes asserts each named theme emits its own signature --bg
// and --accent tokens and does not leak the default light background — i.e. the
// palette is a real swap, not a fallback to the house style.
func TestNamedThemePalettes(t *testing.T) {
	cases := []struct{ name, bg, accent string }{
		{"catppuccin", "--bg:oklch(0.243 0.030 284)", "--accent:oklch(0.787 0.119 305)"},
		{"dracula", "--bg:oklch(0.288 0.022 278)", "--accent:oklch(0.742 0.149 302)"},
		{"one-dark", "--bg:oklch(0.293 0.016 264)", "--accent:oklch(0.730 0.121 245)"},
	}
	for _, c := range cases {
		css := CSS(c.name)
		if !strings.Contains(css, c.bg) {
			t.Errorf("CSS(%q) missing background %q", c.name, c.bg)
		}
		if !strings.Contains(css, c.accent) {
			t.Errorf("CSS(%q) missing accent %q", c.name, c.accent)
		}
		if strings.Contains(css, "--bg:oklch(0.982") {
			t.Errorf("CSS(%q) leaked the default light background", c.name)
		}
		// Shared typography still rides along.
		if !strings.Contains(css, "var(--serif)") {
			t.Errorf("CSS(%q) lost the shared typography", c.name)
		}
	}
}

// TestDefaultPaletteWarmPort pins the warm essay palette ported from the
// approved render=md design (entry j, 2026-08-30): oxblood accent, warm paper,
// near-white card panel, the keyboard-focus ring, and the underline-thickness
// link hover. The retired cool "rojo" palette and its dropped token names must
// be fully gone from both faces.
func TestDefaultPaletteWarmPort(t *testing.T) {
	light := CSS("light")
	for _, marker := range []string{
		"--accent:oklch(0.445 0.122 23)",
		"--bg:oklch(0.982 0.004 91)",
		"--panel:oklch(0.995 0.002 95)",
		"--line-strong:oklch(0.714 0.018 85)",
		":focus-visible{outline:2px solid var(--accent)",
		"a:hover,a:active{text-decoration-thickness:2px",
	} {
		if !strings.Contains(light, marker) {
			t.Errorf("CSS(light) missing %q", marker)
		}
	}
	dark := CSS("dark")
	for _, marker := range []string{
		"--accent:oklch(0.749 0.082 29)",
		"--bg:oklch(0.228 0.006 78)",
	} {
		if !strings.Contains(dark, marker) {
			t.Errorf("CSS(dark) missing %q", marker)
		}
	}
	for _, gone := range []string{
		"0.485 0.135 27", "0.972 0.004 250", "0.225 0.008 255",
		"--accent-hover", "--accent-soft", "--zebra", "--code-inline", "--line-soft",
	} {
		if strings.Contains(light+dark, gone) {
			t.Errorf("retired token %q still present", gone)
		}
	}
}

// TestSyntaxTokensPresentPerTheme asserts every theme (including the light/dark
// toggle sheet) defines the four --tok-* syntax-highlight tokens and that the
// content typography carries the token-class color rules (demiplane-rwj item 3).
// Without a token per theme, highlighted code would fall back to the code-ink
// color and lose its editor-pane read under that palette.
func TestSyntaxTokensPresentPerTheme(t *testing.T) {
	toks := []string{"--tok-key:", "--tok-fn:", "--tok-str:", "--tok-com:"}
	for _, name := range Names {
		css := CSS(name)
		for _, tk := range toks {
			if !strings.Contains(css, tk) {
				t.Errorf("CSS(%q) missing syntax token %q", name, tk)
			}
		}
	}
	// The toggle sheet ships both faces; the dark override must carry its own
	// tokens so a client theme flip recolors the code slab with no round-trip.
	tog := ToggleCSS()
	if strings.Count(tog, "--tok-key:") < 2 {
		t.Errorf("ToggleCSS must define --tok-key in both the light root and the dark override")
	}
	// The token-class color rules ride the shared typography.
	for _, rule := range []string{"pre .k{color:var(--tok-key)", "pre .fn{color:var(--tok-fn)",
		"pre .s{color:var(--tok-str)", "pre .c{color:var(--tok-com)"} {
		if !strings.Contains(Content(), rule) {
			t.Errorf("Content() missing token-class rule %q", rule)
		}
	}
}

// TestBlockquoteTypographicNoBox is the m5 (demiplane-aut) treatment: blockquotes
// are set as a serif-italic indent in --ink with NO box, NO fill, and (house
// refusal) NO side-stripe.
func TestBlockquoteTypographicNoBox(t *testing.T) {
	c := Content()
	i := strings.Index(c, "blockquote{")
	if i < 0 {
		t.Fatal("no blockquote rule in Content()")
	}
	rule := c[i : i+strings.IndexByte(c[i:], '}')]
	for _, want := range []string{"font-family:var(--serif)", "font-style:italic", "color:var(--ink)",
		"border:none", "background:none"} {
		if !strings.Contains(rule, want) {
			t.Errorf("blockquote rule missing %q:\n%s", want, rule)
		}
	}
	// No box, no fill, no muted ink, and (refusal) no left side-stripe.
	for _, gone := range []string{"border-radius", "background:var(--panel)", "color:var(--muted)",
		"border-left"} {
		if strings.Contains(rule, gone) {
			t.Errorf("blockquote rule must not contain %q (box/stripe refusal):\n%s", gone, rule)
		}
	}
}

// TestTableCardTreatment is the entry-j table decision: tables sit on a raised
// card (.table-wrap carries the panel surface, hairline border, and radius),
// rows are separated by hairlines instead of zebra stripes, numerals are
// tabular so figure columns align, and the header row is quiet weight with no
// uppercase run (the byline keeps letterspacing as a one-off).
func TestTableCardTreatment(t *testing.T) {
	c := Content()
	i := strings.Index(c, ".table-wrap{")
	if i < 0 {
		t.Fatal("no .table-wrap rule in Content()")
	}
	rule := c[i : i+strings.IndexByte(c[i:], '}')]
	for _, want := range []string{"background:var(--panel)", "border:1px solid var(--line)",
		"border-radius:8px", "overflow-x:auto"} {
		if !strings.Contains(rule, want) {
			t.Errorf("table-wrap rule missing %q:\n%s", want, rule)
		}
	}
	if strings.Contains(c, "--zebra") || strings.Contains(c, "nth-child(2n)") {
		t.Errorf("zebra striping must be gone; hairlines carry the row structure")
	}
	if !strings.Contains(c, "tbody tr+tr td{border-top:1px solid var(--line)}") {
		t.Errorf("rows should be separated by hairline top borders")
	}
	if !strings.Contains(c, "font-variant-numeric:tabular-nums") {
		t.Errorf("table cells should align numerals with tabular-nums")
	}
	th := strings.Index(c, "thead th{")
	thRule := c[th : th+strings.IndexByte(c[th:], '}')]
	if strings.Contains(thRule, "text-transform") || strings.Contains(thRule, "letter-spacing") {
		t.Errorf("table headers must not run uppercase/letterspaced:\n%s", thRule)
	}
}

// TestCodeTokenContrastAA: the code slab now follows its face (light slab on
// the light face), so every syntax token and the code ink must clear WCAG AA
// (4.5:1) against --code-bg in BOTH faces.
func TestCodeTokenContrastAA(t *testing.T) {
	for _, name := range []string{"light", "dark"} {
		css := CSS(name)
		slab := tokenColor(t, css, "--code-bg")
		for _, tk := range []string{"--code-ink", "--tok-key", "--tok-fn", "--tok-str", "--tok-com"} {
			if r := contrastRatio(tokenColor(t, css, tk), slab); r < 4.5 {
				t.Errorf("%s %s contrast %.3f:1 < 4.5:1 on --code-bg (WCAG AA fail)", name, tk, r)
			}
		}
	}
}

// TestHeadingScaleHierarchy pins the entry-j size-led hierarchy: a 34/24/19
// serif scale at weight 700, h4 above the 1rem body, monotonic down through
// h6, and h5/h6 still styled (serif family + hover-reveal anchors).
func TestHeadingScaleHierarchy(t *testing.T) {
	c := Content()
	for _, r := range []string{
		"h1{font-size:2.125rem", "h2{font-size:1.5rem", "h3{font-size:1.1875rem",
		"h4{font-size:1.0625rem", "h5{font-size:1rem", "h6{font-size:.875rem",
	} {
		if !strings.Contains(c, r) {
			t.Errorf("missing heading rule %q:\n%s", r, c)
		}
	}
	if !strings.Contains(c, "h1,h2,h3,h4,h5,h6{font-family:var(--serif)") {
		t.Errorf("headings not folded into the serif heading rule:\n%s", c)
	}
	if !strings.Contains(c, "h5:hover .heading-anchor,h6:hover .heading-anchor") {
		t.Errorf("h5/h6 anchors not in the hover-reveal set:\n%s", c)
	}
	// h2 is the hairline-topped section scan aid.
	if !strings.Contains(c, "border-top:1px solid var(--line)") {
		t.Errorf("h2 should carry the hairline top rule:\n%s", c)
	}
	// Monotonic scale, h4 above the 1rem body.
	sizes := []float64{2.125, 1.5, 1.1875, 1.0625, 1.0, 0.875}
	for i := 1; i < len(sizes); i++ {
		if sizes[i] >= sizes[i-1] {
			t.Errorf("heading scale not monotonic at index %d", i)
		}
	}
	if sizes[3] <= 1.0 {
		t.Errorf("h4 (%.4f) must exceed the 1rem body", sizes[3])
	}
}

// TestReadingRhythm pins the WCAG 2.2 SC 1.4.8 spacing figures: body leading at
// least 1.5, the baseline step across a paragraph break at least 1.5x the line
// step, headings spaced at least twice as far from the block above as from
// their own text, and inline code that cannot grow the line box.
func TestReadingRhythm(t *testing.T) {
	c := Content()
	num := func(re string) []float64 {
		m := regexp.MustCompile(re).FindStringSubmatch(c)
		if m == nil {
			t.Fatalf("no match for %q:\n%s", re, c)
		}
		var out []float64
		for _, s := range m[1:] {
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				t.Fatalf("parse %q: %v", s, err)
			}
			out = append(out, f)
		}
		return out
	}
	lh := num(`body\{[^}]*font:1rem/([0-9.]+) `)[0]
	if lh < 1.5 {
		t.Errorf("body line-height %.2f < 1.5 (SC 1.4.8)", lh)
	}
	para := num(`\np\{margin:0 0 ([0-9.]+)em`)[0]
	if (lh+para)/lh < 1.5 {
		t.Errorf("paragraph step %.2fem is under 1.5x the %.2f line step", lh+para, lh)
	}
	list := num(`ul,ol\{margin:0 0 ([0-9.]+)em`)[0]
	if list != para {
		t.Errorf("lists (%.3fem) and paragraphs (%.3fem) should share one block gap", list, para)
	}
	for _, h := range []string{"h3", "h4", "h5", "h6"} {
		m := num(h + `\{[^}]*margin:([0-9.]+)rem 0 ([0-9.]+)rem`)
		if m[0] < 2*m[1] {
			t.Errorf("%s space above %.3frem is not at least twice the %.3frem below", h, m[0], m[1])
		}
		if m[0] <= para {
			t.Errorf("%s space above %.3frem must exceed the %.3fem paragraph gap", h, m[0], para)
		}
	}
	h2 := num(`h2\{[^}]*padding-top:([0-9.]+)rem;margin:([0-9.]+)rem 0 ([0-9.]+)rem`)
	if h2[0]+h2[1] < 2*h2[2] {
		t.Errorf("h2 space above is not at least twice the space below: %v", h2)
	}
	code := regexp.MustCompile(`\ncode\{[^}]*\}`).FindString(c)
	for _, want := range []string{"line-height:1;", "border:0;", "background:color-mix(in oklab,var(--code-border) 45%,var(--code-bg))"} {
		if !strings.Contains(code, want) {
			t.Errorf("inline code rule missing %q: %s", want, code)
		}
	}
	if !strings.Contains(c, "pre code{background:none;border:0;color:inherit;padding:0;border-radius:0;font-size:1em;line-height:inherit}") {
		t.Errorf("code blocks must inherit the pre leading, not the chip's line-height:1:\n%s", c)
	}
	if strings.Contains(c, "text-align:justify") {
		t.Errorf("SC 1.4.8: text must not be justified")
	}
}

// TestColorSchemeAndMetaColor is the m2 fix: every pinned theme declares its
// color-scheme, ToggleCSS carries light in root + dark in the override, and
// MetaColor returns the palette's own --bg (never drifting).
func TestColorSchemeAndMetaColor(t *testing.T) {
	if !strings.Contains(CSS("light"), "color-scheme:light") {
		t.Errorf("light theme missing color-scheme:light")
	}
	for _, dark := range []string{"dark", "catppuccin", "dracula", "one-dark"} {
		if !strings.Contains(CSS(dark), "color-scheme:dark") {
			t.Errorf("theme %q missing color-scheme:dark", dark)
		}
	}
	tog := ToggleCSS()
	if !strings.Contains(tog, "color-scheme:light") || !strings.Contains(tog, "color-scheme:dark") {
		t.Errorf("ToggleCSS must carry light (root) and dark (override) color-scheme")
	}
	if got := MetaColor("light"); got != "oklch(0.982 0.004 91)" {
		t.Errorf("MetaColor(light) = %q, want the light --bg", got)
	}
	if got := MetaColor("dracula"); got != "oklch(0.288 0.022 278)" {
		t.Errorf("MetaColor(dracula) = %q, want the dracula --bg", got)
	}
	if MetaColor("neon") != MetaColor(Default) {
		t.Errorf("MetaColor of an unknown theme should fall back to Default")
	}
}

// TestToggleCSSThreeBlockContract pins the house light/dark contract shipped
// to toggle-capable pages: the complete light palette on bare :root, the dark
// palette under @media (prefers-color-scheme: dark) guarded as
// :root:not([data-theme="light"]), and the dark palette again under
// :root[data-theme="dark"], in that order, so an explicit data-theme always
// wins and the OS preference works with no JavaScript.
func TestToggleCSSThreeBlockContract(t *testing.T) {
	tog := ToggleCSS()
	iLight := strings.Index(tog, ":root{")
	iMedia := strings.Index(tog, "@media (prefers-color-scheme: dark){\n:root:not([data-theme=\"light\"]){")
	iOver := strings.Index(tog, `:root[data-theme="dark"]{`)
	if iLight != 0 || iMedia < 0 || iOver < 0 || !(iLight < iMedia && iMedia < iOver) {
		t.Fatalf("toggle sheet must carry light :root, guarded dark media block, explicit dark override, in order (got %d/%d/%d)", iLight, iMedia, iOver)
	}
	if n := strings.Count(tog, "--bg:oklch(0.228"); n != 2 {
		t.Errorf("dark tokens must appear exactly twice (guarded + explicit), got %d", n)
	}
	if strings.Count(tog, "color-scheme:light") != 1 || strings.Count(tog, "color-scheme:dark") != 2 {
		t.Errorf("color-scheme must be declared once per block")
	}
}

// TestWarmPaletteContrastAA measures the ported palette: body ink, muted text,
// and the accent must clear WCAG AA (4.5:1) on both the page and the card
// surface in both faces.
func TestWarmPaletteContrastAA(t *testing.T) {
	for _, name := range []string{"light", "dark"} {
		css := CSS(name)
		surfaces := map[string][3]float64{
			"--bg":    tokenColor(t, css, "--bg"),
			"--panel": tokenColor(t, css, "--panel"),
		}
		for _, tk := range []string{"--ink", "--muted", "--accent"} {
			fg := tokenColor(t, css, tk)
			for surf, sc := range surfaces {
				if r := contrastRatio(fg, sc); r < 4.5 {
					t.Errorf("%s %s on %s: %.3f:1 < 4.5:1 (WCAG AA fail)", name, tk, surf, r)
				}
			}
		}
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
