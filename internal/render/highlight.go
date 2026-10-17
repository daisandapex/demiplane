// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"context"
	"html"
	"strings"
)

// highlight.go is demiplane's dependency-free, server-side syntax highlighter for
// fenced code blocks (demiplane-rwj item 3). It tokenizes a small set of the
// languages demiplane's audience actually ships — go, bash, json, yaml, python —
// into classed <span>s (.k keyword, .fn function, .s string, .c comment) that the
// theme's --tok-* tokens color, so the inverted code slab reads like a real
// editor pane under every palette. Anything else (unknown or empty language)
// falls back to plain, HTML-escaped text, exactly as before.
//
// It is a single shared scanner driven by a per-language spec, not a full
// grammar: enough to color the common shape of a snippet, never a parser. The one
// hard invariant is security — the source is served as text/html same-origin, so
// EVERY byte of the input is HTML-escaped on the way out (inside a span or in a
// plain run), and the class names are compile-time constants. Raw HTML in a code
// block can never inject.

// langSpec configures the shared scanner for one language.
type langSpec struct {
	lineComment  string          // line-comment marker ("//" | "#"); "" = none
	blockComment bool            // C-style /* … */ comments (go)
	strDelims    string          // opening string delimiters, e.g. "\"`" or "\"'"
	rawDelims    string          // subset of strDelims with NO backslash escaping
	keywords     map[string]bool // reserved words colored as .k
	builtins     map[string]bool // commands/builtins colored as .fn (e.g. curl, echo)
	dollarVars   bool            // color shell variables ($VAR / ${VAR} / $1 / $?) as .fn
}

// kw builds a keyword set from a space-separated list.
func kw(words string) map[string]bool {
	m := make(map[string]bool)
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

// Keyword lists. These are the reserved words (plus the canonical predeclared
// literals nil/true/false/None/…) that read as keywords in an editor; they are
// intentionally not exhaustive type/builtin lists — coloring every stdlib name
// would make the slab noisier, not more readable.
var (
	goKeywords = kw(`break case chan const continue default defer else fallthrough for func go goto ` +
		`if import interface map package range return select struct switch type var nil true false iota`)
	bashKeywords = kw(`if then else elif fi for while until do done case esac in function select time ` +
		`return break continue local export readonly declare set unset source`)
	// bashBuiltins are the common shell builtins and everyday commands that read as
	// the "verbs" of a snippet — colored .fn (the command/function hue) so they
	// stand apart from the control keywords (.k) without turning the scanner into a
	// grammar. Curated, not exhaustive: coloring every binary on PATH would make the
	// slab noisier, not more readable (demiplane-wsy asks for restraint).
	bashBuiltins = kw(`echo printf read cd pwd pushd popd eval exec exit test kill sleep wait trap ` +
		`curl wget cat head tail grep egrep sed awk cut tr sort uniq wc tee xargs jq ` +
		`mkdir rmdir rm cp mv ln touch chmod chown find ls env which`)
	pyKeywords = kw(`def class return if elif else for while import from as pass break continue with ` +
		`try except finally raise yield lambda global nonlocal in is not and or del assert async await ` +
		`None True False`)
)

// langs maps a canonical language name to its spec. canonLang folds aliases in.
var langs = map[string]*langSpec{
	"go":     {lineComment: "//", blockComment: true, strDelims: "\"`", rawDelims: "`", keywords: goKeywords},
	"bash":   {lineComment: "#", strDelims: "\"'", rawDelims: "'", keywords: bashKeywords, builtins: bashBuiltins, dollarVars: true},
	"json":   {strDelims: "\"", keywords: kw("true false null")},
	"yaml":   {lineComment: "#", strDelims: "\"'", rawDelims: "'", keywords: kw("true false null yes no on off")},
	"python": {lineComment: "#", strDelims: "\"'", keywords: pyKeywords},
}

// canonLang folds the language fence's info-string to a supported spec key,
// accepting the common aliases. An unsupported or empty language returns "".
func canonLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	// A fence info-string may carry more than the language (```go title=x); the
	// first token is the language.
	if i := strings.IndexAny(l, " \t"); i >= 0 {
		l = l[:i]
	}
	switch l {
	case "go", "golang":
		return "go"
	case "bash", "sh", "shell", "zsh", "console":
		return "bash"
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "python", "py":
		return "python"
	}
	return ""
}

