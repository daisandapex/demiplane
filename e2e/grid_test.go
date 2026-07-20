// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPublishListGetDelete_RESTAndMCP is the headline test the mission calls
// out as never exercised anywhere: it joins a real `demiplane serve`
// subprocess to a real `demiplane mcp` subprocess over actual stdio JSON-RPC,
// and proves publish/list/get/delete behave equivalently through both
// surfaces. Table-driven over how the artifact is PUBLISHED (REST raw body,
// MCP inline content, MCP local file path); every row is then read back and
// deleted through BOTH surfaces to prove the seam holds symmetrically.
func TestPublishListGetDelete_RESTAndMCP(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "grid-token"})
	mcp := startMCP(t, srv.ControlURL, srv.ContentURL, srv.TokenFile)

	tmpFile := filepath.Join(t.TempDir(), "from-disk.html")
	if err := os.WriteFile(tmpFile, []byte("<html><body>from disk</body></html>"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}

	cases := []struct {
		name    string
		slug    string
		body    string
		publish func(t *testing.T) string // returns the slug actually published
	}{
		{
			name: "REST raw body",
			slug: "grid-rest-raw",
			body: "<html><body>REST raw body</body></html>",
			publish: func(t *testing.T) string {
				res := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug=grid-rest-raw",
					strings.NewReader("<html><body>REST raw body</body></html>"), srv.authHeader())
				if res.Status != http.StatusCreated {
					t.Fatalf("REST publish: status=%d body=%s", res.Status, res.Body)
				}
				return "grid-rest-raw"
			},
		},
		{
			name: "MCP inline content",
			slug: "grid-mcp-content",
			body: "<html><body>MCP inline content</body></html>",
			publish: func(t *testing.T) string {
				out := mcp.callTool(t, "publish", map[string]any{
					"content": "<html><body>MCP inline content</body></html>",
					"slug":    "grid-mcp-content",
				})
				if !strings.Contains(out, "grid-mcp-content") {
					t.Fatalf("MCP publish result missing slug: %q", out)
				}
				return "grid-mcp-content"
			},
		},
		{
			name: "MCP path (local file)",
			slug: "grid-mcp-path",
			body: "<html><body>from disk</body></html>",
			publish: func(t *testing.T) string {
				out := mcp.callTool(t, "publish", map[string]any{
					"path": tmpFile,
					"slug": "grid-mcp-path",
				})
				if !strings.Contains(out, "grid-mcp-path") {
					t.Fatalf("MCP publish (path) result missing slug: %q", out)
				}
				return "grid-mcp-path"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slug := tc.publish(t)

			// --- list: both surfaces see it ---
			restList := srv.do(t, http.MethodGet, srv.ControlURL+"/list", nil, srv.authHeader())
			if restList.Status != http.StatusOK || !strings.Contains(string(restList.Body), slug) {
				t.Fatalf("REST list missing %q: status=%d body=%s", slug, restList.Status, restList.Body)
			}
			mcpList := mcp.callTool(t, "list", nil)
			if !strings.Contains(mcpList, slug) {
				t.Fatalf("MCP list missing %q: %s", slug, mcpList)
			}

			// --- get: both surfaces return the same bytes ---
			restGet := srv.do(t, http.MethodGet, srv.ContentURL+"/"+slug, nil, nil)
			if restGet.Status != http.StatusOK {
				t.Fatalf("REST get %s: status=%d", slug, restGet.Status)
			}
			if string(restGet.Body) != tc.body {
				t.Fatalf("REST get %s body mismatch: got %q want %q", slug, restGet.Body, tc.body)
			}
			mcpGet := mcp.callTool(t, "get", map[string]any{"slug": slug})
			if mcpGet != tc.body {
				t.Fatalf("MCP get %s body mismatch: got %q want %q", slug, mcpGet, tc.body)
			}

			// --- delete: use MCP for half, REST for the other half, to prove
			// each surface's write is visible to the other's read.
			deleteViaMCP := strings.Contains(tc.name, "MCP")
			if deleteViaMCP {
				out := mcp.callTool(t, "delete", map[string]any{"slug": slug})
				if !strings.Contains(out, slug) {
					t.Fatalf("MCP delete result missing slug: %q", out)
				}
			} else {
				res := srv.do(t, http.MethodDelete, srv.ControlURL+"/"+slug, nil, srv.authHeader())
				if res.Status != http.StatusNoContent {
					t.Fatalf("REST delete %s: status=%d body=%s", slug, res.Status, res.Body)
				}
			}

			// Confirm gone from BOTH surfaces.
			afterREST := srv.do(t, http.MethodGet, srv.ContentURL+"/"+slug, nil, nil)
			if afterREST.Status != http.StatusNotFound {
				t.Fatalf("REST get after delete %s: status=%d (want 404)", slug, afterREST.Status)
			}
			afterMCP := mcp.callToolExpectError(t, "get", map[string]any{"slug": slug})
			if afterMCP == "" {
				t.Fatalf("MCP get after delete %s: expected an error", slug)
			}
		})
	}
}

