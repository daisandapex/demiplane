// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	stdtls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	// selfSignedCertFile / selfSignedKeyFile persist the generated pair under
	// the module data dir so the certificate is stable across restarts —
	// operators can pin its fingerprint instead of re-trusting on every boot.
	selfSignedCertFile = "self-signed-cert.pem"
	selfSignedKeyFile  = "self-signed-key.pem"

	// selfSignedValidity is deliberately long (2 years): this is a private-
	// deployment convenience cert, not a WebPKI subject, and silent expiry of an
	// internal endpoint is the worse failure. Regeneration is automatic inside
	// selfSignedRenewWindow of expiry.
	selfSignedValidity    = 2 * 365 * 24 * time.Hour
	selfSignedRenewWindow = 30 * 24 * time.Hour
)

// loadOrCreateSelfSigned returns the persisted self-signed certificate from
// dir, regenerating it when absent, within the renew window of expiry, or no
// longer covering every requested host (e.g. the operator moved the bind
// address). hosts is the SAN set — DNS names and IP literals mixed.
func loadOrCreateSelfSigned(dir string, hosts []string) (stdtls.Certificate, error) {
	certPath := filepath.Join(dir, selfSignedCertFile)
	keyPath := filepath.Join(dir, selfSignedKeyFile)

	if cert, ok := loadUsableSelfSigned(certPath, keyPath, hosts); ok {
		return cert, nil
	}
	return generateSelfSigned(certPath, keyPath, hosts)
}

// loadUsableSelfSigned loads the persisted pair and reports whether it is
// still serviceable for hosts. Any load/parse problem (including a missing
// pair — the first run) means "regenerate", never a hard failure.
func loadUsableSelfSigned(certPath, keyPath string, hosts []string) (stdtls.Certificate, bool) {
	cert, err := stdtls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("tls: persisted self-signed pair unreadable (%v) — regenerating", err)
		}
		return stdtls.Certificate{}, false
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		log.Printf("tls: persisted self-signed cert unparseable (%v) — regenerating", err)
		return stdtls.Certificate{}, false
	}
	if time.Now().After(leaf.NotAfter.Add(-selfSignedRenewWindow)) {
		log.Printf("tls: self-signed cert expires %s — regenerating", leaf.NotAfter.Format(time.RFC3339))
		return stdtls.Certificate{}, false
	}
	for _, h := range hosts {
		if err := leaf.VerifyHostname(h); err != nil {
			log.Printf("tls: self-signed cert does not cover %q — regenerating", h)
			return stdtls.Certificate{}, false
		}
	}
	return cert, true
}

// generateSelfSigned mints a fresh ECDSA P-256 self-signed certificate for
// hosts, persists it (key 0600), and logs its SHA-256 fingerprint so a client
// operator can pin it.
func generateSelfSigned(certPath, keyPath string, hosts []string) (stdtls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return stdtls.Certificate{}, fmt.Errorf("tls: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return stdtls.Certificate{}, fmt.Errorf("tls: generate serial: %w", err)
	}

	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "demiplane self-signed", Organization: []string{"demiplane"}},
		// NotBefore backdated an hour so a listener behind a skewed clock
		// doesn't refuse its own fresh certificate.
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(selfSignedValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return stdtls.Certificate{}, fmt.Errorf("tls: create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return stdtls.Certificate{}, fmt.Errorf("tls: marshal key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// Key first (0600 — private material), certificate after; the loader treats
	// any inconsistency as a regenerate, so a torn write cannot brick startup.
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return stdtls.Certificate{}, fmt.Errorf("tls: persist key: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return stdtls.Certificate{}, fmt.Errorf("tls: persist certificate: %w", err)
	}

	sum := sha256.Sum256(der)
	log.Printf("tls: generated self-signed certificate for %v (valid to %s)\n"+
		"tls:   SHA-256 fingerprint %s\n"+
		"tls:   clients that verify certificates must trust %s (curl --cacert, or pin the fingerprint)",
		hosts, tmpl.NotAfter.Format("2006-01-02"), hex.EncodeToString(sum[:]), certPath)

	cert, err := stdtls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return stdtls.Certificate{}, fmt.Errorf("tls: assemble pair: %w", err)
	}
	return cert, nil
}

// selfSignedHosts resolves the SAN set: an explicit tls_hosts wins; otherwise
// derive from the listener bind hosts, always including the loopback names —
// and, when a wildcard bind means "any interface", the machine's hostname.
func selfSignedHosts(override, bindHosts []string) []string {
	if len(override) > 0 {
		return override
	}
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	add("localhost")
	add("127.0.0.1")
	add("::1")
	wildcard := false
	for _, h := range bindHosts {
		if h == "" {
			wildcard = true // ":port" binds every interface
			continue
		}
		if ip := net.ParseIP(h); ip != nil && ip.IsUnspecified() {
			wildcard = true
			continue
		}
		add(h)
	}
	if wildcard {
		if hn, err := os.Hostname(); err == nil {
			add(hn)
		}
	}
	return out
}
