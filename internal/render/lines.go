// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import "strings"

// lines indexes a document's lines by offset, so the renderer reads them out
// of the one string it holds instead of splitting it into a slice of strings
// (16 bytes per line, more than the line itself for short lines). One line,
// the hole, can be left out: the masthead lifts the first H1 out of the body
// this way without copying the document.
type lines struct {
	s    string
	off  []int32 // start of each line; line i ends before off[i+1]-1's '\n'
	hole int     // raw index of the line left out, or -1
}

// splitLines indexes s, which has "\n" line endings. The line count is held
// to st's MaxLines before the index is allocated.
func splitLines(st *state, s string) (*lines, error) {
	n := strings.Count(s, "\n") + 1
	if n > st.lim.MaxLines || len(s) > 1<<31-1 {
		return nil, st.limit("lines", st.lim.MaxLines)
	}
	off := make([]int32, 0, n)
	off = append(off, 0)
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			off = append(off, int32(i+1))
		}
	}
	return &lines{s: s, off: off, hole: -1}, nil
}

// raw is the line at raw index i, ignoring the hole.
func (l *lines) raw(i int) string {
	end := len(l.s)
	if i+1 < len(l.off) {
		end = int(l.off[i+1]) - 1
	}
	return l.s[l.off[i]:end]
}

// rawLen is the number of lines, ignoring the hole.
func (l *lines) rawLen() int { return len(l.off) }

// n is the number of lines, without the hole.
func (l *lines) n() int {
	if l.hole >= 0 {
		return len(l.off) - 1
	}
	return len(l.off)
}

// at is line i, counting past the hole.
func (l *lines) at(i int) string {
	if l.hole >= 0 && i >= l.hole {
		i++
	}
	return l.raw(i)
}
