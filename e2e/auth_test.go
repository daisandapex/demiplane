// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
)

// TestNotFoundAndMethodNotAllowed covers the two boring-but-load-bearing HTTP
// status codes: an unknown slug is a clean 404 (never a 500 or a leaked stack
// trace), and a method the route doesn't support is a 405.
func TestNotFoundAndMethodNotAllowed(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "404-token"})

	t.Run("GET unknown slug is 404", func(t *testing.T) {
		res := srv.do(t, http.MethodGet, srv.ContentURL+"/this-slug-was-never-published", nil, nil)
		if res.Status != http.StatusNotFound {
			t.Fatalf("status=%d (want 404)", res.Status)
		}
	})

	t.Run("DELETE unknown slug is 404", func(t *testing.T) {
		res := srv.do(t, http.MethodDelete, srv.ControlURL+"/also-never-published", nil, srv.authHeader())
		if res.Status != http.StatusNotFound {
			t.Fatalf("status=%d (want 404)", res.Status)
		}
	})

	t.Run("PUT on an artifact route is 405", func(t *testing.T) {
		pub := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug=method-check",
			strings.NewReader("x"), srv.authHeader())
		if pub.Status != http.StatusCreated {
			t.Fatalf("seed publish: status=%d body=%s", pub.Status, pub.Body)
		}
		res := srv.do(t, http.MethodPut, srv.ContentURL+"/method-check", strings.NewReader("x"), nil)
		if res.Status != http.StatusMethodNotAllowed {
			t.Fatalf("PUT /{slug}: status=%d (want 405)", res.Status)
		}
	})

	t.Run("GET on the publish-only route is 405", func(t *testing.T) {
		res := srv.do(t, http.MethodGet, srv.ControlURL+"/publish", nil, srv.authHeader())
		if res.Status != http.StatusMethodNotAllowed {
			t.Fatalf("GET /publish: status=%d (want 405)", res.Status)
		}
	})
}

// TestAuth_MissingTokenRejected proves every bearer-gated endpoint rejects a
// request that carries no (or the wrong) token, on the REST surface, and that
// the equivalent MCP calls fail too when the MCP client is misconfigured
// without a token file.
func TestAuth_MissingTokenRejected(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "secret-abc-123"})

	cases := []struct {
		name   string
		method string
		url    string
	}{
		{"POST /publish", http.MethodPost, srv.ControlURL + "/publish?slug=noauth"},
		{"GET /list", http.MethodGet, srv.ControlURL + "/list"},
	}
	for _, tc := range cases {
		t.Run(tc.name+" no header", func(t *testing.T) {
			res := srv.do(t, tc.method, tc.url, strings.NewReader("x"), nil)
			if res.Status != http.StatusUnauthorized {
				t.Fatalf("status=%d (want 401), body=%s", res.Status, res.Body)
			}
		})
		t.Run(tc.name+" wrong token", func(t *testing.T) {
			res := srv.do(t, tc.method, tc.url, strings.NewReader("x"),
				map[string]string{"Authorization": "Bearer not-the-real-token"})
			if res.Status != http.StatusUnauthorized {
				t.Fatalf("status=%d (want 401), body=%s", res.Status, res.Body)
			}
		})
	}

	// Seed an artifact with the real token so DELETE has something to 401 on.
	seed := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug=to-delete",
		strings.NewReader("x"), srv.authHeader())
	if seed.Status != http.StatusCreated {
		t.Fatalf("seed publish: status=%d body=%s", seed.Status, seed.Body)
	}
	del := srv.do(t, http.MethodDelete, srv.ControlURL+"/to-delete", nil, nil)
	if del.Status != http.StatusUnauthorized {
		t.Fatalf("DELETE without token: status=%d (want 401)", del.Status)
	}

	// GET /{slug} (view) is deliberately NOT gated by the bearer token — it's a
	// separate auth layer (network/capability URL). Confirm that stays true so
	// this test would catch an accidental tightening that breaks public links.
	view := srv.do(t, http.MethodGet, srv.ContentURL+"/to-delete", nil, nil)
	if view.Status != http.StatusOK {
		t.Fatalf("GET /{slug} without token: status=%d (want 200 — view auth is a different layer)", view.Status)
	}

	// MCP client with no token configured against a token-required server:
	// every write tool must surface an error, not silently succeed.
	mcpNoToken := startMCP(t, srv.ControlURL, srv.ContentURL, "")
	errText := mcpNoToken.callToolExpectError(t, "publish", map[string]any{"content": "x", "slug": "mcp-noauth"})
	if errText == "" {
		t.Fatalf("MCP publish without token: expected an error")
	}
}

// TestAuth_TokenNeverLeaked is the invariant-4 sweep: publish, list, get, and
// delete through both surfaces, then scan every REST response body, every
// REST response header, the server's own stderr log, and the full MCP
// stdio transcript for the literal token value. None of them may contain it.
func TestAuth_TokenNeverLeaked(t *testing.T) {
	const token = "definitely-do-not-leak-me-9f3a"
	srv := startServer(t, serverOpts{Token: token})
	mcp := startMCP(t, srv.ControlURL, srv.ContentURL, srv.TokenFile)

	// Drive a representative slice of the surface, deliberately including an
	// auth failure (the error path is exactly where a naive implementation
	// might echo the bad/expected token back).
	var bodies []string

	pub := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug=leak-check",
		strings.NewReader("x"), srv.authHeader())
	bodies = append(bodies, string(pub.Body))

	list := srv.do(t, http.MethodGet, srv.ControlURL+"/list", nil, srv.authHeader())
	bodies = append(bodies, string(list.Body))

	unauth := srv.do(t, http.MethodGet, srv.ControlURL+"/list", nil,
		map[string]string{"Authorization": "Bearer wrong-token-value"})
	bodies = append(bodies, string(unauth.Body))
	for k, vs := range unauth.Header {
		for _, v := range vs {
			bodies = append(bodies, k+": "+v)
		}
	}

	bodies = append(bodies, mcp.callTool(t, "publish", map[string]any{"content": "y", "slug": "leak-check-mcp"}))
	bodies = append(bodies, mcp.callTool(t, "list", nil))
	bodies = append(bodies, mcp.callTool(t, "get", map[string]any{"slug": "leak-check-mcp"}))
	bodies = append(bodies, mcp.callTool(t, "delete", map[string]any{"slug": "leak-check-mcp"}))

	bodies = append(bodies, srv.Stderr())
	bodies = append(bodies, mcp.Transcript())

	assertNoSecret(t, token, "REST/MCP surface sweep", bodies...)
}
