// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"strings"
	"testing"
)

// TestHighlightPerLanguage checks each supported language colors its keywords,
// strings, comments, and function calls with the right token class, and that the
// original code text survives inside the spans.
func TestHighlightPerLanguage(t *testing.T) {
	cases := []struct {
		lang string
		src  string
		want []string // substrings that must appear in the highlighted HTML
	}{
		{"go",
			"// c\nfunc serve() string {\n\treturn \"hi\"\n}",
			[]string{`<span class="c">// c</span>`, `<span class="k">func</span>`,
				`<span class="fn">serve</span>`, `<span class="k">return</span>`, `<span class="s">&#34;hi&#34;</span>`}},
		{"bash",
			"# note\nif true; then\n  echo \"x\"\nfi",
			[]string{`<span class="c"># note</span>`, `<span class="k">if</span>`,
				`<span class="k">then</span>`, `<span class="s">&#34;x&#34;</span>`, `<span class="k">fi</span>`}},
		{"json",
			"{\n  \"a\": true,\n  \"b\": null\n}",
			// Object keys (string then ':') color as .fn/property, not .s/string.
			[]string{`<span class="fn">&#34;a&#34;</span>`, `<span class="k">true</span>`, `<span class="k">null</span>`}},
		{"yaml",
			"# cfg\nname: \"x\"\nenabled: true",
			[]string{`<span class="c"># cfg</span>`, `<span class="s">&#34;x&#34;</span>`, `<span class="k">true</span>`}},
		{"python",
			"# doc\ndef go(x):\n    return \"y\"",
			[]string{`<span class="c"># doc</span>`, `<span class="k">def</span>`,
				`<span class="fn">go</span>`, `<span class="k">return</span>`, `<span class="s">&#34;y&#34;</span>`}},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			got := highlight(tc.lang, tc.src)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("highlight(%s) missing %q\ngot: %s", tc.lang, w, got)
				}
			}
		})
	}
}

// TestHighlightBashVarsAndBuiltins covers demiplane-wsy: the bash highlighter
// colors shell variables ($VAR / ${VAR} / $1 / $?) and common builtins/commands
// (curl, echo, …) as .fn, on top of the existing control keywords/strings/comments.
func TestHighlightBashVarsAndBuiltins(t *testing.T) {
	// Variables sit OUTSIDE string literals — a double-quoted string is scanned as
	// one .s token (no interpolation), which is the intended restraint.
	src := "curl -sf $URL | grep $1 # fetch\ncd ${HOME}\nexit $?"
	got := highlight("bash", src)
	for _, w := range []string{
		`<span class="fn">curl</span>`,    // command
		`<span class="fn">grep</span>`,    // builtin
		`<span class="fn">$URL</span>`,    // simple variable
		`<span class="fn">${HOME}</span>`, // braced variable
		`<span class="fn">$1</span>`,      // positional
		`<span class="fn">$?</span>`,      // special ($?)
		`<span class="fn">cd</span>`,      // builtin
		`<span class="c"># fetch</span>`,  // comment still colored
	} {
		if !strings.Contains(got, w) {
			t.Errorf("bash highlight missing %q\ngot: %s", w, got)
		}
	}
	// A control keyword stays .k (not recolored as a command).
	if kw := highlight("bash", "for x in a; do echo $x; done"); !strings.Contains(kw, `<span class="k">for</span>`) {
		t.Errorf("control keyword should stay .k:\n%s", kw)
	}
	// Non-shell languages must NOT treat '$' as a variable sigil.
	if py := highlight("python", "cost = $5"); strings.Contains(py, `<span class="fn">$5</span>`) {
		t.Errorf("python must not color $ as a shell variable:\n%s", py)
	}
	// Round-trip: variable/builtin scanning preserves every source byte.
	if stripped := stripSpanTags(got); stripped != escapeForCompare(src) {
		t.Errorf("bash highlighter altered source text:\n got: %q\nwant: %q", stripped, escapeForCompare(src))
	}
}

