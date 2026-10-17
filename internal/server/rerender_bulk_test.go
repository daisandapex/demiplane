// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/store"
)

// bulkServer publishes n plain artifacts (nothing to rebake, so each is a
// skip) and returns the server.
func bulkServer(t *testing.T, n int) *Server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for i := 0; i < n; i++ {
		if _, err := st.Put(store.PutOptions{Slug: fmt.Sprintf("plain-%03d", i)}, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	return New(st, Config{})
}

func bulkRequest(ctx context.Context) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/rerender", nil).WithContext(ctx)
	r.Header.Set("Accept", "application/json")
	return r
}

// TestRerenderAllRunsOneAtATime: while one sweep runs, another is refused at
// once instead of walking the store alongside it.
func TestRerenderAllRunsOneAtATime(t *testing.T) {
	s := bulkServer(t, 3)
	h := s.Handler()
	// Hold every render slot so the first sweep waits inside its walk.
	var held []func()
	for {
		release, err := s.acquireRender(context.Background())
		if err != nil {
			break
		}
		held = append(held, release)
		if len(held) == cap(s.renderSem) {
			break
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan int, 1)
	go func() { first <- serve(h, bulkRequest(ctx)).Code }()
	time.Sleep(100 * time.Millisecond)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	t0 := time.Now()
	if code := serve(h, bulkRequest(ctx2)).Code; code != http.StatusConflict {
		t.Errorf("second sweep while one runs = %d, want 409", code)
	}
	if el := time.Since(t0); el > time.Second {
		t.Errorf("second sweep took %v to be refused", el)
	}
	cancel()
	for _, release := range held {
		release()
	}
	<-first
	// Once the first has finished, a new sweep runs.
	if code := serve(h, bulkRequest(context.Background())).Code; code != http.StatusOK {
		t.Errorf("sweep after the first finished = %d, want 200", code)
	}
}

// TestRerenderAllStopsOnCancellation: a sweep whose request is gone stops
// instead of going on through every remaining artifact.
func TestRerenderAllStopsOnCancellation(t *testing.T) {
	s := bulkServer(t, 50)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := serve(s.Handler(), bulkRequest(ctx))
	var out struct {
		Rerendered int               `json:"rerendered"`
		Skipped    int               `json:"skipped"`
		Failed     map[string]string `json:"failed"`
	}
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("body %q: %v", w.Body.String(), err)
		}
	}
	if n := out.Rerendered + out.Skipped + len(out.Failed); n > 0 {
		t.Errorf("a cancelled sweep went on through %d artifacts", n)
	}
}

// TestRerenderAllBoundsItsReport: the report counts every artifact but lists
// at most rerenderListMax of them.
func TestRerenderAllBoundsItsReport(t *testing.T) {
	s := bulkServer(t, 250)
	w := serve(s.Handler(), bulkRequest(context.Background()))
	if w.Code != http.StatusOK {
		t.Fatalf("sweep = %d", w.Code)
	}
	var out struct {
		Rerendered int               `json:"rerendered"`
		Skipped    int               `json:"skipped"`
		Failed     map[string]string `json:"failed"`
		Results    []rerenderResult  `json:"results"`
		Truncated  bool              `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Skipped != 250 || out.Rerendered != 0 {
		t.Errorf("counts rerendered %d skipped %d, want 0 and 250", out.Rerendered, out.Skipped)
	}
	if len(out.Results) > 100 || !out.Truncated {
		t.Errorf("listed %d results, truncated %v; want at most 100 and truncated", len(out.Results), out.Truncated)
	}
}
