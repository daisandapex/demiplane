// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestBulkRerenderRefusesABody: POST /rerender takes no body; one that
// announces a body is refused before the sweep starts.
func TestBulkRerenderRefusesABody(t *testing.T) {
	s := bulkServer(t, 2)
	r := httptest.NewRequest(http.MethodPost, "/rerender", strings.NewReader("x"))
	if w := serve(s.Handler(), r); w.Code != http.StatusBadRequest {
		t.Errorf("bulk rerender with a body = %d, want 400", w.Code)
	}
}

// TestBulkRerenderDisconnectWithUnsentBody: a caller that announces a body,
// never sends it, and disconnects leaves no sweep running.
func TestBulkRerenderDisconnectWithUnsentBody(t *testing.T) {
	s := bulkServer(t, 3)
	// Hold every render slot, so a sweep that started would wait in its walk.
	var held []func()
	for len(held) < cap(s.renderSem) {
		release, err := s.acquireRender(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, release)
	}
	defer func() {
		for _, release := range held {
			release()
		}
	}()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "POST /rerender HTTP/1.1\r\nHost: x\r\nAccept: application/json\r\nContent-Length: 10\r\n\r\n")
	time.Sleep(200 * time.Millisecond)
	conn.Close()
	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if code := serve(s.Handler(), bulkRequest(ctx)).Code; code == http.StatusConflict {
		t.Error("a sweep is still running for a caller that disconnected without sending its body")
	}
}
