// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build audit && linux

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

// TestAuditSitePublishErrorDoesNotNameStorePaths: writeSiteExtractError's
// default branch answers 400 with "invalid archive: " and the error text. A
// filesystem error from the store (a path too long for the filesystem, a full
// disk) is a *PathError, so the body carries the absolute path of the store
// and of the staging directory, and a server-side fault is reported as the
// client's. Everywhere else such errors go through logsafe and the client
// gets "internal error". audit/12-site-publish-error-echoes-store-path.md.
func TestAuditSitePublishErrorDoesNotNameStorePaths(t *testing.T) {
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
