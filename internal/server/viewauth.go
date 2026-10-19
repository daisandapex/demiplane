// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/limit"
	"github.com/daisandapex/demiplane/v3/internal/module"
	"github.com/daisandapex/demiplane/v3/internal/store"
)

// View-password checks run PBKDF2 at the stored work factor (600,000
// iterations by default), which is the defence an offline attacker faces and
// is kept. That cost makes every wrong guess a CPU charge any viewer can levy,
// so the gate in front of the KDF does three things:
//
//  1. Verified credentials are remembered (keyed by an HMAC of the stored
//     hash and the password under a per-process key) until they go unused
//     for verifiedTTL, so a browser re-sending Basic credentials on every
//     page load costs one KDF, not one per request, and an active viewer
//     never has to win a check during a flood. A new password changes the
//     hash and so the key.
//  2. Only failures spend rate budget. Each client has a budget of its own
//     and one per artifact; a KDF run reserves a token from both, refunded
//     when the password is correct, and a client whose budget is empty gets
//     429 without hashing. The artifact and the instance have failure
//     budgets too, charged by every client's failures. When one is empty it
//     refuses, without hashing, only clients that failed recently (on that
//     artifact, or anywhere for the instance budget). Only completed
//     failures count: a client's own attempt still in flight does not. A
//     client with no recent failures is always verified, so nobody else's
//     guesses can lock a legitimate viewer out. The cost is that guessing spread over many
//     addresses, each new, is bounded by the KDF itself (3. below and the
//     work factor), not by a rate.
//  3. At most PasswordConcurrency KDF runs execute at once; the rest wait
//     briefly and then get 503, so a flood cannot take every core from
//     unrelated requests.

// Defaults for the password gate. The per-target rate is the online-guessing
// bound for one artifact; the global rate bounds total KDF work.
var (
	defaultPasswordClientTargetRate = limit.PerMinute(10, 10)
	defaultPasswordClientRate       = limit.PerMinute(20, 10)
	defaultPasswordTargetRate       = limit.PerMinute(10, 10)
	defaultPasswordGlobalRate       = limit.PerMinute(120, 30)
)

const (
	// passwordKDFWait is how long a check waits for a free KDF slot.
	passwordKDFWait = 5 * time.Second
	// verifiedTTL is how long a verified credential skips the KDF after its
	// last use.
	verifiedTTL = 10 * time.Minute
	// verifiedMax bounds the verified-credential cache.
	verifiedMax = 4096
)

// defaultPasswordConcurrency leaves at least half the cores to everything else.
func defaultPasswordConcurrency() int {
	n := runtime.GOMAXPROCS(0) / 2
	if n < 1 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	return n
}

// PasswordLimits configures the view-password gate. Zero fields take the
// defaults above; a Rate with Every < 0 disables that bucket.
type PasswordLimits struct {
	Concurrency     int
	PerClientTarget limit.Rate // one client's failures on one artifact
	PerClient       limit.Rate // one client's failures on any artifact
	PerTarget       limit.Rate // every client's failures on one artifact
	Global          limit.Rate // every client's failures on any artifact
}

// verifyPassword is the KDF check. A variable so tests can observe how many
// run at once.
var verifyPassword = store.PasswordMatches

type viewGate struct {
	kdf          limit.Gate
	clientTarget *limit.Keyed
	target       *limit.Keyed
	client       *limit.Keyed
	global       *limit.Keyed
	// failedClient and failedClientTarget record completed failures only, at
	// the same rates as client and clientTarget, which also hold in-flight
	// reservations. A key whose bucket has not refilled failed recently.
	failedClient       *limit.Keyed
	failedClientTarget *limit.Keyed

	key []byte
	mu  sync.Mutex
	ok  map[[32]byte]time.Time
	// inflight counts each client's attempts waiting for or holding a KDF
	// slot (passwordClientInflight).
	inflight map[string]int
	// flights are the verifications in progress, by client and credential.
	flights map[string]*kdfFlight
	now     func() time.Time // time source; a field so tests can move it
}

// passwordClientInflight is how many password checks one client may have
// queued for or running in the KDF at once. A queued attempt that times out
// is refunded, so without this cap a few clients could keep the queue full
// at no cost and every other viewer's first check would time out behind
// them; past it an attempt is answered busy without queueing.
const passwordClientInflight = 2

// enterKDF counts an attempt by client toward its in-flight cap, reporting
// false (and counting nothing) when the client is at the cap.
func (g *viewGate) enterKDF(client string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inflight[client] >= passwordClientInflight {
		return false
	}
	if g.inflight == nil {
		g.inflight = map[string]int{}
	}
	g.inflight[client]++
	return true
}

