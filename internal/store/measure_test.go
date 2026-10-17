// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

// failingRows yields its first n rows and then fails, the way *sql.Rows
// does: Next returns false and only Err reports the failure (Close does not).
type failingRows struct {
	usageRows
	n int
}

var errIterate = errors.New("injected iteration failure")

func (f *failingRows) Next() bool {
	if f.n <= 0 {
		return false
	}
	f.n--
	return f.usageRows.Next()
}

func (f *failingRows) Err() error { return errIterate }

// TestMeasureRejectsIterationError: a usage measurement whose database scan
// fails part way is rejected whole, and the accounting already in place is
// kept, rather than a partial count admitting writes past the quota.
func TestMeasureRejectsIterationError(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 3; i++ {
		if _, err := s.Put(PutOptions{Slug: fmt.Sprintf("a%d", i)}, bytes.NewReader(payload(10))); err != nil {
			t.Fatal(err)
		}
	}
	wrapUsageRows = func(r usageRows) usageRows { return &failingRows{usageRows: r, n: 1} }
	t.Cleanup(func() { wrapUsageRows = nil })
	if err := s.SetQuota(1 << 20); !errors.Is(err, errIterate) {
		t.Fatalf("SetQuota with a failing scan: %v, want the iteration error", err)
	}
	if used, limit := s.Usage(); used != 0 || limit != 0 {
		t.Fatalf("a failed SetQuota installed %d/%d", used, limit)
	}
	wrapUsageRows = nil
	if err := s.SetQuota(1 << 20); err != nil {
		t.Fatal(err)
	}
	want := int64(3*metaAllowance + 30)
	if used, _ := s.Usage(); used != want {
		t.Fatalf("usage = %d, want %d", used, want)
	}
	wrapUsageRows = func(r usageRows) usageRows { return &failingRows{usageRows: r, n: 1} }
	if err := s.RecountUsage(); !errors.Is(err, errIterate) {
		t.Fatalf("RecountUsage with a failing scan: %v, want the iteration error", err)
	}
	if used, _ := s.Usage(); used != want {
		t.Fatalf("usage after a failed recount = %d, want %d (kept)", used, want)
	}
	s.quota.mu.Lock()
	rows := len(s.quota.counted.rows)
	s.quota.mu.Unlock()
	if rows != 3 {
		t.Fatalf("ledger rows after a failed recount = %d, want 3 (kept)", rows)
	}
}
