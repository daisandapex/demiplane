// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestDeferredGenerationsCharged: generations a deferring store (receive)
// leaves for the server each count an allowance until they are removed, a
// recount measures them, and past the cap on outstanding pending deletions a
// replace is refused.
func TestDeferredGenerationsCharged(t *testing.T) {
	prev := maxPendingDeletions
	maxPendingDeletions = 5
	t.Cleanup(func() { maxPendingDeletions = prev })
	dir := t.TempDir()
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	b.DeferDeletions()
	if err := b.SetQuota(1 << 20); err != nil {
		t.Fatal(err)
	}
	put := func() error {
		_, err := b.Put(PutOptions{Slug: "a"}, strings.NewReader(""))
		return err
	}
	for i := 0; i < 6; i++ {
		if err := put(); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	// One row and five retained generations.
	want := int64(6 * metaAllowance)
	if used, _ := b.Usage(); used != want {
		t.Fatalf("usage after five deferred replacements = %d, want %d", used, want)
	}
	err = put()
	if !errors.Is(err, ErrPendingDeletions) || !errors.Is(err, ErrQuota) {
		t.Fatalf("replace past the pending-deletion cap: %v, want ErrPendingDeletions (an ErrQuota)", err)
	}
	if used, _ := b.Usage(); used != want {
		t.Fatalf("usage after a refused replace = %d, want %d", used, want)
	}
	if err := b.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	if used, _ := b.Usage(); used != want {
		t.Fatalf("usage after a recount = %d, want %d", used, want)
	}

	// The serving store sweeps them away and its recount drops the charge.
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if err := a.SetQuota(1 << 20); err != nil {
		t.Fatal(err)
	}
	if used, _ := a.Usage(); used != want {
		t.Fatalf("serving store's usage = %d, want %d", used, want)
	}
	if _, err := a.SweepExpired(time.Now()); err != nil {
		t.Fatal(err)
	}
	if used, _ := a.Usage(); used != metaAllowance {
		t.Fatalf("serving store's usage after its sweep = %d, want %d", used, metaAllowance)
	}
	if err := put(); err != nil {
		t.Fatalf("replace after the sweep: %v", err)
	}
	if err := a.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	assertAccounted(t, a)
}

// TestImmediateDeletionsChargeNothing: a store that removes replaced
// generations itself ends each replace with no pending charge.
func TestImmediateDeletionsChargeNothing(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	for i := 0; i < 20; i++ {
		if _, err := s.Put(PutOptions{Slug: "a"}, strings.NewReader("")); err != nil {
			t.Fatal(err)
		}
	}
	if used, _ := s.Usage(); used != metaAllowance {
		t.Fatalf("usage after 20 replacements = %d, want %d", used, metaAllowance)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != 0 {
		t.Fatalf("usage after a delete = %d, want 0", used)
	}
	assertAccounted(t, s)
}
