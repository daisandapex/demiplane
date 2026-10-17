// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/store"
)

// TestLongHostIsRefusedBeforeRouting: a Host longer than maxHostLen gets 400
// on every listener's handler before any route runs; one at the limit is
// routed.
func TestLongHostIsRefusedBeforeRouting(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, Config{Browse: true, ContentPort: "8081"})
	for name, h := range map[string]http.Handler{
		"combined": s.Handler(),
		"control":  s.ControlHandler(),
		"content":  s.ContentHandler(),
	} {
		for _, tc := range []struct {
			host string
			want bool // refused
		}{
			{strings.Repeat("a", 300), true},
			{strings.Repeat("a", 250) + ":8080", false},
		} {
			r := httptest.NewRequest(http.MethodGet, "/gallery", nil)
			r.Host = tc.host
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if refused := w.Code == http.StatusBadRequest; refused != tc.want {
				t.Errorf("%s: Host of %d bytes = %d, refused=%v want %v", name, len(tc.host), w.Code, refused, tc.want)
			}
		}
	}
	if n := len(sanitizeHost(strings.Repeat("a", 1000))); n > maxHostLen {
		t.Errorf("sanitizeHost produced %d bytes, cap %d", n, maxHostLen)
	}
}
