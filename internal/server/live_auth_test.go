// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/store"
)

// sseStatus opens /_events/{slug} with optional Basic credentials and returns
// the status; a 200 stream is held open until test cleanup.
func sseStatus(t *testing.T, base, slug, pass string) int {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/_events/"+slug, nil)
	if pass != "" {
		req.SetBasicAuth("", pass)
	}
	resp, err := sseClient(t).Do(req)
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	} else {
		t.Cleanup(func() { resp.Body.Close() })
	}
	return resp.StatusCode
}

// openSSEAuth is openSSE with Basic credentials.
func openSSEAuth(t *testing.T, base, slug, pass string) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/_events/"+slug, nil)
	req.SetBasicAuth("", pass)
	resp, err := sseClient(t).Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("open SSE: %v %v", err, resp)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		defer resp.Body.Close()
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			for _, l := range strings.SplitAfter(string(buf[:n]), "\n") {
				if l != "" {
					lines <- l
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return lines
}

// waitClosed waits for the stream to end without carrying an event.
func waitClosed(t *testing.T, lines <-chan string, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return
			}
			if strings.HasPrefix(l, "event:") {
				t.Fatalf("stream delivered %q after its authorization lapsed", l)
			}
		case <-deadline:
			t.Fatalf("stream still open after %s", within)
		}
	}
}

// TestSSEAuthorizedAgainstArtifact: a stream is a view of its artifact. A slug
// that does not exist (yet), an expired artifact, and a password-protected one
// without the password are all refused, and none of them takes a slot.
func TestSSEAuthorizedAgainstArtifact(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)

	if _, err := st.Put(store.PutOptions{Slug: "brief", TTL: time.Millisecond}, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := st.Put(store.PutOptions{Slug: "locked", Password: "pw-1"}, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(store.PutOptions{Slug: "open"}, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	priv, err := st.Put(store.PutOptions{Private: true}, strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, slug, pass string
		want             int
	}{
		{"unknown slug", "not-published-yet", "", http.StatusNotFound},
		{"expired", "brief", "", http.StatusNotFound},
		{"password missing", "locked", "", http.StatusUnauthorized},
		{"password wrong", "locked", "nope", http.StatusUnauthorized},
		{"password right", "locked", "pw-1", http.StatusOK},
		{"public", "open", "", http.StatusOK},
		{"private by capability", priv.Slug, "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sseStatus(t, ts.URL, tc.slug, tc.pass); got != tc.want {
				t.Errorf("status %d, want %d", got, tc.want)
			}
		})
	}
}

// TestSSEGrantRecheckedOnRepublish: a republish that keeps the password (and
// so re-salts its hash) still reaches the stream; one that changes the
// password ends the stream without an event.
func TestSSEGrantRecheckedOnRepublish(t *testing.T) {
	ts := newTestServer(t, "")
	publishPassword(t, ts, "?slug=guarded", "pw-a", "v1")

	lines := openSSEAuth(t, ts.URL, "guarded", "pw-a")
	go func() {
		time.Sleep(20 * time.Millisecond)
		publishPassword(t, ts, "?slug=guarded", "pw-a", "v2")
	}()
	waitReloadEvent(t, lines, 3*time.Second)

	publishPassword(t, ts, "?slug=guarded", "pw-b", "v3")
	waitClosed(t, lines, 3*time.Second)
}

// TestSSEGrantRecheckedOnHeartbeat: deleting the artifact ends an open stream
// at the next heartbeat, with no republish to trigger it.
func TestSSEGrantRecheckedOnHeartbeat(t *testing.T) {
	saved := liveHeartbeat
	liveHeartbeat = 50 * time.Millisecond
	ts := newTestServer(t, "")
	liveHeartbeat = saved
	publishPassword(t, ts, "?slug=doomed", "pw", "v1")

	lines := openSSEAuth(t, ts.URL, "doomed", "pw")
	if code, _ := do(t, http.MethodDelete, ts.URL+"/doomed", "", ""); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	waitClosed(t, lines, 3*time.Second)
}

