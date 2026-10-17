// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http"
	"testing"
)

// TestSitePublishRejectsBackslashPaths: an archive entry whose name contains a
// backslash is refused rather than reinterpreted as a directory separator.
func TestSitePublishRejectsBackslashPaths(t *testing.T) {
	ts := newTestServer(t, "")
	for _, format := range []string{"tar", "zip"} {
		files := []srcFile{{"index.html", "<p>hi</p>"}, {`css\style.css`, "body{}"}}
		body, ct := makeTar(t, files), "application/x-tar"
		if format == "zip" {
			body, ct = makeZip(t, files), "application/zip"
		}
		if resp, rb := postSite(t, ts.URL, "bs-"+format, "fmt="+format, ct, body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s entry with a backslash: status %d (%s), want 400", format, resp.StatusCode, rb)
		}
	}
}
