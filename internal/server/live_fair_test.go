// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/limit"
)

// proxiedLiveServer is a test server behind a trusted proxy at 127.0.0.1, so
// X-Forwarded-For models distinct users.
func proxiedLiveServer(t *testing.T) *httptest.Server {
	t.Helper()
	saved := liveHeartbeat
	liveHeartbeat = 50 * time.Millisecond
	t.Cleanup(func() { liveHeartbeat = saved })
	p, _ := limit.ParseProxies("127.0.0.1")
	return newConfiguredServer(t, Config{TrustedProxies: p})
}

// liveStream is an open SSE stream whose body a goroutine keeps reading
// (consuming heartbeats); ended closes when the server ends it.
type liveStream struct {
	status int
	ended  chan struct{}
}

func openFrom(t *testing.T, base, slug, user string) liveStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/_events/"+slug, nil)
	req.Header.Set("X-Forwarded-For", user)
	resp, err := sseClient(t).Do(req)
	if err != nil {
		t.Fatalf("open SSE as %s: %v", user, err)
	}
	s := liveStream{status: resp.StatusCode, ended: make(chan struct{})}
	go func() {
		defer close(s.ended)
		defer resp.Body.Close()
		buf := make([]byte, 512)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				return
			}
		}
	}()
	return s
}

// TestSSEOneClientCannotHoldASlug: one client opening many streams on a
// public slug is held to its per-slug share, and another user still gets in.
func TestSSEOneClientCannotHoldASlug(t *testing.T) {
	ts := proxiedLiveServer(t)
	publish(t, ts, "?slug=hot", "x")
	ok := 0
	for i := 0; i < 16; i++ {
		if openFrom(t, ts.URL, "hot", "203.0.113.1").status == http.StatusOK {
			ok++
		}
	}
	if ok > 2 {
		t.Errorf("one client holds %d streams on one slug, want at most 2", ok)
	}
	time.Sleep(120 * time.Millisecond) // heartbeats flow; the streams stay up
	if got := openFrom(t, ts.URL, "hot", "198.51.100.2").status; got != http.StatusOK {
		t.Fatalf("another user = %d, want 200", got)
	}
}

// TestSSEFullSlugAdmitsANewcomer: a slug filled by many clients still admits
// a new one, by ending the oldest stream of a client that holds the most;
// capacity recovers without the holders' cooperation.
func TestSSEFullSlugAdmitsANewcomer(t *testing.T) {
	ts := proxiedLiveServer(t)
	publish(t, ts, "?slug=full", "x")
	var held []liveStream
	for i := 1; i <= 16; i++ {
		s := openFrom(t, ts.URL, "full", fmt.Sprintf("203.0.113.%d", i))
		if s.status != http.StatusOK {
			t.Fatalf("filler %d = %d", i, s.status)
		}
		held = append(held, s)
		time.Sleep(2 * time.Millisecond) // distinct ages
	}
	if got := openFrom(t, ts.URL, "full", "198.51.100.2").status; got != http.StatusOK {
		t.Fatalf("newcomer on a full slug = %d, want 200", got)
	}
	select {
	case <-held[0].ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the oldest stream was not ended to make room")
	}
	for i, s := range held[1:] {
		select {
		case <-s.ended:
			t.Fatalf("stream %d ended too; only one should make room", i+1)
		default:
		}
	}
}

// TestSSEDistinctProxiedUsersEachGetAStream: behind a trusted proxy, one
// user at its per-client cap across many slugs does not stop other users.
func TestSSEDistinctProxiedUsersEachGetAStream(t *testing.T) {
	ts := proxiedLiveServer(t)
	for i := 0; i < 20; i++ {
		publish(t, ts, fmt.Sprintf("?slug=p%d", i), "x")
	}
	for i := 0; i < 18; i++ {
		openFrom(t, ts.URL, fmt.Sprintf("p%d", i), "203.0.113.1")
	}
	for i, user := range []string{"198.51.100.2", "198.51.100.3"} {
		if got := openFrom(t, ts.URL, fmt.Sprintf("p%d", 18+i), user).status; got != http.StatusOK {
			t.Errorf("user %s = %d, want 200", user, got)
		}
	}
}

func gone(s *liveSub) bool {
	select {
	case <-s.gone:
		return true
	default:
		return false
	}
}

// TestLiveHubCaps: the per-client caps refuse a client's own extra streams;
// a full total admits a newcomer by ending the oldest stream of the client
// holding the most; and nobody is ended for a client that holds as many.
func TestLiveHubCaps(t *testing.T) {
	h := newLiveHub(LiveLimits{PerSlug: 3, PerClient: 3, PerClientSlug: 2, Total: 4})
	a1, _ := h.subscribe("s1", "a")
	if _, ok := h.subscribe("s1", "a"); !ok {
		t.Fatal("second stream on one slug refused")
	}
	if _, ok := h.subscribe("s1", "a"); ok {
		t.Fatal("third stream from one client on one slug admitted")
	}
	if _, ok := h.subscribe("s2", "a"); !ok {
		t.Fatal("stream on another slug refused")
	}
	if _, ok := h.subscribe("s3", "a"); ok {
		t.Fatal("client admitted past its total cap")
	}
	b1, ok := h.subscribe("s3", "b")
	if !ok || h.total != 4 {
		t.Fatalf("b refused or total %d", h.total)
	}
	// Total full: c takes the place of a's oldest stream.
	if _, ok := h.subscribe("s4", "c"); !ok {
		t.Fatal("newcomer refused at the total cap")
	}
	if !gone(a1) || gone(b1) {
		t.Fatalf("ended a1=%v b1=%v; want only a's oldest", gone(a1), gone(b1))
	}
	// a, b and c now hold 2, 1 and 1: d (holding none) ends a's oldest left.
	if _, ok := h.subscribe("s5", "d"); !ok {
		t.Fatal("d refused")
	}
	if len(h.byClient["a"]) != 1 {
		t.Fatalf("a holds %d, want 1", len(h.byClient["a"]))
	}
	// A client that already holds as many as everyone else is refused.
	if _, ok := h.subscribe("s6", "d"); ok {
		t.Fatal("d admitted by ending a stream of a client holding no more than d")
	}
	h.unsubscribe(a1) // already ended: a no-op
	if h.total != 4 {
		t.Fatalf("total %d after a no-op unsubscribe, want 4", h.total)
	}
}
