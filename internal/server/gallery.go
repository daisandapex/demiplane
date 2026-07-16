// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"fmt"
	"html"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/daisandapex/demiplane/internal/store"
)

// gallery.go owns feature T2.3: the GET /gallery artifact index on the control
// plane. It renders the non-private, non-expired artifacts as house-style cards
// (title/slug, type badge, size, published + expiry, lock icon for a password
// gate), with a dependency-free inline filter/sort/search/group script and a
// per-card copy-URL button. The empty state points a first-time user at /connect.
//
// Findability (demiplane-k3x): the flat card wall is grouped by slug prefix — the
// text before the first hyphen (dispatch-08 → dispatch) — because artifacts are
// named in stable families; grouping by prefix matches how a user hunts for "my
// demiplanes" better than recency buckets, which churn daily and split a family
// across time windows. Singleton prefixes fold into an "Other" section so one-offs
// don't each become their own header. Grouping, filtering, and sorting are all
// client-side and progressive: with JS disabled the page is the flat card grid
// exactly as before; the inline script layers the collapsible groups on top.
//
// Origin note: the gallery lives on the control plane (like /list and the
// landing) but every artifact link points at the CONTENT origin via
// s.contentBase(r) — artifact bodies are served cross-origin per ADR 0003.
//
// Security: store.List already excludes private + expired artifacts; we skip
// a.Private again as defense-in-depth and html.EscapeString every field. The
// filter/sort script is a tiny first-party inline <script> operating only on the
// DOM — no user input is reflected into executable positions and no dependency is
// pulled in. The control plane serves no CSP, matching the landing/docs pages
// that already carry inline chrome script and style.
func init() {
	registerCoreControlRoute([]string{"gallery"}, func(mux *http.ServeMux, s *Server) {
		mux.HandleFunc("GET /gallery", s.handleGallery)
	})
}

