// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"regexp"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/v3/internal/theme"
)

// TestPrintSheetHasNoPureWhite: the print sheet honours the no-pure-white
// refusal like every screen surface.
func TestPrintSheetHasNoPureWhite(t *testing.T) {
	if regexp.MustCompile(`(?i)#fff\b|#ffffff\b`).MatchString(printCSS) {
		t.Errorf("print sheet uses pure white:\n%s", printCSS)
	}
}

// TestLayoutFallbacksMatchTokens: a var(--token, fallback) in the layout
// sheets falls back to the default light value of that token, so a --css page
// without tokens looks like the house page, not a drifted copy of it.
func TestLayoutFallbacksMatchTokens(t *testing.T) {
	tok := map[string]string{}
	for _, m := range regexp.MustCompile(`(--[a-z-]+):([^;]+);`).FindAllStringSubmatch(theme.Tokens("light"), -1) {
		tok[m[1]] = strings.TrimSpace(m[2])
	}
	fb := regexp.MustCompile(`var\((--[a-z-]+),([^)]*\))\)`)
	for name, sheet := range map[string]string{"fCSS": fCSS, "fMetaCSS": fMetaCSS, "docChromeCSS": docChromeCSS} {
		for _, m := range fb.FindAllStringSubmatch(sheet, -1) {
			want, ok := tok[m[1]]
			if ok && m[2] != want {
				t.Errorf("%s: var(%s,%s) falls back to a drifted value; the token is %s", name, m[1], m[2], want)
			}
		}
	}
}
