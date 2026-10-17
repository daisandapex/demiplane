// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"strings"
	"testing"
)

// TestRecountDuringSiteReplace: a recount while Commit removes the replaced
// tree neither drops it nor lets the removal subtract it twice.
func TestRecountDuringSiteReplace(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	if err := publishSiteBody(s, "site", "x.txt", strings.Repeat("1", 1000)); err != nil {
		t.Fatal(err)
	}
	beforeBackupRemove = func() {
		if err := s.RecountUsage(); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeBackupRemove = nil })
	if err := publishSiteBody(s, "site", "x.txt", strings.Repeat("2", 1000)); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != 2*metaAllowance+1000 {
		t.Fatalf("usage after a recount during a replace = %d, want %d", used, 2*metaAllowance+1000)
	}
	assertAccounted(t, s)
}

func publishSiteBody(s *Store, site, name, body string) error {
	sw, err := s.NewSiteWriter(site)
	if err != nil {
		return err
	}
	defer sw.Abort()
	if err := sw.AddFile(name, strings.NewReader(body)); err != nil {
		return err
	}
	_, err = sw.Commit()
	return err
}
