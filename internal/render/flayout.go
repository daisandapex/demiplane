// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"html"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/theme"
)

// The F layout is the masthead page's structure. Everything hangs on one
// centred axis (theme.FrameCSS): the text column holds to --measure, tables
// and code break out to --wide around the same centre, and the title and the
// byline hold to the text column, so they end at the text edge. The page
// opens with the document: the title, then one byline line (date, author,
// status, tags, read time) with the theme toggle at its end, then the text.
// There is no top bar.
//
// Two optional margin navs sit outside the wide frame on a wide screen and
// fold to a one-line disclosure under the byline on a narrow one: the series
// list on the left (a series of two or more), the contents on the right (three
// or more h2 sections). Palettes are untouched: the layout is CSS only, so
// every --theme value and the light/dark contract work as before.

type outlineItem struct {
	level int
	id    string
	text  string // plain text, entities still escaped
}

// minOutlineSections is the number of h2 sections from which a page gets a
// contents list. A two-section lesson reads top to bottom and needs none.
const minOutlineSections = 3

// tocHTML renders the contents list, or "" when the page has fewer than
// minOutlineSections h2 sections.
//
// The list counts against the render's output budget like the body: it is
// checked as it grows, so a document with many headings is refused before its
// outline is built rather than after. Each entry names its section by a short
// index (data-sec="s3"), never by repeating the section's heading id.
func tocHTML(st *state, outline []outlineItem) (string, error) {
	sections := 0
	for _, it := range outline {
		if it.level == 2 {
			sections++
		}
	}
	if sections < minOutlineSections {
		return "", nil
	}
	b := st.newOut()
	b.WriteString(`<details class="mnav toc"><summary>Contents</summary>` +
		`<nav aria-label="Contents"><ol class="outline">`)
	sec, n := "", 0
	for _, it := range outline {
		if err := st.tick(); err != nil {
			return "", err
		}
		if st.err != nil {
			return "", st.err
		}
		if it.level == 2 {
			n++
			sec = "s" + strconv.Itoa(n)
		}
		cls := "l2"
		if it.level == 3 {
			cls = "l3"
		}
		b.WriteString(`<li class="` + cls + `" data-sec="` + sec + `"><a href="#` + it.id + `">`)
		b.WriteString(it.text)
		b.WriteString(`</a></li>`)
	}
	b.WriteString("</ol></nav></details>\n")
	if st.err != nil {
		return "", st.err
	}
	return b.String(), nil
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

// seriesPrefix returns the slug prefix every member of fam shares, cut back to
// its last hyphen, so "audit-series-01" and "audit-series-02" share
// "audit-series-" and never a lone "0".
func seriesPrefix(fam []string) string {
	p := fam[0]
	for _, s := range fam[1:] {
		for !strings.HasPrefix(s, p) {
			p = p[:len(p)-1]
		}
	}
	if i := strings.LastIndexByte(p, '-'); i >= 0 {
		return p[:i+1]
	}
	return ""
}

// seriesLabel names a member by what sets it apart from its siblings: the slug
// without the family's shared prefix, hyphens read as spaces, first letter
// capitalised. A bare number reads "Part N". The store keeps no titles for
// siblings, so the slug is all there is to name them by.
func seriesLabel(s, prefix string) string {
	r := strings.TrimPrefix(s, prefix)
	if r == "" {
		r = s
	}
	if n, err := strconv.Atoi(r); err == nil && n >= 0 {
		return "Part " + strconv.Itoa(n)
	}
	r = strings.ReplaceAll(r, "-", " ")
	if c := r[0]; c >= 'a' && c <= 'z' {
		r = string(c-'a'+'A') + r[1:]
	}
	return r
}

// treeHTML renders the series list: every member of the family, the current
// page marked, unlinked and named by its own title (current, already escaped;
// its label when empty), under a summary that says where the reader is
// ("Series 2 of 3"). It writes through the render's budgeted out, each entry
// held to the remaining budget before it is built; the family is at most
// MaxSeriesMembers long.
func treeHTML(st *state, slug string, fam []string, base, current string) (string, error) {
	pos := 0
	for i, s := range fam {
		if s == slug {
			pos = i + 1
		}
	}
	b := st.newOut()
	b.WriteString(`<details class="mnav seq" id="ftree"><summary>Series <span class="pos">` +
		strconv.Itoa(pos) + ` of ` + strconv.Itoa(len(fam)) + `</span></summary><nav aria-label="Series"><ol>`)
	prefix := seriesPrefix(fam)
	for _, s := range fam {
		if err := st.tick(); err != nil {
			return "", err
		}
		if err := st.fits(escapedLen(base) + 2*escapedLen(s) + len(current) + 72); err != nil {
			return "", err
		}
		label := html.EscapeString(seriesLabel(s, prefix))
		if s == slug {
			if current != "" {
				label = current
			}
			b.WriteString(`<li><span aria-current="page">` + label + `</span></li>`)
			continue
		}
		b.WriteString(`<li><a href="` + navHref(base, s) + `">` + label + `</a></li>`)
	}
	b.WriteString("</ol></nav></details>\n")
	if st.err != nil {
		return "", st.err
	}
	return b.String(), nil
}

// wordsPerMinute sets the read time in the byline. A read time shows only
// from two minutes: "1 min read" on a three-line note is noise.
const wordsPerMinute = 230

// countWords counts whitespace-separated runs in s without allocating.
func countWords(s string) int {
	n, in := 0, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\n', '\t', '\r':
			in = false
		default:
			if !in {
				n++
			}
			in = true
		}
	}
	return n
}

