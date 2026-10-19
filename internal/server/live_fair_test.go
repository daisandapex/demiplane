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

	"github.com/daisandapex/demiplane/v3/internal/limit"
	"github.com/daisandapex/demiplane/v3/internal/store"
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

// TestSSEFullSlugAdmitsANewcomer: a slug filled by clients holding two
// streams each still admits a new client, by ending the oldest stream of one
// of them; capacity recovers without the holders' cooperation. The client
// that lost a stream, now holding fewer than the rest, cannot take it back.
func TestSSEFullSlugAdmitsANewcomer(t *testing.T) {
	ts := proxiedLiveServer(t)
	publish(t, ts, "?slug=full", "x")
	var held []liveStream
	for i := 0; i < 16; i++ {
		s := openFrom(t, ts.URL, "full", fmt.Sprintf("203.0.113.%d", 1+i/2))
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
	if got := openFrom(t, ts.URL, "full", "203.0.113.1").status; got != http.StatusServiceUnavailable {
		t.Fatalf("the client that lost a stream reconnecting = %d, want 503 (no churn)", got)
	}
}

// TestSSEFullSlugOfSingleStreamsDoesNotChurn: when every client on a full
// slug holds one stream, a newcomer is refused rather than swapped in; moving
// a stream from one single-stream client to another would not improve
// balance, and its reconnect would swap it back.
func TestSSEFullSlugOfSingleStreamsDoesNotChurn(t *testing.T) {
	ts := proxiedLiveServer(t)
	publish(t, ts, "?slug=busy", "x")
	var held []liveStream
	for i := 1; i <= 16; i++ {
		s := openFrom(t, ts.URL, "busy", fmt.Sprintf("203.0.113.%d", i))
		if s.status != http.StatusOK {
			t.Fatalf("filler %d = %d", i, s.status)
		}
		held = append(held, s)
	}
	if got := openFrom(t, ts.URL, "busy", "198.51.100.17").status; got != http.StatusServiceUnavailable {
		t.Errorf("17th client = %d, want 503", got)
	}
	time.Sleep(100 * time.Millisecond)
	for i, s := range held {
		select {
		case <-s.ended:
			t.Errorf("stream %d was ended for a client holding no fewer", i)
		default:
		}
	}
}

// admit subscribes and runs a stand-in handler that exits when the hub ends
// the stream, like handleLiveEvents.
func admit(h *liveHub, slug, client string) (*liveSub, bool) {
	sub, ok := subscribeNow(h, slug, client)
	if ok {
		go func() {
			<-sub.gone
			h.unsubscribe(sub)
		}()
	}
	return sub, ok
}

// TestLiveHubEvictionConverges: two clients that each want two streams under
// a total of three, reconnecting whenever a stream ends, settle after at most
// one eviction instead of trading a stream back and forth.
func TestLiveHubEvictionConverges(t *testing.T) {
	h := newLiveHub(LiveLimits{PerSlug: 16, PerClient: 16, PerClientSlug: 16, Total: 3})
	type want struct {
		client string
		subs   []*liveSub
	}
	clients := []*want{{client: "a"}, {client: "b"}}
	evictions := 0
	for round := 0; round < 20; round++ {
		for _, c := range clients {
			live := c.subs[:0]
			for _, s := range c.subs {
				if gone(s) {
					evictions++
				} else {
					live = append(live, s)
				}
			}
			c.subs = live
			for len(c.subs) < 2 {
				s, ok := admit(h, fmt.Sprintf("%s%d", c.client, round), c.client)
				if !ok {
					break
				}
				c.subs = append(c.subs, s)
			}
			time.Sleep(2 * time.Millisecond) // let ended handlers exit
		}
	}
	if evictions > 1 {
		t.Fatalf("%d evictions over 20 reconnect rounds, want at most 1", evictions)
	}
}

// TestLiveHubEvictedStreamCountsUntilItsHandlerExits: a stream ended to make
// room keeps its slot until its handler returns (a handler blocked in a
// write lives until the write deadline), so the newcomer waits for it rather
// than running over the cap.
func TestLiveHubEvictedStreamCountsUntilItsHandlerExits(t *testing.T) {
	h := newLiveHub(LiveLimits{PerSlug: 16, PerClient: 16, PerClientSlug: 16, Total: 2})
	a1, _ := subscribeNow(h, "s1", "a") // handlers that do not exit by themselves
	subscribeNow(h, "s2", "a")
	result := make(chan bool, 1)
	go func() {
		_, ok := subscribeNow(h, "s3", "b")
		result <- ok
	}()
	select {
	case <-a1.gone:
	case <-time.After(time.Second):
		t.Fatal("a's oldest stream was not ended for b")
	}
	select {
	case <-result:
		t.Fatal("b admitted while the ended stream's handler is still running: 3 streams over a total of 2")
	case <-time.After(100 * time.Millisecond):
	}
	h.unsubscribe(a1) // the blocked handler finally returns
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("b refused after the ended stream's handler exited")
		}
	case <-time.After(time.Second):
		t.Fatal("b still waiting after the ended stream's handler exited")
	}
	if h.total != 2 {
		t.Fatalf("total %d, want 2", h.total)
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

// subscribeNow is subscribe with no request context.
func subscribeNow(h *liveHub, slug, client string) (*liveSub, bool) {
	return h.subscribe(context.Background(), slug, client)
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
// holding the most, once that client's handler has returned; and nobody is
// ended for a client holding fewer than two less.
func TestLiveHubCaps(t *testing.T) {
	h := newLiveHub(LiveLimits{PerSlug: 3, PerClient: 3, PerClientSlug: 2, Total: 4})
	a1, _ := admit(h, "s1", "a")
	if _, ok := admit(h, "s1", "a"); !ok {
		t.Fatal("second stream on one slug refused")
	}
	if _, ok := admit(h, "s1", "a"); ok {
		t.Fatal("third stream from one client on one slug admitted")
	}
	if _, ok := admit(h, "s2", "a"); !ok {
		t.Fatal("stream on another slug refused")
	}
	if _, ok := admit(h, "s3", "a"); ok {
		t.Fatal("client admitted past its total cap")
	}
	b1, ok := admit(h, "s3", "b")
	if !ok || h.total != 4 {
		t.Fatalf("b refused or total %d", h.total)
	}
	// Total full; a holds 3, c none: c takes the place of a's oldest stream.
	if _, ok := admit(h, "s4", "c"); !ok {
		t.Fatal("newcomer refused at the total cap")
	}
	if !gone(a1) || gone(b1) {
		t.Fatalf("ended a1=%v b1=%v; want only a's oldest", gone(a1), gone(b1))
	}
	// a, b and c hold 2, 1 and 1: d (none) takes a's oldest left.
	if _, ok := admit(h, "s5", "d"); !ok {
		t.Fatal("d refused")
	}
	// Everyone holds one: moving a stream would not improve balance.
	if _, ok := admit(h, "s6", "e"); ok {
		t.Fatal("e admitted by ending the stream of a client holding one")
	}
	if h.total != 4 {
		t.Fatalf("total %d, want 4", h.total)
	}
	h.unsubscribe(a1) // already gone: a no-op
	if h.total != 4 {
		t.Fatalf("total %d after a no-op unsubscribe, want 4", h.total)
	}
}

// TestLiveHubNewcomerGivesUpOnAStuckHandler: if the ended stream's handler
// does not return within drainWait, the newcomer is refused and the slot
// stays with the ended stream until it does.
func TestLiveHubNewcomerGivesUpOnAStuckHandler(t *testing.T) {
	h := newLiveHub(LiveLimits{PerSlug: 16, PerClient: 16, PerClientSlug: 16, Total: 2})
	h.drainWait = 50 * time.Millisecond
	a1, _ := subscribeNow(h, "s1", "a")
	subscribeNow(h, "s2", "a")
	if _, ok := subscribeNow(h, "s3", "b"); ok {
		t.Fatal("b admitted while the ended stream's handler never returned")
	}
	if !gone(a1) || h.total != 2 {
		t.Fatalf("a1 ended=%v total=%d; want ended and 2", gone(a1), h.total)
	}
	// The stuck stream is not ended twice, and its slot frees on return.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		h.unsubscribe(a1)
	}()
	if _, ok := h.subscribe(ctx, "s3", "b"); !ok {
		t.Fatal("b refused once the ended stream's handler returned")
	}
}

// TestSSENewcomersWaitingOnADrainDoNotStarveTheListener: newcomers waiting
// for a stuck ended stream's handler hold no ordinary request slots, so
// unrelated requests on the listener are served while they wait; the pending
// caps refuse the excess at once.
func TestSSENewcomersWaitingOnADrainDoNotStarveTheListener(t *testing.T) {
	p, _ := limit.ParseProxies("127.0.0.1")
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, Config{
		TrustedProxies: p,
		// Fewer request slots than waiting newcomers: had the waiters kept
		// theirs, nothing else could be served.
		Requests: RequestLimits{MaxRequests: 3, MaxRequestsPerClient: -1},
		Live:     LiveLimits{PerSlug: 16, PerClient: 16, PerClientSlug: 16, Total: 2, Pending: 4, PendingPerClient: 1},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	publish(t, ts, "?slug=drain", "x")
	hub := s.liveHub
	hub.drainWait = 1500 * time.Millisecond
	// Two streams whose handlers never return by themselves.
	stuck1, _ := subscribeNow(hub, "drain", "203.0.113.200")
	stuck2, _ := subscribeNow(hub, "drain", "203.0.113.200")
	t.Cleanup(func() { hub.unsubscribe(stuck1); hub.unsubscribe(stuck2) })

	const newcomers = 40
	codes := make(chan int, newcomers)
	start := time.Now()
	for i := 0; i < newcomers; i++ {
		go func() {
			req, _ := http.NewRequest(http.MethodGet, ts.URL+"/_events/drain", nil)
			// Ten clients, four requests each.
			req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i%10))
			resp, err := sseClient(t).Do(req)
			if err != nil {
				codes <- 0
				return
			}
			resp.Body.Close()
			codes <- resp.StatusCode
		}()
		time.Sleep(3 * time.Millisecond) // each passes request admission in turn
	}
	time.Sleep(300 * time.Millisecond) // the newcomers are waiting now
	hub.mu.Lock()
	waiting, perClient := hub.pending, 0
	for _, n := range hub.pendingClient {
		perClient = max(perClient, n)
	}
	hub.mu.Unlock()
	if waiting != 4 || perClient != 1 {
		t.Errorf("%d newcomers waiting, at most %d from one client; want 4 and 1 (the pending caps)", waiting, perClient)
	}
	for i := 0; i < 5; i++ {
		resp, err := http.Get(ts.URL + "/drain")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unrelated GET while newcomers wait = %d, want 200", resp.StatusCode)
		}
	}
	// Every newcomer is refused: the excess at once, the waiters when the
	// single drain wait runs out (the stuck handlers never return).
	for i := 0; i < newcomers; i++ {
		select {
		case c := <-codes:
			if c != http.StatusServiceUnavailable {
				t.Errorf("newcomer = %d, want 503", c)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a newcomer never returned")
		}
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("newcomers took %v; the wait is one drainWait (1.5s) in all", el)
	}
	assertNoPending(t, hub)
}