// TestSSEPerClientCap: one client cannot hold more than liveMaxPerClient
// streams, even spread across distinct slugs; another client still gets in.
func TestSSEPerClientCap(t *testing.T) {
	ts := newTestServer(t, "")
	for i := 0; i <= liveMaxPerClient; i++ {
		publish(t, ts, fmt.Sprintf("?slug=s%d", i), "x")
	}
	for i := 0; i < liveMaxPerClient; i++ {
		if got := sseStatus(t, ts.URL, fmt.Sprintf("s%d", i), ""); got != http.StatusOK {
			t.Fatalf("stream %d = %d, want 200", i, got)
		}
	}
	if got := sseStatus(t, ts.URL, fmt.Sprintf("s%d", liveMaxPerClient), ""); got != http.StatusServiceUnavailable {
		t.Errorf("stream past the per-client cap = %d, want 503", got)
	}

	h := newLiveHub(LiveLimits{})
	for i := 0; i < liveMaxPerClient; i++ {
		if _, ok := h.subscribe(fmt.Sprintf("x%d", i), "client-a"); !ok {
			t.Fatalf("client-a subscription %d refused", i)
		}
	}
	if _, ok := h.subscribe("y", "client-a"); ok {
		t.Error("client-a admitted past its cap")
	}
	if _, ok := h.subscribe("y", "client-b"); !ok {
		t.Error("client-b refused while client-a is at its cap")
	}
}

// TestLiveClientKey groups IPv6 peers by /64 and keeps IPv4 peers distinct.
func TestLiveClientKey(t *testing.T) {
	key := func(addr string) string {
		return liveClientKey(&http.Request{RemoteAddr: addr})
	}
	if key("[2001:db8:1:2::1]:5000") != key("[2001:db8:1:2:ffff::9]:6000") {
		t.Error("two addresses in one IPv6 /64 got different keys")
	}
	if key("[2001:db8:1:2::1]:5000") == key("[2001:db8:1:3::1]:5000") {
		t.Error("two IPv6 /64s share a key")
	}
	if key("192.0.2.1:1") == key("192.0.2.2:1") {
		t.Error("two IPv4 addresses share a key")
	}
}

// TestSSEPasswordChallengeUsesArtifactRealm: a refused stream challenges with
// the artifact's own realm, so a browser that holds that realm's credentials
// answers from its cache instead of failing for good.
func TestSSEPasswordChallengeUsesArtifactRealm(t *testing.T) {
	ts := newTestServer(t, "")
	publishPassword(t, ts, "?slug=realmed", "pw", "x")
	resp := getWith(t, ts.URL+"/_events/realmed", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
	if got, want := resp.Header.Get("WWW-Authenticate"), passwordChallenge("realmed"); got != want {
		t.Errorf("challenge %q, want %q", got, want)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("Cache-Control %q, want private, no-store", cc)
	}
}

// TestLiveWrapperTakesArtifactPassword: the ?live wrapper of a
// password-protected artifact needs the same password, so the viewer
// authenticates once, at the top level.
func TestLiveWrapperTakesArtifactPassword(t *testing.T) {
	ts := newTestServer(t, "")
	url := publishPassword(t, ts, "?slug=wrapped", "pw", "<html>x</html>")
	resp := getWith(t, url+"?live", nil)
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("wrapper without password: status %d, challenge %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	req, _ := http.NewRequest(http.MethodGet, url+"?live", nil)
	req.SetBasicAuth("", "pw")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK || !strings.Contains(string(body), "/_events/") {
		t.Errorf("wrapper with password: status %d, wrapper served=%v", r2.StatusCode, strings.Contains(string(body), "/_events/"))
	}
}

// TestSSERecheckGoesThroughPasswordGate: when a republish re-salts the hash,
// open streams re-verify through the view-password gate (its KDF seam, its
// verified-credential cache), not a direct hash comparison. Two streams
// sharing a password (the per-client cap on one slug) cost one KDF run for
// the republish, not two, and both keep receiving events.
func TestSSERecheckGoesThroughPasswordGate(t *testing.T) {
	ts := newTestServer(t, "")
	publishPassword(t, ts, "?slug=gated", "pw-g", "v1")
	calls, _ := countKDF(t, 0)

	const streams = liveMaxPerClientSlug
	var all []<-chan string
	for i := 0; i < streams; i++ {
		all = append(all, openSSEAuth(t, ts.URL, "gated", "pw-g"))
	}
	opened := calls.Load()
	if opened != 1 {
		t.Fatalf("opening %d streams ran the KDF %d times, want 1 (then remembered)", streams, opened)
	}

	publishPassword(t, ts, "?slug=gated", "pw-g", "v2")
	for _, lines := range all {
		waitReloadEvent(t, lines, 3*time.Second)
	}
	if got := calls.Load() - opened; got != 1 {
		t.Fatalf("re-checking %d streams after a republish ran the gate's KDF %d times, want 1", streams, got)
	}
}
