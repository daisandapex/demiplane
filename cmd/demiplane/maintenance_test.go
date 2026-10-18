// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/store"
)

// TestDeleteSiteCommand: delete-site removes a site, its interrupted
// replace's copy included, and refuses while a server holds the store.
func TestDeleteSiteCommand(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sw, err := st.NewSiteWriter("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.AddFile("index.html", strings.NewReader("<p>site")); err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Commit(); err != nil {
		t.Fatal(err)
	}
	st.Close()

	unlock, err := store.LockServe(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := runDeleteSite([]string{"--store", dir, "demo"}); err == nil || !strings.Contains(err.Error(), "stop the server") {
		t.Fatalf("delete-site with a server running: %v", err)
	}
	unlock()
	if err := runDeleteSite([]string{"--store", dir, "demo"}); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.SiteExists("demo") {
		t.Fatal("site still exists after delete-site")
	}
}
