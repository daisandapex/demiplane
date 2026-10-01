// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"html"
	"regexp"
	"sort"
	"strings"
)

// The F layout is the masthead page's structure: a one-row top bar, an optional
// series tree on the left, the reading column (title, then body), and a right
// rail carrying the frontmatter facts and an outline of the page. It is the
// single-page form of the KB's F layout. Palettes are untouched: F is layout
// only, so every --theme value and the light/dark contract work as before.
//
// The reading column keeps one left edge: the title, every heading and every
// text block start at the column edge and stop at --measure; code and tables
// may use the whole column. The tree starts closed below 1410 px and `[`
// toggles it, the choice stored in localStorage and applied in <head> before
// paint, so it holds on every page. Below 51.25rem the layout is one column
// with the rail under the title.

// reOutlineHead matches the h2/h3 lines renderBlocks emits. The body HTML is
// the renderer's own output (source text is escaped), so the shape is fixed.
var reOutlineHead = regexp.MustCompile(`<h([23]) id="([a-z0-9-]+)">(.*?)<a class="heading-anchor"`)

type outlineItem struct {
	level int
	id    string
	text  string // plain text, entities still escaped
}

// outlineFrom lists the h2 and h3 headings of rendered body HTML in order.
func outlineFrom(bodyHTML string) []outlineItem {
	var items []outlineItem
	for _, m := range reOutlineHead.FindAllStringSubmatch(bodyHTML, -1) {
		items = append(items, outlineItem{level: int(m[1][0] - '0'), id: m[2], text: stripTags(m[3])})
	}
	return items
}

// reCodeFirst matches a top-level paragraph whose first content is a code span.
var reCodeFirst = regexp.MustCompile(`(?m)^<p( class="lead")?><code>`)

// markCodeFirst tags paragraphs that open with inline code. The chip's side
// padding would start the first glyph .3em inside the column edge; the F sheet
// hangs a tagged chip by that padding so the text, not the chip, sits on the
// edge the headings share (the reading check compares glyphs).
func markCodeFirst(bodyHTML string) string {
	return reCodeFirst.ReplaceAllStringFunc(bodyHTML, func(m string) string {
		if strings.Contains(m, "lead") {
			return `<p class="lead code-first"><code>`
		}
		return `<p class="code-first"><code>`
	})
}

