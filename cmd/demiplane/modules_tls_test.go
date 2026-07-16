// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build tls

package main

// Wiring tests for the native-TLS module (mirrors the reply module's cmd
// wiring pattern): the tls_* config keys are recognized exactly when the
// module is compiled in, the applier validates loudly, and the moduleTLS
// listener seam is installed and honours the off-by-default contract.

import (
	"os"
	"path/filepath"
	"testing"

	tlsmod "github.com/daisandapex/demiplane/internal/modules/tls"
	"github.com/daisandapex/demiplane/internal/server"
	"github.com/daisandapex/demiplane/internal/store"
)

func TestTLSConfigKeysRecognized(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	writeFile(t, p, "tls = on\ntls_hosts = demi.internal, 203.0.113.7\n")
	m, err := loadConfig(p)
	if err != nil {
		t.Fatalf("tls keys should parse in a -tags tls build: %v", err)
	}
	if m["tls"] != "on" || m["tls_hosts"] != "demi.internal, 203.0.113.7" {
		t.Errorf("parsed config = %v", m)
	}
}

func TestTLSApplierFailsLoudly(t *testing.T) {
	t.Cleanup(func() { _ = tlsmod.Configure(map[string]string{}) })
	if err := applyModuleConfig(map[string]string{"tls": "sideways"}); err == nil {
		t.Error("an invalid tls value should be a hard startup error")
	}
	if err := applyModuleConfig(map[string]string{"tls": "on", "tls_cert": "only-half"}); err == nil {
		t.Error("tls_cert without tls_key should be a hard startup error")
	}
}

// TestModuleTLSSeam covers the listener seam end to end: absent/off config
// yields a nil *tls.Config (plain HTTP — the byte-identical default), and
// enabling self-signed TLS yields a servable config with the material
// persisted under the module data dir.
func TestModuleTLSSeam(t *testing.T) {
	if moduleTLS == nil {
		t.Fatal("moduleTLS seam not installed by the tls wiring file")
	}
	t.Cleanup(func() { _ = tlsmod.Configure(map[string]string{}) })

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	host := server.New(st, server.Config{})

	// Module compiled in, TLS not enabled → nil config, no error.
	if err := tlsmod.Configure(map[string]string{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := moduleTLS(host, []string{"127.0.0.1"})
	if err != nil || cfg != nil {
		t.Errorf("unconfigured: (%v, %v), want (nil, nil)", cfg, err)
	}

	// tls = on → self-signed default, persisted in <store>/modules/tls.
	if err := tlsmod.Configure(map[string]string{"tls": "on"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = moduleTLS(host, []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if cfg == nil || len(cfg.Certificates) != 1 {
		t.Fatalf("enabled: config = %+v, want one self-signed certificate", cfg)
	}
	certPath := filepath.Join(st.Root(), "modules", "tls", "self-signed-cert.pem")
	if _, err := os.Stat(certPath); err != nil {
		t.Errorf("self-signed certificate not persisted at %s: %v", certPath, err)
	}
}

func TestDisplayURLScheme(t *testing.T) {
	if got := displayURL("", "127.0.0.1:8080", false); got != "http://127.0.0.1:8080" {
		t.Errorf("plain: %q", got)
	}
	if got := displayURL("", "127.0.0.1:8080", true); got != "https://127.0.0.1:8080" {
		t.Errorf("tls: %q", got)
	}
	// An explicit base URL is authoritative either way.
	if got := displayURL("https://demi.example", "127.0.0.1:8080", false); got != "https://demi.example" {
		t.Errorf("explicit base: %q", got)
	}
}
