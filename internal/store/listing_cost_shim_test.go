// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"testing"
)

func familiesForTest(t *testing.T, st *Store, limit int) ([]Artifact, map[string]SeriesSize, bool) {
	t.Helper()
	newest, members, truncated, err := st.NewestPerFamily(context.Background(), DefaultOwner, limit)
	if err != nil {
		t.Fatal(err)
	}
	return newest, members, truncated
}

// listingQueriesForTest is each listing query with arguments for bigStore,
// and the index constraints its plan must show.
func listingQueriesForTest() map[string]listingQuery {
	return map[string]listingQuery{
		"families":       {qFamilies, []any{"local", FamilyScanBudget}, []string{"private=?"}},
		"series members": {qSeriesMembers, []any{"local", "big", seriesCountExamine}, []string{"private=? AND series=?"}},
		"list ties":      {qListTies, []any{"local", 1700050100, "big-00100", 201}, []string{"private=? AND created_at=? AND slug>?"}},
		"list older":     {qListOlder, []any{"local", 1700050100, 201}, []string{"private=? AND created_at<?"}},
		"slug page":      {qSlugPage, []any{"local", "big-00100", 500}, []string{"owner=? AND slug>?"}},
		"series slugs":   {qSeriesSlugs, []any{"local", "big", "big-00001", 50}, []string{"private=? AND series=?"}},
	}
}
