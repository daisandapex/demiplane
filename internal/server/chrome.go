// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"fmt"
	"html"
	"strings"

	"github.com/daisandapex/demiplane/internal/theme"
)

// This file holds the shared visual chrome for the human-facing HTML surfaces
// (the / landing and the /docs pages). The aesthetic is a cool near-neutral
// paper with old-style serif headings, sans body, mono code, and a true-red
// primary accent with a navy secondary, echoing demiplane's "sealed by default,
// shared by choice" posture.
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
// shared house style (tokens + content typography) plus the chrome-only classes
// in chromeCSS. An empty/unknown configured theme falls back to the default
// (light) house style inside theme.CSS.
func (s *Server) chromeStyle() string {
	return theme.CSS(s.renderTheme) + chromeCSS
}

// chromeCSS holds ONLY the page-chrome classes (top nav, hero pills, endpoint
// cards, badges, doc tiles, copy-button code blocks, footer). Document-level
// typography (body, headings, code, tables) lives in internal/theme. Every
// color is a theme token, so the chrome re-skins cleanly under --theme dark and
// under every named palette.
const chromeCSS = `
.kicker{font-family:var(--sans);font-size:.72rem;font-weight:700;letter-spacing:.16em;
  text-transform:uppercase;color:var(--accent)}
.tagline{font-family:var(--serif);font-size:1.35rem;font-style:italic;color:var(--muted);
  letter-spacing:-0.012em;margin:.15rem 0 .7rem}
.scount{font-family:var(--mono);font-size:.82rem;color:var(--muted)}
header.top{border-bottom:1px solid var(--line);background:var(--panel);position:sticky;top:0;z-index:5}
header.top .wrap{display:flex;align-items:center;gap:1.25rem;height:3.4rem}
.brand{font-family:var(--serif);font-weight:700;font-size:1.15rem;color:var(--ink)}
.brand .dot{color:var(--accent)}
header.top nav{margin-left:auto;display:flex;gap:1.1rem;font-size:.9rem}
header.top nav a{color:var(--muted)}
header.top nav a.active,header.top nav a:hover{color:var(--accent)}
.ethos{display:inline-block;font-size:.92rem;color:var(--info-ink);background:var(--info-bg);
  border:1px solid var(--info-line);border-radius:999px;padding:.3rem .9rem;margin:.4rem 0 1rem}
.badge{display:inline-block;font-family:var(--mono);font-size:.72rem;font-weight:700;
  letter-spacing:.04em;padding:.15rem .5rem;border-radius:5px;color:oklch(0.985 0.004 250);vertical-align:middle}
.badge.post{background:var(--accent)}
.badge.get{background:var(--navy)}
.badge.delete{background:var(--danger)}
.card{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1rem 1.15rem;margin:.7rem 0}
.card .path{font-family:var(--mono);font-size:.95rem;margin-left:.5rem}
.card .desc{color:var(--muted);font-size:.92rem;margin:.4rem 0 0}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(13rem,1fr));gap:.7rem;margin:1rem 0}
.tile{display:block;background:var(--panel);border:1px solid var(--line);border-radius:12px;
  padding:.9rem 1rem}
.tile:hover{border-color:var(--accent);text-decoration:none}
.tile .t{font-family:var(--serif);font-size:1.05rem;color:var(--ink)}
.tile .d{color:var(--muted);font-size:.85rem;margin-top:.2rem}
.codeblock{margin:1rem 0;border-radius:10px;overflow:hidden;border:1px solid var(--code-line)}
.codeblock .cbhead{display:flex;align-items:center;justify-content:space-between;
  background:var(--code-bg);color:var(--code-ink);font-family:var(--mono);font-size:.7rem;
  letter-spacing:.12em;text-transform:uppercase;padding:.45rem .9rem}
.codeblock .cbhead button{font:inherit;letter-spacing:.08em;background:transparent;
  color:var(--code-ink);border:1px solid var(--code-line);border-radius:5px;padding:.1rem .55rem;cursor:pointer}
.codeblock .cbhead button:hover{background:var(--code-line);color:var(--code-ink)}
.codeblock pre{margin:0;border:none;border-radius:0}
footer{border-top:1px solid var(--line);color:var(--muted);font-size:.85rem;padding:1.5rem 0}
footer .mark{font-family:var(--serif);color:var(--ink)}
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

// navLink describes one top-nav entry.
type navLink struct {
	href, label string
	active      bool
}

// pageHTML wraps inner body HTML in the full styled document with top nav and
// footer. inner is trusted HTML produced by this package or the markdown engine.
// It is a method so the chrome stylesheet honors the server's configured theme.
func (s *Server) pageHTML(title string, nav []navLink, inner string) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\"><head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s</title>\n", html.EscapeString(title))
	b.WriteString(faviconChromeLinks)
	b.WriteString("<style>" + s.chromeStyle() + "</style>\n</head>\n<body>\n")
	b.WriteString("<header class=\"top\"><div class=\"wrap\">")
	b.WriteString("<a class=\"brand\" href=\"/\">demiplane<span class=\"dot\">.</span></a><nav>")
	for _, n := range nav {
		cls := ""
		if n.active {
			cls = " class=\"active\""
		}
		fmt.Fprintf(&b, "<a href=\"%s\"%s>%s</a>", html.EscapeString(n.href), cls, html.EscapeString(n.label))
	}
	b.WriteString("</nav></div></header>\n")
	b.WriteString("<main><div class=\"wrap\">")
	b.WriteString(inner)
	b.WriteString("</div></main>\n")
	b.WriteString("<footer><div class=\"wrap\"><span class=\"mark\">demiplane</span>")
	b.WriteString(" · your private publishing plane.</div></footer>\n")
	b.WriteString("<script>" + copyJS + "</script>\n")
	b.WriteString("</body></html>\n")
	return b.String()
}

// topNav returns the standard nav with the given path marked active.
func topNav(active string) []navLink {
	links := []navLink{
		{href: "/", label: "Home"},
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

// codeBlock renders a dark code block with an uppercase label header and a copy
// button. code is plain text (escaped here).
func codeBlock(label, code string) string {
	return fmt.Sprintf(
		"<div class=\"codeblock\"><div class=\"cbhead\"><span>%s</span>"+
			"<button data-copy>Copy</button></div><pre><code>%s</code></pre></div>",
		html.EscapeString(label), html.EscapeString(code))
}

// methodBadge renders a colored HTTP-method badge.
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

// endpointCard renders an endpoint as a full-border card (no side-stripe).
func endpointCard(method, path, desc string) string {
	return fmt.Sprintf(
		"<div class=\"card\">%s<span class=\"path\">%s</span><p class=\"desc\">%s</p></div>",
		methodBadge(method), html.EscapeString(path), html.EscapeString(desc))
}
