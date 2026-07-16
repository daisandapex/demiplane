// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package tls is demiplane's native transport-encryption module: it terminates
// TLS on both planes (control + content) inside the binary, so a deployment
// without a WireGuard mesh or a reverse proxy still gets encrypted transport.
// It is opt-in twice over — compiled in only with `go build -tags tls` (see
// cmd/demiplane/modules_tls.go), and OFF at runtime until the operator sets
// `tls = on` in the config file. Design: docs/adr/0004-native-tls-module.md.
//
// Three certificate sources, resolved from the config keys:
//
//   - self-signed (default): a persistent, locally generated certificate under
//     <store>/modules/tls/ — zero further configuration.
//   - ACME / Let's Encrypt: `tls_acme_domains` names the public hostname(s);
//     certificates are obtained and renewed automatically via the TLS-ALPN-01
//     challenge (the control listener must be reachable on port 443).
//   - BYO (manual): `tls_cert` + `tls_key` point at operator-managed PEM files.
//
// The module never touches core: cmd/demiplane consumes it through the
// build-tag-gated moduleTLS seam and hands the resulting *tls.Config to its
// listeners. Core's scheme derivation (requestBase) keys off r.TLS, so minted
// URLs turn https automatically.
package tls

import (
	stdtls "crypto/tls"
	"fmt"
	"log"
	"strings"

	"github.com/daisandapex/demiplane/internal/module"
)

// Mode names a certificate source.
type Mode string

const (
	// ModeOff — TLS not enabled; listeners stay plain HTTP.
	ModeOff Mode = "off"
	// ModeSelfSigned — generate (once) and serve a persistent local certificate.
	ModeSelfSigned Mode = "self-signed"
	// ModeACME — obtain and renew certificates from an ACME CA (Let's Encrypt).
	ModeACME Mode = "acme"
	// ModeManual — serve an operator-supplied certificate/key pair.
	ModeManual Mode = "manual"
)

// ConfigKeys is the exact config-file surface this module owns, registered by
// the cmd wiring file so the keys are recognized iff the module is compiled in.
var ConfigKeys = []string{
	"tls",              // on|off (default off) — the master switch
	"tls_cert",         // path to a PEM certificate (manual mode; requires tls_key)
	"tls_key",          // path to the PEM private key (manual mode; requires tls_cert)
	"tls_hosts",        // comma-separated SANs for the self-signed cert (default: derived from the bind addresses)
	"tls_acme_domains", // comma-separated public hostnames (ACME mode)
	"tls_acme_email",   // contact email for the ACME account (optional)
	"tls_acme_ca",      // ACME directory URL override (default Let's Encrypt production)
}

// settings is the parsed module configuration. Package state, written once by
// Configure during single-threaded startup (the same contract as the reply
// module's hook config) and read-only afterwards.
type settings struct {
	enabled     bool
	mode        Mode
	certFile    string
	keyFile     string
	hosts       []string
	acmeDomains []string
	acmeEmail   string
	acmeCA      string
}

var current = settings{mode: ModeOff}

// tlsModule registers the module in the global registry (introspection only —
// TLS adds no HTTP routes; its seam is the listener configuration).
type tlsModule struct{}

// Name doubles as the ModuleDataDir key: certificates and the ACME cache live
// under <store>/modules/tls/.
func (tlsModule) Name() string { return "tls" }

func init() { module.Register(tlsModule{}) }