func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	arts, err := s.store.List(store.DefaultOwner)
	if err != nil {
		log.Printf("gallery list failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Artifact bodies live on the content origin (ADR 0003); mint their links
	// against that base, not the control-plane request host.
	base := s.contentBase(r)

	var b strings.Builder
	b.WriteString(`<span class="kicker">Artifact index</span>`)
	b.WriteString(`<h1>Gallery</h1>`)
	b.WriteString(`<p>Every published artifact on this instance. Private (capability-URL) ` +
		`artifacts are never listed; their unguessable URL is the secret.</p>`)
	b.WriteString(galleryStyle)

	// Card grid. Only non-private artifacts are shown; store.List already filters
	// them, and we skip a.Private again as defense-in-depth.
	var cards strings.Builder
	shown := 0
	for _, a := range arts {
		if a.Private {
			continue
		}
		cards.WriteString(s.galleryCard(base, a))
		shown++
	}

	if shown == 0 {
		// True empty state (no public artifacts at all) — orient a newcomer.
		b.WriteString(`<div class="gempty"><div class="t">No artifacts yet</div>` +
			`<p>Publish your first page, then it shows up here. New to demiplane? ` +
			`<a href="/connect">Connect your tools</a> to publish from your editor, ` +
			`agent, or the command line.</p></div>`)
		writeHTML(w, s.pageHTML("Gallery — demiplane", galleryNav(), b.String()))
		return
	}

	// Search + sort toolbar. IDs are wired by the inline script below.
	fmt.Fprintf(&b, `<div class="gtools"><input id="gq" class="gsearch" type="search" `+
		`placeholder="Filter %s by name or slug" `+
		`autocomplete="off" spellcheck="false" aria-label="Filter artifacts">`,
		html.EscapeString(plural(shown, "artifact")))
	b.WriteString(`<select id="ggroup" class="gsortsel" aria-label="Group artifacts">` +
		`<option value="prefix">Group by prefix</option>` +
		`<option value="none">No grouping</option>` +
		`</select>`)
	b.WriteString(`<select id="gsort" class="gsortsel" aria-label="Sort artifacts">` +
		`<option value="newest">Newest first</option>` +
		`<option value="oldest">Oldest first</option>` +
		`<option value="name">Name (A–Z)</option>` +
		`<option value="size">Largest first</option>` +
		`</select></div>`)

	b.WriteString(`<div id="ggrid" class="ggrid">`)
	b.WriteString(cards.String())
	b.WriteString(`</div>`)
	b.WriteString(`<p id="gnomatch" class="gnomatch" style="display:none">No artifacts match that filter.</p>`)
	b.WriteString(galleryScript)

	writeHTML(w, s.pageHTML("Gallery — demiplane", galleryNav(), b.String()))
}

// galleryCard renders one artifact as a slug-first card: the slug is the card,
// with a single published-date caption and the open/copy actions. The
// type/size/expiry micro-labels the design audit flagged (demiplane-0pw) are
// dropped from the visible surface, but the underlying values stay as data-*
// attributes so the inline filter/sort/group script keeps working. Every dynamic
// field is escaped; the URL points at the content origin.
func (s *Server) galleryCard(base string, a store.Artifact) string {
	url := base + "/" + a.Slug
	created := a.CreatedAt.Format("2006-01-02 15:04")
	lock := ""
	if a.HasPassword() {
		lock = `<span class="glock" title="password-protected" aria-label="password protected">🔒</span>`
	}
	badge := shortTypeLabel(a.ContentType) // data-type only; not shown

	var c strings.Builder
	c.WriteString(`<div class="gcard"`)
	fmt.Fprintf(&c, ` data-name="%s"`, html.EscapeString(a.Slug))
	fmt.Fprintf(&c, ` data-slug="%s"`, html.EscapeString(a.Slug))
	fmt.Fprintf(&c, ` data-type="%s"`, html.EscapeString(badge))
	fmt.Fprintf(&c, ` data-size="%d"`, a.Size)
	fmt.Fprintf(&c, ` data-group="%s"`, html.EscapeString(slugGroup(a.Slug)))
	fmt.Fprintf(&c, ` data-created="%d">`, a.CreatedAt.Unix())

	c.WriteString(`<div class="ghead">`)
	fmt.Fprintf(&c, `<a class="gtitle" href="%s">%s</a>%s`,
		html.EscapeString(url), html.EscapeString(a.Slug), lock)
	c.WriteString(`</div>`)

	fmt.Fprintf(&c, `<div class="gdate">%s</div>`, html.EscapeString(created))

	c.WriteString(`<div class="gactions">`)
	fmt.Fprintf(&c, `<a class="gopen" href="%s">Open</a>`, html.EscapeString(url))
	fmt.Fprintf(&c, `<button class="gcopy" type="button" data-url="%s">Copy URL</button>`,
		html.EscapeString(url))
	c.WriteString(`</div>`)

	c.WriteString(`</div>`)
	return c.String()
}

// galleryNav returns the shared top nav with the Gallery entry marked active.
func galleryNav() []navLink {
	return topNav("/gallery")
}

// shortTypeLabel derives a compact type badge from a content type: the subtype
// with any structured-syntax suffix and parameters trimmed (image/svg+xml → svg,
// text/html; charset=utf-8 → html, application/octet-stream → bin).
func shortTypeLabel(ct string) string {
	ct = shortType(ct) // strip charset/boundary params
	sub := ct
	if i := strings.IndexByte(ct, '/'); i >= 0 {
		sub = ct[i+1:]
	}
	if i := strings.IndexByte(sub, '+'); i >= 0 {
		sub = sub[:i] // svg+xml → svg
	}
	sub = strings.TrimSpace(sub)
	switch sub {
	case "", "octet-stream":
		return "bin"
	case "plain":
		return "text"
	case "javascript":
		return "js"
	case "markdown":
		return "md"
	}
	return sub
}

// slugGroup derives a card's group key from its slug: the lowercased text before
// the first hyphen (dispatch-08 → dispatch). A hyphen-free slug is its own key
// (the client folds any single-member group into the shared "Other" section, so a
// stray one-off never gets a lonely header). An empty slug falls back to "other".
func slugGroup(slug string) string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return "other"
	}
	if i := strings.IndexByte(slug, '-'); i > 0 {
		return slug[:i]
	}
	return slug
}

