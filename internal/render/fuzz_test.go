// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"context"
	"errors"
	"html"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/daisandapex/demiplane/v3"
)

// fuzzRenderBound is the longest one fuzz input may take to render. Inputs
// stay small under the fuzzer, so this is generous even with coverage
// instrumentation; a render past it is a hang or a quadratic path.
const fuzzRenderBound = 10 * time.Second

// markdownSeeds are documents from the render tests and earlier escaping and
// link fixes, the corpus every markdown target starts from.
var markdownSeeds = []string{
	"",
	"# Title\n\nbody",
	"## \"><script>alert(1)</script>",
	"<script>alert(1)</script>",
	"<img src=x onerror=alert(1)>",
	"[x](javascript:alert(1))",
	"[x](JaVaScRiPt:alert(1))",
	"[x](java\tscript:alert(1))",
	"[x](data:text/html,<b>x</b>)",
	"[x](vbscript:msgbox)",
	"[x](//attacker.com)",
	"[x](/\\attacker.com)",
	"[x](https://trusted.example@attacker.com/)",
	"[x](https:////trusted.example@attacker.com/)",
	"[x](https://a&#34;@b)",
	"[x](&#106;avascript:alert(1))",
	"[x](https://a\"onmouseover=\"x)",
	"[a](/reports) [b](#anchor) [c](?q=1) [d](page.html) [e](mailto:a@b.com)",
	"**bold** *it* _u_ `code` snake_case_word",
	"---\ntitle: T\ndate: 2026-01-02\n---\n# H\n",
	"---\ntitle: <script>x</script>\nauthor: \" onload=\"x\n---\nbody",
	"| a | b |\n|---|:-:|\n| 1 | 2 |\n",
	"- a\n  - b\n    - c\n1. one\n2. two\n",
	"> quote\n> more",
	"```go\nfunc main() { fmt.Println(\"<x>\") }\n```",
	"```bash\necho \"$HOME\" # c\n```",
	"```python\ndef f(): return '</span>'\n```",
	"```json\n{\"a\": [1, \"<b>\"]}\n```",
	"```yaml\nk: v # c\n```",
	"```javascript\nalert(1)\n```",
	"***\n___\n- - -",
	"line\r\nwith crlf\r\n",
}

// knownScripts collects the script elements and tags a render may
// legitimately carry: the ones the renderer's own chrome produces for documents with no hostile
// content. A fuzzed document must never yield a script outside this set.
func knownScripts(t testing.TB, opts Options) map[string]bool {
	known := map[string]bool{}
	for _, src := range []string{
		"",
		"# T\n\nbody",
		"---\ntitle: T\ndate: 2026-01-02\n---\nbody",
		"---\ntitle: T\ndate: 2026-01-02T10:00:00Z\nupdated: 2026-01-03\n---\nbody",
		"## a\n\n## b\n\n## c\n\n## d\n",
	} {
		out, err := Render(context.Background(), []byte(src), opts)
		if err != nil {
			t.Fatalf("baseline render: %v", err)
		}
		for _, s := range scriptRe.FindAllString(string(out), -1) {
			known[s] = true
		}
		// Chrome icons (inline SVG) and the like are known by their exact tag.
		for _, tag := range tagRe.FindAllString(string(out), -1) {
			known[tag] = true
		}
	}
	return known
}

var (
	scriptRe = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	// tagRe finds the tags in rendered output. The renderer escapes every
	// '<' of the source, so each match is markup the renderer wrote.
	tagRe = regexp.MustCompile(`<[a-zA-Z][^>]*>`)
	// attrRe finds attribute names inside a tag.
	attrRe  = regexp.MustCompile(`\s([^\s"'=<>/]+)\s*=`)
	hrefRe  = regexp.MustCompile(`(?i)\s(?:href|src|action|formaction|xlink:href)\s*=\s*"([^"]*)"`)
	nameTag = regexp.MustCompile(`^<([a-zA-Z][a-zA-Z0-9-]*)`)
)

// forbiddenTags are elements the markdown renderer never writes; one in the
// output means source markup survived escaping.
var forbiddenTags = map[string]bool{
	"iframe": true, "object": true, "embed": true, "svg": true, "math": true,
	"img": true, "base": true, "frame": true, "frameset": true, "applet": true,
	"video": true, "audio": true, "source": true, "link": false, "meta": false,
	"script": false, // checked against knownScripts instead
}