// bylineHTML renders the masthead's one line of facts under the title, in
// this order: the date (the frontmatter date or published field, else the
// publish date), "by" the author, the status, the tags, any other field as
// "Key: value", and the read time. The title field is never repeated: the H1
// above already says it. tools (the theme toggle) closes the line. It returns
// whether the localize script is needed (a full timestamp was rendered).
func bylineHTML(st *state, fields []metaField, published time.Time, words int, tools string) (string, bool, error) {
	var items []string
	needsScript := false
	dateUsed := false
	for _, f := range fields {
		if isDateKey(f.key) && f.value != "" {
			row, script := dateText(f.value)
			items = append(items, row)
			needsScript = script
			dateUsed = true
			break
		}
	}
	if !dateUsed && !published.IsZero() {
		d := published.UTC().Format("2006-01-02")
		items = append(items, `<time class="tstamp" datetime="`+d+`">`+d+`</time>`)
	}
	var who, status, tags, rest []string
	skippedDate := false
	for _, f := range fields {
		k := strings.ToLower(f.key)
		v := html.EscapeString(f.value)
		switch {
		case dateUsed && !skippedDate && isDateKey(f.key) && f.value != "":
			skippedDate = true
		case k == "title" || f.value == "":
		case k == "author" || k == "by":
			who = append(who, `<span class="who">by `+v+`</span>`)
		case k == "status":
			status = append(status, `<span class="status">`+v+`</span>`)
		case k == "tags" || k == "tag":
			tags = append(tags, `<span class="tags">`+v+`</span>`)
		default:
			rest = append(rest, `<span>`+titleCase(f.key)+`: `+v+`</span>`)
		}
	}
	items = append(append(append(append(items, who...), status...), tags...), rest...)
	if m := words / wordsPerMinute; m >= 2 {
		items = append(items, `<span class="rt">`+strconv.Itoa(m)+` min read</span>`)
	}
	b := st.newOut()
	b.WriteString(`<div class="byline"><p class="facts">`)
	for i, it := range items {
		if err := st.tick(); err != nil {
			return "", false, err
		}
		if i > 0 {
			b.WriteString(`<span class="sep" aria-hidden="true"> · </span>`)
		}
		b.WriteString(it)
	}
	b.WriteString(`</p>` + tools + "</div>\n")
	if st.err != nil {
		return "", false, st.err
	}
	return b.String(), needsScript, nil
}

// dateText renders the byline's date: a full timestamp gets the server-side
// UTC text plus a data-localize hook, a bare date renders as the date, and an
// unparseable value is shown verbatim.
func dateText(v string) (string, bool) {
	t, hasTime, ok := parseDate(v)
	switch {
	case !ok:
		return `<span class="tstamp">` + html.EscapeString(v) + `</span>`, false
	case !hasTime:
		d := t.UTC().Format("2006-01-02")
		return `<time class="tstamp" datetime="` + d + `">` + d + `</time>`, false
	}
	iso := t.UTC().Format(time.RFC3339)
	return `<time class="tstamp" data-localize datetime="` + iso + `">` +
		t.UTC().Format("2006-01-02 · 15:04") + ` UTC</time>`, true
}

