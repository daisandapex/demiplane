// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"archive/tar"
	"bytes"
	"net/http"
	"testing"
)

// TestMultiFileSitePublish_REST publishes a tar archive via POST
// /publish?site=<name> and verifies both the index (GET /{site}/) and a
// nested asset (GET /{site}/{path}) serve the right bytes — the multi-file
// publish surface (site.go), never exercised by a real server before.
func TestMultiFileSitePublish_REST(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "site-token"})

	files := map[string]string{
		"index.html":     "<html><body>site index</body></html>",
		"about.html":     "<html><body>about page</body></html>",
		"assets/app.css": "body { color: black; }",
	}
	archive := buildTar(t, files)

	pub := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?site=demo-site",
		bytes.NewReader(archive), mergeHeaders(srv.authHeader(), map[string]string{
			"Content-Type": "application/x-tar",
		}))
	if pub.Status != http.StatusCreated {
		t.Fatalf("site publish: status=%d body=%s", pub.Status, pub.Body)
	}

	for path, want := range map[string]string{
		"/demo-site/":               files["index.html"],
		"/demo-site/about.html":     files["about.html"],
		"/demo-site/assets/app.css": files["assets/app.css"],
	} {
		got := srv.do(t, http.MethodGet, srv.ContentURL+path, nil, nil)
		if got.Status != http.StatusOK {
			t.Fatalf("GET %s: status=%d", path, got.Status)
		}
		if string(got.Body) != want {
			t.Fatalf("GET %s: body=%q want=%q", path, got.Body, want)
		}
	}

	// A path outside the published set is a clean 404, not a directory
	// listing or a fallthrough to the flat-artifact catch-all.
	missing := srv.do(t, http.MethodGet, srv.ContentURL+"/demo-site/does-not-exist.html", nil, nil)
	if missing.Status != http.StatusNotFound {
		t.Fatalf("GET missing site asset: status=%d (want 404)", missing.Status)
	}
}

// buildTar builds an in-memory tar archive from a path->content map.
func buildTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}