// humanSize formats a byte count as a compact human-readable string.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	val := float64(n) / float64(div)
	return strconv.FormatFloat(val, 'f', 1, 64) + " " + [...]string{"KB", "MB", "GB", "TB", "PB"}[exp]
}

// galleryStyle holds the gallery-only classes, scoped with a g-prefix so they do
// not collide with the shared chrome classes in chrome.go. Every color is a theme
// token so the gallery re-skins under --theme dark like the rest of the chrome.
const galleryStyle = `<style>
.gtools{display:flex;flex-wrap:wrap;gap:.6rem;align-items:center;margin:1.2rem 0 .4rem}
.gsearch{flex:1 1 16rem;min-width:12rem;font:inherit;font-size:.95rem;color:var(--ink);
  background:var(--panel);border:1px solid var(--line);border-radius:9px;padding:.5rem .8rem}
.gsearch:focus{border-color:var(--accent)}
.gsortsel{font:inherit;font-size:.9rem;color:var(--ink);background:var(--panel);
  border:1px solid var(--line);border-radius:9px;padding:.5rem .7rem}
.gsortsel:focus{border-color:var(--accent)}
.ggrid{display:grid;grid-template-columns:repeat(auto-fill,minmax(15rem,1fr));gap:.8rem;margin:1rem 0}
.gcard{display:flex;flex-direction:column;gap:.55rem;background:var(--panel);
  border:1px solid var(--line);border-radius:12px;padding:.95rem 1.05rem}
.gcard:hover{border-color:var(--accent)}
.ghead{display:flex;align-items:baseline;gap:.5rem}
.gtitle{font-family:var(--serif);font-size:1.12rem;color:var(--ink);word-break:break-word}
.gtitle:hover{color:var(--accent);text-decoration:none}
.glock{font-size:.85rem}
.gdate{font-family:var(--sans);font-size:.8rem;color:var(--muted)}
.gactions{display:flex;gap:.5rem;margin-top:.2rem}
.gopen{font-size:.85rem;font-weight:600;color:var(--accent)}
.gcopy{font:inherit;font-size:.82rem;background:transparent;color:var(--muted);
  border:1px solid var(--line);border-radius:6px;padding:.2rem .6rem;cursor:pointer}
.gcopy:hover{border-color:var(--accent);color:var(--accent)}
.ggrid.grouped{display:block}
.ggroup{margin:1.2rem 0}
.ggrouphead{display:flex;align-items:center;gap:.55rem;width:100%;text-align:left;font:inherit;
  background:transparent;border:none;border-bottom:1px solid var(--line);color:var(--ink);
  padding:.35rem 0;margin-bottom:.7rem;cursor:pointer}
.ggrouphead:hover .ggname{color:var(--accent)}
.ggname{font-family:var(--serif);font-size:1.08rem;text-transform:capitalize}
.ggcount{margin-left:auto;font-family:var(--mono);font-size:.68rem;font-weight:700;color:var(--muted);
  background:var(--panel);border:1px solid var(--line);border-radius:999px;padding:.05rem .5rem}
.ggrouphead::after{content:"\25be";color:var(--muted);font-size:.85rem;transition:transform .15s}
.ggrouphead[aria-expanded="false"]::after{transform:rotate(-90deg)}
.ggroupbody{margin:0}
.gnomatch{color:var(--muted);font-style:italic;margin:1rem 0}
.gempty{background:var(--panel);border:1px solid var(--line);border-radius:12px;
  padding:1.6rem 1.4rem;margin:1.4rem 0;text-align:center}
.gempty .t{font-family:var(--serif);font-size:1.3rem;color:var(--ink);margin-bottom:.3rem}
.gempty p{color:var(--muted);margin:0}
</style>`

