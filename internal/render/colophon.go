// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
)

// colophon.go emits the editorial colophon baked at the foot of every ?render=md
// document (demiplane-rwj items 1+2), matching the locked design mock:
//
//   - Row 1 (nav), a real series only: `← previous` (muted) · position (muted) ·
//     `next →` (accent, weight 600) · the `demiplane` wordmark, MUTED and pushed
//     right, small-caps. A singleton artifact renders no nav row.
//   - Row 2 (meta): `published <date>` · `<size>` · `all artifacts →` (gallery
//     link), right-aligned.
//
// The metadata is real: the publish timestamp, the instance gallery URL (base-URL
// aware), and the rendered document's own byte size — which is exactly what the
// store records, so the "size" the reader sees is the artifact's size. Series
// prev/next is computed from the slug's prefix family (the same grouping the
// gallery uses), so a lesson in a family links to its neighbors and a one-off
// links to nothing.

// sizeSentinel is a placeholder the colophon emits in place of the document size,
// which is not known until the whole document (colophon included) is assembled.
// Markdown() measures the finished bytes and substitutes the real human size. The
// byte is a NUL, which cannot appear in the rendered HTML, so the replace is
// unambiguous.
const sizeSentinel = "\x00DP_SIZE\x00"

// seriesNav is the computed prev/next position of a slug within its family.
type seriesNav struct {
	prev, next string // neighbor slugs; "" when the current slug is an endpoint
	pos, total int    // 1-based position and family size
	isSeries   bool   // total >= 2 (a real family, not a singleton)
}

// computeSeries orders the current slug together with its already-published
// siblings and reports the neighbors and position. siblings is assumed to be the
// same-family set (the caller groups by slug prefix); duplicates, blanks, and the
// current slug are defused defensively. Ordering is lexical, which is correct for
// the zero-padded numeric suffixes artifacts use (dispatch-07 < -08 < -09) and
// stable for any other naming.
func computeSeries(slug string, siblings []string) seriesNav {
	fam := make([]string, 0, len(siblings)+1)
	seen := map[string]bool{slug: true}
	for _, s := range siblings {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		fam = append(fam, s)
	}
	fam = append(fam, slug)
	sort.Strings(fam)

	idx := 0
	for i, s := range fam {
		if s == slug {
			idx = i
			break
		}
	}
	nav := seriesNav{pos: idx + 1, total: len(fam), isSeries: len(fam) >= 2}
	if nav.isSeries {
		if idx > 0 {
			nav.prev = fam[idx-1]
		}
		if idx < len(fam)-1 {
			nav.next = fam[idx+1]
		}
	}
	return nav
}

// colophon renders the colophon block with a sizeSentinel standing in for the
// document size. Markdown() fills the real size after measuring the finished doc.
func colophon(opts Options) string {
	var b strings.Builder
	b.WriteString(`<div class="colo">`)

	nav := computeSeries(opts.Slug, opts.Siblings)
	if nav.isSeries {
		b.WriteString(`<div class="nav">`)
		if nav.prev != "" {
			b.WriteString(`<a class="prev" href="` + navHref(opts.ContentBase, nav.prev) + `">&larr;&nbsp;previous</a>`)
		}
		fmt.Fprintf(&b, `<span class="pos">%d of %d</span>`, nav.pos, nav.total)
		if nav.next != "" {
			b.WriteString(`<a class="next" href="` + navHref(opts.ContentBase, nav.next) + `">next&nbsp;&rarr;</a>`)
		}
		b.WriteString(`<span class="mark">demiplane</span>`)
		b.WriteString(`</div>`)
	}

	b.WriteString(`<div class="meta">`)
	b.WriteString(`<span>published ` + html.EscapeString(opts.Published.Format("2006-01-02")) + `</span>`)
	b.WriteString(`<span>&middot;</span>`)
	b.WriteString(`<span>` + sizeSentinel + `</span>`)
	if opts.IndexURL != "" {
		b.WriteString(`<span class="r"><a href="` + html.EscapeString(opts.IndexURL) + `">all artifacts&nbsp;&rarr;</a></span>`)
	}
	b.WriteString(`</div>`)

	b.WriteString(`</div>`)
	return b.String()
}

// navHref builds a series link. A slug is URL-safe by construction, but both the
// base and slug are attribute-escaped defensively.
func navHref(base, slug string) string {
	return html.EscapeString(base + "/" + slug)
}

// fillSize substitutes the sizeSentinel in the finished document with the real
// human-readable size. The stored artifact size is exactly len(doc) once the
// number is inlined, so the value is derived as a fixed point: the length with
// the sentinel removed, plus the length of the size string itself.
func fillSize(doc string) string {
	if !strings.Contains(doc, sizeSentinel) {
		return doc
	}
	base := len(doc) - len(sizeSentinel)
	size := humanSize(int64(base))
	size = humanSize(int64(base + len(size))) // one refinement for the number's own bytes
	return strings.Replace(doc, sizeSentinel, size, 1)
}

// humanSize formats a byte count compactly (B/KB/MB/…). A local copy of the
// gallery's formatter, kept here so the render package carries no server-package
// dependency.
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

// coloCSS styles the colophon to the locked mock: a hairline top rule, a small
// sans face, muted prev/position and accented next, the muted small-caps wordmark
// pushed right, and a right-aligned meta row. Colors are theme tokens with OKLCH
// fallbacks so the block still renders under a --css override that ships none.
const coloCSS = `
.colo{margin-top:3.5rem;padding-top:1rem;border-top:1px solid var(--line,oklch(0.895 0.008 255));
  font:.8rem/1.7 var(--sans)}
.colo .nav{display:flex;flex-wrap:wrap;gap:.4rem 1.4rem;align-items:baseline}
.colo .nav a.prev{color:var(--muted,oklch(0.495 0.012 258))}
.colo .nav .pos{color:var(--muted,oklch(0.495 0.012 258))}
.colo .nav a.next{color:var(--accent,oklch(0.485 0.135 27));font-weight:600}
.colo .nav a:hover{text-decoration:underline;text-underline-offset:2px}
.colo .mark{margin-left:auto;letter-spacing:.12em;text-transform:uppercase;
  color:var(--muted,oklch(0.495 0.012 258));font-size:.72rem}
.colo .meta{display:flex;flex-wrap:wrap;gap:.3rem 1.4rem;color:var(--muted,oklch(0.495 0.012 258));
  font-size:.75rem;margin-top:.5rem}
.colo .meta a{color:var(--muted,oklch(0.495 0.012 258))}
.colo .meta a:hover{color:var(--accent,oklch(0.485 0.135 27));text-decoration:underline;text-underline-offset:2px}
.colo .meta .r{margin-left:auto}
`
