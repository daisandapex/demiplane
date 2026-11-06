// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/daisandapex/demiplane/v3/internal/render"
	"github.com/daisandapex/demiplane/v3/internal/theme"
)

// This file holds the shared visual chrome for the human-facing HTML surfaces
// (the / landing, /docs, /help, /connect, /gallery and the status pages). They
// wear the published pages' frame: the same measure, the same wide blocks, the
// same margin navs, the same light/dark contract and toggle, with one quiet
// site bar on top (the only place the name appears) and one foot line.
//
// The design tokens and content typography live in internal/theme — the SINGLE
// source of the house style, shared with the ?render=md markdown renderer. The
// page stylesheet is theme.CSS(<configured theme>)+chromeCSS, built per request
// by chromeStyle(), so there is no second stylesheet to drift. The chrome
// honors the server's --theme setting: `--theme dark` darkens EVERYTHING (the
// nav/cards/footer chrome and rendered content) as one setting.
//
// Per the impeccable design ban, NO colored left-border side-stripes are used:
// cards and callouts use full hairline borders or a background tint instead.

// chromeStyle returns the full page stylesheet for the configured theme: the
// shared house style (tokens + content typography), the shared page frame,
// and the chrome-only classes in chromeCSS. An unpinned theme ships the
// three-block light/dark contract, exactly as a published page does, so the
// operator pages follow the OS and the reader's toggle; a pinned palette ships
// its one :root block and no toggle.
func (s *Server) chromeStyle() string {
	base := theme.ToggleCSS()
	if theme.Pinned(s.renderTheme) {
		base = theme.CSS(s.renderTheme)
	}
	return base + theme.FrameCSS() + chromeCSS
}

// chromeToggle reports whether the operator pages carry the theme toggle: only
// the default palette has two faces to flip between.
func (s *Server) chromeToggle() bool { return !theme.Pinned(s.renderTheme) }

// chromeCSS holds ONLY the page-chrome classes: the site bar, the lead line,
// endpoint rows, method labels, the docs index list, captioned code blocks and
// the foot line. Document-level typography (body, headings, code, tables) lives
// in internal/theme, and so does the frame (measure, wide blocks, margin navs,
// toggle) that these pages share with published ones. Every color is a theme
// token, so the chrome re-skins cleanly under every palette, and the accent is
// spent on links and the focus ring only.
const chromeCSS = `
.scount{font-family:var(--mono);font-size:.82rem;color:var(--muted)}
.sitehead{padding:0 1.25rem}
.sitebar{display:flex;flex-wrap:wrap;align-items:center;gap:.6rem 1.5rem;max-width:var(--wide);margin:0 auto;
  padding:1.25rem 0 1rem;border-bottom:1px solid var(--line)}
.brand{font:700 1.0625rem/1.2 var(--serif);letter-spacing:-.01em;color:var(--ink);text-decoration:none}
.brand:hover{text-decoration:underline;text-decoration-color:var(--line-strong)}
.sitebar nav{margin-left:auto;display:flex;flex-wrap:wrap;column-gap:1.1rem;row-gap:.25rem;
  font:.875rem/1.5 var(--sans);font-size-adjust:none}
.sitebar nav a{color:var(--muted);text-decoration:none}
.sitebar nav a:hover{color:var(--ink);text-decoration:underline;text-decoration-color:var(--line-strong)}
.sitebar nav a[aria-current="page"]{color:var(--ink);font-weight:600}
@media (max-width:40rem){.sitebar .themetoggle{order:2;margin-left:auto}
  .sitebar nav{order:3;margin-left:0;flex-basis:100%;column-gap:1rem}}
.sitebar>.brand+.themetoggle{margin-left:auto}
.frame>main.doc{padding:2.75rem 0 4rem}
@media (min-width:78.5rem){.sitehead+.frame>.mnav{margin-top:3rem}}
.sitehead+.frame>.dochead{padding-top:2.75rem}
.sitehead+.frame>.dochead>h1{max-width:var(--measure);margin-top:0;margin-bottom:.6rem}
.frame>.dochead~main.doc{padding-top:.75rem}
@media (min-width:78.5rem){.sitehead+.frame>.dochead~.mnav{margin-top:.75rem}}
.ethos{display:block;font-size:1.0625rem;color:var(--ink);margin:0 auto 1.5em}
.badge{display:inline-block;min-width:3.6rem;font:600 .72rem/1.5 var(--mono);letter-spacing:.04em;
  padding:.05rem .45rem;border-radius:5px;color:var(--ink);background:var(--code-bg);
  border:1px solid var(--code-border);text-align:center;vertical-align:.08em}
.card{background:none;border:0;border-top:1px solid var(--line);border-radius:0;padding:.8rem 0;margin:0 auto}
.card+:not(.card){margin-top:1.5em}
.card:last-of-type{border-bottom:1px solid var(--line)}
.card .path{font-family:var(--mono);font-size:.9rem;margin-left:.6rem}
.card .desc{color:var(--muted);font-size:.9375rem;margin:.3rem 0 0}
.grid{display:block;margin:0 auto 1.5em;border-top:1px solid var(--line)}
.tile{display:block;padding:.75rem 0;border-bottom:1px solid var(--line);text-decoration:none}
.tile .t{font:700 1.0625rem/1.35 var(--serif);color:var(--accent);text-decoration:underline;
  text-decoration-thickness:1px;text-underline-offset:.18em;
  text-decoration-color:color-mix(in srgb,var(--accent) 55%,transparent)}
.tile:hover .t{text-decoration-thickness:2px;text-decoration-color:var(--accent)}
.tile .d{color:var(--muted);font-size:.9375rem;margin-top:.15rem}
.codeblock{margin:0 auto 1.125em}
.codeblock .cbhead{display:flex;align-items:baseline;justify-content:space-between;gap:1rem;
  padding:0 0 .35rem;font:600 .8125rem/1.4 var(--sans);font-size-adjust:none;color:var(--muted)}
.codeblock .cbhead button{font:500 .75rem/1.4 var(--sans);color:var(--muted);background:var(--panel);
  border:1px solid var(--line);border-radius:999px;padding:.1rem .65rem;cursor:pointer}
.codeblock .cbhead button:hover{color:var(--ink);border-color:var(--line-strong)}
.codeblock pre{margin:0}
.lock{width:.85em;height:.85em;vertical-align:-.08em;color:var(--muted)}
.nf-code{font:600 .875rem/1.5 var(--sans);color:var(--muted);margin:0 auto .5rem;font-variant-numeric:tabular-nums}
.nf-h{margin-top:0}
.nf-p{color:var(--muted)}
`

