// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// TestWriteGuardComparesFullOrigin covers the Origin fallback (no
// Sec-Fetch-Site header): the write is same-origin only when scheme, host and
// port all match, with default ports normalized. Comparing host:port alone
// let an http:// page write to an https:// control plane on the same host and
// port, and vice versa.
func TestWriteGuardComparesFullOrigin(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		tls     bool
		host    string
		origin  string
		blocked bool
	}{
		{"same origin http", "", false, "demi.example:8080", "http://demi.example:8080", false},
		{"same origin, case-folded host", "", false, "Demi.Example:8080", "http://demi.example:8080", false},
		{"same origin https", "", true, "demi.example:8443", "https://demi.example:8443", false},
		{"default port written out", "", false, "demi.example", "http://demi.example:80", false},
		{"default port in Host", "", true, "demi.example:443", "https://demi.example", false},
		{"https page to http listener", "", false, "demi.example:8080", "https://demi.example:8080", true},
		{"http page to https listener", "", true, "demi.example:8443", "http://demi.example:8443", true},
		{"other port", "", false, "demi.example:8080", "http://demi.example:8081", true},
		{"other host", "", false, "demi.example:8080", "http://evil.example:8080", true},
		{"opaque origin", "", false, "demi.example:8080", "null", true},
		{"unparseable origin", "", false, "demi.example:8080", "http://%zz", true},
		// Behind a TLS-terminating proxy the listener sees plain HTTP; the
		// configured base URL names the origin browsers actually use.
		{"proxy, base URL origin", "https://demi.example", false, "demi.example", "https://demi.example", false},
		{"proxy, other scheme than base URL", "https://demi.example", false, "demi.example:8080", "http://demi.example", true},
		{"base URL path ignored", "https://demi.example/prefix/", false, "internal:8080", "https://demi.example", false},
		{"proxy, http page, same host", "https://demi.example", false, "demi.example", "http://demi.example", true},
		{"base URL set, request origin not enough", "https://demi.example", false, "mesh.lan:8080", "http://mesh.lan:8080", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			h := New(st, Config{BaseURL: c.baseURL, ContentPort: "1"}).ControlHandler()
			req := httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader("x"))
			req.Host = c.host
			if c.tls {
				req.TLS = &tls.ConnectionState{}
			}
			req.Header.Set("Origin", c.origin)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			blocked := rec.Code == http.StatusForbidden
			if blocked != c.blocked {
				t.Errorf("Origin %q to %s (tls=%v, base=%q): status %d, blocked=%v, want blocked=%v",
					c.origin, c.host, c.tls, c.baseURL, rec.Code, blocked, c.blocked)
			}
		})
	}
}
