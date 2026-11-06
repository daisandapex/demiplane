// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"strings"
	"testing"
)

// TestNoWordmarkKicker: a published page opens with the document, not the
// tool's name. Neither the F top bar nor the plain --css masthead carries the
// "demiplane" kicker.
func TestNoWordmarkKicker(t *testing.T) {
	for name, opts := range map[string]Options{
		"F layout":   {Header: true},
		"css header": {Header: true, CSS: "body{color:red}"},
	} {
		out := string(Markdown([]byte(fDoc), opts))
		if strings.Contains(out, `class="kicker"`) || strings.Contains(out, `>demiplane</span>`) {
			t.Errorf("%s: page still carries the wordmark kicker:\n%s", name, out)
		}
		if strings.Contains(out, `.docbar .kicker`) {
			t.Errorf("%s: the stylesheet still styles a kicker", name)
		}
	}
}
