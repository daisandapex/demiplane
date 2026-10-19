// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// TestLiveStreamSurvivesQuietPeriodsOverH2: a write deadline left armed after
// a frame is flushed would reset an HTTP/2 stream when it expires between
// heartbeats; a quiet stream must stay up across several.
func TestLiveStreamSurvivesQuietPeriodsOverH2(t *testing.T) {
	savedHB, savedWT := liveHeartbeat, liveWriteTimeout
	liveHeartbeat, liveWriteTimeout = 600*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() { liveHeartbeat, liveWriteTimeout = savedHB, savedWT })
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Put(store.PutOptions{Slug: "quiet"}, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(New(st, Config{}).Handler())
	ts.EnableHTTP2 = true
	ts.StartTLS()
	t.Cleanup(ts.Close)
	client := ts.Client()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/_events/quiet", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("stream is %s, want HTTP/2", resp.Proto)
	}
	br := bufio.NewReader(resp.Body)
	pings := 0
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) && pings < 3 {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended after %d heartbeats: %v", pings, err)
		}
		if strings.HasPrefix(line, ": ping") {
			pings++
		}
	}
	if pings < 3 {
		t.Fatalf("only %d heartbeats in 2.5s", pings)
	}
}