// highlight tokenizes src for lang into HTML with classed token spans, with no
// output limit (trusted callers and tests). See highlightTo.
func highlight(lang, src string) string {
	out, _ := highlightTo(newState(context.Background(), unlimited), unlimited.MaxOutputBytes, lang, src)
	return out
}

// highlightChecks is how many scanner steps pass between context checks.
const highlightChecks = 4096

// escapeChunk is the slice of an unhighlighted block escaped at a time.
const escapeChunk = 64 << 10

// highlightTo tokenizes src for lang into HTML with classed token spans. For an
// unsupported language it returns src HTML-escaped and unstyled. Every code
// byte is escaped on output, so the result is always injection-safe.
//
// Highlighting can expand a block more than tenfold (every call token becomes
// a span), so the output is held to limit bytes before anything is built: a
// first pass only counts the output, stopping with a budget error at the
// first step past limit, and a second pass writes into a buffer of exactly
// that size. st's context is checked every highlightChecks steps of either
// pass.
func highlightTo(st *state, limit int, lang, src string) (string, error) {
	spec := langs[canonLang(lang)]
	if spec == nil {
		n := escapedLen(src)
		if n > limit {
			return "", over(st)
		}
		var b strings.Builder
		b.Grow(n)
		for len(src) > 0 {
			k := min(len(src), escapeChunk)
			b.WriteString(html.EscapeString(src[:k]))
			src = src[k:]
			if err := st.ctxErr(); err != nil {
				return "", err
			}
		}
		return b.String(), nil
	}
	count := &emitter{}
	if err := scanHighlight(st, spec, src, count, limit); err != nil {
		return "", err
	}
	var b strings.Builder
	b.Grow(count.n)
	if err := scanHighlight(st, spec, src, &emitter{b: &b}, limit); err != nil {
		return "", err
	}
	return b.String(), nil
}

// over is the budget error for highlighted output past its limit.
func over(st *state) error {
	return &BudgetError{Limit: "output bytes", Max: st.lim.MaxOutputBytes}
}

// emitter receives highlighted output. With b nil it only counts the bytes
// that would be written, so a pass can size or refuse the output without
// building it.
type emitter struct {
	b *strings.Builder
	n int
}

// raw emits markup that needs no escaping.
func (e *emitter) raw(s string) {
	e.n += len(s)
	if e.b != nil {
		e.b.WriteString(s)
	}
}

// esc emits source text HTML-escaped.
func (e *emitter) esc(s string) {
	if e.b == nil {
		e.n += escapedLen(s)
		return
	}
	before := e.b.Len()
	e.b.WriteString(html.EscapeString(s))
	e.n += e.b.Len() - before
}

