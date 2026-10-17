// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"testing"
	"time"
)

// TestOtherProcessLeavesOpenGenerationLinked: a replace through a second
// store on the same directory (as `demiplane receive` is) leaves the old
// generation for the serving store, which keeps it counted while its reader
// holds it and removes it on a sweep once the reader is done.
func TestOtherProcessLeavesOpenGenerationLinked(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if err := a.SetQuota(1 << 20); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	b.DeferDeletions()
	if _, err := a.Put(PutOptions{Slug: "a"}, bytes.NewReader(payload(1000))); err != nil {
		t.Fatal(err)
	}
	_, f, err := a.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := b.Put(PutOptions{Slug: "a"}, bytes.NewReader(payload(2000))); err != nil {
		t.Fatal(err)
	}
	if err := a.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	both := int64(metaAllowance + 1000 + 2000)
	if used, _ := a.Usage(); used != both {
		t.Fatalf("usage in the serving store after a replace elsewhere = %d, want %d (the open old generation)", used, both)
	}
	// A sweep while the reader holds it: removed from the tree, still charged.
	if _, err := a.SweepExpired(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	if used, _ := a.Usage(); used != both {
		t.Fatalf("usage after a sweep with the reader open = %d, want %d", used, both)
	}
	f.Close()
	if used, _ := a.Usage(); used != metaAllowance+2000 {
		t.Fatalf("usage after the reader closed = %d, want %d", used, metaAllowance+2000)
	}
	assertAccounted(t, a)
}
