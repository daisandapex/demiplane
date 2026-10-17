// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/limit"
	"github.com/daisandapex/demiplane/internal/store"
)

// gateServer opens a store with one password-gated and one public artifact
// and returns the combined handler.
func gateServer(t *testing.T, cfg Config) http.Handler {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Put(store.PutOptions{Slug: "locked", Password: "letmein"}, strings.NewReader("secret bytes")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(store.PutOptions{Slug: "locked-two", Password: "letmein"}, strings.NewReader("other secret")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(store.PutOptions{Slug: "open"}, strings.NewReader("public bytes")); err != nil {
		t.Fatal(err)
	}
	return New(st, cfg).Handler()
}

func basicReq(slug, pass, remote string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/"+slug, nil)
	if pass != "" {
		r.SetBasicAuth("u", pass)
	}
	r.RemoteAddr = remote
	return r
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// countKDF replaces the KDF with a counting wrapper for one test.
func countKDF(t *testing.T, delay time.Duration) (calls, peak *atomic.Int64) {
	t.Helper()
	calls, peak = new(atomic.Int64), new(atomic.Int64)
	var active atomic.Int64
	prev := verifyPassword
	verifyPassword = func(hash, pw string) bool {
		calls.Add(1)
		n := active.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		defer active.Add(-1)
		return prev(hash, pw)
	}
	t.Cleanup(func() { verifyPassword = prev })
	return calls, peak
}

func TestWrongPasswordFloodIsThrottledPerTarget(t *testing.T) {
	calls, _ := countKDF(t, 0)
	h := gateServer(t, Config{Password: PasswordLimits{
		PerTarget: limit.Rate{Every: time.Hour, Burst: 5},
		PerClient: limit.Rate{Every: -1},
		Global:    limit.Rate{Every: -1},
	}})

	var throttled int
	for i := 0; i < 40; i++ {
		w := serve(h, basicReq("locked", "wrong", "192.0.2.1:1000"))
		switch w.Code {
		case http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			throttled++
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
		default:
			t.Fatalf("attempt %d: status %d", i, w.Code)
		}
	}
	if throttled != 35 {
		t.Fatalf("throttled %d of 40 attempts, want 35 (burst 5)", throttled)
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("KDF ran %d times, want 5: throttled attempts must not hash", got)
	}
	// Another target has its own budget.
	if w := serve(h, basicReq("locked-two", "wrong", "192.0.2.1:1000")); w.Code != http.StatusUnauthorized {
		t.Fatalf("other target = %d, want 401", w.Code)
	}
	// No credentials at all is a plain challenge and costs nothing.
	if w := serve(h, basicReq("locked", "", "192.0.2.1:1000")); w.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials = %d, want 401", w.Code)
	}
}

func TestWrongPasswordFloodIsThrottledPerClientAndGlobally(t *testing.T) {
	countKDF(t, 0)
	h := gateServer(t, Config{Password: PasswordLimits{
		PerTarget: limit.Rate{Every: -1},
		PerClient: limit.Rate{Every: time.Hour, Burst: 3},
		Global:    limit.Rate{Every: time.Hour, Burst: 5},
	}})
	codes := func(remote string, n int) (unauth, throttled int) {
		for i := 0; i < n; i++ {
			switch serve(h, basicReq("locked", "wrong", remote)).Code {
			case http.StatusUnauthorized:
				unauth++
			case http.StatusTooManyRequests:
				throttled++
			}
		}
		return
	}
	if u, th := codes("192.0.2.1:1", 6); u != 3 || th != 3 {
		t.Fatalf("client A: %d unauthorized, %d throttled; want 3 and 3", u, th)
	}
	// A second client has its own bucket but shares the global one (2 left).
	if u, th := codes("192.0.2.2:1", 6); u != 2 || th != 4 {
		t.Fatalf("client B: %d unauthorized, %d throttled; want 2 and 4", u, th)
	}
}

func TestCorrectPasswordIsRefundedAndRemembered(t *testing.T) {
	calls, _ := countKDF(t, 0)
	h := gateServer(t, Config{Password: PasswordLimits{
		PerTarget: limit.Rate{Every: time.Hour, Burst: 2},
		PerClient: limit.Rate{Every: -1},
		Global:    limit.Rate{Every: -1},
	}})
	for i := 0; i < 20; i++ {
		w := serve(h, basicReq("locked", "letmein", "192.0.2.1:1"))
		if w.Code != http.StatusOK || w.Body.String() != "secret bytes" {
			t.Fatalf("correct password attempt %d: %d %q", i, w.Code, w.Body.String())
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("KDF ran %d times for one repeated correct password, want 1", got)
	}
	// The correct attempts spent nothing, so the full burst of failures remains.
	for i := 0; i < 2; i++ {
		if w := serve(h, basicReq("locked", "wrong", "192.0.2.1:1")); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d inside the burst = %d, want 401", i, w.Code)
		}
	}
	if w := serve(h, basicReq("locked", "wrong", "192.0.2.1:1")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("failure past the burst = %d, want 429", w.Code)
	}
	// A remembered credential still works while the target is throttled.
	if w := serve(h, basicReq("locked", "letmein", "192.0.2.1:1")); w.Code != http.StatusOK {
		t.Fatalf("remembered credential while throttled = %d, want 200", w.Code)
	}
}

func TestRememberedCredentialDoesNotOutliveAPasswordChange(t *testing.T) {
	countKDF(t, 0)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	put := func(pw string) {
		if _, err := st.Put(store.PutOptions{Slug: "locked", Password: pw}, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	put("old")
	h := New(st, Config{}).Handler()
	if w := serve(h, basicReq("locked", "old", "192.0.2.1:1")); w.Code != http.StatusOK {
		t.Fatalf("old password = %d", w.Code)
	}
	put("new")
	if w := serve(h, basicReq("locked", "old", "192.0.2.1:1")); w.Code != http.StatusUnauthorized {
		t.Fatalf("old password after change = %d, want 401", w.Code)
	}
}

func TestKDFConcurrencyIsBoundedAndUnrelatedRequestsServe(t *testing.T) {
	_, peak := countKDF(t, 50*time.Millisecond)
	h := gateServer(t, Config{Password: PasswordLimits{
		Concurrency: 2,
		PerTarget:   limit.Rate{Every: -1},
		PerClient:   limit.Rate{Every: -1},
		Global:      limit.Rate{Every: -1},
	}})

	const flood = 40
	var wg sync.WaitGroup
	var busy atomic.Int64
	start := make(chan struct{})
	for i := 0; i < flood; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if serve(h, basicReq("locked", "wrong", "192.0.2.9:1")).Code == http.StatusServiceUnavailable {
				busy.Add(1)
			}
		}()
	}
	close(start)
	// While the flood holds every KDF slot, an unrelated artifact serves.
	time.Sleep(20 * time.Millisecond)
	t0 := time.Now()
	w := serve(h, basicReq("open", "", "192.0.2.10:1"))
	if w.Code != http.StatusOK || w.Body.String() != "public bytes" {
		t.Fatalf("unrelated GET during flood: %d %q", w.Code, w.Body.String())
	}
	if el := time.Since(t0); el > 500*time.Millisecond {
		t.Fatalf("unrelated GET took %v during the flood", el)
	}
	wg.Wait()
	if p := peak.Load(); p > 2 {
		t.Fatalf("%d KDF runs at once, limit 2", p)
	}
	if p := peak.Load(); p < 2 {
		t.Fatalf("peak KDF concurrency %d: the flood never filled the gate, so the bound was not exercised", p)
	}
}

func TestKDFWaitHonorsCancellation(t *testing.T) {
	countKDF(t, 0)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Put(store.PutOptions{Slug: "locked", Password: "letmein"}, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	s := New(st, Config{Password: PasswordLimits{Concurrency: 1}})
	// Hold the only KDF slot.
	if !s.views.kdf.TryAcquire() {
		t.Fatal("could not take the KDF slot")
	}
	defer s.views.kdf.Release()
	r := basicReq("locked", "letmein", "192.0.2.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	w := serve(s.Handler(), r.WithContext(ctx))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status with the gate full = %d, want 503", w.Code)
	}
	if el := time.Since(t0); el > 2*time.Second {
		t.Fatalf("waited %v despite a cancelled request", el)
	}
}