// ToggleButton is the sun / moon theme switch, shared by published pages and
// the operator pages. aria-pressed starts true when the server stamps dark.
func ToggleButton(serverTheme string) string {
	pressed := "false"
	if serverTheme == "dark" {
		pressed = "true"
	}
	return `<button class="themetoggle" type="button" aria-pressed="` + pressed + `" ` +
		`aria-label="Dark theme" title="Switch light or dark">` + sunSVG + moonSVG + `</button>`
}

// ThemeInitScript re-applies a stored light or dark choice before paint, where
// the page's origin can store one (the control plane; a sandboxed content page
// cannot, and follows the OS or the server theme).
func ThemeInitScript() string { return initThemeScript() }

// FrameScripts wires the theme toggle and the margin navs. The operator pages
// end their body with it; published pages add the same two scripts.
func FrameScripts() string { return docChromeScript + fScript }

// fScript opens the margin navs on a wide screen and folds them on a narrow
// one, and marks the contents entry for the section in view (showing only that
// section's h3s).
const fScript = `<script>(function(){
var r=document.documentElement;r.classList.add('fjs');
var wide=matchMedia('(min-width:78.5rem)'),navs=document.querySelectorAll('details.mnav');
var fold=function(){for(var i=0;i<navs.length;i++)navs[i].open=wide.matches;};
fold();if(wide.addEventListener)wide.addEventListener('change',fold);else wide.addListener(fold);
var links={};document.querySelectorAll('.outline a').forEach(function(a){links[a.getAttribute('href').slice(1)]=a;});
var heads=[].filter.call(document.querySelectorAll('main h2[id],main h3[id]'),function(h){return links[h.id];});
if(!heads.length)return;
var cur=null,mark=function(){
var c=heads[0];
for(var i=0;i<heads.length;i++){if(heads[i].getBoundingClientRect().top-32<=0)c=heads[i];else break;}
if(c===cur)return;cur=c;
Object.keys(links).forEach(function(k){links[k].removeAttribute('aria-current');});
links[c.id].setAttribute('aria-current','true');
var sec=links[c.id].parentNode.getAttribute('data-sec');
document.querySelectorAll('.outline .l3').forEach(function(li){li.classList.toggle('open',li.getAttribute('data-sec')===sec);});};
var q=false;window.addEventListener('scroll',function(){if(!q){q=true;requestAnimationFrame(function(){q=false;mark();});}},{passive:true});
mark();
})();</script>
`

// fCSS lays out the parts of an F page the operator pages do not share: the
// masthead (title and byline) and the reading column inside the frame.
const fCSS = `
.f main.wrap{padding:0 0 4rem}
.f main.wrap>pre,.f main.wrap>.table-wrap{max-width:100%}
.f main.wrap>p.code-first>code:first-child{margin-left:-.3em}
.dochead .doctitle{margin-top:0;margin-bottom:.9rem;overflow-wrap:break-word}
.dochead>.doctitle,.dochead>.byline{max-width:var(--measure)}
.byline{display:flex;align-items:flex-start;gap:1rem;padding-bottom:1rem;margin-bottom:2rem;
  border-bottom:1px solid var(--line)}
.byline .facts{flex:1 1 auto;min-width:0;margin:0;font:.875rem/1.5 var(--sans);color:var(--muted);
  font-size-adjust:none;font-variant-numeric:tabular-nums;text-wrap:pretty}
.byline .tstamp{color:var(--ink);font-weight:600}
.byline .themetoggle{margin-block:-.42rem}
.frame>.mnav:last-of-type{margin-bottom:1.5rem}
@media (min-width:78.5rem){.frame>.mnav:last-of-type{margin-bottom:0}}
@media (max-width:40rem){.dochead{padding-top:1.75rem}.dochead .doctitle{font-size:1.625rem}
  .byline{margin-bottom:1.25rem}}
`

// fPrintCSS prints an F page as one column with no navs or toggle.
const fPrintCSS = `
@media print{
.frame{display:block;padding:0}
.mnav,.themetoggle{display:none}
.byline{border-bottom-color:#d6d2c9}}
`

// frameCSS is the shared frame plus the F page's own rules.
func frameCSS() string { return theme.FrameCSS() + fCSS + fPrintCSS }
