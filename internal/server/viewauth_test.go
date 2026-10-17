// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"fmt"
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

// TestPasswordOKSharesTheServersLimits: the plain passwordOK helper (used by
// routes other than GET /{slug}) draws on the same buckets as CheckView.
func TestPasswordOKSharesTheServersLimits(t *testing.T) {
	calls, _ := countKDF(t, 0)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	art, err := st.Put(store.PutOptions{Slug: "locked", Password: "letmein"}, strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, Config{Password: PasswordLimits{
		PerTarget: limit.Rate{Every: time.Hour, Burst: 3},
		PerClient: limit.Rate{Every: -1},
		Global:    limit.Rate{Every: -1},
	}})
	var ok int
	h := s.withRequestLimits(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if passwordOK(r, art.PasswordHash) {
			ok++
		}
	}))
	for i := 0; i < 10; i++ {
		serve(h, basicReq("locked", "wrong", "192.0.2.1:1"))
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("passwordOK ran the KDF %d times for 10 wrong attempts, want 3 (the target burst)", got)
	}
	// The same budget is spent for GET /{slug}.
	if w := serve(s.Handler(), basicReq("locked", "wrong", "192.0.2.1:1")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("GET after passwordOK spent the budget = %d, want 429", w.Code)
	}
	if ok != 0 {
		t.Fatal("a wrong password passed")
	}
}

// TestTargetFailuresDoNotLockOutAnotherViewer: a flood of wrong guesses
// against one artifact throttles the guessing client, but a viewer from
// another address with the correct password is still verified and served.
func TestTargetFailuresDoNotLockOutAnotherViewer(t *testing.T) {
	countKDF(t, 0)
	h := gateServer(t, Config{Password: PasswordLimits{
		PerTarget: limit.Rate{Every: time.Hour, Burst: 5},
		Global:    limit.Rate{Every: time.Hour, Burst: 5},
	}})
	throttled := 0
	for i := 0; i < 40; i++ {
		if serve(h, basicReq("locked", "wrong", "192.0.2.1:1")).Code == http.StatusTooManyRequests {
			throttled++
		}
	}
	if throttled == 0 {
		t.Fatal("the guessing client was never throttled")
	}
	if w := serve(h, basicReq("locked", "letmein", "198.51.100.7:1")); w.Code != http.StatusOK {
		t.Fatalf("correct password from another address after the flood = %d, want 200", w.Code)
	}
	// The guessing client's own wrong guesses stay throttled.
	if w := serve(h, basicReq("locked", "wrong", "192.0.2.1:1")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("guessing client after the flood = %d, want 429", w.Code)
	}
}

// proxiedGateServer serves three password-gated artifacts behind a proxy at
// 10.0.0.1, trusted or not.
func proxiedGateServer(t *testing.T, trusted bool) http.Handler {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, slug := range []string{"one", "two", "three"} {
		if _, err := st.Put(store.PutOptions{Slug: slug, Password: "letmein"}, strings.NewReader(slug)); err != nil {
			t.Fatal(err)
		}
	}
	var proxies limit.Proxies
	if trusted {
		proxies, _ = limit.ParseProxies("10.0.0.1")
	}
	return New(st, Config{TrustedProxies: proxies, Password: PasswordLimits{
		PerClient: limit.Rate{Every: time.Hour, Burst: 4},
	}}).Handler()
}

func proxiedReq(slug, pass, client string) *http.Request {
	r := basicReq(slug, pass, "10.0.0.1:1")
	r.Header.Set("X-Forwarded-For", client)
	return r
}

// TestProxiedViewerNotLockedOutByAnotherUser: behind a trusted proxy, a user
// whose failures across two artifacts drain their own client budget does not
// lock another user out of a third. Without --trusted-proxy every user is the
// proxy, so the drained budget is shared and the bound still holds.
func TestProxiedViewerNotLockedOutByAnotherUser(t *testing.T) {
	countKDF(t, 0)
	drain := func(h http.Handler) {
		for i := 0; i < 10; i++ {
			serve(h, proxiedReq([]string{"one", "two"}[i%2], "wrong", "203.0.113.1"))
		}
	}
	h := proxiedGateServer(t, true)
	drain(h)
	if w := serve(h, proxiedReq("one", "wrong", "203.0.113.1")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("draining user = %d, want 429", w.Code)
	}
	if w := serve(h, proxiedReq("three", "letmein", "203.0.113.2")); w.Code != http.StatusOK {
		t.Fatalf("another user behind the trusted proxy = %d, want 200", w.Code)
	}

	h = proxiedGateServer(t, false)
	drain(h)
	if w := serve(h, proxiedReq("three", "letmein", "203.0.113.2")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("untrusted proxy: shared client budget = %d, want 429 (the proxy is one client)", w.Code)
	}
}

