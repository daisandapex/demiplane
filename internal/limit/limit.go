// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package limit holds the small admission-control primitives demiplane uses on
// its unauthenticated paths: keyed token buckets (per client, per target,
// global), a context-aware concurrency gate, and the client key a request is
// charged to. Everything is in memory and per process; nothing here persists.
package limit

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rate is a token-bucket rate: a bucket holds up to Burst tokens and gains one
// every Every. A zero Rate (Every == 0 or Burst <= 0) means unlimited.
type Rate struct {
	Every time.Duration
	Burst int
}

// Unlimited reports whether r imposes no limit.
func (r Rate) Unlimited() bool { return r.Every <= 0 || r.Burst <= 0 }

// PerMinute returns a Rate of n tokens a minute with the given burst.
func PerMinute(n, burst int) Rate {
	if n <= 0 {
		return Rate{}
	}
	return Rate{Every: time.Minute / time.Duration(n), Burst: burst}
}

// String renders r for logs and docs ("20/min burst 10", "unlimited").
func (r Rate) String() string {
	if r.Unlimited() {
		return "unlimited"
	}
	perMin := float64(time.Minute) / float64(r.Every)
	return strconv.FormatFloat(perMin, 'f', -1, 64) + "/min burst " + strconv.Itoa(r.Burst)
}

// DefaultMaxKeys bounds how many distinct keys a Keyed limiter tracks.
const DefaultMaxKeys = 16384