// railHTML renders the right rail: the facts (the frontmatter meta-header, when
// there is one) above the outline. An outline of fewer than two headings is
// not worth a rail; a page with neither part renders no rail at all ("").
func railHTML(metaHTML string, outline []outlineItem) string {
	if len(outline) < 2 {
		outline = nil
	}
	if metaHTML == "" && outline == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<aside class="frail" aria-label="About this page">`)
	if metaHTML != "" {
		b.WriteString(`<section class="rail-facts"><p class="rail-h">Details</p>` + metaHTML + `</section>`)
	}
	if outline != nil {
		b.WriteString(`<details class="rail-outline" open><summary class="rail-h">On this page</summary><ol class="outline">`)
		sec := ""
		for _, it := range outline {
			if it.level == 2 {
				sec = it.id
			}
			cls := "l2"
			if it.level == 3 {
				cls = "l3"
			}
			b.WriteString(`<li class="` + cls + `" data-sec="` + sec + `"><a href="#` + it.id + `">` + it.text + `</a></li>`)
		}
		b.WriteString(`</ol></details>`)
	}
	b.WriteString("</aside>\n")
	return b.String()
}

// seriesFamily returns the sorted family of slug and its siblings, the same
// set and order computeSeries walks, or nil when there is no family of two.
func seriesFamily(slug string, siblings []string) []string {
	if slug == "" {
		return nil
	}
	fam := []string{slug}
	seen := map[string]bool{slug: true}
	for _, s := range siblings {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		fam = append(fam, s)
	}
	if len(fam) < 2 {
		return nil
	}
	sort.Strings(fam)
	return fam
}

// treeHTML renders the series tree: every member of the family, the current
// page marked and unlinked.
func treeHTML(slug string, fam []string, base string) string {
	var b strings.Builder
	b.WriteString(`<nav class="ftree" id="ftree" aria-label="Series"><p class="rail-h">Series</p><ol>`)
	for _, s := range fam {
		if s == slug {
			b.WriteString(`<li><span aria-current="page">` + html.EscapeString(s) + `</span></li>`)
			continue
		}
		b.WriteString(`<li><a href="` + navHref(base, s) + `">` + html.EscapeString(s) + `</a></li>`)
	}
	b.WriteString("</ol></nav>\n")
	return b.String()
}

// topbar is the one-row sticky bar of an F page: the wordmark kicker on the
// column edge, the tree button (series pages only) and the theme toggle at the
// right. The title is not here; it heads the reading column so it can wrap.
func topbar(serverTheme string, toggle, tree bool) string {
	var b strings.Builder
	b.WriteString(`<header class="docbar"><div class="wrap"><span class="kicker">demiplane</span><div class="tools">`)
	if tree {
		b.WriteString(`<button class="treebtn" type="button" aria-controls="ftree" aria-expanded="false" hidden ` +
			`aria-label="Show or hide the series list" title="Series list ([)">` + treeSVG + `</button>`)
	}
	if toggle {
		pressed := "false"
		if serverTheme == "dark" {
			pressed = "true"
		}
		b.WriteString(`<button class="themetoggle" type="button" aria-pressed="` + pressed + `" ` +
			`aria-label="Toggle light or dark theme" title="Toggle theme">` +
			sunSVG + moonSVG + `</button>`)
	}
	b.WriteString("</div></div></header>\n")
	return b.String()
}

const treeSVG = `<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" ` +
	`stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">` +
	`<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><path d="M9.5 4.5v15"/></svg>`

// fInitScript runs in <head> before paint: it opens the tree when the stored
// choice says so, or with no stored choice at 1410 px and wider.
const fInitScript = `<script>(function(){var r=document.documentElement,p=null;r.classList.add('fjs');` +
	`try{p=localStorage.getItem('demiplane-nav');}catch(e){}` +
	`var w=window.matchMedia&&matchMedia('(min-width:1410px)').matches;` +
	`if(p==='open'||(p!=='closed'&&w))r.classList.add('nav-on');})();</script>`

// fScript wires the tree button and `[`, folds the outline in the narrow flow,
// and marks the outline entry for the section in view (showing only that
// section's h3s, as F does).
const fScript = `<script>(function(){
var r=document.documentElement,b=document.body,K='demiplane-nav',btn=document.querySelector('.treebtn');
var narrow=matchMedia('(max-width:51.24rem)'),wide=matchMedia('(min-width:1410px)');
var pref=function(){try{return localStorage.getItem(K);}catch(e){return null;}};
var isOpen=function(){return r.classList.contains(narrow.matches?'nav-drawer':'nav-on');};
var paint=function(){if(btn)btn.setAttribute('aria-expanded',isOpen()?'true':'false');};
var toggle=function(){
if(!b.classList.contains('has-tree'))return;
if(narrow.matches){r.classList.toggle('nav-drawer');}
else{var o=!r.classList.contains('nav-on');r.classList.toggle('nav-on',o);
try{localStorage.setItem(K,o?'open':'closed');}catch(e){}}
paint();};
var on=function(m,f){if(m.addEventListener)m.addEventListener('change',f);else m.addListener(f);};
if(btn){btn.hidden=false;btn.addEventListener('click',toggle);}
on(wide,function(){var p=pref();if(p!=='open'&&p!=='closed'){r.classList.toggle('nav-on',wide.matches);paint();}});
on(narrow,function(){r.classList.remove('nav-drawer');paint();var d=document.querySelector('.rail-outline');if(d)d.open=!narrow.matches;});
document.addEventListener('keydown',function(e){
if(e.key!=='['||e.metaKey||e.ctrlKey||e.altKey)return;
var t=e.target;if(t&&(t.isContentEditable||/^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)))return;
if(!b.classList.contains('has-tree'))return;e.preventDefault();toggle();});
paint();
var det=document.querySelector('.rail-outline');if(det&&narrow.matches)det.open=false;
var links={};document.querySelectorAll('.outline a').forEach(function(a){links[a.getAttribute('href').slice(1)]=a;});
var heads=[].filter.call(document.querySelectorAll('main h2[id],main h3[id]'),function(h){return links[h.id];});
if(!heads.length)return;
var cur=null,mark=function(){
var bar=document.querySelector('.docbar'),y=(bar?bar.offsetHeight:52)+24,c=heads[0];
for(var i=0;i<heads.length;i++){if(heads[i].getBoundingClientRect().top-y<=0)c=heads[i];else break;}
if(c===cur)return;cur=c;
Object.keys(links).forEach(function(k){links[k].removeAttribute('aria-current');});
links[c.id].setAttribute('aria-current','true');
var sec=links[c.id].parentNode.getAttribute('data-sec');
document.querySelectorAll('.outline .l3').forEach(function(li){li.classList.toggle('open',li.getAttribute('data-sec')===sec);});};
var q=false;window.addEventListener('scroll',function(){if(!q){q=true;requestAnimationFrame(function(){q=false;mark();});}},{passive:true});
mark();
})();</script>
`

// fCSS lays out an F page. The column template is one set of custom properties
// on body, chosen by the has-tree/has-rail classes and the nav-on state, and the
// top bar, the page grid and the footer all use it, so the kicker, the title,
// the text and the footer share one left edge whatever is open. Token
// references carry literal fallbacks for a --css override that ships none.
const fCSS = `
:root{--top-h:3.25rem;--col:46rem;--rail-w:15rem;--tree-w:14rem;--fgap:2.5rem;--gutter:1.25rem;
  scroll-padding-top:calc(var(--top-h) + 1rem)}
.f{--fcols:minmax(0,var(--col));--fareas:"head" "body";--fbar:"head";--frows:auto 1fr}
.f.has-rail{--fcols:minmax(0,var(--col)) var(--rail-w);--fareas:"head rail" "body rail";--fbar:"head rail"}
@media (min-width:51.25rem){
html.nav-on .f.has-tree{--fcols:var(--tree-w) minmax(0,var(--col));--fareas:"tree head" "tree body";--fbar:"tree head"}
html.nav-on .f.has-tree.has-rail{--fcols:var(--tree-w) minmax(0,var(--col)) var(--rail-w);
  --fareas:"tree head rail" "tree body rail";--fbar:"tree head rail"}
html.nav-on .f.has-tree .ftree{display:block}}
@media (max-width:51.24rem){
.f,.f.has-rail{--fcols:minmax(0,1fr);--fareas:"tree" "head" "rail" "body";--fbar:"head";--frows:none}
body.f.has-rail .docbar .tools{grid-area:head}
html.nav-drawer .f.has-tree .ftree{display:block}}
.f .fgrid,.f .docbar>.wrap,.f .docfoot>.wrap{display:grid;grid-template-columns:var(--fcols);
  column-gap:var(--fgap);justify-content:center;max-width:none;margin:0;padding:0 var(--gutter)}
.f .fgrid{grid-template-areas:var(--fareas);grid-template-rows:var(--frows)}
.f .docbar>.wrap,.f .docfoot>.wrap{grid-template-areas:var(--fbar)}
.f .docbar>.wrap{height:var(--top-h);padding-top:0;padding-bottom:0;align-items:center}
.f .docbar .kicker{grid-area:head}
.f .docbar .tools{grid-area:head;justify-self:end;display:flex;gap:.5rem;align-items:center}
.f.has-rail .docbar .tools{grid-area:rail}
.f .docfoot .fcell{grid-area:head}
.f .themetoggle{margin-left:0}
.treebtn{display:inline-flex;align-items:center;justify-content:center;width:2.15rem;height:2.15rem;cursor:pointer;
  color:var(--muted,oklch(0.462 0.011 78));background:var(--panel,oklch(0.995 0.002 95));
  border:1px solid var(--line,oklch(0.910 0.013 87));border-radius:999px}
.treebtn:hover{color:var(--ink,oklch(0.237 0.009 75));border-color:var(--line-strong,oklch(0.714 0.018 85))}
.treebtn[aria-expanded="true"]{color:var(--accent,oklch(0.445 0.122 23))}
.treebtn svg{width:1.05rem;height:1.05rem;display:block}
.dochead{grid-area:head;min-width:0;padding-top:2.4rem}
.dochead .doctitle{max-width:var(--measure);margin:0 0 1.4rem;line-height:1.2;overflow-wrap:break-word;
  white-space:normal}
.f main.wrap{grid-area:body;min-width:0;max-width:none;margin:0;padding:0 0 5rem}
.f main.wrap>*{max-width:var(--measure);margin-inline:0}
.f main.wrap>pre,.f main.wrap>.table-wrap{max-width:none}
.f main.wrap>p.code-first>code:first-child{margin-left:-.3em}
.frail,.ftree{min-width:0;position:sticky;top:var(--top-h);align-self:start;max-height:calc(100vh - var(--top-h));
  overflow-y:auto;overscroll-behavior:contain;padding:2.6rem 0 1.5rem;font:.875rem/1.45 var(--sans)}
.frail{grid-area:rail}
.ftree{grid-area:tree;display:none}
.rail-h{display:block;margin:0;padding:0 0 .35rem;font:600 .75rem/1.45 var(--sans);letter-spacing:.08em;
  text-transform:uppercase;color:var(--muted,oklch(0.462 0.011 78));
  border-bottom:1px solid var(--line-strong,oklch(0.714 0.018 85))}
summary.rail-h{cursor:pointer;list-style:none}
summary.rail-h::-webkit-details-marker{display:none}
.rail-facts+.rail-outline{margin-top:1.75rem}
.outline,.ftree ol{list-style:none;margin:0;padding:.35rem 0 0}
.outline li,.ftree li{margin:0}
.outline a,.ftree a,.ftree [aria-current]{display:block;padding:.22rem .5rem;margin-left:-.5rem;border-radius:6px;
  color:var(--muted,oklch(0.462 0.011 78));text-decoration:none;overflow-wrap:anywhere}
.outline a:hover,.ftree a:hover{color:var(--ink,oklch(0.237 0.009 75));background:var(--code-bg,oklch(0.955 0.008 88))}
.outline a[aria-current="true"],.ftree [aria-current="page"]{color:var(--ink,oklch(0.237 0.009 75));font-weight:600;
  background:var(--code-bg,oklch(0.955 0.008 88))}
.outline .l3 a{padding-left:1.25rem}
.fjs .outline .l3{display:none}
.fjs .outline .l3.open{display:block}
@media (max-width:51.24rem){
.dochead{padding-top:1.6rem}
.frail,.ftree{position:static;max-height:none;overflow:visible;padding:0 0 1.5rem;max-width:var(--measure)}
.frail .outline{columns:2 11rem;column-gap:1.5rem}
.frail .outline li{break-inside:avoid}
.fjs .frail .outline .l3,.fjs .frail .outline .l3.open{display:none}
summary.rail-h::after{content:" +"}
details[open]>summary.rail-h::after{content:" \2212"}}
`

// fMetaCSS restyles the meta-header as the rail's facts list. Added only when
// the rail carries facts, so a page without frontmatter names no metahead.
const fMetaCSS = `
.frail .metahead{margin:0;padding:0;border:0}
.frail .metahead .metadate{margin:0;padding:.45rem 0;border-bottom:1px solid var(--line,oklch(0.910 0.013 87))}
.frail .metahead .metafields{gap:0}
.frail .metahead .metarow{display:grid;grid-template-columns:minmax(0,1fr);gap:.1rem;padding:.45rem 0;
  border-bottom:1px solid var(--line,oklch(0.910 0.013 87))}
.frail .metahead .metarow dt{min-width:0}
.frail .metahead .metarow dd{overflow-wrap:break-word;font-family:var(--sans)}
`

// fPrintCSS prints an F page as one column. Like printCSS it is skipped under
// a --css override, whose sheet owns print.
const fPrintCSS = `
@media print{
.f .fgrid{display:flex;flex-direction:column;padding:0}
.dochead{order:0}.frail{order:1}.f main.wrap{order:2}
.frail{position:static;max-height:none;overflow:visible;padding:0 0 1rem}
.ftree,.treebtn,.rail-outline{display:none}
.f .docbar>.wrap{height:auto;padding:.6rem 0}}
`
