// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/store"
)

// stalledWriter is a response whose client never takes the body: the status
// goes out, but every body write blocks until its write deadline passes and
// then fails, as net/http's do for a client that withholds TCP or HTTP/2
// flow-control credit.
type stalledWriter struct {
	mu       sync.Mutex
	header   http.Header
	deadline time.Time
	changed  chan struct{} // closed and replaced when the deadline moves
}

func newStalledWriter() *stalledWriter {
	return &stalledWriter{header: http.Header{}, changed: make(chan struct{})}
}

func (s *stalledWriter) Header() http.Header { return s.header }
func (s *stalledWriter) WriteHeader(int)     {}

func (s *stalledWriter) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	s.deadline = t
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	return nil
}

func (s *stalledWriter) Write(p []byte) (int, error) {
	for {
		s.mu.Lock()
		d, changed := s.deadline, s.changed
		s.mu.Unlock()
		var expired <-chan time.Time
		if !d.IsZero() {
			wait := time.Until(d)
			if wait <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			expired = time.After(wait)
		}
		select {
		case <-expired:
			return 0, os.ErrDeadlineExceeded
		case <-changed:
		}
	}
}

func (s *stalledWriter) FlushError() error { return nil }

// TestBulkRerenderReleasesTheLockForAStalledClient: a client that stops
// taking the response cannot hold the one-sweep lock for the sweep's whole
// time limit; a stalled keep-alive write times out and the sweep stops.
func TestBulkRerenderReleasesTheLockForAStalledClient(t *testing.T) {
	savedKA, savedWT := rerenderKeepAlive, rerenderWriteTimeout
	rerenderKeepAlive, rerenderWriteTimeout = 50*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { rerenderKeepAlive, rerenderWriteTimeout = savedKA, savedWT })
	s := bulkServer(t, 5)
	holdRenderSlots(t, s)

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		r := httptest.NewRequest(http.MethodPost, "/rerender", nil)
		r.Header.Set("Accept", "application/json")
		s.Handler().ServeHTTP(newStalledWriter(), r)
	}()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("the sweep for a stalled client is still running after 3s")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if code := serve(s.Handler(), bulkRequest(ctx)).Code; code == http.StatusConflict {
		t.Error("the one-sweep lock is still held")
	}
}

// TestResumedSweepReportsWhereItStopped: a sweep resumed with ?after= that
// stops before finishing an artifact reports the resume point it was given,
// and one that stops after a page of only expired artifacts reports a point
// past them, never "" (the start of the store).
func TestResumedSweepReportsWhereItStopped(t *testing.T) {
	savedMax, savedWait, savedPage := rerenderSweepMax, renderWait, rerenderPage
	rerenderSweepMax, renderWait, rerenderPage = 300*time.Millisecond, time.Hour, 5
	t.Cleanup(func() { rerenderSweepMax, renderWait, rerenderPage = savedMax, savedWait, savedPage })
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	put := func(slug string, ttl time.Duration) {
		if _, err := st.Put(store.PutOptions{Slug: slug, TTL: ttl}, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		put(fmt.Sprintf("a-gone-%d", i), time.Nanosecond) // a whole page, expired
	}
	put("b-live", 0)
	put("c-live", 0)
	time.Sleep(5 * time.Millisecond)
	s := New(st, Config{})
	holdRenderSlots(t, s) // no artifact can finish

	sweep := func(target string) (complete bool, next string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, target, nil)
		r.Header.Set("Accept", "application/json")
		w := serve(s.Handler(), r)
		var out struct {
			Complete bool   `json:"complete"`
			Next     string `json:"next"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %d %q: %v", target, w.Code, w.Body.String(), err)
		}
		return out.Complete, out.Next
	}
	if complete, next := sweep("/rerender?after=b-live"); complete || next != "b-live" {
		t.Errorf("resumed sweep that finished nothing: complete=%v next=%q, want next \"b-live\"", complete, next)
	}
	if complete, next := sweep("/rerender"); complete || next != "a-gone-4" {
		t.Errorf("sweep stopped after an expired-only page: complete=%v next=%q, want \"a-gone-4\"", complete, next)
	}
}

// TestBulkRerenderReportWriteIsBounded: a sweep that finishes before any
// keep-alive still gets only one write timeout to hand over its report to a
// client that stopped reading.
func TestBulkRerenderReportWriteIsBounded(t *testing.T) {
	saved := rerenderWriteTimeout
	rerenderWriteTimeout = 300 * time.Millisecond
	t.Cleanup(func() { rerenderWriteTimeout = saved })
	s := bulkServer(t, 3)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		r := httptest.NewRequest(http.MethodPost, "/rerender", nil)
		r.Header.Set("Accept", "application/json")
		s.Handler().ServeHTTP(newStalledWriter(), r)
	}()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("the sweep is still writing its report to a stalled client after 3s")
	}
}
