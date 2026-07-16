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
	for _, marker := range []string{"--accent:oklch(0.485", "--bg:oklch(0.972", "var(--serif)", ".wrap{"} {
		if !strings.Contains(css, marker) {
			t.Errorf("CSS(light) missing %q", marker)
		}
	}
	dark := CSS("dark")
	if !strings.Contains(dark, "--bg:oklch(0.225") || strings.Contains(dark, "--bg:oklch(0.972") {
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
		if strings.Contains(css, "--bg:oklch(0.972") {
			t.Errorf("CSS(%q) leaked the default light background", c.name)
		}
		// Shared typography still rides along.
		if !strings.Contains(css, "var(--serif)") {
			t.Errorf("CSS(%q) lost the shared typography", c.name)
		}
	}
}

// TestDefaultPaletteRecolor pins the red-on-cool-neutral default: a two-step
// accent (--accent + --accent-hover), the cool-neutral background, and the
// keyboard-focus ring that the a11y pass added. Both faces carry the hover token.
func TestDefaultPaletteRecolor(t *testing.T) {
	light := CSS("light")
	for _, marker := range []string{
		"--accent:oklch(0.485 0.135 27)",
		"--accent-hover:oklch(0.44 0.15 26)",
		"--bg:oklch(0.972 0.004 250)",
		":focus-visible{outline:2px solid var(--accent)",
		"a:hover,a:active{color:var(--accent-hover)",
	} {
		if !strings.Contains(light, marker) {
			t.Errorf("CSS(light) missing %q", marker)
		}
	}
	dark := CSS("dark")
	for _, marker := range []string{
		"--accent:oklch(0.700 0.130 26)",
		"--accent-hover:oklch(0.660 0.135 26)",
		"--bg:oklch(0.225 0.008 255)",
	} {
		if !strings.Contains(dark, marker) {
			t.Errorf("CSS(dark) missing %q", marker)
		}
	}
	// The old warm-parchment default must be fully gone from both faces.
	for _, gone := range []string{"0.555 0.162 47", "0.972 0.009 78", "0.760 0.150 62"} {
		if strings.Contains(light+dark, gone) {
			t.Errorf("stale warm-parchment token %q still present", gone)
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

// TestZebraTokenPerFace is the m6 (demiplane-b09) decision: light-face table zebra
// is dropped (transparent — the row rules carry the structure), while every dark
// face keeps a real perceptible stripe via a dedicated --zebra token.
func TestZebraTokenPerFace(t *testing.T) {
	if !strings.Contains(Content(), "tbody tr:nth-child(2n) td{background:var(--zebra)}") {
		t.Error("zebra rule must reference the --zebra token")
	}
	// Light face: zebra off.
	if !strings.Contains(CSS("light"), "--zebra:transparent") {
		t.Error("light face should drop zebra (--zebra:transparent)")
	}
	// Every dark/pinned face defines a non-transparent --zebra step.
	for _, name := range []string{"dark", "catppuccin", "dracula", "one-dark"} {
		css := CSS(name)
		if !strings.Contains(css, "--zebra:oklch(") {
			t.Errorf("%s face should keep a real zebra step (--zebra:oklch(...))", name)
		}
	}
	// The toggle sheet flips zebra with the face: light root transparent, dark
	// override a real step.
	tog := ToggleCSS()
	if !strings.Contains(tog, "--zebra:transparent") || !strings.Contains(tog, "--zebra:oklch(") {
		t.Error("ToggleCSS must define --zebra in both the light root and the dark override")
	}
}

// TestCodeCommentContrastAA is the C1 fix: the code-comment ink (--tok-com) must
// clear WCAG AA (4.5:1) on its code slab (--code-bg) in BOTH faces, at normal
// size. The light face failed at 4.04:1 (L 0.62) and is lifted to L 0.67; the
// dark face keeps 0.62 and must not regress from its prior 5.17:1.
func TestCodeCommentContrastAA(t *testing.T) {
	light := CSS("light")
	lc := contrastRatio(tokenColor(t, light, "--tok-com"), tokenColor(t, light, "--code-bg"))
	if lc < 4.5 {
		t.Errorf("light code-comment contrast %.3f:1 < 4.5:1 (WCAG AA fail)", lc)
	}
	if lc < 4.85 || lc > 4.95 {
		t.Errorf("light code-comment contrast %.3f:1 drifted from the ~4.91:1 target", lc)
	}
	dark := CSS("dark")
	dc := contrastRatio(tokenColor(t, dark, "--tok-com"), tokenColor(t, dark, "--code-bg"))
	if dc < 4.5 {
		t.Errorf("dark code-comment contrast %.3f:1 < 4.5:1 (WCAG AA fail)", dc)
	}
	if dc < 5.16 {
		t.Errorf("dark code-comment contrast %.3f:1 regressed below its prior 5.17:1", dc)
	}
}

// TestHeadingScaleHierarchy is the M2/M3 fix: h4 sits ABOVE the 1.0625rem body,
// the modular scale stays monotonic down through h5/h6, and h5/h6 are styled at
// all (serif family + hover-reveal anchors), closing the a11y gap where the
// renderer emitted them but the theme did not.
func TestHeadingScaleHierarchy(t *testing.T) {
	c := Content()
	if !strings.Contains(c, "h4{font-size:1.15rem") {
		t.Errorf("h4 should be bumped above the 1.0625rem body:\n%s", c)
	}
	if strings.Contains(c, "h4{font-size:1.06rem") {
		t.Errorf("h4 still at the too-small 1.06rem")
	}
	for _, r := range []string{"h5{font-size:1.02rem", "h6{font-size:.86rem"} {
		if !strings.Contains(c, r) {
			t.Errorf("missing heading rule %q:\n%s", r, c)
		}
	}
	if !strings.Contains(c, "h1,h2,h3,h4,h5,h6{font-family:var(--serif)") {
		t.Errorf("h5/h6 not folded into the serif heading rule:\n%s", c)
	}
	if !strings.Contains(c, "h5:hover .heading-anchor,h6:hover .heading-anchor") {
		t.Errorf("h5/h6 anchors not added to the hover-reveal set:\n%s", c)
	}
	// Monotonic non-increasing scale: h2 > h3 > h4 > body >= h5 > h6.
	sizes := []float64{1.7, 1.32, 1.15, 1.02, 0.86}
	for i := 1; i < len(sizes); i++ {
		if sizes[i] >= sizes[i-1] {
			t.Errorf("heading scale not monotonic at index %d", i)
		}
	}
	if sizes[2] <= 1.0625 {
		t.Errorf("h4 (%.3f) must exceed the body 1.0625rem", sizes[2])
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
	if got := MetaColor("light"); got != "oklch(0.972 0.004 250)" {
		t.Errorf("MetaColor(light) = %q, want the light --bg", got)
	}
	if got := MetaColor("dracula"); got != "oklch(0.288 0.022 278)" {
		t.Errorf("MetaColor(dracula) = %q, want the dracula --bg", got)
	}
	if MetaColor("neon") != MetaColor(Default) {
		t.Errorf("MetaColor of an unknown theme should fall back to Default")
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
