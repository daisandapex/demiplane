// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"html"
	"strings"
	"time"
)

// metaField is one parsed frontmatter key/value pair. Order is preserved (a
// slice, not a map) so the rendered meta-header lines follow the source order.
type metaField struct {
	key   string
	value string
}

// frontmatterEnd finds a leading YAML-style frontmatter block. The block must
// START the document: the first line is exactly `---`, and a later line is
// exactly `---` (the close). It returns the raw index of the closing line, or
// -1 when there is no block: with no opening or no closing fence the body is
// untouched, so a document that merely opens with a `---` horizontal rule is
// not mistaken for frontmatter. It allocates nothing.
func frontmatterEnd(l *lines) int {
	if l.rawLen() == 0 || strings.TrimSpace(l.raw(0)) != "---" {
		return -1
	}
	for i := 1; i < l.rawLen(); i++ {
		if strings.TrimSpace(l.raw(i)) == "---" {
			return i
		}
	}
	return -1
}

// parseFrontmatter parses the block that frontmatterEnd found, between line 0
// and the closing line close. Each `key: value` line becomes a field (value is
// the verbatim string, trimmed); lines without a colon are ignored, as are
// comments and nested/complex constructs (this is a deliberately minimal,
// dependency-free parser: strings only). Values are slices of the document,
// not copies. The block's size, its field count and each key and value are
// held to st's caps before any field is kept.
func parseFrontmatter(st *state, l *lines, close int) ([]metaField, error) {
	if size := int(l.off[close]) - int(l.off[1]); size > st.lim.MaxMetaBytes {
		return nil, st.limit("frontmatter bytes", st.lim.MaxMetaBytes)
	}
	var fields []metaField
	for i := 1; i < close; i++ {
		t := strings.TrimSpace(l.raw(i))
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, found := strings.Cut(t, ":")
		if !found {
			continue
		}
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		value := strings.TrimSpace(v)
		switch {
		case len(fields) >= st.lim.MaxMetaFields:
			return nil, st.limit("frontmatter fields", st.lim.MaxMetaFields)
		case len(key) > maxMetaKey:
			return nil, st.limit("frontmatter key bytes", maxMetaKey)
		case len(value) > maxMetaValue:
			return nil, st.limit("frontmatter value bytes", maxMetaValue)
		}
		fields = append(fields, metaField{key: key, value: value})
	}
	return fields, nil
}

// metaHeader renders the parsed frontmatter as a styled meta-header: the first
// date/published field as a localized timestamp row, then every other field as
// its own labeled line. It writes through a budgeted out, so it is charged as
// it is built. It returns the HTML and whether a localize <script> is needed
// (true only when a full timestamp, not a bare date, was rendered).
func metaHeader(st *state, fields []metaField) (htmlOut string, needsScript bool, err error) {
	if len(fields) == 0 {
		return "", false, nil
	}
	b := st.newOut()
	b.WriteString(`<div class="metahead">`)

	dateUsed := false
	for _, f := range fields {
		if isDateKey(f.key) {
			row, script := dateRow(f.value)
			b.WriteString(row)
			needsScript = script
			dateUsed = true
			break
		}
	}

	rows := len(fields)
	if dateUsed {
		rows-- // the one field promoted to the date row
	}
	if rows > 0 {
		b.WriteString(`<dl class="metafields">`)
		skipped := false
		for _, f := range fields {
			if !skipped && dateUsed && isDateKey(f.key) {
				skipped = true
				continue
			}
			b.WriteString(`<div class="metarow"><dt>` + titleCase(f.key) +
				`</dt><dd>` + html.EscapeString(f.value) + `</dd></div>`)
		}
		b.WriteString(`</dl>`)
	}
	b.WriteString("</div>\n")
	if st.err != nil {
		return "", false, st.err
	}
	return b.String(), needsScript, nil
}

// isDateKey reports whether key marks the timestamp field (date or published).
func isDateKey(key string) bool {
	switch strings.ToLower(key) {
	case "date", "published":
		return true
	}
	return false
}

// dateLayouts are the accepted timestamp shapes, most specific first. Layouts
// without a zone parse as UTC (time.Parse's default location).
var dateLayouts = []struct {
	layout  string
	hasTime bool
}{
	{time.RFC3339, true},
	{"2006-01-02T15:04:05", true},
	{"2006-01-02 15:04:05", true},
	{"2006-01-02T15:04", true},
	{"2006-01-02 15:04", true},
	{"2006-01-02", false},
}