// Keyed is a set of token buckets, one per key, sharing one Rate. It is safe
// for concurrent use. The number of tracked keys is bounded: when the table is
// full, buckets that have refilled completely are dropped first (they carry no
// state worth keeping), then arbitrary ones. Dropping a drained bucket resets
// that key, so a Keyed limiter keyed by client should always sit beside a
// global one that cannot be reset this way.
type Keyed struct {
	mu      sync.Mutex
	rate    Rate
	maxKeys int
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewKeyed returns a Keyed limiter. maxKeys <= 0 uses DefaultMaxKeys.
func NewKeyed(r Rate, maxKeys int) *Keyed {
	if maxKeys <= 0 {
		maxKeys = DefaultMaxKeys
	}
	return &Keyed{rate: r, maxKeys: maxKeys, buckets: map[string]*bucket{}, now: time.Now}
}

// SetClock replaces the time source (tests).
func (k *Keyed) SetClock(now func() time.Time) {
	k.mu.Lock()
	k.now = now
	k.mu.Unlock()
}

// Rate returns the limiter's rate.
func (k *Keyed) Rate() Rate { return k.rate }

// refill brings b up to date as of now and returns it.
func (k *Keyed) refill(b *bucket, now time.Time) {
	if el := now.Sub(b.last); el > 0 {
		b.tokens += float64(el) / float64(k.rate.Every)
		if max := float64(k.rate.Burst); b.tokens > max {
			b.tokens = max
		}
	}
	b.last = now
}

// get returns key's bucket, creating a full one (and evicting if the table is
// at capacity). Caller holds k.mu.
func (k *Keyed) get(key string, now time.Time) *bucket {
	if b, ok := k.buckets[key]; ok {
		k.refill(b, now)
		return b
	}
	if len(k.buckets) >= k.maxKeys {
		k.evict(now)
	}
	b := &bucket{tokens: float64(k.rate.Burst), last: now}
	k.buckets[key] = b
	return b
}

// evict frees room in a full table. Caller holds k.mu.
func (k *Keyed) evict(now time.Time) {
	for key, b := range k.buckets {
		k.refill(b, now)
		if b.tokens >= float64(k.rate.Burst) {
			delete(k.buckets, key)
		}
	}
	// Still full: drop an arbitrary quarter so the next inserts do not each
	// pay for a full scan.
	if len(k.buckets) >= k.maxKeys {
		drop := k.maxKeys / 4
		if drop < 1 {
			drop = 1
		}
		for key := range k.buckets {
			if drop == 0 {
				break
			}
			delete(k.buckets, key)
			drop--
		}
	}
}

// Allow takes one token from key's bucket and reports whether one was
// available. When it was not, wait is how long until one will be.
func (k *Keyed) Allow(key string) (ok bool, wait time.Duration) {
	if k == nil || k.rate.Unlimited() {
		return true, 0
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	b := k.get(key, now)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	need := 1 - b.tokens
	return false, time.Duration(need * float64(k.rate.Every))
}

// Refund returns one token to key's bucket (an admitted attempt that turned out
// not to count, such as a correct password).
func (k *Keyed) Refund(key string) {
	if k == nil || k.rate.Unlimited() {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	b, ok := k.buckets[key]
	if !ok {
		return
	}
	k.refill(b, k.now())
	b.tokens++
	if max := float64(k.rate.Burst); b.tokens > max {
		b.tokens = max
	}
}

// Empty reports whether key's bucket has no whole token, without taking one,
// and if so how long until it will. A key never charged is full.
func (k *Keyed) Empty(key string) (empty bool, wait time.Duration) {
	if k == nil || k.rate.Unlimited() {
		return false, 0
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	b, ok := k.buckets[key]
	if !ok {
		return false, 0
	}
	k.refill(b, k.now())
	if b.tokens >= 1 {
		return false, 0
	}
	return true, time.Duration((1 - b.tokens) * float64(k.rate.Every))
}

// Spent reports whether key has been charged and its bucket has not yet
// refilled to the burst: the key has used some of its budget recently.
func (k *Keyed) Spent(key string) bool {
	if k == nil || k.rate.Unlimited() {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	b, ok := k.buckets[key]
	if !ok {
		return false
	}
	k.refill(b, k.now())
	return b.tokens < float64(k.rate.Burst)
}

// Len reports how many keys are tracked (tests).
func (k *Keyed) Len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.buckets)
}

// Charge is one (limiter, key) pair for AllowAll.
type Charge struct {
	L   *Keyed
	Key string
}

// AllowAll takes one token from every charge, or from none: if any bucket is
// empty, the tokens already taken are refunded and the longest wait among the
// refusals is returned. Charges with a nil limiter are skipped.
func AllowAll(charges ...Charge) (ok bool, wait time.Duration) {
	for i, c := range charges {
		if c.L == nil {
			continue
		}
		if allowed, w := c.L.Allow(c.Key); !allowed {
			for _, prev := range charges[:i] {
				if prev.L != nil {
					prev.L.Refund(prev.Key)
				}
			}
			return false, w
		}
	}
	return true, 0
}

// RefundAll refunds one token to every charge.
func RefundAll(charges ...Charge) {
	for _, c := range charges {
		if c.L != nil {
			c.L.Refund(c.Key)
		}
	}
}

// Gate bounds concurrency: at most cap(g) holders at once. A nil Gate admits
// everyone.
type Gate chan struct{}

// NewGate returns a Gate admitting n holders; n <= 0 returns nil (unbounded).
func NewGate(n int) Gate {
	if n <= 0 {
		return nil
	}
	return make(Gate, n)
}

// TryAcquire takes a slot without waiting.
func (g Gate) TryAcquire() bool {
	if g == nil {
		return true
	}
	select {
	case g <- struct{}{}:
		return true
	default:
		return false
	}
}

// Acquire takes a slot, waiting at most maxWait and no longer than ctx allows.
func (g Gate) Acquire(ctx context.Context, maxWait time.Duration) bool {
	if g == nil {
		return true
	}
	select {
	case g <- struct{}{}:
		return true
	default:
	}
	if maxWait <= 0 {
		return false
	}
	t := time.NewTimer(maxWait)
	defer t.Stop()
	select {
	case g <- struct{}{}:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// Release frees a slot taken by Acquire or TryAcquire.
func (g Gate) Release() {
	if g == nil {
		return
	}
	<-g
}

// InUse reports how many slots are held.
func (g Gate) InUse() int { return len(g) }

// ClientKey returns the key a request is charged to: the client address,
// with IPv6 grouped by /64 (one subscriber's allocation). The address is the
// one the server resolved for the request (WithClient), which is the TCP peer
// unless the peer is a trusted proxy (Proxies); a request with none resolved
// is charged to its TCP peer.
func ClientKey(r *http.Request) string {
	if k, ok := r.Context().Value(clientCtxKey{}).(string); ok && k != "" {
		return k
	}
	return AddrKey(r.RemoteAddr)
}

type clientCtxKey struct{}

// WithClient returns ctx carrying key as its request's client key.
func WithClient(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, clientCtxKey{}, key)
}

// Proxies is the set of reverse proxies (--trusted-proxy) whose
// X-Forwarded-For names the client. Empty means none: forwarding headers are
// ignored and every request is its TCP peer, so behind a proxy all clients
// share the proxy's key.
type Proxies []netip.Prefix

// ParseProxies parses a comma-separated list of CIDRs or bare IP addresses.
func ParseProxies(csv string) (Proxies, error) {
	var out Proxies
	for _, e := range strings.Split(csv, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if p, err := netip.ParsePrefix(e); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(e)
		if err != nil {
			return nil, fmt.Errorf("--trusted-proxy entry %q: want an IP address or CIDR", e)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func (p Proxies) trusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, n := range p {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// ClientAddr returns the address r is charged to. It is the TCP peer unless
// the peer is a trusted proxy; then X-Forwarded-For is read from the right,
// past every trusted hop, and the first address that is not a trusted proxy
// is the client. Entries to its left were written by the client and are never
// used. A missing or malformed header leaves the proxy as the client.
func (p Proxies) ClientAddr(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || len(p) == 0 || !p.trusted(peer) {
		return host
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		client = a.Unmap()
		if !p.trusted(client) {
			break
		}
	}
	return client.String()
}

// Key is ClientKey's value for r under these proxies.
func (p Proxies) Key(r *http.Request) string { return AddrKey(p.ClientAddr(r)) }

// AddrKey is ClientKey for a bare "host:port" (or host) address.
func AddrKey(addr string) string {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// RetryAfter formats a wait as a Retry-After header value (whole seconds,
// rounded up, at least 1).
func RetryAfter(wait time.Duration) string {
	s := int64((wait + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return strconv.FormatInt(s, 10)
}
