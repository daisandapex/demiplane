// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestRetiredFilesStayChargedWhileOpen: a replaced or deleted file that a
// reader still holds open keeps its bytes on disk, so it stays charged until
// the last reader closes it, across a recount too.
func TestRetiredFilesStayChargedWhileOpen(t *testing.T) {
	const limit = 10_000 + metaAllowance
	s := newQuotaStore(t, limit)
	if _, err := s.Put(PutOptions{Slug: "a"}, bytes.NewReader(payload(1000))); err != nil {
		t.Fatal(err)
	}
	var open []io.Closer
	for i := 0; i < 5; i++ {
		_, f, err := s.Get("a")
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, f)
		if _, err := s.Put(PutOptions{Slug: "a"}, bytes.NewReader(payload(1000))); err != nil {
			t.Fatalf("replace %d: %v", i, err)
		}
	}
	held := int64(metaAllowance + 6000) // the live copy and five open ones
	if used, _ := s.Usage(); used != held {
		t.Fatalf("usage with five replaced generations open = %d, want %d", used, held)
	}
	if _, err := s.Put(PutOptions{Slug: "b"}, bytes.NewReader(payload(4500))); !errors.Is(err, ErrQuota) {
		t.Fatalf("Put beside open retired files: %v, want ErrQuota", err)
	}
	if err := s.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != held {
		t.Fatalf("usage after a recount = %d, want %d", used, held)
	}
	for _, f := range open {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if used, _ := s.Usage(); used != metaAllowance+1000 {
		t.Fatalf("usage after the readers closed = %d, want %d", used, metaAllowance+1000)
	}
	if _, err := s.Put(PutOptions{Slug: "b"}, bytes.NewReader(payload(4500))); err != nil {
		t.Fatalf("Put after the readers closed: %v", err)
	}
	assertAccounted(t, s)
}

// TestRetiredSiteFilesStayChargedWhileOpen: the same for a site file whose
// tree is replaced or deleted while it is being served. The entry allowance
// goes with the directory entry; the bytes stay until the reader closes.
func TestRetiredSiteFilesStayChargedWhileOpen(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	put := func(body string) {
		t.Helper()
		sw, err := s.NewSiteWriter("site")
		if err != nil {
			t.Fatal(err)
		}
		if err := sw.AddFile("x.txt", strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		if _, err := sw.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	put(strings.Repeat("1", 2000))
	f1, _, _, err := s.OpenSiteFile("site", "x.txt")
	if err != nil {
		t.Fatal(err)
	}
	put(strings.Repeat("2", 3000))
	if used, _ := s.Usage(); used != 2*metaAllowance+3000+2000 {
		t.Fatalf("usage with the replaced file open = %d, want %d", used, 2*metaAllowance+3000+2000)
	}
	f2, _, _, err := s.OpenSiteFile("site", "x.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSite("site"); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != 5000 {
		t.Fatalf("usage with both deleted files open = %d, want 5000", used)
	}
	f1.Close()
	f2.Close()
	if used, _ := s.Usage(); used != 0 {
		t.Fatalf("usage after the readers closed = %d, want 0", used)
	}
	assertAccounted(t, s)
}
