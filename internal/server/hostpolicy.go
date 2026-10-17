// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// The accepted-host policy (--allowed-hosts). Without --base-url and
// --content-base-url, advertised URLs (publish responses, /list, llms.txt,
// rendered colophons) are derived from the request's Host header, which the
// client chooses. Setting the base URLs makes them authoritative; the policy
// additionally refuses, with 421 Misdirected Request, any request whose Host
// is not one the operator named, on both planes, before routing. The host
// names of --base-url and --content-base-url are always accepted, on any port:
// behind a proxy the Host arrives without the public port, and the content
// origin derived from --base-url uses the content port. An empty list means
// no policy (any Host is served), the default for a loopback or mesh-only
// instance.

// hostEntryRe is a hostname or IP literal, optionally with a port: letters,
// digits, dots, hyphens and underscores, or a bracketed IPv6 address.
var hostEntryRe = regexp.MustCompile(`^([A-Za-z0-9._-]+|\[[0-9A-Fa-f:.]+\])(:[0-9]{1,5})?$`)

// ParseHostList parses a comma-separated --allowed-hosts value. Each entry is
// a host ("demi.example", "[fd00::1]") accepted on any port, or host:port
// accepted on that port only. Empty entries are ignored; an entry with a
// scheme, path, userinfo or wildcard is an error.
func ParseHostList(csv string) ([]string, error) {
	var out []string
	for _, e := range strings.Split(csv, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if !hostEntryRe.MatchString(e) {
			return nil, fmt.Errorf("--allowed-hosts entry %q: want host or host:port", e)
		}
		out = append(out, e)
	}
	return out, nil
}

// hostRule is one accepted host; port "" accepts any port.
type hostRule struct{ host, port string }

// splitHost lowercases a Host value and splits off its port, unbracketing an
// IPv6 literal. A value without a port yields port "".
func splitHost(hostport string) (host, port string) {
	hostport = strings.ToLower(hostport)
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return h, p
	}
	return strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]"), ""
}

// buildHostRules compiles the accepted-host list, adding the host names of
// the configured base URLs (any port). nil means no policy.
func buildHostRules(allowed []string, baseURLs ...string) []hostRule {
	if len(allowed) == 0 {
		return nil
	}
	var rules []hostRule
	for _, e := range allowed {
		h, p := splitHost(e)
		rules = append(rules, hostRule{h, p})
	}
	for _, b := range baseURLs {
		if b == "" {
			continue
		}
		if u, err := url.Parse(b); err == nil && u.Hostname() != "" {
			rules = append(rules, hostRule{strings.ToLower(u.Hostname()), ""})
		}
	}
	return rules
}

// hostAllowed reports whether a request Host matches a rule. A Host without
// a port means the default port of the scheme the request arrived over, so a
// rule for host:443 does not accept a bare Host on plain HTTP.
func hostAllowed(rules []hostRule, hostport string, tls bool) bool {
	h, p := splitHost(hostport)
	if h == "" {
		return false
	}
	if p == "" {
		p = "80"
		if tls {
			p = "443"
		}
	}
	for _, r := range rules {
		if r.host == h && (r.port == "" || r.port == p) {
			return true
		}
	}
	return false
}

// maxHostLen bounds the Host header (a DNS name is at most 253 bytes; the
// rest allows a port). The Host is repeated in every URL demiplane advertises
// when no base URL is configured, so an unbounded one would multiply each
// listing's size.
const maxHostLen = 255

// boundHost refuses a request whose Host is longer than maxHostLen, before
// the host policy and routing.
func boundHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Host) > maxHostLen {
			http.Error(w, "host too long", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostPolicy refuses requests for a Host outside the accepted list.
func (s *Server) hostPolicy(next http.Handler) http.Handler {
	if s.hostRules == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(s.hostRules, r.Host, r.TLS != nil) {
			http.Error(w, "unknown host", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
