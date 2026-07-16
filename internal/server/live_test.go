// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/store"
)

// liveSlug extracts the trailing slug from a published artifact URL.
func liveSlug(t *testing.T, u string) string {
	t.Helper()
	i := strings.LastIndexByte(u, '/')
	if i < 0 {
		t.Fatalf("no slug in URL %q", u)
	}
	return u[i+1:]
}

// sseClient returns an http.Client with its OWN transport (never the shared
// DefaultClient pool) plus cleanup that cancels in-flight streams and closes
// idle conns. Long-lived SSE streams left on DefaultClient stall httptest
// teardown (Close waits on the pooled connection); a dedicated transport keeps
// each test fast and isolated.
func sseClient(t *testing.T) *http.Client {
	t.Helper()
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr}
}

// openSSE opens the SSE stream for slug and returns the response plus a channel
// of raw lines fed by a SINGLE reader goroutine (so no two goroutines ever read
// the body concurrently). The stream is torn down via context cancel at test
// cleanup; the caller may also close resp.Body early.
func openSSE(t *testing.T, base, slug string) (*http.Response, <-chan string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/_events/"+slug, nil)
	if err != nil {
		t.Fatalf("new SSE request: %v", err)
	}
	resp, err := sseClient(t).Do(req)
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("SSE status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		resp.Body.Close()
		t.Fatalf("SSE content-type = %q, want text/event-stream", ct)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		br := bufio.NewReader(resp.Body)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				lines <- line
			}
			if err != nil {
				return
			}
		}
	}()
	return resp, lines
}

// waitReloadEvent consumes lines until an `event: reload` frame arrives or the
// deadline passes.
func waitReloadEvent(t *testing.T, lines <-chan string, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("SSE stream closed before a reload event arrived")
			}
			if strings.HasPrefix(line, "event: reload") {
				return
			}
		case <-deadline:
			t.Fatalf("no reload event within %s", within)
		}
	}
}

// expectNoEvent asserts that no `event:` frame arrives within the window
// (comments/heartbeats are ignored).
func expectNoEvent(t *testing.T, lines <-chan string, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return // stream closed, no event seen
			}
			if strings.HasPrefix(line, "event:") {
				t.Fatalf("unexpected SSE event: %q", line)
			}
		case <-deadline:
			return // good: no event in the window
		}
	}
}