// TestActiveCredentialOutlivesTheCacheTTL: a verified credential that keeps
// being used stays verified past verifiedTTL, even while other clients fail
// against the same artifact; one left unused for verifiedTTL is hashed again.
func TestActiveCredentialOutlivesTheCacheTTL(t *testing.T) {
	calls, _ := countKDF(t, 0)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Put(store.PutOptions{Slug: "locked", Password: "letmein"}, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	s := New(st, Config{Password: PasswordLimits{
		PerTarget: limit.Rate{Every: time.Hour, Burst: 3},
	}})
	now := time.Now()
	s.views.now = func() time.Time { return now }
	h := s.Handler()
	ok := func() {
		t.Helper()
		if w := serve(h, basicReq("locked", "letmein", "192.0.2.1:1")); w.Code != http.StatusOK {
			t.Fatalf("viewer = %d, want 200", w.Code)
		}
	}
	ok()
	for step := 0; step < 4; step++ {
		now = now.Add(verifiedTTL * 3 / 4)
		for i := 0; i < 5; i++ {
			serve(h, basicReq("locked", "wrong", fmt.Sprintf("198.51.100.%d:1", 10+step*5+i)))
		}
		ok()
	}
	before := calls.Load()
	now = now.Add(verifiedTTL + time.Second)
	ok()
	if calls.Load() != before+1 {
		t.Fatal("a credential unused for verifiedTTL was not hashed again")
	}
	// Only the first use and the one after the idle gap hashed the correct
	// password; the rest were the wrong guesses.
	if got := calls.Load() - 20; got != 2 {
		t.Fatalf("correct password hashed %d times, want 2", got)
	}
}

// TestClientGuessingIsBoundedPerArtifact: one client's wrong guesses on one
// artifact are bounded by its own per-artifact budget, which does not touch
// its budget for another artifact.
func TestClientGuessingIsBoundedPerArtifact(t *testing.T) {
	calls, _ := countKDF(t, 0)
	h := gateServer(t, Config{Password: PasswordLimits{
		PerClientTarget: limit.Rate{Every: time.Hour, Burst: 3},
		PerClient:       limit.Rate{Every: -1},
		PerTarget:       limit.Rate{Every: -1},
		Global:          limit.Rate{Every: -1},
	}})
	unauth, throttled := 0, 0
	for i := 0; i < 10; i++ {
		switch serve(h, basicReq("locked", "wrong", "192.0.2.1:1")).Code {
		case http.StatusUnauthorized:
			unauth++
		case http.StatusTooManyRequests:
			throttled++
		}
	}
	if unauth != 3 || throttled != 7 || calls.Load() != 3 {
		t.Fatalf("%d unauthorized, %d throttled, %d KDF runs; want 3, 7, 3", unauth, throttled, calls.Load())
	}
	if w := serve(h, basicReq("locked-two", "wrong", "192.0.2.1:1")); w.Code != http.StatusUnauthorized {
		t.Fatalf("same client, other artifact = %d, want 401", w.Code)
	}
	if w := serve(h, basicReq("locked", "letmein", "192.0.2.2:1")); w.Code != http.StatusOK {
		t.Fatalf("other client, correct password = %d, want 200", w.Code)
	}
}

// TestConcurrentCorrectAttemptsAreNotRecentFailures: with a shared failure
// budget spent by others, a fresh client's concurrent correct, uncached
// requests all succeed; one request's in-flight reservation is not a failure
// that refuses the next. Covers the per-artifact budget (one artifact) and
// the instance budget (two artifacts).
func TestConcurrentCorrectAttemptsAreNotRecentFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits PasswordLimits
		slugs  []string
	}{
		{"per-artifact budget", PasswordLimits{
			Concurrency: 4,
			PerTarget:   limit.Rate{Every: time.Hour, Burst: 3},
			Global:      limit.Rate{Every: -1},
		}, []string{"locked", "locked", "locked", "locked"}},
		{"instance budget", PasswordLimits{
			Concurrency: 4,
			PerTarget:   limit.Rate{Every: -1},
			Global:      limit.Rate{Every: time.Hour, Burst: 3},
		}, []string{"locked", "locked-two", "locked", "locked-two"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			countKDF(t, 100*time.Millisecond)
			h := gateServer(t, Config{Password: tc.limits})
			for i := 0; i < 6; i++ {
				serve(h, basicReq("locked", "wrong", fmt.Sprintf("198.51.100.%d:1", 10+i)))
			}
			if w := serve(h, basicReq("locked", "wrong", "198.51.100.10:1")); w.Code != http.StatusTooManyRequests {
				t.Fatalf("shared budget not spent: a recent failer got %d, want 429", w.Code)
			}
			codes := make([]int, len(tc.slugs))
			var wg sync.WaitGroup
			for i, slug := range tc.slugs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					codes[i] = serve(h, basicReq(slug, "letmein", "192.0.2.50:1")).Code
				}()
			}
			wg.Wait()
			for i, c := range codes {
				if c != http.StatusOK {
					t.Errorf("concurrent correct request %d (%s) = %d, want 200", i, tc.slugs[i], c)
				}
			}
		})
	}
}
