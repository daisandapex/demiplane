// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"html"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

// regexInline is inline as it was written with regexp passes for code spans
// and links, kept as the oracle for the index-based passes.
func regexInline(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "�")
	}
	s = html.EscapeString(s)
	var spans []string
	s = reCode.ReplaceAllStringFunc(s, func(m string) string {
		spans = append(spans, "<code>"+reCode.FindStringSubmatch(m)[1]+"</code>")
		return codeSentinel + strconv.Itoa(len(spans)-1) + codeSentinel
	})
	s = reLink.ReplaceAllStringFunc(s, func(match string) string {
		m := reLink.FindStringSubmatch(match)
		if !safeURL(m[2]) {
			return m[1]
		}
		return `<a href="` + m[2] + `" rel="noopener noreferrer">` + m[1] + `</a>`
	})
	s = reBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = reItalic.ReplaceAllString(s, "<em>$1</em>")
	s = reItalicU.ReplaceAllString(s, "<em>$1</em>")
	if len(spans) == 0 {
		return s
	}
	var b strings.Builder
	restored := make([]bool, len(spans))
	for {
		j := strings.IndexByte(s, 0)
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:j])
		rest := s[j+1:]
		k := strings.IndexByte(rest, 0)
		if k < 0 {
			b.WriteString(s[j:])
			return b.String()
		}
		if idx, err := strconv.Atoi(rest[:k]); err == nil && idx >= 0 && idx < len(spans) && !restored[idx] {
			restored[idx] = true
			b.WriteString(spans[idx])
			s = rest[k+1:]
			continue
		}
		b.WriteByte(0)
		s = rest
	}
}

// TestInlineMatchesRegexOracle: the index-based code-span and link passes
// produce exactly what the regexp passes did, over random text built from
// the characters those passes care about, and inlineUpper bounds every result.
func TestInlineMatchesRegexOracle(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	alphabet := []string{"`", "`", "a", " ", "*", "_", "[", "]", "(", ")", "b", "<", "&", "\x00", "0", "/", "https://x", ":"}
	for n := 0; n < 200000; n++ {
		var b strings.Builder
		for k := r.Intn(24); k > 0; k-- {
			b.WriteString(alphabet[r.Intn(len(alphabet))])
		}
		s := b.String()
		got, want := inline(s), regexInline(s)
		if got != want {
			t.Fatalf("inline(%q)\n got %q\nwant %q", s, got, want)
		}
		if len(got) > inlineUpper(s) {
			t.Fatalf("inline(%q) is %d bytes, inlineUpper says at most %d", s, len(got), inlineUpper(s))
		}
	}
}