// galleryScript is the dependency-free client filter/sort/copy behavior. It
// reads only DOM data-* attributes (all server-escaped) — no user string is ever
// interpolated into this script.
const galleryScript = `<script>
(function(){
  var q=document.getElementById('gq');
  var sort=document.getElementById('gsort');
  var groupSel=document.getElementById('ggroup');
  var grid=document.getElementById('ggrid');
  var none=document.getElementById('gnomatch');
  if(!grid) return;
  var cards=Array.prototype.slice.call(grid.querySelectorAll('.gcard'));
  // userSet records groups the reader explicitly toggled; unset groups take the
  // default (a real series starts COLLAPSED so it reads as one reference —
  // demiplane-0pw — while the 'other' one-offs bucket stays open).
  var userSet={};
  function isColl(k){return (k in userSet)?userSet[k]:(k!=='other');}
  function keyOf(c){return c.getAttribute('data-group')||'other';}
  function apply(){
    var term=(q.value||'').trim().toLowerCase();
    var visible=[],hidden=[];
    cards.forEach(function(c){
      var hay=(c.getAttribute('data-name')+' '+c.getAttribute('data-slug')+' '+
        c.getAttribute('data-type')).toLowerCase();
      if(hay.indexOf(term)>=0){visible.push(c);}else{hidden.push(c);}
    });
    var mode=sort.value;
    visible.sort(function(a,b){
      if(mode==='oldest') return (+a.dataset.created)-(+b.dataset.created);
      if(mode==='name') return a.dataset.name.localeCompare(b.dataset.name);
      if(mode==='size') return (+b.dataset.size)-(+a.dataset.size);
      return (+b.dataset.created)-(+a.dataset.created);
    });
    while(grid.firstChild) grid.removeChild(grid.firstChild);
    var grouping=groupSel&&groupSel.value==='prefix';
    grid.classList.toggle('grouped',grouping);
    if(grouping){
      // Fold single-member prefixes into a shared 'other' bucket; preserve the
      // sorted order for both the cards within a group and the group sequence
      // (first-seen order == sort order). 'other' is forced to the end.
      var counts={};
      visible.forEach(function(c){var k=keyOf(c);counts[k]=(counts[k]||0)+1;});
      var order=[],buckets={};
      visible.forEach(function(c){
        var k=keyOf(c); if(counts[k]<2) k='other';
        if(!buckets[k]){buckets[k]=[];order.push(k);}
        buckets[k].push(c);
      });
      order.sort(function(a,b){if(a==='other')return 1;if(b==='other')return -1;return 0;});
      order.forEach(function(k){
        var sec=document.createElement('section'); sec.className='ggroup';
        var head=document.createElement('button'); head.type='button'; head.className='ggrouphead';
        var coll=isColl(k); head.setAttribute('aria-expanded',coll?'false':'true');
        var nm=document.createElement('span'); nm.className='ggname'; nm.textContent=k;
        var ct=document.createElement('span'); ct.className='ggcount'; ct.textContent=buckets[k].length;
        head.appendChild(nm); head.appendChild(ct);
        var body=document.createElement('div'); body.className='ggrid ggroupbody';
        if(coll) body.style.display='none';
        buckets[k].forEach(function(c){c.style.display='';body.appendChild(c);});
        head.addEventListener('click',function(){
          var now=!isColl(k); userSet[k]=now;
          head.setAttribute('aria-expanded',now?'false':'true');
          body.style.display=now?'none':'';
        });
        sec.appendChild(head); sec.appendChild(body); grid.appendChild(sec);
      });
    } else {
      visible.forEach(function(c){c.style.display='';grid.appendChild(c);});
    }
    hidden.forEach(function(c){c.style.display='none';grid.appendChild(c);});
    if(none) none.style.display=visible.length?'none':'';
  }
  q.addEventListener('input',apply);
  sort.addEventListener('change',apply);
  if(groupSel) groupSel.addEventListener('change',apply);
  apply();
})();
document.addEventListener('click',function(e){
  var b=e.target.closest('button.gcopy'); if(!b) return;
  var url=b.getAttribute('data-url');
  navigator.clipboard.writeText(url).then(function(){
    var o=b.textContent; b.textContent='Copied'; setTimeout(function(){b.textContent=o},1200);
  });
});
</script>`