// scanHighlight runs the shared scanner for spec over src into e, failing once
// the output passes limit or st's context is done.
func scanHighlight(st *state, spec *langSpec, src string, e *emitter, limit int) error {
	span := func(class, s string) {
		e.raw(`<span class="`)
		e.raw(class)
		e.raw(`">`)
		e.esc(s)
		e.raw(`</span>`)
	}

	n := len(src)
	i := 0
	plainStart := 0 // start of the pending run of unclassified bytes
	flush := func(upto int) {
		if upto > plainStart {
			e.esc(src[plainStart:upto])
		}
	}

	for steps := 1; i < n; steps++ {
		if e.n > limit {
			return over(st)
		}
		if steps%highlightChecks == 0 {
			if err := st.ctxErr(); err != nil {
				return err
			}
		}
		c := src[i]

		// Line comment: consume to end of line.
		if spec.lineComment != "" && strings.HasPrefix(src[i:], spec.lineComment) {
			flush(i)
			j := i + len(spec.lineComment)
			for j < n && src[j] != '\n' {
				j++
			}
			span("c", src[i:j])
			i, plainStart = j, j
			continue
		}

		// Block comment /* … */ (go). An unterminated block runs to EOF.
		if spec.blockComment && c == '/' && i+1 < n && src[i+1] == '*' {
			flush(i)
			j := i + 2
			if end := strings.Index(src[j:], "*/"); end >= 0 {
				j += end + 2
			} else {
				j = n
			}
			span("c", src[i:j])
			i, plainStart = j, j
			continue
		}

		// String literal. Raw delimiters (go backtick, single quotes) take no
		// backslash escapes; the rest do. A non-raw string is also terminated by an
		// unescaped newline so a stray quote cannot swallow the rest of the block.
		if strings.IndexByte(spec.strDelims, c) >= 0 {
			flush(i)
			raw := strings.IndexByte(spec.rawDelims, c) >= 0
			j := i + 1
			for j < n {
				if !raw && src[j] == '\\' && j+1 < n {
					j += 2
					continue
				}
				if src[j] == c {
					j++
					break
				}
				if !raw && src[j] == '\n' {
					break
				}
				j++
			}
			// A string immediately followed (past spaces) by a ':' is a property
			// key, not a value — the object-key shape in JSON/YAML. Color it as a
			// property (the .fn/blue token) so keys read distinctly from string
			// values (.s/green), instead of both being the same string color.
			if nextNonSpaceIsColon(src, j) {
				span("fn", src[i:j])
			} else {
				span("s", src[i:j])
			}
			i, plainStart = j, j
			continue
		}

		// Shell variable: $NAME, ${...}, or a special ($?, $$, $1, $@, …). Colored
		// .fn so a $VAR reads distinctly from the plain run around it. Only shells
		// opt in (dollarVars); other languages treat '$' as an ordinary byte.
		if spec.dollarVars && c == '$' {
			j := i + 1
			switch {
			case j < n && src[j] == '{':
				j++
				for j < n && src[j] != '}' && src[j] != '\n' {
					j++
				}
				if j < n && src[j] == '}' {
					j++ // include the closing brace
				}
			case j < n && isIdentStart(src[j]):
				for j < n && isIdentPart(src[j]) {
					j++
				}
			case j < n && isSpecialVar(src[j]):
				j++ // $?, $$, $!, $@, $*, $#, $-, $0-$9
			}
			// Only color when a sigil actually introduced a variable; a lone '$'
			// (e.g. before a command substitution '$(') stays in the plain run.
			if j > i+1 {
				flush(i)
				span("fn", src[i:j])
				plainStart = j
			}
			i = j
			continue
		}

		// Identifier / keyword / builtin / function call.
		if isIdentStart(c) {
			j := i + 1
			for j < n && isIdentPart(src[j]) {
				j++
			}
			word := src[i:j]
			switch {
			case spec.keywords[word]:
				flush(i)
				span("k", word)
				plainStart = j
			case spec.builtins[word]:
				flush(i)
				span("fn", word)
				plainStart = j
			case nextNonSpaceIsParen(src, j):
				flush(i)
				span("fn", word)
				plainStart = j
			}
			// Non-keyword, non-call identifiers stay in the pending plain run.
			i = j
			continue
		}

		i++
	}
	flush(n)
	if e.n > limit {
		return over(st)
	}
	return nil
}

// nextNonSpaceIsColon reports whether the first non-space/tab byte at or after i
// is ':' — used to tell an object key (a string followed by a colon) from a
// string value. Like nextNonSpaceIsParen it does not cross a newline: a key and
// its ':' sit on the same line.
func nextNonSpaceIsColon(src string, i int) bool {
	for i < len(src) {
		switch src[i] {
		case ' ', '\t':
			i++
		case ':':
			return true
		default:
			return false
		}
	}
	return false
}

// isSpecialVar reports whether c is a shell special-parameter character that
// follows '$' — $?, $$, $!, $@, $*, $#, $-, and the positionals $0-$9.
func isSpecialVar(c byte) bool {
	switch c {
	case '?', '$', '!', '@', '*', '#', '-':
		return true
	}
	return c >= '0' && c <= '9'
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// nextNonSpaceIsParen reports whether the first non-space/tab byte at or after i
// is '(', i.e. the just-scanned identifier is being called — the cheap, reliable
// heuristic for a function name (fn) that works across go/python/json without a
// grammar. It deliberately does not cross a newline: a bare name on its own line
// is not a call.
func nextNonSpaceIsParen(src string, i int) bool {
	for i < len(src) {
		switch src[i] {
		case ' ', '\t':
			i++
		case '(':
			return true
		default:
			return false
		}
	}
	return false
}
