// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package limit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestKeyedBurstThenRefill(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	k := NewKeyed(Rate{Every: time.Second, Burst: 3}, 0)
	k.SetClock(clk.now)

	for i := 0; i < 3; i++ {
		if ok, _ := k.Allow("a"); !ok {
			t.Fatalf("attempt %d refused inside the burst", i)
		}
	}
	ok, wait := k.Allow("a")
	if ok {
		t.Fatal("fourth attempt admitted past the burst")
	}
	if wait <= 0 || wait > time.Second {
		t.Fatalf("wait = %v, want (0, 1s]", wait)
	}
	// Another key has its own bucket.
	if ok, _ := k.Allow("b"); !ok {
		t.Fatal("independent key refused")
	}
	clk.add(time.Second)
	if ok, _ := k.Allow("a"); !ok {
		t.Fatal("no token after one refill interval")
	}
	if ok, _ := k.Allow("a"); ok {
		t.Fatal("refill granted more than one token per interval")
	}
}

func TestKeyedRefundCapsAtBurst(t *testing.T) {
	k := NewKeyed(Rate{Every: time.Hour, Burst: 2}, 0)
	k.Allow("a")
	k.Refund("a")
	k.Refund("a")
	k.Refund("a")
	for i := 0; i < 2; i++ {
		if ok, _ := k.Allow("a"); !ok {
			t.Fatalf("attempt %d refused", i)
		}
	}
	if ok, _ := k.Allow("a"); ok {
		t.Fatal("refunds raised the bucket past its burst")
	}
}

func TestKeyedBoundedKeys(t *testing.T) {
	k := NewKeyed(Rate{Every: time.Hour, Burst: 1}, 64)
	for i := 0; i < 10000; i++ {
		k.Allow("k" + strconv.Itoa(i))
		if n := k.Len(); n > 64 {
			t.Fatalf("tracking %d keys, cap 64", n)
		}
	}
}

func TestUnlimitedAlwaysAllows(t *testing.T) {
	var nilKeyed *Keyed
	for _, k := range []*Keyed{nilKeyed, NewKeyed(Rate{}, 0)} {
		for i := 0; i < 100; i++ {
			if ok, _ := k.Allow("x"); !ok {
				t.Fatal("unlimited limiter refused")
			}
		}
	}
}

func TestAllowAllIsAllOrNothing(t *testing.T) {
	a := NewKeyed(Rate{Every: time.Hour, Burst: 5}, 0)
	b := NewKeyed(Rate{Every: time.Hour, Burst: 1}, 0)
	if ok, _ := AllowAll(Charge{a, "x"}, Charge{b, "y"}); !ok {
		t.Fatal("first combined attempt refused")
	}
	if ok, _ := AllowAll(Charge{a, "x"}, Charge{b, "y"}); ok {
		t.Fatal("second combined attempt admitted though b is empty")
	}
	// a was refunded after b refused, so 4 tokens remain in a.
	for i := 0; i < 4; i++ {
		if ok, _ := a.Allow("x"); !ok {
			t.Fatalf("a lost a token to a refused combined attempt (at %d)", i)
		}
	}
	if ok, _ := a.Allow("x"); ok {
		t.Fatal("a has more tokens than expected")
	}
}

