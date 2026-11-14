// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// TestSitePublishErrorDoesNotNameStorePaths: a site entry whose staged path
// would pass PATH_MAX used to reach the filesystem, and the *PathError it got
// was answered as "invalid archive: " and its text, the absolute store and
// staging paths included. The entry is now refused before the filesystem is
// touched (audit finding 04), and a filesystem error is answered 500 without
// its text (TestSiteExtractErrorHidesFilesystemPaths); either way the body
// must not name the store. See audit/12-site-publish-error-echoes-store-path.md.
func TestSitePublishErrorDoesNotNameStorePaths(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)

	// 32 segments of 128 bytes: a valid entry name whose staged path is
	// longer than PATH_MAX.
	name := strings.TrimSuffix(strings.Repeat(strings.Repeat("a", 128)+"/", 32), "/")
	body := tarOf(t, map[string]string{name: "x"}, false)
	resp, err := http.Post(ts.URL+"/publish?site=s", "application/x-tar", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(msg), dir) {
		t.Errorf("publish = %d, and its body names the store directory: %.160s …", resp.StatusCode, msg)
	}
}
