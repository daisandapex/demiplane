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

	"github.com/daisandapex/demiplane/internal/limit"
	"github.com/daisandapex/demiplane/internal/module"
	"github.com/daisandapex/demiplane/internal/store"
)

// View-password checks run PBKDF2 at the stored work factor (600,000
// iterations by default), which is the defence an offline attacker faces and
// is kept. That cost makes every wrong guess a CPU charge any viewer can levy,
// so the gate in front of the KDF does three things:
//
//  1. Verified credentials are remembered for a short while (keyed by an HMAC
//     of the stored hash and the password under a per-process key), so a
//     browser re-sending Basic credentials on every page load costs one KDF,
//     not one per request.
//  2. A KDF run is an attempt charged to three token buckets: the target
//     artifact, the client, and the whole instance. An empty bucket answers
//     429 without hashing. A correct password is refunded, so only failures
//     spend the budget.
//  3. At most PasswordConcurrency KDF runs execute at once; the rest wait
//     briefly and then get 503, so a flood cannot take every core from
//     unrelated requests.

// Defaults for the password gate. The per-target rate is the online-guessing
// bound for one artifact; the global rate bounds total KDF work.
var (
	defaultPasswordTargetRate = limit.PerMinute(10, 10)
	defaultPasswordClientRate = limit.PerMinute(20, 10)
	defaultPasswordGlobalRate = limit.PerMinute(120, 30)
)

const (
	// passwordKDFWait is how long a check waits for a free KDF slot.
	passwordKDFWait = 5 * time.Second
	// verifiedTTL is how long a verified credential skips the KDF.
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
	Concurrency int
	PerTarget   limit.Rate
	PerClient   limit.Rate
	Global      limit.Rate
}

// verifyPassword is the KDF check. A variable so tests can observe how many
// run at once.
var verifyPassword = store.PasswordMatches

type viewGate struct {
	kdf    limit.Gate
	target *limit.Keyed
	client *limit.Keyed
	global *limit.Keyed

	key []byte
	mu  sync.Mutex
	ok  map[[32]byte]time.Time
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
	return &viewGate{
		kdf:    limit.NewGate(n),
		target: limit.NewKeyed(pickRate(cfg.PerTarget, defaultPasswordTargetRate), 0),
		client: limit.NewKeyed(pickRate(cfg.PerClient, defaultPasswordClientRate), 0),
		global: limit.NewKeyed(pickRate(cfg.Global, defaultPasswordGlobalRate), 1),
		key:    key,
		ok:     map[[32]byte]time.Time{},
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
// Retry-After hint for the push-back results. The per-target bucket is keyed
// by the hash, which every publish of an artifact replaces.
func (g *viewGate) check(ctx context.Context, r *http.Request, hash string) (res module.ViewAccess, wait time.Duration) {
	if hash == "" {
		return module.ViewOK, 0
	}
	_, pass, ok := r.BasicAuth()
	if !ok {
		return module.ViewNeedPassword, 0
	}
	now := time.Now()
	ck := g.credKey(hash, pass)
	if g.remembered(ck, now) {
		return module.ViewOK, 0
	}
	charges := []limit.Charge{
		{L: g.global, Key: ""},
		{L: g.client, Key: limit.ClientKey(r)},
		{L: g.target, Key: hash},
	}
	if allowed, w := limit.AllowAll(charges...); !allowed {
		return module.ViewThrottled, w
	}
	if !g.kdf.Acquire(ctx, passwordKDFWait) {
		limit.RefundAll(charges...)
		return module.ViewBusy, time.Second
	}
	match := verifyPassword(hash, pass)
	g.kdf.Release()
	if !match {
		return module.ViewNeedPassword, 0
	}
	limit.RefundAll(charges...)
	g.remember(ck, now)
	return module.ViewOK, 0
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

// writeViewDenied writes the response for a refused view check. It returns
// false when access was granted and nothing was written.
func writeViewDenied(w http.ResponseWriter, res module.ViewAccess, wait time.Duration) bool {
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
		w.Header().Set("WWW-Authenticate", module.PasswordChallenge)
		http.Error(w, "password required", http.StatusUnauthorized)
	}
	return true
}