func TestGateBoundsHolders(t *testing.T) {
	g := NewGate(2)
	if !g.TryAcquire() || !g.TryAcquire() {
		t.Fatal("could not take both slots")
	}
	if g.TryAcquire() {
		t.Fatal("third holder admitted")
	}
	start := time.Now()
	if g.Acquire(context.Background(), 30*time.Millisecond) {
		t.Fatal("Acquire succeeded on a full gate")
	}
	if time.Since(start) < 25*time.Millisecond {
		t.Fatal("Acquire did not wait")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if g.Acquire(ctx, time.Minute) {
		t.Fatal("Acquire succeeded with a cancelled context")
	}
	g.Release()
	if !g.Acquire(context.Background(), time.Second) {
		t.Fatal("slot not reusable after Release")
	}
	if g.InUse() != 2 {
		t.Fatalf("InUse = %d, want 2", g.InUse())
	}
	var nilGate Gate
	if !nilGate.TryAcquire() || !nilGate.Acquire(context.Background(), 0) {
		t.Fatal("nil gate refused")
	}
	nilGate.Release()
}

func TestClientKey(t *testing.T) {
	cases := []struct{ addr, want string }{
		{"192.0.2.7:4242", "192.0.2.7"},
		{"[2001:db8:1:2:aaaa::1]:80", "2001:db8:1:2::/64"},
		{"[2001:db8:1:2:bbbb::9]:81", "2001:db8:1:2::/64"},
		{"[::ffff:192.0.2.7]:1", "192.0.2.7"},
		{"not-an-ip", "not-an-ip"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.addr
		if got := ClientKey(r); got != c.want {
			t.Errorf("ClientKey(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{{0, "1"}, {time.Millisecond, "1"}, {time.Second, "1"}, {1500 * time.Millisecond, "2"}} {
		if got := RetryAfter(c.d); got != c.want {
			t.Errorf("RetryAfter(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestRateString(t *testing.T) {
	if got := PerMinute(20, 10).String(); got != "20/min burst 10" {
		t.Errorf("PerMinute(20,10) = %q", got)
	}
	if got := (Rate{}).String(); got != "unlimited" {
		t.Errorf("zero rate = %q", got)
	}
}

func TestProxiesClientAddr(t *testing.T) {
	p, err := ParseProxies("10.0.0.0/8, 192.0.2.7 ,fd00::/64")
	if err != nil {
		t.Fatal(err)
	}
	req := func(peer string, xff ...string) *http.Request {
		r := &http.Request{RemoteAddr: peer, Header: http.Header{}}
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	for _, tc := range []struct {
		name string
		r    *http.Request
		want string
	}{
		{"untrusted peer ignores the header", req("198.51.100.9:1", "203.0.113.5"), "198.51.100.9"},
		{"trusted peer, one hop", req("10.1.2.3:1", "203.0.113.5"), "203.0.113.5"},
		{"client-written entries to the left are ignored", req("10.1.2.3:1", "1.1.1.1, 203.0.113.5"), "203.0.113.5"},
		{"chained trusted proxies are skipped", req("10.1.2.3:1", "203.0.113.5, 192.0.2.7"), "203.0.113.5"},
		{"several header lines read as one list", req("10.1.2.3:1", "203.0.113.5", "10.9.9.9"), "203.0.113.5"},
		{"no header leaves the proxy", req("10.1.2.3:1"), "10.1.2.3"},
		{"malformed hop stops at the last good one", req("10.1.2.3:1", "203.0.113.5, junk"), "10.1.2.3"},
		{"IPv6 proxy", req("[fd00::1]:1", "2001:db8::5"), "2001:db8::5"},
		{"all hops trusted", req("10.1.2.3:1", "10.4.4.4"), "10.4.4.4"},
	} {
		if got := p.ClientAddr(tc.r); got != tc.want {
			t.Errorf("%s: ClientAddr = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := Proxies(nil).ClientAddr(req("10.1.2.3:1", "203.0.113.5")); got != "10.1.2.3" {
		t.Errorf("no trusted proxies: ClientAddr = %q, want the peer", got)
	}
	for _, bad := range []string{"10.0.0.0/33", "proxy.example", "10.0.0.1:80"} {
		if _, err := ParseProxies(bad); err == nil {
			t.Errorf("ParseProxies(%q) accepted", bad)
		}
	}
}

func TestClientKeyUsesResolvedClient(t *testing.T) {
	r := &http.Request{RemoteAddr: "10.1.2.3:1", Header: http.Header{}}
	if ClientKey(r) != "10.1.2.3" {
		t.Fatalf("unresolved request: %q", ClientKey(r))
	}
	r = r.WithContext(WithClient(context.Background(), "203.0.113.5"))
	if ClientKey(r) != "203.0.113.5" {
		t.Fatalf("resolved request: %q", ClientKey(r))
	}
}

// TestProxiesMappedIPv4: an IPv4-mapped IPv6 entry matches the IPv4 peer it
// names, as a bare address or a prefix of at least /96; a shorter mapped
// prefix spans more than IPv4 and is refused.
func TestProxiesMappedIPv4(t *testing.T) {
	for _, e := range []string{"::ffff:127.0.0.1", "::ffff:127.0.0.1/128", "::ffff:127.0.0.0/104"} {
		p, err := ParseProxies(e)
		if err != nil {
			t.Fatalf("ParseProxies(%q): %v", e, err)
		}
		r := &http.Request{RemoteAddr: "127.0.0.1:1", Header: http.Header{"X-Forwarded-For": {"203.0.113.5"}}}
		if got := p.ClientAddr(r); got != "203.0.113.5" {
			t.Errorf("%s: ClientAddr = %q, want the forwarded client", e, got)
		}
	}
	if _, err := ParseProxies("::ffff:0:0/80"); err == nil {
		t.Error("a mapped prefix shorter than /96 was accepted")
	}
}

// TestAddrKeyGroupsScopedIPv6: a link-local peer with an interface zone is
// grouped by /64 like any other IPv6 peer, and the zone does not split it.
func TestAddrKeyGroupsScopedIPv6(t *testing.T) {
	a := AddrKey("[fe80::1%eth0]:5000")
	b := AddrKey("[fe80::ffff:2%eth0]:6000")
	c := AddrKey("[fe80::3%eth1]:6000")
	if a != b || a != c {
		t.Errorf("one link-local /64 got keys %q, %q, %q", a, b, c)
	}
	if AddrKey("[fe80:0:0:1::1%eth0]:1") == a {
		t.Error("two link-local /64s share a key")
	}
	if got := AddrKey("192.0.2.1:1"); got != "192.0.2.1" {
		t.Errorf("IPv4 key %q", got)
	}
	if AddrKey("[2001:db8:1:2::1]:1") != AddrKey("[2001:db8:1:2:ffff::9]:1") {
		t.Error("two addresses in one global /64 got different keys")
	}
}