// parseDate tries the accepted layouts. ok=false means the value is not an
// ISO-8601 date/timestamp and should be shown as-is.
func parseDate(v string) (t time.Time, hasTime, ok bool) {
	s := strings.TrimSpace(v)
	for _, l := range dateLayouts {
		if parsed, err := time.Parse(l.layout, s); err == nil {
			return parsed, l.hasTime, true
		}
	}
	return time.Time{}, false, false
}

// dateRow renders the timestamp row. A full timestamp gets the server-side UTC
// text plus a data-localize hook (the client script appends local time + zone);
// a bare date renders as the date alone; an unparseable value is shown verbatim.
func dateRow(v string) (htmlOut string, needsScript bool) {
	t, hasTime, ok := parseDate(v)
	if !ok {
		return `<div class="metadate"><span class="tstamp">` + html.EscapeString(v) + `</span></div>`, false
	}
	if !hasTime {
		d := t.UTC().Format("2006-01-02")
		return `<div class="metadate"><time class="tstamp" datetime="` + d + `">` + d + `</time></div>`, false
	}
	iso := t.UTC().Format(time.RFC3339)
	txt := t.UTC().Format("2006-01-02 · 15:04") + " UTC"
	return `<div class="metadate"><time class="tstamp" data-localize datetime="` + iso + `">` +
		txt + `</time></div>`, true
}

// titleCase turns a frontmatter key into a display label: underscores/hyphens
// become spaces and each word is capitalized (e.g. "footer_link" → "Footer
// Link"). The result is HTML-escaped.
func titleCase(key string) string {
	key = strings.ReplaceAll(key, "_", " ")
	key = strings.ReplaceAll(key, "-", " ")
	words := strings.Fields(key)
	for i, w := range words {
		r := []rune(w)
		r[0] = []rune(strings.ToUpper(string(r[0])))[0]
		words[i] = string(r)
	}
	return html.EscapeString(strings.Join(words, " "))
}

// tstampScript localizes every full-timestamp meta-header date on the client:
// it rebuilds the text as "YYYY-MM-DD · HH:MM UTC · HH:MM <localtz>", reading the
// machine ISO value from the datetime attribute. No-JS viewers keep the
// server-rendered UTC text. Self-contained, no external dependency.
const tstampScript = `<script>(function(){
function p(n){return(n<10?'0':'')+n;}
var els=document.querySelectorAll('time.tstamp[data-localize]');
for(var i=0;i<els.length;i++){
var el=els[i],d=new Date(el.getAttribute('datetime'));
if(isNaN(d.getTime()))continue;
var utc=d.getUTCFullYear()+'-'+p(d.getUTCMonth()+1)+'-'+p(d.getUTCDate())+' · '+p(d.getUTCHours())+':'+p(d.getUTCMinutes())+' UTC';
var tz='';try{var parts=new Intl.DateTimeFormat('en-US',{timeZoneName:'short'}).formatToParts(d);
for(var j=0;j<parts.length;j++){if(parts[j].type==='timeZoneName')tz=parts[j].value;}}catch(e){}
var loc=p(d.getHours())+':'+p(d.getMinutes())+(tz?' '+tz:'');
el.textContent=utc+' · '+loc;
}
})();</script>
`

// metaHeadCSS styles the meta-header as the document byline: a letterspaced
// small sans over a closing hairline (the approved design's .meta grammar),
// then a definition-list of muted labels and body-ink values. The accent stays
// out of it (links only).
// Token references carry literal fallbacks so the block still reads under a
// --css override that ships no tokens.
const metaHeadCSS = `
.metahead{margin:.2rem 0 1.75rem;border-bottom:1px solid var(--line,oklch(0.910 0.013 87));
  padding-bottom:1.25rem}
.metahead .metadate{margin-bottom:1rem}
.metahead .tstamp{font:600 .8125rem/1.5 var(--sans);text-transform:uppercase;letter-spacing:.08em;
  color:var(--muted,oklch(0.462 0.011 78));font-variant-numeric:tabular-nums}
.metahead .metafields{margin:0;display:grid;gap:.4rem}
.metahead .metarow{display:flex;gap:.85rem;margin:0;align-items:baseline}
.metahead .metarow dt{flex:0 0 auto;min-width:7.5rem;font-family:var(--sans);font-size:.7rem;
  font-weight:600;letter-spacing:.08em;text-transform:uppercase;line-height:1.5;
  color:var(--muted,oklch(0.462 0.011 78))}
.metahead .metarow dd{margin:0;color:var(--ink,oklch(0.237 0.009 75))}
`
