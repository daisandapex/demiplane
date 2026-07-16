// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package tls

import (
	"context"
	stdtls "crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// configure is a test helper: Configure with a fresh map, failing the test on
// unexpected error.
func configure(t *testing.T, cfg map[string]string) {
	t.Helper()
	if err := Configure(cfg); err != nil {
		t.Fatalf("Configure(%v): %v", cfg, err)
	}
}

// reset restores the package's zero configuration after a test (package state
// mirrors the reply module's hook config; tests must not leak into each other).
func reset(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { current = settings{mode: ModeOff} })
}

func TestConfigureDefaultsOff(t *testing.T) {
	reset(t)
	configure(t, map[string]string{})
	if Enabled() || ActiveMode() != ModeOff {
		t.Errorf("empty config: Enabled=%v mode=%v, want disabled/off", Enabled(), ActiveMode())
	}
}

func TestConfigureModeResolution(t *testing.T) {
	reset(t)
	cases := []struct {
		cfg  map[string]string
		want Mode
	}{
		{map[string]string{"tls": "on"}, ModeSelfSigned},
		{map[string]string{"tls": "on", "tls_hosts": "demi.internal"}, ModeSelfSigned},
		{map[string]string{"tls": "on", "tls_cert": "c.pem", "tls_key": "k.pem"}, ModeManual},
		{map[string]string{"tls": "on", "tls_acme_domains": "demi.example.com"}, ModeACME},
		{map[string]string{"tls": "on", "tls_acme_domains": "a.example, b.example", "tls_acme_email": "ops@example.com"}, ModeACME},
	}
	for _, c := range cases {
		configure(t, c.cfg)
		if !Enabled() || ActiveMode() != c.want {
			t.Errorf("Configure(%v): Enabled=%v mode=%v, want enabled/%v", c.cfg, Enabled(), ActiveMode(), c.want)
		}
	}
}

func TestConfigureInvalid(t *testing.T) {
	reset(t)
	cases := []map[string]string{
		{"tls": "maybe"},                   // bad on/off
		{"tls": "on", "tls_cert": "c.pem"}, // cert without key
		{"tls": "on", "tls_key": "k.pem"},  // key without cert
		{"tls": "on", "tls_cert": "c", "tls_key": "k", "tls_acme_domains": "d"}, // two sources
		{"tls": "on", "tls_acme_email": "x@y"},                                  // acme knob without domains
		{"tls": "on", "tls_acme_ca": "https://ca"},                              // acme knob without domains
		{"tls": "on", "tls_cert": "c", "tls_key": "k", "tls_hosts": "h"},        // hosts with manual source
	}
	for _, cfg := range cases {
		if err := Configure(cfg); err == nil {
			t.Errorf("Configure(%v) succeeded, want hard error", cfg)
		}
	}
}

// TestConfigureOffKeepsListenersPlain: `tls = off` (or absent) with staged
// keys parses fine and resolves to ModeOff — plain HTTP.
func TestConfigureOffKeepsListenersPlain(t *testing.T) {
	reset(t)
	configure(t, map[string]string{"tls": "off", "tls_acme_domains": "demi.example.com"})
	if Enabled() || ActiveMode() != ModeOff {
		t.Errorf("staged-but-off: Enabled=%v mode=%v, want disabled/off", Enabled(), ActiveMode())
	}
	if cfg, err := ServerConfig(t.TempDir(), nil); err != nil || cfg != nil {
		t.Errorf("ServerConfig while off = (%v, %v), want (nil, nil)", cfg, err)
	}
}

