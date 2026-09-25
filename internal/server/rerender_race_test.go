// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// TestRerenderDoesNotOverwriteNewerPublish: a rebake racing a publish of the
// same slug must never leave the page rendered from a source older than the
// one retained. Each round publishes a new version while a rebake runs, then
// checks that the served page matches the retained source.
func TestRerenderDoesNotOverwriteNewerPublish(t *testing.T) {
	st := newSharedStore(t)
	ts := newStoreServer(t, st, renderConfig("https://example.com/f"))
	publish(t, ts, "?render=md&slug=racy", "# v0\n\nmarker-v0-end\n")
	for i := 1; i <= 150; i++ {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			resp, err := http.Post(ts.URL+"/publish?render=md&slug=racy", "text/markdown",
				strings.NewReader(fmt.Sprintf("# v%d\n\nmarker-v%d-end\n", i, i)))
			if err != nil {
				t.Errorf("publish: %v", err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/rerender/racy", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("rerender: %v", err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
		wg.Wait()
		rec, err := st.Source("racy")
		if err != nil {
			t.Fatalf("round %d: Source: %v", i, err)
		}
		want := fmt.Sprintf("marker-v%d-end", i)
		if !strings.Contains(string(rec.Body), want) {
			t.Fatalf("round %d: retained source is not the latest publish", i)
		}
		_, page := get(t, ts.URL+"/racy")
		if !strings.Contains(string(page), want) {
			t.Fatalf("round %d: served page was rendered from an older source than the retained one", i)
		}
	}
}
