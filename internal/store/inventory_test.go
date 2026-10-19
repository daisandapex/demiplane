// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOwnerInventory(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	put := func(o PutOptions) Artifact {
		t.Helper()
		a, err := s.Put(o, strings.NewReader("bytes"))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	pub := put(PutOptions{Slug: "public"})
	priv := put(PutOptions{Private: true})
	put(PutOptions{Slug: "expired", TTL: time.Nanosecond})
	sw, err := s.NewSiteWriter("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err = sw.AddFile("index.html", strings.NewReader("site")); err != nil {
		t.Fatal(err)
	}
	if _, err = sw.Commit(); err != nil {
		t.Fatal(err)
	}
	staged, err := s.NewSiteWriter("staged")
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Abort()
	for _, name := range []string{".tmp-backup", ".tmp-del-dead"} {
		if err = os.Mkdir(filepath.Join(s.siteRoot(), name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(s.siteRoot(), "plain-file"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(s.siteRoot(), "demo"), filepath.Join(s.siteRoot(), "symlink")); err != nil {
		t.Fatal(err)
	}
	names, err := s.ListSites()
	if err != nil || strings.Join(names, ",") != "demo" {
		t.Fatalf("%v %v", names, err)
	}
	arts, err := s.OwnerArtifacts(context.Background(), DefaultOwner, "", 10)
	if err != nil || len(arts) != 2 {
		t.Fatalf("%v %v", arts, err)
	}
	if arts[0].Slug > arts[1].Slug {
		t.Fatal("wrong ordering")
	}
	seen := map[string]bool{}
	for _, a := range arts {
		seen[a.Slug] = true
	}
	if !seen[pub.Slug] || !seen[priv.Slug] {
		t.Fatal("missing artifact")
	}
	next, err := s.OwnerArtifacts(context.Background(), DefaultOwner, arts[0].Slug, 1)
	if err != nil || len(next) != 1 || next[0].Slug != arts[1].Slug {
		t.Fatalf("%v %v", next, err)
	}
	count, err := s.Count(DefaultOwner)
	if err != nil || count+len(names) != 3 {
		t.Fatalf("count %d %v", count, err)
	}
	staged.Abort()
	s.Close()
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	names, err = s.ListSites()
	if err != nil || len(names) != 1 {
		t.Fatalf("reopen: %v %v", names, err)
	}
	arts, err = s.OwnerArtifacts(context.Background(), DefaultOwner, "", 10)
	if err != nil || len(arts) != 2 {
		t.Fatalf("reopen: %v %v", arts, err)
	}
}