// TestHighlightJSONKeyValueDistinct is the M4 fix: a JSON object key (a string
// immediately followed by ':') colors as a property (.fn), while a string VALUE
// stays .s — so keys and values are visually distinct instead of both green.
func TestHighlightJSONKeyValueDistinct(t *testing.T) {
	got := highlight("json", "{\n  \"key\": \"value\"\n}")
	if !strings.Contains(got, `<span class="fn">&#34;key&#34;</span>`) {
		t.Errorf("JSON key should be a property span (.fn):\n%s", got)
	}
	if !strings.Contains(got, `<span class="s">&#34;value&#34;</span>`) {
		t.Errorf("JSON string value should stay a string span (.s):\n%s", got)
	}
	// A key with spaces before the colon is still a key.
	spaced := highlight("json", "{ \"k\" : 1 }")
	if !strings.Contains(spaced, `<span class="fn">&#34;k&#34;</span>`) {
		t.Errorf("key with whitespace before ':' should still color as property:\n%s", spaced)
	}
	// A trailing string with no following colon must remain a value.
	tail := highlight("json", "[\"a\", \"b\"]")
	if strings.Contains(tail, `<span class="fn">`) {
		t.Errorf("array string elements are values, not keys:\n%s", tail)
	}
}

// TestHighlightAliases folds common language aliases onto the canonical spec.
func TestHighlightAliases(t *testing.T) {
	for _, a := range []string{"golang", "sh", "shell", "py", "yml"} {
		if canonLang(a) == "" {
			t.Errorf("canonLang(%q) should resolve to a supported language", a)
		}
	}
	if canonLang("ruby") != "" || canonLang("") != "" {
		t.Errorf("unsupported languages must resolve to empty")
	}
	// A fence info-string with extra tokens still resolves on its first word.
	if canonLang("go title=x") != "go" {
		t.Errorf("first info-string token should select the language")
	}
}

// TestHighlightUnsupportedIsPlainEscaped is the security-critical case: an
// unsupported language is rendered as plain, HTML-escaped text — no spans, no
// injection.
func TestHighlightUnsupportedIsPlainEscaped(t *testing.T) {
	got := highlight("ruby", "puts \"<script>\"")
	if strings.Contains(got, "<span") {
		t.Errorf("unsupported language must not emit token spans: %s", got)
	}
	if strings.Contains(got, "<script>") || !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("unsupported language must HTML-escape its source: %s", got)
	}
}

// TestHighlightEscapesInsideTokens guards that raw HTML in code cannot inject even
// when it falls inside a classed token (a string, comment, or the plain run).
func TestHighlightEscapesInsideTokens(t *testing.T) {
	for _, lang := range []string{"go", "bash", "python", "json", "yaml"} {
		got := highlight(lang, "x := \"<img onerror=alert(1)>\" // <b>c</b>")
		if strings.Contains(got, "<img") || strings.Contains(got, "<b>") {
			t.Errorf("highlight(%s) leaked raw HTML: %s", lang, got)
		}
	}
}

// TestHighlightRoundTripsText verifies the highlighter preserves every character
// of the source (stripping the tags it adds returns the escaped original), so the
// code slab is never silently corrupted.
func TestHighlightRoundTripsText(t *testing.T) {
	src := "func f() {\n\ts := \"a\\\"b\" // t\n\treturn s\n}"
	got := highlight("go", src)
	stripped := stripSpanTags(got)
	if want := escapeForCompare(src); stripped != want {
		t.Errorf("highlighter altered source text:\n got: %q\nwant: %q", stripped, want)
	}
}

// stripSpanTags removes only the <span…>/</span> wrappers the highlighter adds.
func stripSpanTags(s string) string {
	s = strings.ReplaceAll(s, "</span>", "")
	for {
		i := strings.Index(s, "<span")
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			break
		}
		s = s[:i] + s[i+j+1:]
	}
	return s
}

// escapeForCompare mirrors html.EscapeString for the round-trip assertion.
func escapeForCompare(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&#34;", "'", "&#39;")
	return r.Replace(s)
}
