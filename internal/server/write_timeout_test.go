// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/store"
)

// TestWriteTimeoutEndsALiveStream: --write-timeout (http.Server.WriteTimeout)
// bounds a whole response from its start, a live-reload stream included,
// however the handlers set their own write deadlines.
func TestWriteTimeoutEndsALiveStream(t *testing.T) {
	saved := liveHeartbeat
	liveHeartbeat = 50 * time.Millisecond
	t.Cleanup(func() { liveHeartbeat = saved })
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewUnstartedServer(New(st, Config{}).Handler())
	ts.Config.WriteTimeout = 400 * time.Millisecond
	ts.Start()
	t.Cleanup(ts.Close)
	publish(t, ts, "?slug=wt", "x")

	t0 := time.Now()
	_, lines := openSSE(t, ts.URL, "wt")
	timeout := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				if el := time.Since(t0); el < 300*time.Millisecond {
					t.Errorf("stream ended after %v, before the write timeout", el)
				}
				return
			}
		case <-timeout:
			t.Fatal("live-reload stream outlived --write-timeout by seconds")
		}
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	got time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error { d.got = t; return nil }

// TestCappedWriteDeadline: a later deadline, or none, becomes the cap; an
// earlier one stands.
func TestCappedWriteDeadline(t *testing.T) {
	limit := time.Now().Add(time.Minute)
	for _, tc := range []struct {
		name string
		set  time.Time
		want time.Time
	}{
		{"cleared", time.Time{}, limit},
		{"later", limit.Add(time.Hour), limit},
		{"earlier", limit.Add(-time.Second), limit.Add(-time.Second)},
	} {
		rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		c := &cappedWriteDeadline{ResponseWriter: rec, limit: limit}
		if err := http.NewResponseController(c).SetWriteDeadline(tc.set); err != nil {
			t.Fatal(err)
		}
		if !rec.got.Equal(tc.want) {
			t.Errorf("%s: deadline %v, want %v", tc.name, rec.got, tc.want)
		}
	}
}