func TestSSEReloadOnRepublish(t *testing.T) {
	ts := newTestServer(t, "")
	url := publish(t, ts, "?slug=live-demo", "<html>v1</html>")
	slug := liveSlug(t, url)

	resp, lines := openSSE(t, ts.URL, slug)
	defer resp.Body.Close()

	// First frame is the ": connected" comment.
	select {
	case line := <-lines:
		if !strings.HasPrefix(line, ":") {
			t.Fatalf("first frame = %q, want a comment", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no opening frame")
	}

	// Republish to the same slug; the subscriber must get a reload event.
	go func() {
		time.Sleep(20 * time.Millisecond)
		publish(t, ts, "?slug=live-demo", "<html>v2</html>")
	}()
	waitReloadEvent(t, lines, 3*time.Second)
}

func TestSSEReloadIsSlugScoped(t *testing.T) {
	ts := newTestServer(t, "")
	watched := liveSlug(t, publish(t, ts, "?slug=watched", "a"))
	other := liveSlug(t, publish(t, ts, "?slug=other", "b"))

	resp, lines := openSSE(t, ts.URL, watched)
	defer resp.Body.Close()

	// A republish to a DIFFERENT slug must not wake this subscriber.
	publish(t, ts, "?slug="+other, "b2")
	expectNoEvent(t, lines, 400*time.Millisecond)

	// Now republish the watched slug; the event must arrive.
	go func() {
		time.Sleep(20 * time.Millisecond)
		publish(t, ts, "?slug="+watched, "a2")
	}()
	waitReloadEvent(t, lines, 3*time.Second)
}

func TestSSEPerSlugSubscriberCap(t *testing.T) {
	ts := newTestServer(t, "")
	slug := liveSlug(t, publish(t, ts, "?slug=capped", "x"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // ends all held streams so the server drains at teardown
	cl := sseClient(t)

	var bodies []io.Closer
	defer func() {
		for _, b := range bodies {
			b.Close()
		}
	}()

	// Fill the per-slug cap.
	for i := 0; i < liveMaxPerSlug; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/_events/"+slug, nil)
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("subscriber %d: %v", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("subscriber %d status = %d, want 200", i, resp.StatusCode)
		}
		bodies = append(bodies, resp.Body)
	}

	// One past the cap must be rejected.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/_events/"+slug, nil)
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("over-cap request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("over-cap status = %d, want 503", resp.StatusCode)
	}
}

func TestSSEUnsubscribeFreesSlot(t *testing.T) {
	ts := newTestServer(t, "")
	slug := liveSlug(t, publish(t, ts, "?slug=freeing", "x"))

	cl := sseClient(t)

	// Fill the per-slug cap so a free slot is observable only after a disconnect.
	fillCtx, fillCancel := context.WithCancel(context.Background())
	defer fillCancel()
	for i := 0; i < liveMaxPerSlug-1; i++ {
		req, _ := http.NewRequestWithContext(fillCtx, http.MethodGet, ts.URL+"/_events/"+slug, nil)
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("filler %d: %v", i, err)
		}
		defer resp.Body.Close()
	}

	// Open the last subscriber (now at cap), then cancel it and confirm the slot
	// is reclaimed (a fresh subscriber succeeds where an at-cap one would 503).
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/_events/"+slug, nil)
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("open subscriber: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscriber at cap = %d, want 200", resp.StatusCode)
	}
	// Read the opening frame so the handler is fully engaged before we cancel.
	buf := make([]byte, 1)
	_, _ = resp.Body.Read(buf)
	cancel()
	resp.Body.Close()

	// Give the server a moment to run the deferred unsubscribe.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r2, _ := http.NewRequestWithContext(fillCtx, http.MethodGet, ts.URL+"/_events/"+slug, nil)
		resp2, err := cl.Do(r2)
		if err != nil {
			t.Fatalf("re-subscribe: %v", err)
		}
		code := resp2.StatusCode
		resp2.Body.Close()
		if code == http.StatusOK {
			return // slot reclaimed
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("subscriber slot was not reclaimed after client disconnect")
}

var nonceRe = regexp.MustCompile(`'nonce-([A-Za-z0-9+/=]+)'`)

func TestLiveWrapperInjectsNonceCSP(t *testing.T) {
	ts := newTestServer(t, "")
	url := publish(t, ts, "?slug=wrapme", "<html><body>real</body></html>")

	resp, body := get(t, url+"?live=1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("live view status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("live content-type = %q", ct)
	}

	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("live view missing Content-Security-Policy")
	}
	m := nonceRe.FindStringSubmatch(csp)
	if m == nil {
		t.Fatalf("CSP has no nonce: %q", csp)
	}
	nonce := m[1]

	page := string(body)
	// The script and style must carry the same nonce the CSP authorizes.
	if !strings.Contains(page, `script nonce="`+nonce+`"`) {
		t.Errorf("script tag missing CSP nonce %q\npage:\n%s", nonce, page)
	}
	if !strings.Contains(page, `style nonce="`+nonce+`"`) {
		t.Errorf("style tag missing CSP nonce %q", nonce)
	}
	// The wrapper must reference the SSE endpoint and iframe the real artifact.
	if !strings.Contains(page, "/_events/") {
		t.Errorf("wrapper does not open the SSE stream:\n%s", page)
	}
	if !strings.Contains(page, `<iframe src="/wrapme"`) {
		t.Errorf("wrapper does not iframe the artifact:\n%s", page)
	}
	// The wrapper must NOT inline the artifact's own bytes.
	if strings.Contains(page, "real") {
		t.Errorf("live wrapper leaked artifact bytes into the shell:\n%s", page)
	}
}

func TestLiveNonceIsPerResponse(t *testing.T) {
	ts := newTestServer(t, "")
	url := publish(t, ts, "?slug=noncy", "<html>x</html>")

	nonceOf := func() string {
		resp, _ := get(t, url+"?live")
		m := nonceRe.FindStringSubmatch(resp.Header.Get("Content-Security-Policy"))
		if m == nil {
			t.Fatal("no nonce in CSP")
		}
		return m[1]
	}
	if nonceOf() == nonceOf() {
		t.Error("CSP nonce is reused across responses; must be per-response")
	}
}

func TestNonLiveViewIsByteIdentical(t *testing.T) {
	ts := newTestServer(t, "")
	content := "<!DOCTYPE html><html><body>verbatim &amp; unmodified</body></html>"
	url := publish(t, ts, "?slug=verbatim", content)

	resp, body := get(t, url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if string(body) != content {
		t.Errorf("non-live body mutated:\n got %q\nwant %q", body, content)
	}
	// A non-live view must not carry the wrapper's CSP or any reload script —
	// only the baseline anti-clickjacking directive every response gets.
	if strings.Contains(string(body), "_events") {
		t.Errorf("non-live body contains reload wiring: %q", body)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); csp != "frame-ancestors 'self'" {
		t.Errorf("non-live HTML view CSP = %q, want the baseline frame-ancestors 'self' only", csp)
	}
}

func TestLiveOffValuesFallThrough(t *testing.T) {
	ts := newTestServer(t, "")
	content := "<html>plain</html>"
	url := publish(t, ts, "?slug=offcase", content)

	// ?live=false / 0 / off must serve the stored bytes, not the wrapper.
	for _, q := range []string{"?live=false", "?live=0", "?live=off"} {
		resp, body := get(t, url+q)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", q, resp.StatusCode)
		}
		if string(body) != content {
			t.Errorf("%s served a wrapper instead of the artifact: %q", q, body)
		}
	}
}

func TestLiveViewMissingSlugIs404(t *testing.T) {
	ts := newTestServer(t, "")
	resp, _ := get(t, ts.URL+"/does-not-exist?live=1")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("live view of missing slug = %d, want 404", resp.StatusCode)
	}
}