const copyJS = `
document.addEventListener('click',function(e){
  var b=e.target.closest('button[data-copy]'); if(!b)return;
  var pre=b.closest('.codeblock').querySelector('code,pre');
  navigator.clipboard.writeText(pre.innerText).then(function(){
    var o=b.textContent; b.textContent='Copied'; setTimeout(function(){b.textContent=o},1200);
  });
});
`

// lockIcon marks a password-protected artifact. It is an inline SVG, not the
// lock emoji, so it renders on a machine without an emoji font.
const lockIcon = `<svg class="lock" viewBox="0 0 24 24" role="img" aria-label="password protected" ` +
	`fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">` +
	`<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/></svg>`

// navLink describes one top-nav entry.
type navLink struct {
	href, label string
	active      bool
}

// pageHTML wraps inner body HTML in the full styled document with the site bar
// and the foot line. inner is trusted HTML produced by this package or the
// markdown engine. It is a method so the chrome stylesheet honors the server's
// configured theme.
func (s *Server) pageHTML(title string, nav []navLink, inner string) string {
	return s.framePage(title, nav, "", contentsOf(inner), inner)
}

// chromeH2 matches an operator page's own section headings that carry an id.
var chromeH2 = regexp.MustCompile(`<h2 id="([^"]+)">(.*?)</h2>`)

