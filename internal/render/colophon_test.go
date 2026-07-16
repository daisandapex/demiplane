// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"strings"
	"testing"
	"time"
)

func TestComputeSeries(t *testing.T) {
	cases := []struct {
		name             string
		slug             string
		siblings         []string
		wantPrev, wantNx string
		wantPos, wantTot int
		wantSeries       bool
	}{
		{"middle", "dispatch-08", []string{"dispatch-07", "dispatch-09"}, "dispatch-07", "dispatch-09", 2, 3, true},
		{"first", "demo-a", []string{"demo-b", "demo-c"}, "", "demo-b", 1, 3, true},
		{"last", "demo-c", []string{"demo-a", "demo-b"}, "demo-b", "", 3, 3, true},
		{"singleton", "solo", nil, "", "", 1, 1, false},
		{"pair", "x-1", []string{"x-2"}, "", "x-2", 1, 2, true},
		{"gap-prev-missing", "l-05", []string{"l-03", "l-09"}, "l-03", "l-09", 2, 3, true},
		{"dupe-and-self-defused", "d-2", []string{"d-1", "d-2", "d-1"}, "d-1", "", 2, 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := computeSeries(tc.slug, tc.siblings)
			if n.prev != tc.wantPrev || n.next != tc.wantNx {
				t.Errorf("prev/next = %q/%q, want %q/%q", n.prev, n.next, tc.wantPrev, tc.wantNx)
			}
			if n.pos != tc.wantPos || n.total != tc.wantTot {
				t.Errorf("pos/total = %d/%d, want %d/%d", n.pos, n.total, tc.wantPos, tc.wantTot)
			}
			if n.isSeries != tc.wantSeries {
				t.Errorf("isSeries = %v, want %v", n.isSeries, tc.wantSeries)
			}
		})
	}
}

// TestColophonSeriesRendering pins the mock's nav row for a mid-series artifact:
// muted previous, position, accented next, and the muted wordmark, with sibling
// links minted against the content base, plus the meta row.
func TestColophonSeriesRendering(t *testing.T) {
	pub := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	out := string(Markdown([]byte("# Lesson\n\nBody."), Options{
		Colophon:    true,
		Slug:        "dispatch-08",
		Published:   pub,
		Siblings:    []string{"dispatch-07", "dispatch-09"},
		ContentBase: "https://c.example",
		IndexURL:    "https://demiplane.example/gallery",
	}))
	for _, want := range []string{
		`<div class="colo">`,
		`<a class="prev" href="https://c.example/dispatch-07">`,
		`<span class="pos">2 of 3</span>`,
		`<a class="next" href="https://c.example/dispatch-09">`,
		`<span class="mark">demiplane</span>`,
		`published 2026-07-15`,
		`<a href="https://demiplane.example/gallery">all artifacts`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("colophon missing %q\n%s", want, out)
		}
	}
	// The token colors and colophon styling are wired in.
	for _, css := range []string{"--tok-key", ".colo .nav a.next", ".colo .mark"} {
		if !strings.Contains(out, css) {
			t.Errorf("colophon output missing CSS %q", css)
		}
	}
	// p1 (demiplane-10n): the colophon meta links hover with an underline, unifying
	// their affordance with the nav links (which already underline on hover).
	if !strings.Contains(out, ".colo .meta a:hover{color:var(--accent,oklch(0.485 0.135 27));text-decoration:underline") {
		t.Errorf("colophon meta hover should underline to match nav links:\n%s", out)
	}
}

// TestColophonSingletonNoNav verifies a singleton artifact shows the meta row but
// no prev/next nav row (and thus no wordmark, which lives in that row).
func TestColophonSingletonNoNav(t *testing.T) {
	out := string(Markdown([]byte("# Solo"), Options{
		Colophon:  true,
		Slug:      "solo",
		Published: time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC),
		IndexURL:  "https://demiplane.example/gallery",
	}))
	if strings.Contains(out, `class="nav"`) || strings.Contains(out, `class="prev"`) || strings.Contains(out, `class="next"`) {
		t.Errorf("singleton colophon must not render a nav row:\n%s", out)
	}
	if !strings.Contains(out, `<div class="meta">`) || !strings.Contains(out, "published 2026-07-15") {
		t.Errorf("singleton colophon must still render the meta row:\n%s", out)
	}
}

// TestColophonOmittedWhenDisabled confirms the colophon is opt-in.
func TestColophonOmittedWhenDisabled(t *testing.T) {
	out := string(Markdown([]byte("# X"), Options{}))
	if strings.Contains(out, `class="colo"`) {
		t.Errorf("colophon emitted without Options.Colophon:\n%s", out)
	}
}

// TestColophonSizeIsReal verifies the size sentinel is resolved to the document's
// own byte length (a human size), never leaked, and self-consistent to the KB.
func TestColophonSizeIsReal(t *testing.T) {
	out := string(Markdown([]byte("# X\n\n"+strings.Repeat("word ", 400)), Options{
		Colophon:  true,
		Slug:      "solo",
		Published: time.Now(),
		IndexURL:  "https://demiplane.example/gallery",
	}))
	if strings.Contains(out, sizeSentinel) {
		t.Errorf("size sentinel leaked into output")
	}
	if !strings.Contains(out, " KB") && !strings.Contains(out, " B") {
		t.Errorf("colophon meta row missing a human size:\n%s", out)
	}
	// The reported size equals the finished document length (fixed-point): the
	// document IS what the store records, so this is the artifact's real size.
	if !strings.Contains(out, humanSize(int64(len(out)))) {
		t.Errorf("colophon size does not match the document byte length (%d → %q)",
			len(out), humanSize(int64(len(out))))
	}
}
