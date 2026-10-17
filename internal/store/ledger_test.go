// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestReleaseOnlyCounted: a serving store releases only what it counted.
// Generations, rows and pending deletion records another process (receive)
// created after the serving store's last recount were never in its usage, so
// removing them must not take its usage below its real content.
func TestReleaseOnlyCounted(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if err := a.SetQuota(1 << 20); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Put(PutOptions{Slug: "x"}, bytes.NewReader(payload(3000))); err != nil {
		t.Fatal(err)
	}
	real := int64(metaAllowance + 3000)
	if used, _ := a.Usage(); used != real {
		t.Fatalf("usage = %d, want %d", used, real)
	}

	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	b.DeferDeletions()
	for i := 0; i < 5; i++ {
		if _, err := b.Put(PutOptions{Slug: "y"}, bytes.NewReader(payload(1000))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Put(PutOptions{Slug: "z"}, strings.NewReader("zz")); err != nil {
		t.Fatal(err)
	}

	// No recount in a: none of b's writes are in its usage.
	if _, err := a.SweepExpired(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete("z"); err != nil {
		t.Fatal(err)
	}
	if used, _ := a.Usage(); used != real {
		t.Fatalf("usage after removing another process's uncounted files = %d, want %d (its own content)", used, real)
	}
	if err := a.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	// x, and y's row and current generation.
	if used, _ := a.Usage(); used != real+metaAllowance+1000 {
		t.Fatalf("usage after a recount = %d, want %d", used, real+metaAllowance+1000)
	}
	// Now counted, y's removal releases it.
	if err := a.Delete("y"); err != nil {
		t.Fatal(err)
	}
	if used, _ := a.Usage(); used != real {
		t.Fatalf("usage after deleting a counted artifact = %d, want %d", used, real)
	}
	assertAccounted(t, a)
}