// contentsOf builds the contents list for an operator page from its own h2s,
// in the published pages' .mnav grammar, once it has three or more sections.
// inner is trusted markup this package wrote.
func contentsOf(inner string) string {
	m := chromeH2.FindAllStringSubmatch(inner, -1)
	if len(m) < 3 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<details class="mnav toc"><summary>Contents</summary><nav aria-label="Contents"><ol class="outline">`)
	for i, h := range m {
		text := strings.TrimSpace(chromeTag.ReplaceAllString(h[2], ""))
		fmt.Fprintf(&b, `<li class="l2" data-sec="s%d"><a href="#%s">%s</a></li>`, i+1, h[1], text)
	}
	b.WriteString("</ol></nav></details>\n")
	return b.String()
}

// chromeTag strips inline tags from a heading for its contents entry.
var chromeTag = regexp.MustCompile(`<[^>]*>`)

// framePage is pageHTML with the two margin navs a long page may carry: seq
// (a sequence of sibling pages, such as the docs list) and toc (the page's
// contents), each trusted markup in the .mnav grammar the published pages use.
func (s *Server) framePage(title string, nav []navLink, seq, toc, inner string) string {
	var b strings.Builder
	initial := s.renderTheme
	tag := "<html lang=\"en\">"
	if initial != "" {
		tag = "<html lang=\"en\" data-theme=\"" + html.EscapeString(initial) + "\">"
	}
	b.WriteString("<!DOCTYPE html>\n" + tag + "<head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s</title>\n", html.EscapeString(title))
	b.WriteString(faviconChromeLinks)
	b.WriteString("<style>" + s.chromeStyle() + "</style>\n")
	if s.chromeToggle() {
		b.WriteString(render.ThemeInitScript() + "\n")
	}
	b.WriteString("</head>\n<body>\n")
	b.WriteString("<a class=\"skip\" href=\"#dp-main\">Skip to content</a>\n")
	b.WriteString("<header class=\"sitehead\"><div class=\"sitebar\">")
	b.WriteString("<a class=\"brand\" href=\"/\">demiplane</a><nav aria-label=\"Site\">")
	for _, n := range nav {
		cur := ""
		if n.active {
			cur = " aria-current=\"page\""
		}
		fmt.Fprintf(&b, "<a href=\"%s\"%s>%s</a>", html.EscapeString(n.href), cur, html.EscapeString(n.label))
	}
	b.WriteString("</nav>")
	if s.chromeToggle() {
		b.WriteString(render.ToggleButton(s.renderTheme))
	}
	b.WriteString("</div></header>\n")
	title, inner = splitTitle(inner)
	b.WriteString("<div class=\"frame\">\n")
	if title != "" {
		b.WriteString("<header class=\"dochead\">" + render.KeepShortCode(title) + "</header>\n")
	}
	b.WriteString(seq + toc)
	b.WriteString("<main id=\"dp-main\" class=\"doc\">")
	b.WriteString(render.KeepShortCode(inner))
	b.WriteString("<footer class=\"colo\">Sealed by default, shared by choice.</footer>")
	b.WriteString("</main>\n</div>\n")
	b.WriteString("<script>" + copyJS + "</script>\n")
	b.WriteString(render.FrameScripts())
	b.WriteString("</body></html>\n")
	return b.String()
}

// splitTitle lifts a page's leading h1 out of inner, so an operator page heads
// its frame with the title the way a published page does and, on a phone, the
// folded navs come after the title rather than above it. A page that does not
// open with an h1 is returned whole.
func splitTitle(inner string) (h1, rest string) {
	if !strings.HasPrefix(inner, "<h1") {
		return "", inner
	}
	i := strings.Index(inner, "</h1>")
	if i < 0 {
		return "", inner
	}
	return inner[:i+len("</h1>")], inner[i+len("</h1>"):]
}

// topNav returns the standard nav with the given path marked active.
func topNav(active string) []navLink {
	links := []navLink{
		{href: "/docs", label: "Docs"},
		{href: "/docs/api", label: "API"},
		{href: "/connect", label: "Connect"},
		{href: "/gallery", label: "Gallery"},
		{href: "/help", label: "Help"},
		{href: "/llms.txt", label: "llms.txt"},
	}
	for i := range links {
		if links[i].href == active {
			links[i].active = true
		}
	}
	return links
}

// codeBlock renders a code block: the house code slab (the same pre a
// published page sets) under a small caption naming it, with a Copy button at
// the caption's end. code is plain text (escaped here).
func codeBlock(label, code string) string {
	return fmt.Sprintf(
		"<div class=\"codeblock\"><div class=\"cbhead\"><span>%s</span>"+
			"<button data-copy>Copy</button></div><pre><code>%s</code></pre></div>",
		html.EscapeString(label), html.EscapeString(code))
}

// methodBadge renders an HTTP-method label: mono ink on the code surface, the
// same for every method, since the accent is held to links.
func methodBadge(method string) string {
	cls := "get"
	switch method {
	case "POST":
		cls = "post"
	case "DELETE":
		cls = "delete"
	}
	return fmt.Sprintf("<span class=\"badge %s\">%s</span>", cls, html.EscapeString(method))
}

// endpointCard renders an endpoint as one hairline-ruled row (no card, no
// side-stripe).
func endpointCard(method, path, desc string) string {
	return fmt.Sprintf(
		"<div class=\"card\">%s<span class=\"path\">%s</span><p class=\"desc\">%s</p></div>",
		methodBadge(method), html.EscapeString(path), html.EscapeString(desc))
}
