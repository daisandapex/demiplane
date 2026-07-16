// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package tls

import (
	stdtls "crypto/tls"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// acmeConfig builds the tls.Config for ACME mode: certificates are obtained
// and renewed automatically for the configured domains via autocert, answering
// the TLS-ALPN-01 challenge inline on the TLS listener itself. Setting
// tls_acme_domains constitutes acceptance of the CA's Terms of Service (the
// same posture as Caddy) — there is no interactive prompt to accept them.
//
// Deployment requirements (documented in the README): each domain must resolve
// to this host, and the CA must be able to reach the CONTROL listener on
// port 443 — TLS-ALPN-01 is only ever attempted there. The content listener
// serves the same certificates from the shared cache.
func acmeConfig(dataDir string, s settings) (*stdtls.Config, error) {
	cacheDir := filepath.Join(dataDir, "acme")
	// 0700 like the module dir: the cache holds account and certificate keys.
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("tls: create acme cache dir: %w", err)
	}
	m := newACMEManager(cacheDir, s)
	log.Printf("tls: ACME enabled for %v (cache %s) — the control listener must be reachable on :443 for the TLS-ALPN-01 challenge",
		s.acmeDomains, cacheDir)
	cfg := m.TLSConfig()
	cfg.MinVersion = stdtls.VersionTLS12
	return cfg, nil
}

// newACMEManager assembles the autocert manager; split from acmeConfig so the
// host policy, cache, and CA override are unit-testable without network I/O.
func newACMEManager(cacheDir string, s settings) *autocert.Manager {
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cacheDir),
		HostPolicy: autocert.HostWhitelist(s.acmeDomains...),
		Email:      s.acmeEmail,
	}
	if s.acmeCA != "" {
		// Directory override — Let's Encrypt staging, Pebble, or an internal CA.
		m.Client = &acme.Client{DirectoryURL: s.acmeCA}
	}
	return m
}