func (g *viewGate) leaveKDF(client string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inflight[client]--; g.inflight[client] <= 0 {
		delete(g.inflight, client)
	}
}

// inflightFor reports client's attempts in flight (tests).
func (g *viewGate) inflightFor(client string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inflight[client]
}

func pickRate(r, def limit.Rate) limit.Rate {
	switch {
	case r.Every < 0:
		return limit.Rate{}
	case r.Unlimited():
		return def
	}
	return r
}

func newViewGate(cfg PasswordLimits, key []byte) *viewGate {
	n := cfg.Concurrency
	if n <= 0 {
		n = defaultPasswordConcurrency()
	}
	clientTargetRate := pickRate(cfg.PerClientTarget, defaultPasswordClientTargetRate)
	clientRate := pickRate(cfg.PerClient, defaultPasswordClientRate)
	return &viewGate{
		kdf:                limit.NewGate(n),
		clientTarget:       limit.NewKeyed(clientTargetRate, 0),
		target:             limit.NewKeyed(pickRate(cfg.PerTarget, defaultPasswordTargetRate), 0),
		client:             limit.NewKeyed(clientRate, 0),
		global:             limit.NewKeyed(pickRate(cfg.Global, defaultPasswordGlobalRate), 1),
		failedClient:       limit.NewKeyed(clientRate, 0),
		failedClientTarget: limit.NewKeyed(clientTargetRate, 0),
		key:                key,
		ok:                 map[[32]byte]time.Time{},
		now:                time.Now,
	}
}

func (g *viewGate) credKey(hash, pass string) [32]byte {
	mac := hmac.New(sha256.New, g.key)
	mac.Write([]byte(hash))
	mac.Write([]byte{0})
	mac.Write([]byte(pass))
	var k [32]byte
	copy(k[:], mac.Sum(nil))
	return k
}

func (g *viewGate) remembered(k [32]byte, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	exp, ok := g.ok[k]
	if !ok {
		return false
	}
	if now.After(exp) {
		delete(g.ok, k)
		return false
	}
	g.ok[k] = now.Add(verifiedTTL) // sliding: each use extends it
	return true
}

func (g *viewGate) remember(k [32]byte, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.ok) >= verifiedMax {
		for ck, exp := range g.ok {
			if now.After(exp) {
				delete(g.ok, ck)
			}
		}
		for ck := range g.ok {
			if len(g.ok) < verifiedMax {
				break
			}
			delete(g.ok, ck)
		}
	}
	g.ok[k] = now.Add(verifiedTTL)
}

// check verifies the request's Basic password against the stored hash. The
// result says whether to serve, ask for a password, or push back; wait is the
// Retry-After hint for the push-back results. Artifact budgets are keyed by
// the hash, which every publish of an artifact replaces.
func (g *viewGate) check(ctx context.Context, r *http.Request, hash string) (res module.ViewAccess, wait time.Duration) {
	if hash == "" {
		return module.ViewOK, 0
	}
	_, pass, ok := r.BasicAuth()
	if !ok {
		return module.ViewNeedPassword, 0
	}
	now := g.now()
	ck := g.credKey(hash, pass)
	if g.remembered(ck, now) {
		return module.ViewOK, 0
	}
	client := limit.ClientKey(r)
	// Concurrent checks of one credential by one client (a browser fetching
	// a page's resources in parallel before the password is remembered)
	// share one verification: one KDF run, one queue place, one result.
	key := client + "\x00" + string(ck[:])
	g.mu.Lock()
	if f, ok := g.flights[key]; ok {
		g.mu.Unlock()
		select {
		case <-f.done:
			return f.res, f.wait
		case <-ctx.Done():
			return module.ViewBusy, time.Second
		}
	}
	f := &kdfFlight{done: make(chan struct{})}
	if g.flights == nil {
		g.flights = map[string]*kdfFlight{}
	}
	g.flights[key] = f
	g.mu.Unlock()
	defer func() {
		f.res, f.wait = res, wait
		g.mu.Lock()
		delete(g.flights, key)
		g.mu.Unlock()
		close(f.done)
	}()
	return g.verify(ctx, hash, pass, ck, client, now)
}

// kdfFlight is one client's verification of one credential in progress;
// identical checks wait for its result (check).
type kdfFlight struct {
	done chan struct{}
	res  module.ViewAccess
	wait time.Duration
}

