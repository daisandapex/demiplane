// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// holdRenderSlots takes every render slot until the test ends, so a sweep
// waits in its walk.
func holdRenderSlots(t *testing.T, s *Server) {
	t.Helper()
	var held []func()
	for len(held) < cap(s.renderSem) {
		release, err := s.acquireRender(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, release)
	}
	t.Cleanup(func() {
		for _, release := range held {
			release()
		}
	})
}

// TestBulkRerenderStopsForAGoneClientAfterPipelinedBytes: with request bytes
// pipelined after a bodyless POST /rerender, net/http stops watching the
// connection, so the request context does not end when the client goes. The
// sweep still notices (its keep-alive writes fail), stops, and releases the
// one-sweep lock.
func TestBulkRerenderStopsForAGoneClientAfterPipelinedBytes(t *testing.T) {
	saved := rerenderKeepAlive
	rerenderKeepAlive = 100 * time.Millisecond
	t.Cleanup(func() { rerenderKeepAlive = saved })
	s := bulkServer(t, 5)
	holdRenderSlots(t, s)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "POST /rerender HTTP/1.1\r\nHost: x\r\nAccept: application/json\r\n\r\n")
	// The next request arrives once the sweep is running: net/http's
	// background read takes its first byte and stops watching the connection.
	time.Sleep(200 * time.Millisecond)
	fmt.Fprintf(conn, "GET /health HTTP/1.1\r\nHost: x\r\n\r\n")
	time.Sleep(200 * time.Millisecond)
	conn.Close()

	deadline := time.Now().Add(3 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		code := serve(s.Handler(), bulkRequest(ctx)).Code
		cancel()
		if code != http.StatusConflict {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the sweep for a disconnected client still holds the lock after 3s")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestBulkRerenderStopsAtItsTimeLimitAndResumes: a sweep stops at
// rerenderSweepMax and reports where; ?after= resumes there, and the two
// sweeps together account for every artifact once.
func TestBulkRerenderStopsAtItsTimeLimitAndResumes(t *testing.T) {
	savedMax, savedWait, savedKA := rerenderSweepMax, renderWait, rerenderKeepAlive
	// Keep-alives start before the report, so it follows leading newlines.
	rerenderSweepMax, renderWait, rerenderKeepAlive = 350*time.Millisecond, 100*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { rerenderSweepMax, renderWait, rerenderKeepAlive = savedMax, savedWait, savedKA })
	s := bulkServer(t, 20)
	h := s.Handler()

	type report struct {
		Rerendered  int               `json:"rerendered"`
		Skipped     int               `json:"skipped"`
		FailedCount int               `json:"failed_count"`
		Failed      map[string]string `json:"failed"`
		Complete    bool              `json:"complete"`
		Next        string            `json:"next"`
	}
	sweep := func(target string) report {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, target, nil)
		r.Header.Set("Accept", "application/json")
		w := serve(h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d", target, w.Code)
		}
		if target == "/rerender" && !strings.HasPrefix(w.Body.String(), "\n") {
			t.Errorf("a long sweep's response does not start with keep-alive newlines: %q", w.Body.String())
		}
		var out report
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s body %q: %v", target, w.Body.String(), err)
		}
		return out
	}

	// Every render slot held: each artifact waits renderWait and fails busy,
	// so the sweep reaches its limit partway.
	var held []func()
	for len(held) < cap(s.renderSem) {
		release, err := s.acquireRender(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, release)
	}
	first := sweep("/rerender")
	for _, release := range held {
		release()
	}
	if first.Complete || first.Next == "" {
		t.Fatalf("first sweep complete=%v next=%q; want stopped with a resume point", first.Complete, first.Next)
	}
	if first.FailedCount == 0 || first.FailedCount >= 20 {
		t.Fatalf("first sweep handled %d artifacts, want some but not all", first.FailedCount)
	}

	rerenderSweepMax = time.Minute
	second := sweep("/rerender?after=" + first.Next)
	if !second.Complete {
		t.Fatal("resumed sweep did not complete")
	}
	if total := first.FailedCount + second.Skipped + second.Rerendered + second.FailedCount; total != 20 {
		t.Errorf("the two sweeps handled %d artifacts, want 20", total)
	}
}
