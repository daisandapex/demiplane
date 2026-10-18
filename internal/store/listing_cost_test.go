// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// bigStore holds 20,000 private artifacts (the newest), 20,000 public members
// of one series, and five older public singletons. analyze gives the planner
// statistics, which a deployed database usually lacks.
func bigStore(t *testing.T, analyze bool) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, q := range []string{
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 20000)
		 INSERT INTO artifacts (slug, filename, content_type, size, created_at, owner, private, writer_tag)
		 SELECT printf('priv-%05d', i), '', 'text/plain', 1, 1700100000 + i, 'local', 1, 't' FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 20000)
		 INSERT INTO artifacts (slug, filename, content_type, size, created_at, owner, series, writer_tag)
		 SELECT printf('big-%05d', i), '', 'text/plain', 1, 1700050000 + i, 'local', 'big', 't' FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 5)
		 INSERT INTO artifacts (slug, filename, content_type, size, created_at, owner, writer_tag)
		 SELECT printf('solo-%d', i), '', 'text/plain', 1, 1700000000 + i, 'local', 't' FROM n`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if analyze {
		if _, err := st.db.Exec(`ANALYZE`); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// plan returns SQLite's query plan for q, one detail per line.
func plan(t *testing.T, st *Store, q string, args ...any) string {
	t.Helper()
	rows, err := st.db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	return strings.Join(out, "\n")
}

// TestBrowseFamiliesReadBoundedRows: the browse index's family scan reads a
// bounded number of rows, however many members one series has, and series
// sizes are counted only up to a cap.
func TestBrowseFamiliesReadBoundedRows(t *testing.T) {
	st := bigStore(t, false)
	newest, members, truncated := familiesForTest(t, st, 9)
	if !truncated {
		t.Error("the scan budget ran out among one series' members, but truncated is false")
	}
	if n := st.rowsExamined.Load(); n > FamilyScanBudget+int64(seriesCountExamine) {
		t.Errorf("NewestPerFamily read %d rows for 9 families", n)
	}
	if len(newest) == 0 || newest[0].Slug != "big-20000" {
		t.Errorf("first family = %v, want big-20000", newest)
	}
	if n := members["big"]; n.N != SeriesCountCap || !n.More {
		t.Errorf("series size = %+v, want %d and more", n, SeriesCountCap)
	}
}

// TestListingQueriesAreIndexWalks: every listing query seeks on the owner and
// public flag and reads in index order: no sort of the whole table, no walk
// over private rows.
func TestListingQueriesAreIndexWalks(t *testing.T) {
	for _, analyze := range []bool{false, true} {
		st := bigStore(t, analyze)
		for name, q := range listingQueriesForTest() {
			p := plan(t, st, q.sql, q.args...)
			t.Logf("%s (analyze=%v):\n%s", name, analyze, p)
			if strings.Contains(p, "TEMP B-TREE") {
				t.Errorf("%s (analyze=%v) sorts in a temporary b-tree:\n%s", name, analyze, p)
			}
			for _, want := range q.wantConstraints {
				if !strings.Contains(p, want) {
					t.Errorf("%s (analyze=%v) plan lacks %q:\n%s", name, analyze, want, p)
				}
			}
		}
	}
}

type listingQuery struct {
	sql             string
	args            []any
	wantConstraints []string
}

// expiredStore holds 20,000 public artifacts past their TTL but not yet
// swept (the newest; every other one in series "big"), then 300 live ones
// (every third in "big").
func expiredStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, q := range []string{
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 20000)
		 INSERT INTO artifacts (slug, filename, content_type, size, created_at, owner, series, expires_at, writer_tag)
		 SELECT printf('exp-%05d', i), '', 'text/plain', 1, 1700100000 + i, 'local',
		        CASE WHEN i % 2 = 0 THEN 'big' ELSE '' END, 1, 't' FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 300)
		 INSERT INTO artifacts (slug, filename, content_type, size, created_at, owner, series, writer_tag)
		 SELECT printf('live-%03d', i), '', 'text/plain', 1, 1700000000 + i, 'local',
		        CASE WHEN i % 3 = 0 THEN 'big' ELSE '' END, 't' FROM n`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// TestListingsStepOverABoundedExpiredBatch: an unswept expired batch costs
// each listing read at most its budget of index entries, the reads report
// that they stopped short, and paging still lists every live artifact once.
func TestListingsStepOverABoundedExpiredBatch(t *testing.T) {
	st := expiredStore(t)
	ctx := context.Background()

	seen := map[string]int{}
	var after *ListCursor
	for calls := 0; ; calls++ {
		if calls > 200 {
			t.Fatal("paging never ends")
		}
		st.rowsExamined.Store(0)
		page, next, err := st.ListPage(ctx, DefaultOwner, after, 200)
		if err != nil {
			t.Fatal(err)
		}
		if n := st.rowsExamined.Load(); n > 200+1+ExamineSlack {
			t.Fatalf("one ListPage examined %d entries", n)
		}
		for _, a := range page {
			if strings.HasPrefix(a.Slug, "exp-") {
				t.Fatalf("expired %s listed", a.Slug)
			}
			seen[a.Slug]++
		}
		if next == nil {
			break
		}
		after = next
	}
	if len(seen) != 300 {
		t.Errorf("paging listed %d live artifacts, want 300", len(seen))
	}
	for slug, n := range seen {
		if n != 1 {
			t.Errorf("%s listed %d times", slug, n)
		}
	}

	st.rowsExamined.Store(0)
	newest, members, truncated, err := st.NewestPerFamily(ctx, DefaultOwner, 9)
	if err != nil {
		t.Fatal(err)
	}
	if n := st.rowsExamined.Load(); n > FamilyScanBudget+int64(len(members))*seriesCountExamine {
		t.Errorf("NewestPerFamily examined %d entries", n)
	}
	if len(newest) != 0 || !truncated {
		t.Errorf("families = %d, truncated %v; want none found and truncated within the expired batch", len(newest), truncated)
	}

	st.rowsExamined.Store(0)
	size, err := st.seriesSize(ctx, DefaultOwner, "big", time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if n := st.rowsExamined.Load(); n > seriesCountExamine || !size.More {
		t.Errorf("seriesSize examined %d entries, size %+v; want at most %d and More", n, size, seriesCountExamine)
	}

	st.rowsExamined.Store(0)
	slugs, err := st.SeriesSlugs(ctx, DefaultOwner, "big", "", 1000)
	if !errors.Is(err, ErrListingTruncated) {
		t.Errorf("SeriesSlugs err = %v, want ErrListingTruncated (%d slugs)", err, len(slugs))
	}
	if n := st.rowsExamined.Load(); n > 1000+ExamineSlack {
		t.Errorf("SeriesSlugs examined %d entries", n)
	}
	for _, s := range slugs {
		if strings.HasPrefix(s, "exp-") {
			t.Fatalf("expired %s returned", s)
		}
	}
}
