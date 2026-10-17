// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"sync"
	"testing"
)

// TestRetireRacesLastClose: the last reader closing a file at the moment it
// is retired leaves nothing charged, whichever comes first.
func TestRetireRacesLastClose(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	if _, err := s.Put(PutOptions{Slug: "a"}, bytes.NewReader(payload(1000))); err != nil {
		t.Fatal(err)
	}
	_, f, err := s.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	afterRetire = func() { once.Do(func() { f.Close() }) }
	t.Cleanup(func() { afterRetire = nil })
	if _, err := s.Put(PutOptions{Slug: "a"}, bytes.NewReader(payload(1000))); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != metaAllowance+1000 {
		t.Fatalf("usage after the reader closed mid-retirement = %d, want %d", used, metaAllowance+1000)
	}
	assertAccounted(t, s)
}
