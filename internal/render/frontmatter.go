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

// parseFrontmatter lifts a leading YAML-style frontmatter block out of src. The
// block must START the document: the first line is exactly `---`, and a later
// line is exactly `---` (the close). Between them, each `key: value` line
// becomes a field (value is the verbatim string, trimmed); lines without a colon
// are ignored, as are nested/complex constructs (this is a deliberately minimal,
// dependency-free parser — strings only). It returns the fields, the body with
// the block removed, and ok=true. If there is no opening fence or no closing
// fence, it returns ok=false and leaves the body untouched — so a document that
// merely opens with a `---` horizontal rule is not mistaken for frontmatter.
func parseFrontmatter(src []byte) (fields []metaField, rest []byte, ok bool) {
	s := strings.ReplaceAll(string(src), "\r\n", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, src, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			// Found the close: parse the lines between the fences.
			for _, ln := range lines[1:i] {
				t := strings.TrimSpace(ln)
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
				fields = append(fields, metaField{key: key, value: strings.TrimSpace(v)})
			}
			rest = []byte(strings.Join(lines[i+1:], "\n"))
			return fields, rest, true
		}
	}
	// No closing fence: not frontmatter.
	return nil, src, false
}

// metaHeader renders the parsed frontmatter as a styled meta-header: the first
// date/published field as a localized timestamp row, then every other field as
// its own labeled line. It returns the HTML and whether a localize <script> is
// needed (true only when a full timestamp — not a bare date — was rendered).
func metaHeader(fields []metaField) (htmlOut string, needsScript bool) {
	if len(fields) == 0 {
		return "", false
	}
	var b strings.Builder
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

	var rows strings.Builder
	skipped := false
	for _, f := range fields {
		if !skipped && dateUsed && isDateKey(f.key) {
			skipped = true // drop only the one field promoted to the date row
			continue
		}
		rows.WriteString(`<div class="metarow"><dt>` + titleCase(f.key) +
			`</dt><dd>` + html.EscapeString(f.value) + `</dd></div>`)
	}
	if rows.Len() > 0 {
		b.WriteString(`<dl class="metafields">`)
		b.WriteString(rows.String())
		b.WriteString(`</dl>`)
	}
	b.WriteString("</div>\n")
	return b.String(), needsScript
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

// metaHeadCSS styles the meta-header: a quiet tabular timestamp, then a clean
// definition-list of labeled fields (accent kicker labels, body-ink values).
// Token references carry literal fallbacks so the block still reads under a
// --css override that ships no tokens.
const metaHeadCSS = `
.metahead{margin:.2rem 0 2.4rem}
.metahead .metadate{margin-bottom:1rem}
.metahead .tstamp{font-family:var(--sans);font-size:.82rem;font-weight:600;letter-spacing:.03em;
  color:var(--muted,oklch(0.495 0.022 62));font-variant-numeric:tabular-nums}
.metahead .metafields{margin:0;display:grid;gap:.4rem}
.metahead .metarow{display:flex;gap:.85rem;margin:0;align-items:baseline}
.metahead .metarow dt{flex:0 0 auto;min-width:7.5rem;font-family:var(--sans);font-size:.7rem;
  font-weight:700;letter-spacing:.13em;text-transform:uppercase;line-height:1.5;
  color:var(--accent,oklch(0.555 0.162 47))}
.metahead .metarow dd{margin:0;color:var(--ink,oklch(0.265 0.020 60))}
`