// checkLinkTarget fails when a link destination the renderer accepted or
// emitted could navigate to an active scheme or to another host than it
// names. raw is the attribute value as written; the browser decodes its
// entities once, and that decoded value is judged as it stands: nothing is
// stripped first, so a tab, newline or other control character anywhere in
// it is itself a failure, as SECURITY.md promises such a destination is
// never a link.
func checkLinkTarget(t testing.TB, raw string) {
	t.Helper()
	dec := html.UnescapeString(raw)
	for _, v := range []string{raw, dec} {
		for _, r := range v {
			if unicode.IsControl(r) {
				t.Fatalf("link %q carries control character %U", raw, r)
			}
		}
	}
	if strings.ContainsRune(dec, '\\') {
		t.Fatalf("link %q contains a backslash", raw)
	}
	// Only spaces are left for the browser's URL parser to trim.
	u := strings.ToLower(strings.Trim(dec, " "))
	if strings.HasPrefix(u, "//") {
		t.Fatalf("link %q is protocol-relative", raw)
	}
	scheme := ""
	if i := strings.IndexByte(u, ':'); i >= 0 {
		if j := strings.IndexAny(u, "/?#"); j < 0 || i < j {
			scheme = u[:i]
		}
	}
	switch scheme {
	case "", "http", "https", "mailto":
	case "data":
		// The one data: URI allowed is the favicon the chrome writes
		// itself, byte for byte.
		if dec != demiplane.FaviconDataURI {
			t.Fatalf("link %q is a data: URI other than the favicon", raw)
		}
	default:
		t.Fatalf("link %q has scheme %q", raw, scheme)
	}
	if scheme == "http" || scheme == "https" {
		rest := strings.TrimLeft(u[len(scheme)+1:], "/")
		if k := strings.IndexAny(rest, "/?#"); k >= 0 {
			rest = rest[:k]
		}
		if strings.Contains(rest, "@") {
			t.Fatalf("link %q carries userinfo", raw)
		}
	}
}

// checkRenderedHTML asserts the sanitiser's promises on one rendered page.
func checkRenderedHTML(t testing.TB, out string, known map[string]bool) {
	t.Helper()
	for _, s := range scriptRe.FindAllString(out, -1) {
		if !known[s] {
			t.Fatalf("render produced a script the chrome does not: %q", s)
		}
	}
	// Strip the known scripts, then no other script tag may remain.
	rest := scriptRe.ReplaceAllString(out, "")
	if strings.Contains(strings.ToLower(rest), "<script") {
		t.Fatalf("render left an unterminated or unknown script tag")
	}
	for _, tag := range tagRe.FindAllString(rest, -1) {
		if m := nameTag.FindStringSubmatch(tag); m != nil && forbiddenTags[strings.ToLower(m[1])] && !known[tag] {
			t.Fatalf("render wrote element %q", tag)
		}
		for _, a := range attrRe.FindAllStringSubmatch(tag, -1) {
			if strings.HasPrefix(strings.ToLower(a[1]), "on") {
				t.Fatalf("render wrote an event handler attribute in %q", tag)
			}
			if strings.EqualFold(a[1], "style") && strings.Contains(strings.ToLower(tag), "expression(") {
				t.Fatalf("render wrote a script-bearing style in %q", tag)
			}
		}
		for _, h := range hrefRe.FindAllStringSubmatch(tag, -1) {
			checkLinkTarget(t, h[1])
		}
	}
}

// FuzzRender renders hostile markdown on the publish path and checks what
// SECURITY.md promises of it: source HTML is escaped, links are allowlisted,
// limits hold and a render finishes in bounded time.
func FuzzRender(f *testing.F) {
	for _, s := range markdownSeeds {
		f.Add([]byte(s), uint8(0))
		f.Add([]byte(s), uint8(0xff))
	}
	optsFor := func(mode uint8) Options {
		o := Options{
			Title:      "fuzz",
			Header:     mode&1 != 0,
			MetaHeader: mode&2 != 0,
			Colophon:   mode&4 != 0,
			Slug:       "fuzz-a",
			Published:  time.Unix(1_700_000_000, 0).UTC(),
			Siblings:   []string{"fuzz-b", "fuzz-c"},
		}
		if mode&8 != 0 {
			o.ReplySlug, o.ReplyToken, o.ReplyNext = "fuzz-a", "tok", "fuzz-b"
		}
		if mode&16 != 0 {
			o.ContentBase, o.IndexURL = "http://127.0.0.1:8081", "http://127.0.0.1:8080/gallery"
		}
		return o
	}
	known := map[uint8]map[string]bool{}
	f.Fuzz(func(t *testing.T, src []byte, mode uint8) {
		mode &= 31
		opts := optsFor(mode)
		k, ok := known[mode]
		if !ok {
			k = knownScripts(t, opts)
			known[mode] = k
		}
		ctx, cancel := context.WithTimeout(context.Background(), fuzzRenderBound)
		defer cancel()
		out, err := Render(ctx, src, opts)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("render did not finish within %v", fuzzRenderBound)
			}
			if !errors.Is(err, ErrBudget) {
				t.Fatalf("render failed with a non-budget error: %v", err)
			}
			return
		}
		checkRenderedHTML(t, string(out), k)

		// A tight output budget is never exceeded: the page fits or the
		// render is refused.
		opts.Limits = Limits{MaxOutputBytes: len(out) / 2}
		if opts.Limits.MaxOutputBytes > 0 {
			small, err := Render(context.Background(), src, opts)
			if err == nil && len(small) > opts.Limits.MaxOutputBytes {
				t.Fatalf("render wrote %d bytes past a %d byte budget", len(small), opts.Limits.MaxOutputBytes)
			}
			if err != nil && !errors.Is(err, ErrBudget) {
				t.Fatalf("budgeted render failed with a non-budget error: %v", err)
			}
		}
	})
}