// Configure parses and validates the module's config keys. Called from the
// registered config applier at startup; any error is a hard startup error
// (fail-loud contract). Absent keys read as "".
func Configure(cfg map[string]string) error {
	s := settings{mode: ModeOff}

	if v := cfg["tls"]; v != "" {
		on, err := onOff("tls", v)
		if err != nil {
			return err
		}
		s.enabled = on
	}

	s.certFile = strings.TrimSpace(cfg["tls_cert"])
	s.keyFile = strings.TrimSpace(cfg["tls_key"])
	s.hosts = splitList(cfg["tls_hosts"])
	s.acmeDomains = splitList(cfg["tls_acme_domains"])
	s.acmeEmail = strings.TrimSpace(cfg["tls_acme_email"])
	s.acmeCA = strings.TrimSpace(cfg["tls_acme_ca"])

	// Certificate-source resolution: manual beats ACME beats self-signed, and
	// mixing sources is ambiguous — refuse rather than guess.
	manual := s.certFile != "" || s.keyFile != ""
	acme := len(s.acmeDomains) > 0
	switch {
	case manual && acme:
		return fmt.Errorf("tls: tls_cert/tls_key and tls_acme_domains are mutually exclusive — pick one certificate source")
	case manual && (s.certFile == "" || s.keyFile == ""):
		return fmt.Errorf("tls: tls_cert and tls_key must be set together")
	case manual:
		s.mode = ModeManual
	case acme:
		s.mode = ModeACME
	default:
		s.mode = ModeSelfSigned
	}

	// Keys that belong to a mode other than the resolved one would be silently
	// ignored — fail loudly instead (same philosophy as an unknown key).
	if s.mode != ModeACME && (s.acmeEmail != "" || s.acmeCA != "") {
		return fmt.Errorf("tls: tls_acme_email/tls_acme_ca require tls_acme_domains")
	}
	if s.mode != ModeSelfSigned && len(s.hosts) > 0 {
		return fmt.Errorf("tls: tls_hosts is only meaningful for the self-signed default — remove it or the other certificate source")
	}

	if !s.enabled {
		s.mode = ModeOff
		// Staged-but-disabled config is legitimate (an operator preparing a
		// cutover); say so once rather than silently ignoring the keys.
		if manual || acme || len(s.hosts) > 0 {
			log.Print("tls: tls_* keys present but tls is off — listeners stay plain HTTP")
		}
	}

	current = s
	return nil
}

// Enabled reports whether the operator turned TLS on.
func Enabled() bool { return current.enabled }

// ActiveMode returns the resolved certificate source (ModeOff when disabled).
func ActiveMode() Mode { return current.mode }

// ServerConfig builds the *tls.Config both listeners serve with. dataDir is
// the module's private storage (self-signed material, the ACME cache);
// bindHosts are the hosts of the --bind/--content-bind addresses, used to
// derive self-signed SANs when tls_hosts is not set. Returns (nil, nil) when
// TLS is not enabled.
func ServerConfig(dataDir string, bindHosts []string) (*stdtls.Config, error) {
	switch current.mode {
	case ModeOff:
		return nil, nil
	case ModeManual:
		cert, err := stdtls.LoadX509KeyPair(current.certFile, current.keyFile)
		if err != nil {
			return nil, fmt.Errorf("tls: load certificate: %w", err)
		}
		log.Printf("tls: serving operator-supplied certificate %s", current.certFile)
		return baseConfig(cert), nil
	case ModeACME:
		return acmeConfig(dataDir, current)
	case ModeSelfSigned:
		cert, err := loadOrCreateSelfSigned(dataDir, selfSignedHosts(current.hosts, bindHosts))
		if err != nil {
			return nil, err
		}
		return baseConfig(cert), nil
	}
	return nil, fmt.Errorf("tls: unknown mode %q", current.mode)
}

// baseConfig wraps a single certificate in the module's baseline TLS policy.
func baseConfig(cert stdtls.Certificate) *stdtls.Config {
	return &stdtls.Config{
		MinVersion:   stdtls.VersionTLS12,
		Certificates: []stdtls.Certificate{cert},
	}
}

// onOff mirrors the cmd config parser's on/off vocabulary; duplicated here (7
// lines) so the module package stays importable without the cmd package.
func onOff(key, v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("%s: want on|off, got %q", key, v)
}

// splitList parses a comma-separated config value into trimmed, non-empty items.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