// TestLiveHubPendingReservations: a newcomer's pending reservation is
// released when its wait is cancelled, times out or is admitted, and the
// per-client pending cap refuses a second wait from the same client.
func TestLiveHubPendingReservations(t *testing.T) {
	h := newLiveHub(LiveLimits{PerSlug: 16, PerClient: 16, PerClientSlug: 16, Total: 2, Pending: 8, PendingPerClient: 1})
	h.drainWait = 200 * time.Millisecond
	a1, _ := subscribeNow(h, "s", "a")
	subscribeNow(h, "s", "a")

	// Cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan bool, 1)
	go func() { _, ok := h.subscribe(ctx, "t", "b"); res <- ok }()
	time.Sleep(50 * time.Millisecond)
	if _, ok := subscribeNow(h, "u", "b"); ok {
		t.Fatal("second pending wait from one client admitted")
	}
	cancel()
	if <-res {
		t.Fatal("cancelled newcomer admitted")
	}
	assertNoPending(t, h)

	// Timed out.
	if _, ok := subscribeNow(h, "t", "b"); ok {
		t.Fatal("newcomer admitted while the slot never freed")
	}
	assertNoPending(t, h)

	// Admitted.
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.unsubscribe(a1)
	}()
	if _, ok := subscribeNow(h, "t", "b"); !ok {
		t.Fatal("newcomer refused after the slot freed")
	}
	assertNoPending(t, h)
}