// TestRenderModes_RESTAndMCP covers raw HTML passthrough vs ?render=md /
// render=md (MCP arg), on both surfaces, asserting the markdown path actually
// transforms the source (not just that it returns 2xx).
func TestRenderModes_RESTAndMCP(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "render-token"})
	mcp := startMCP(t, srv.ControlURL, srv.ContentURL, srv.TokenFile)

	t.Run("REST raw HTML is byte-identical", func(t *testing.T) {
		body := "<html><body><p>untouched</p></body></html>"
		pub := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug=render-raw",
			strings.NewReader(body), srv.authHeader())
		if pub.Status != http.StatusCreated {
			t.Fatalf("publish: status=%d body=%s", pub.Status, pub.Body)
		}
		got := srv.do(t, http.MethodGet, srv.ContentURL+"/render-raw", nil, nil)
		if string(got.Body) != body {
			t.Fatalf("raw HTML mutated: got %q want %q", got.Body, body)
		}
	})

	t.Run("REST ?render=md renders markdown to HTML", func(t *testing.T) {
		md := "# Heading\n\nSome *emphasis* text.\n"
		pub := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug=render-md-rest&render=md",
			strings.NewReader(md), srv.authHeader())
		if pub.Status != http.StatusCreated {
			t.Fatalf("publish: status=%d body=%s", pub.Status, pub.Body)
		}
		got := srv.do(t, http.MethodGet, srv.ContentURL+"/render-md-rest", nil, nil)
		body := string(got.Body)
		if !strings.Contains(body, "<h1") || !strings.Contains(body, "Heading") {
			t.Fatalf("expected rendered <h1>Heading</h1>, got:\n%s", body)
		}
		if !strings.Contains(body, "<em>emphasis</em>") {
			t.Fatalf("expected rendered <em>emphasis</em>, got:\n%s", body)
		}
		if strings.Contains(body, "# Heading") {
			t.Fatalf("markdown source leaked through unrendered:\n%s", body)
		}
	})

	t.Run("MCP render=md renders markdown to HTML", func(t *testing.T) {
		md := "# MCP Heading\n\nMCP *emphasis* text.\n"
		out := mcp.callTool(t, "publish", map[string]any{
			"content": md,
			"slug":    "render-md-mcp",
			"render":  "md",
		})
		if !strings.Contains(out, "render-md-mcp") {
			t.Fatalf("publish result missing slug: %q", out)
		}
		got := srv.do(t, http.MethodGet, srv.ContentURL+"/render-md-mcp", nil, nil)
		body := string(got.Body)
		if !strings.Contains(body, "<h1") || !strings.Contains(body, "MCP Heading") {
			t.Fatalf("expected rendered heading, got:\n%s", body)
		}
	})
}