// plainRelRe and plainAbsRe are destinations no rule refuses: a path of
// unreserved characters, and an http(s) URL with a plain host and such a
// path. safeURL must accept them, so the target also fails when the
// allowlist starts refusing ordinary links.
var (
	plainRelRe = regexp.MustCompile(`^(/[A-Za-z0-9._~-]*)+$`)
	plainAbsRe = regexp.MustCompile(`^https?://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(/[A-Za-z0-9._~-]*)*$`)
)

// FuzzSafeURL checks the link allowlist against an independent reading of
// what a browser does with the value: an accepted value passes
// checkLinkTarget; a value whose decoded form holds a control character, a
// backslash, a protocol-relative start or http(s) userinfo is refused (the
// rules SECURITY.md states); a plain path or plain http(s) URL is accepted.
func FuzzSafeURL(f *testing.F) {
	for _, s := range []string{
		"//attacker.com", " //attacker.com", `\\attacker.com`, `/\attacker.com`,
		`\/attacker.com`, `https:\\attacker.com`, `page\x.html`, "/\t/attacker.com",
		"/\n/attacker.com", "\x01//attacker.com", "/a\x7fb", "java\tscript:alert(1)",
		"https://trusted.example@attacker.com/", "http://user:pw@attacker.com",
		"https:////trusted.example@attacker.com/", "https:///attacker.com", "http://",
		"https://?q", "https://attacker.com/path@x", "mailto:a@b.com?subject=x",
		"/reports", "/", "#anchor", "?q=1", "page.html", "sub/page.html",
		"javascript:alert(1)", "data:text/html,x", "vbscript:msgbox",
		"&#106;avascript:x", "https://a&#34;@b", "&#x2F;/evil", "/&#92;evil",
		"java&#x09;script:x", "&Tab;//evil", "%2F%2Fevil", "/a&#0;b", "/a\x00b",
		"/a\u0085b", "/a\x0bb", "https://ok.example/a/b", "/a/b/c", "data:image/svg+xml;base64,AA",
	} {
		f.Add(html.EscapeString(s))
		f.Add(s)
	}
	f.Add(demiplane.FaviconDataURI)
	f.Fuzz(func(t *testing.T, u string) {
		dec := html.UnescapeString(u)
		low := strings.ToLower(strings.TrimSpace(dec))
		refuse := strings.ContainsRune(dec, '\\') || strings.IndexFunc(dec, unicode.IsControl) >= 0 ||
			strings.HasPrefix(low, "//")
		for _, scheme := range []string{"http:", "https:"} {
			if rest, ok := strings.CutPrefix(low, scheme); ok {
				auth := strings.TrimLeft(rest, "/")
				if k := strings.IndexAny(auth, "/?#"); k >= 0 {
					auth = auth[:k]
				}
				refuse = refuse || strings.Contains(auth, "@")
			}
		}
		ok := safeURL(u)
		if ok && refuse {
			t.Fatalf("safeURL accepted %q (decoded %q), which a stated rule refuses", u, dec)
		}
		if !ok && !refuse && (plainRelRe.MatchString(dec) || plainAbsRe.MatchString(dec)) {
			t.Fatalf("safeURL refused the plain link %q", u)
		}
		if ok {
			checkLinkTarget(t, u)
		}
	})
}

var spanRe = regexp.MustCompile(`<span class="[a-z]+">|</span>`)

// FuzzHighlight checks that highlighting only adds token spans: removing them
// and decoding the entities gives the source back, and no other markup
// appears.
func FuzzHighlight(f *testing.F) {
	for _, l := range []string{"go", "bash", "json", "yaml", "python", "", "js"} {
		f.Add(l, `func main() { s := "<b>" + '\'' // </span><script>`)
		f.Add(l, "echo \"$HOME\" 'x' # c\n${a} $(b)")
		f.Add(l, "{\"k\": [1, true, null, \"\\\"\"]}")
		f.Add(l, "def f():\n  return '''x''' # \"")
	}
	f.Fuzz(func(t *testing.T, lang, src string) {
		out := highlight(lang, src)
		plain := spanRe.ReplaceAllString(out, "")
		if strings.ContainsAny(plain, "<>\"'") {
			t.Fatalf("highlight wrote markup beyond token spans: %q", out)
		}
		if got := html.UnescapeString(plain); got != src {
			t.Fatalf("highlight changed the code: got %q want %q", got, src)
		}
	})
}