func TestSSEHeartbeatConfigUnderIdleDefault(t *testing.T) {
	// A regression guard on the invariant that the heartbeat stays below the
	// server's default idle timeout so an active stream never looks idle.
	const defaultIdleTimeout = 120 * time.Second
	if liveHeartbeat >= defaultIdleTimeout {
		t.Fatalf("liveHeartbeat %s must stay below the default idle timeout %s",
			liveHeartbeat, defaultIdleTimeout)
	}
}

func TestLiveHubNotifyIsNonBlocking(t *testing.T) {
	// Notify must never block on a full/absent consumer: a subscriber that never
	// drains its channel still leaves Notify returning promptly.
	h := newLiveHub()
	ch, ok := h.subscribe("s")
	if !ok {
		t.Fatal("subscribe failed")
	}
	_ = ch // deliberately never drained

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			h.Notify("s")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocked on a non-draining subscriber")
	}
	h.unsubscribe("s", ch)
}

func TestLiveHubConcurrentSubscribeNotify(t *testing.T) {
	// Race-detector exercise: concurrent subscribe/unsubscribe/notify.
	h := newLiveHub()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				ch, ok := h.subscribe("hot")
				if !ok {
					continue
				}
				h.Notify("hot")
				h.unsubscribe("hot", ch)
			}
		}()
	}
	wg.Wait()
	if h.total != 0 {
		t.Fatalf("hub leaked %d subscribers", h.total)
	}
}

func TestSSERouteMountsWithoutConflict(t *testing.T) {
	// The SSE route registers on the content mux (split) AND the combined mux
	// (--unsafe-same-origin). Go 1.22 ServeMux panics on a conflicting pattern
	// at registration, so simply building each handler is the guard.
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv := New(st, Config{})
	_ = srv.ContentHandler() // split content origin
	_ = srv.Handler()        // combined legacy origin
	// "_events" must not be a claimable named slug (regex + reservation).
	if err := store.ValidateNamedSlug("_events"); err == nil {
		t.Error("_events should not be a valid artifact slug")
	}
}
