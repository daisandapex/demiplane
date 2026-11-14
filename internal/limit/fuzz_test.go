// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package limit

import (
	"net"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// FuzzClientAddr checks the per-client key against SECURITY.md's rule,
// stated as properties of the result and judged with net/netip (and
// net.SplitHostPort for the peer's port), not by re-running the walk
// ClientAddr does:
//
//   - an untrusted (or unparseable) peer is its own client, port removed;
//   - behind a trusted peer the client is the peer itself or an address
//     that literally appears as one comma-separated X-Forwarded-For entry,
//     and every entry to its right parses and is a trusted proxy, so it is
//     never one left of an untrusted or malformed hop;
//   - the walk does not stop early: a trusted client is the leftmost entry,
//     or the entry left of it does not parse, or (for the peer) the header
//     is empty or its last entry does not parse.
func FuzzClientAddr(f *testing.F) {
	for _, s := range [][3]string{
		{"10.0.0.1", "10.0.0.1:5000", "203.0.113.9"},
		{"10.0.0.0/8", "10.0.0.1:5000", "198.51.100.1, 203.0.113.9, 10.0.0.2"},
		{"", "10.0.0.1:5000", "203.0.113.9"},
		{"10.0.0.1", "192.0.2.1:5000", "203.0.113.9"},
		{"::ffff:10.0.0.0/104", "[::ffff:10.0.0.1]:1", "2001:db8::1"},
		{"10.0.0.1", "10.0.0.1:1", "garbage, 203.0.113.9"},
		{"10.0.0.1", "10.0.0.1:1", "203.0.113.9, garbage"},
		{"2001:db8::/32", "[2001:db8::1]:1", "2001:db8:1:2:3:4:5:6"},
		{"10.0.0.1", "10.0.0.1:1", "fe80::1%eth0"},
		{"10.0.0.0/8", "10.0.0.1:1", "10.0.0.3, 10.0.0.2"},
		{"10.0.0.0/8", "10.0.0.1:1", "203.0.113.9, 10.0.0.2, ::ffff:10.0.0.5"},
		{"10.0.0.1", "proxy:80", "203.0.113.9"},
	} {
		f.Add(s[0], s[1], s[2])
	}
	f.Fuzz(func(t *testing.T, csv, remote, xff string) {
		p, err := ParseProxies(csv)
		if err != nil {
			return
		}
		for _, n := range p {
			if n != n.Masked() || n.Addr().Is4In6() {
				t.Fatalf("proxy prefix %v not normalized", n)
			}
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		got := p.ClientAddr(r)

		// A proxy is an address inside one of the parsed prefixes; an
		// IPv4-mapped address is the IPv4 address it carries.
		proxy := func(a netip.Addr) bool {
			for _, n := range p {
				if n.Contains(a.Unmap()) {
					return true
				}
			}
			return false
		}
		// The peer is RemoteAddr with its port removed by the standard
		// library's own splitter.
		peerText := remote
		if h, _, err := net.SplitHostPort(remote); err == nil {
			peerText = h
		}
		peer, perr := netip.ParseAddr(peerText)
		if perr != nil || !proxy(peer) {
			// Untrusted peer: the header is ignored.
			// The client is the peer as given, at most without its port.
			if got != peerText && !strings.HasPrefix(remote, got+":") && !strings.HasPrefix(remote, "["+got+"]:") {
				t.Fatalf("untrusted peer %q (proxies %q, header %q) gave client %q", remote, csv, xff, got)
			}
		} else {
			hops := strings.Split(xff, ",")
			if xff == "" {
				hops = nil
			}
			hop := func(i int) (netip.Addr, bool) {
				if i == len(hops) {
					return peer, true
				}
				a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
				return a, err == nil
			}
			ga, gerr := netip.ParseAddr(got)
			if gerr != nil {
				t.Fatalf("trusted peer %q gave client %q, not an address", remote, got)
			}
			found := false
			for k := len(hops); k >= 0 && !found; k-- {
				a, ok := hop(k)
				if !ok || a.Unmap() != ga.Unmap() {
					continue
				}
				rightTrusted := true
				for j := k + 1; j <= len(hops); j++ {
					if b, ok := hop(j); !ok || !proxy(b) {
						rightTrusted = false
					}
				}
				if !rightTrusted {
					continue
				}
				if k > 0 && proxy(a) {
					if _, leftOK := hop(k - 1); leftOK {
						continue // the walk would have gone on past a trusted hop
					}
				}
				found = true
			}
			if !found {
				t.Fatalf("ClientAddr(%q, %q, %q) = %q: not the peer or an X-Forwarded-For entry right of which every hop is a trusted proxy", csv, remote, xff, got)
			}
		}
		key := AddrKey(got)
		if a, err := netip.ParseAddr(got); err == nil && a.Unmap().Is6() {
			pfx, err := netip.ParsePrefix(key)
			if err != nil || pfx.Bits() != 64 {
				t.Fatalf("IPv6 client %q keyed as %q, not its /64", got, key)
			}
		}
	})
}