// verify is check's verification, budgets and KDF, for one flight.
func (g *viewGate) verify(ctx context.Context, hash, pass string, ck [32]byte, client string, now time.Time) (module.ViewAccess, time.Duration) {
	clientTarget := client + "\x00" + hash
	// Shared failure budgets refuse only clients with recent completed
	// failures; an attempt still in flight is not one.
	if empty, w := g.target.Empty(hash); empty && g.failedClientTarget.Spent(clientTarget) {
		return module.ViewThrottled, w
	}
	if empty, w := g.global.Empty(""); empty && (g.failedClient.Spent(client) || g.failedClientTarget.Spent(clientTarget)) {
		return module.ViewThrottled, w
	}
	// The client's own budgets are reserved before hashing, so its concurrent
	// attempts cannot overshoot them, and refunded if the password is right.
	own := []limit.Charge{
		{L: g.client, Key: client},
		{L: g.clientTarget, Key: clientTarget},
	}
	if allowed, w := limit.AllowAll(own...); !allowed {
		return module.ViewThrottled, w
	}
	if !g.enterKDF(client) {
		limit.RefundAll(own...)
		return module.ViewBusy, time.Second
	}
	if !g.kdf.Acquire(ctx, passwordKDFWait) {
		g.leaveKDF(client)
		limit.RefundAll(own...)
		return module.ViewBusy, time.Second
	}
	match := verifyPassword(hash, pass)
	g.kdf.Release()
	g.leaveKDF(client)
	if !match {
		g.target.Allow(hash)
		g.global.Allow("")
		g.failedClient.Allow(client)
		g.failedClientTarget.Allow(clientTarget)
		return module.ViewNeedPassword, 0
	}
	limit.RefundAll(own...)
	g.remember(ck, now)
	return module.ViewOK, 0
}

// hashNew hashes a new view password at publish under the KDF gate, so
// publishes and checks together run at most the gate's KDFs at once. ok is
// false when no slot frees within passwordKDFWait (or ctx ends).
//
// It counts toward client's in-flight cap like a check does.
func (g *viewGate) hashNew(ctx context.Context, client, pw string) (hash string, ok bool, err error) {
	if !g.enterKDF(client) {
		return "", false, nil
	}
	defer g.leaveKDF(client)
	if !g.kdf.Acquire(ctx, passwordKDFWait) {
		return "", false, nil
	}
	defer g.kdf.Release()
	hash, err = store.HashPassword(pw)
	return hash, true, err
}

// CheckView is the module.Host password gate: the same check GET /{slug}
// runs, so a module serving or accepting writes for an artifact enforces its
// view password with the same limits.
func (s *Server) CheckView(r *http.Request, art store.Artifact) (module.ViewAccess, time.Duration) {
	return s.views.check(r.Context(), r, art.PasswordHash)
}

// serverCtxKey carries the *Server handling a request (set by
// withRequestLimits), so passwordOK can reach its limits from any handler.
type serverCtxKey struct{}

// fallbackViews gates password checks on a request that did not come through
// a Server's handler (none in production; a guard against a new route that
// skips the middleware getting no limits at all).
var fallbackViews = newViewGate(PasswordLimits{}, viewKey())

// passwordOK reports whether r carries HTTP Basic credentials whose password
// matches hash (the username is ignored), through the same rate and
// concurrency limits as CheckView. Every view-password check goes through
// here or CheckView; a refusal for load (429/503 in CheckView) reads false.
func passwordOK(r *http.Request, hash string) bool {
	g := fallbackViews
	if s, ok := r.Context().Value(serverCtxKey{}).(*Server); ok && s.views != nil {
		g = s.views
	}
	res, _ := g.check(r.Context(), r, hash)
	return res == module.ViewOK
}

// writeViewDenied writes the response for a refused view check of slug. It
// returns false when access was granted and nothing was written.
func writeViewDenied(w http.ResponseWriter, slug string, res module.ViewAccess, wait time.Duration) bool {
	switch res {
	case module.ViewOK:
		return false
	case module.ViewThrottled:
		w.Header().Set("Retry-After", limit.RetryAfter(wait))
		http.Error(w, "too many password attempts; try again later", http.StatusTooManyRequests)
	case module.ViewBusy:
		w.Header().Set("Retry-After", limit.RetryAfter(wait))
		http.Error(w, "server busy; try again shortly", http.StatusServiceUnavailable)
	default:
		w.Header().Set("WWW-Authenticate", passwordChallenge(slug))
		http.Error(w, "password required", http.StatusUnauthorized)
	}
	return true
}