// TestPrivateSlugs_RESTAndMCP proves a private publish mints an unguessable
// capability URL that is (a) fetchable and (b) absent from both surfaces'
// list output, and that a named+private combination is rejected by both.
func TestPrivateSlugs_RESTAndMCP(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "private-token"})
	mcp := startMCP(t, srv.ControlURL, srv.ContentURL, srv.TokenFile)

	t.Run("REST private artifact is fetchable but unlisted", func(t *testing.T) {
		pub := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?private=true",
			strings.NewReader("secret via REST"), mergeHeaders(srv.authHeader(), map[string]string{
				"Accept": "application/json", // else the server replies with the plain-text URL, not JSON
			}))
		if pub.Status != http.StatusCreated {
			t.Fatalf("publish: status=%d body=%s", pub.Status, pub.Body)
		}
		var decoded struct {
			URL  string `json:"url"`
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal(pub.Body, &decoded); err != nil {
			t.Fatalf("decode publish response: %v (body=%s)", err, pub.Body)
		}
		got := srv.do(t, http.MethodGet, decoded.URL, nil, nil)
		if got.Status != http.StatusOK || string(got.Body) != "secret via REST" {
			t.Fatalf("fetch private artifact by capability URL: status=%d body=%s", got.Status, got.Body)
		}
		list := srv.do(t, http.MethodGet, srv.ControlURL+"/list", nil, srv.authHeader())
		if strings.Contains(string(list.Body), decoded.Slug) {
			t.Fatalf("private slug %q leaked into /list: %s", decoded.Slug, list.Body)
		}
	})

	t.Run("MCP private artifact is fetchable but unlisted", func(t *testing.T) {
		out := mcp.callTool(t, "publish", map[string]any{
			"content": "secret via MCP",
			"private": true,
		})
		mcpList := mcp.callTool(t, "list", nil)
		// out is the URL text; extract the slug (last path segment) to check
		// it never shows up in the list.
		slug := out[strings.LastIndex(out, "/")+1:]
		slug = strings.TrimSpace(slug)
		if strings.Contains(mcpList, slug) {
			t.Fatalf("private slug %q leaked into MCP list: %s", slug, mcpList)
		}
		got := srv.do(t, http.MethodGet, strings.TrimSpace(out), nil, nil)
		if got.Status != http.StatusOK || string(got.Body) != "secret via MCP" {
			t.Fatalf("fetch private artifact by capability URL: status=%d body=%s", got.Status, got.Body)
		}
	})

	t.Run("named + private is rejected on both surfaces", func(t *testing.T) {
		rest := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?private=true&slug=nope",
			strings.NewReader("x"), srv.authHeader())
		if rest.Status != http.StatusBadRequest {
			t.Fatalf("REST named+private: status=%d (want 400)", rest.Status)
		}
		errText := mcp.callToolExpectError(t, "publish", map[string]any{
			"content": "x", "slug": "nope", "private": true,
		})
		if errText == "" {
			t.Fatalf("MCP named+private: expected an error")
		}
	})
}

// TestPasswordGate_REST proves a password-gated artifact 401s without
// credentials and serves with correct HTTP Basic credentials. The password
// travels via header/Basic-auth (never the URL query) on both the publish and
// fetch side — the query-rejection is asserted explicitly.
func TestPasswordGate_REST(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "pw-token"})

	pub := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug=pw-gated",
		strings.NewReader("gated content"), mergeHeaders(srv.authHeader(), map[string]string{
			"X-Demiplane-Password": "hunter2",
		}))
	if pub.Status != http.StatusCreated {
		t.Fatalf("publish: status=%d body=%s", pub.Status, pub.Body)
	}

	// ?password= in the URL is rejected outright at publish time.
	rejectQuery := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug=pw-gated-2&password=leaky",
		strings.NewReader("x"), srv.authHeader())
	if rejectQuery.Status != http.StatusBadRequest {
		t.Fatalf("publish with ?password= in query: status=%d (want 400)", rejectQuery.Status)
	}

	noAuth := srv.do(t, http.MethodGet, srv.ContentURL+"/pw-gated", nil, nil)
	if noAuth.Status != http.StatusUnauthorized {
		t.Fatalf("get without password: status=%d (want 401)", noAuth.Status)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.ContentURL+"/pw-gated", nil)
	req.SetBasicAuth("ignored", "hunter2")
	resp, err := srv.client.Do(req)
	if err != nil {
		t.Fatalf("get with password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get with correct password: status=%d", resp.StatusCode)
	}
}

// TestTTLExpiry_REST proves a short-TTL artifact stops resolving once expired.
// It polls (never blind-sleeps) for the artifact to flip to 404, bounded by a
// generous deadline well past the configured TTL.
func TestTTLExpiry_REST(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "ttl-token"})

	// ttl=3s (not 1s): the "fresh" check right after publish must reliably
	// land before expiry even on a loaded CI runner, where the round trip
	// itself can eat a nontrivial fraction of a too-tight TTL and turn a
	// correct implementation into a flaky test.
	pub := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug=ttl-short&ttl=3s",
		strings.NewReader("ephemeral"), srv.authHeader())
	if pub.Status != http.StatusCreated {
		t.Fatalf("publish: status=%d body=%s", pub.Status, pub.Body)
	}

	fresh := srv.do(t, http.MethodGet, srv.ContentURL+"/ttl-short", nil, nil)
	if fresh.Status != http.StatusOK {
		t.Fatalf("get before expiry: status=%d", fresh.Status)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		res := srv.do(t, http.MethodGet, srv.ContentURL+"/ttl-short", nil, nil)
		if res.Status == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("artifact with ttl=3s still resolves (status=%d) after 15s", res.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func mergeHeaders(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
