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

// FuzzClientAddr checks the per-client key against SECURITY.md's rule:
// forwarding headers count only from a trusted peer, X-Forwarded-For is read
// from the right past every trusted hop, and the first other address is the
// client; entries further left are never used.
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

		// The reference reading of the rule.
		host := remote
		if h, _, err := net.SplitHostPort(remote); err == nil {
			host = h
		}
		want := host
		if peer, err := netip.ParseAddr(host); err == nil && len(p) > 0 && p.trusted(peer) {
			client := peer
			hops := strings.Split(xff, ",")
			for i := len(hops) - 1; i >= 0 && xff != ""; i-- {
				a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
				if err != nil {
					break
				}
				client = a.Unmap()
				if !p.trusted(client) {
					break
				}
			}
			want = client.String()
		}
		if got != want {
			t.Fatalf("ClientAddr(%q, %q, %q) = %q, want %q", csv, remote, xff, got, want)
		}
		if peer, err := netip.ParseAddr(host); err != nil || len(p) == 0 || !p.trusted(peer) {
			if got != host {
				t.Fatalf("untrusted peer %q gave client %q from forwarding headers", remote, got)
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