func TestSelfSignedGeneratePersistReuse(t *testing.T) {
	reset(t)
	dir := t.TempDir()
	hosts := []string{"localhost", "127.0.0.1", "203.0.113.7"}

	c1, err := loadOrCreateSelfSigned(dir, hosts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	leaf, err := x509.ParseCertificate(c1.Certificate[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, h := range hosts {
		if err := leaf.VerifyHostname(h); err != nil {
			t.Errorf("generated cert does not cover %q: %v", h, err)
		}
	}
	if got := leaf.NotAfter.Sub(leaf.NotBefore); got < selfSignedValidity {
		t.Errorf("validity %v, want >= %v", got, selfSignedValidity)
	}
	// The private key must not be world-readable.
	fi, err := os.Stat(filepath.Join(dir, selfSignedKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", fi.Mode().Perm())
	}

	// Same hosts → the persisted pair is reused byte-for-byte.
	c2, err := loadOrCreateSelfSigned(dir, hosts)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if string(c1.Certificate[0]) != string(c2.Certificate[0]) {
		t.Error("persisted certificate was regenerated on an unchanged host set")
	}

	// A host the cert does not cover → regenerated with the new SAN.
	c3, err := loadOrCreateSelfSigned(dir, []string{"newhost.internal"})
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if string(c1.Certificate[0]) == string(c3.Certificate[0]) {
		t.Error("certificate not regenerated for an uncovered host")
	}
	leaf3, _ := x509.ParseCertificate(c3.Certificate[0])
	if err := leaf3.VerifyHostname("newhost.internal"); err != nil {
		t.Errorf("regenerated cert does not cover new host: %v", err)
	}
}

func TestSelfSignedHostsDerivation(t *testing.T) {
	// Explicit override wins outright.
	if got := selfSignedHosts([]string{"only.example"}, []string{"127.0.0.1"}); len(got) != 1 || got[0] != "only.example" {
		t.Errorf("override ignored: %v", got)
	}
	// Bind hosts are added on top of the loopback set, de-duplicated.
	got := selfSignedHosts(nil, []string{"203.0.113.7", "203.0.113.7", "127.0.0.1"})
	want := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, "203.0.113.7": true}
	if len(got) != len(want) {
		t.Errorf("derived hosts = %v, want exactly %v", got, want)
	}
	for _, h := range got {
		if !want[h] {
			t.Errorf("unexpected derived host %q in %v", h, got)
		}
	}
	// A wildcard bind pulls in the machine hostname.
	hn, _ := os.Hostname()
	got = selfSignedHosts(nil, []string{"0.0.0.0"})
	if hn != "" && !contains(got, hn) {
		t.Errorf("wildcard bind should add hostname %q: %v", hn, got)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestServerConfigManual(t *testing.T) {
	reset(t)
	// Mint a pair with the module's own generator, then serve it as BYO.
	dir := t.TempDir()
	if _, err := loadOrCreateSelfSigned(dir, []string{"byo.example"}); err != nil {
		t.Fatal(err)
	}
	configure(t, map[string]string{
		"tls":      "on",
		"tls_cert": filepath.Join(dir, selfSignedCertFile),
		"tls_key":  filepath.Join(dir, selfSignedKeyFile),
	})
	cfg, err := ServerConfig(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("certificates = %d, want 1", len(cfg.Certificates))
	}
	if cfg.MinVersion != stdtls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2", cfg.MinVersion)
	}
}

func TestServerConfigManualMissingFiles(t *testing.T) {
	reset(t)
	configure(t, map[string]string{"tls": "on", "tls_cert": "/nope/c.pem", "tls_key": "/nope/k.pem"})
	if _, err := ServerConfig(t.TempDir(), nil); err == nil {
		t.Error("missing BYO files should be a hard startup error")
	}
}

// TestACMEManagerPlumbing unit-tests the autocert wiring without any network:
// host policy admits exactly the configured domains, the cache lands in the
// module data dir, and the CA directory override reaches the ACME client.
func TestACMEManagerPlumbing(t *testing.T) {
	reset(t)
	dir := t.TempDir()
	s := settings{
		enabled:     true,
		mode:        ModeACME,
		acmeDomains: []string{"demi.example.com", "alt.example.com"},
		acmeEmail:   "ops@example.com",
		acmeCA:      "https://acme-staging-v02.api.letsencrypt.org/directory",
	}
	m := newACMEManager(filepath.Join(dir, "acme"), s)

	ctx := context.Background()
	for _, d := range s.acmeDomains {
		if err := m.HostPolicy(ctx, d); err != nil {
			t.Errorf("host policy rejected configured domain %q: %v", d, err)
		}
	}
	if err := m.HostPolicy(ctx, "evil.example.net"); err == nil {
		t.Error("host policy admitted an unconfigured domain")
	}
	if m.Email != "ops@example.com" {
		t.Errorf("email = %q", m.Email)
	}
	if m.Client == nil || m.Client.DirectoryURL != s.acmeCA {
		t.Errorf("CA directory override not applied: %+v", m.Client)
	}

	// The full config path: TLS-ALPN challenge protocol advertised, cache dir
	// created 0700.
	current = s
	cfg, err := ServerConfig(dir, nil)
	if err != nil {
		t.Fatalf("ServerConfig(acme): %v", err)
	}
	if cfg.GetCertificate == nil {
		t.Error("acme config missing GetCertificate")
	}
	if !contains(cfg.NextProtos, "acme-tls/1") {
		t.Errorf("NextProtos %v missing acme-tls/1", cfg.NextProtos)
	}
	fi, err := os.Stat(filepath.Join(dir, "acme"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("acme cache dir not created: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("acme cache dir mode = %v, want 0700", fi.Mode().Perm())
	}
}

// TestSelfSignedEndToEnd serves a real HTTPS listener with the module's
// self-signed config and fetches from it with a client that trusts the
// generated certificate — the whole in-process termination path, no network.
func TestSelfSignedEndToEnd(t *testing.T) {
	reset(t)
	dir := t.TempDir()
	configure(t, map[string]string{"tls": "on"})
	cfg, err := ServerConfig(dir, []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}

	var sawTLS bool
	srv := &http.Server{
		TLSConfig: cfg,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawTLS = r.TLS != nil // what core's requestBase keys the https scheme off
			io.WriteString(w, "over tls")
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.ServeTLS(ln, "", "")
	defer srv.Close()

	pool := x509.NewCertPool()
	pemBytes, err := os.ReadFile(filepath.Join(dir, selfSignedCertFile))
	if err != nil {
		t.Fatal(err)
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("could not trust the generated certificate")
	}
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &stdtls.Config{RootCAs: pool}},
	}

	resp, err := client.Get("https://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET over TLS (verified against the generated cert): %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "over tls" {
		t.Errorf("status=%d body=%q", resp.StatusCode, body)
	}
	if !sawTLS {
		t.Error("handler saw r.TLS == nil — https scheme derivation would break")
	}
	if resp.TLS == nil || resp.TLS.Version < stdtls.VersionTLS12 {
		t.Errorf("negotiated TLS version %x, want >= 1.2", resp.TLS.Version)
	}
	if !strings.HasPrefix(resp.Request.URL.String(), "https://") {
		t.Errorf("request did not go over https: %s", resp.Request.URL)
	}
}
