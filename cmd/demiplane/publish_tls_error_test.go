// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// hostileSAN is a DNS name an on-path attacker puts in a self-made
// certificate. Go reports a hostname mismatch before it builds the chain, so
// the attacker needs no trusted certificate for this text to reach the error.
const hostileSAN = "a\x1b]0;PWNED\x07\x1b[2J\x1b[1;1H\r\nforged\x7f.published-ok.example"

// selfCert makes a self-signed certificate for the given DNS names.
func selfCert(t *testing.T, names ...string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              names,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf
}

// tlsControlPlane starts a TLS listener on 127.0.0.1 that answers a publish
// with a good URL. pick chooses the certificate for the n-th handshake. The
// returned base URL names localhost, so Go checks the certificate's DNS SANs.
func tlsControlPlane(t *testing.T, pick func(n int) *tls.Certificate) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Connection", "close")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "https://content.example/s", "slug": "s"})
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	var mu sync.Mutex
	n := 0
	srv.TLS = &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		mu.Lock()
		defer mu.Unlock()
		c := pick(n)
		n++
		return c, nil
	}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
}

// assertNoRawControl fails when s holds a byte below 0x20 (other than one
// final newline when allowed), DEL or a C1 control.
func assertNoRawControl(t *testing.T, label, s string, finalNewline bool) {
	t.Helper()
	body := s
	if finalNewline {
		body = strings.TrimSuffix(s, "\n")
	}
	for i := 0; i < len(body); i++ {
		if c := body[i]; c < 0x20 || c == 0x7f {
			t.Fatalf("%s: raw byte %#x at %d reached the terminal: %q", label, c, i, s)
		}
	}
	for _, r := range body {
		if r >= 0x80 && r <= 0x9f {
			t.Fatalf("%s: C1 control %U reached the terminal: %q", label, r, s)
		}
	}
}

func TestPublishEscapesTLSErrorText(t *testing.T) {
	hostile, _ := selfCert(t, hostileSAN)
	base := tlsControlPlane(t, func(int) *tls.Certificate { return &hostile })
	src := filepath.Join(t.TempDir(), "p.html")
	writeFile(t, src, "<p>x</p>")

	err := runPublish([]string{"--url", base, src})
	if err == nil || !strings.Contains(err.Error(), "certificate is valid for") {
		t.Fatalf("runPublish = %v, want the certificate error", err)
	}
	assertNoRawControl(t, "publish TLS error", err.Error(), false)
	if !strings.Contains(err.Error(), "PWNED") {
		t.Errorf("the escaped SAN is gone from the error: %q", err)
	}
}

func TestWatchEscapesTLSErrorText(t *testing.T) {
	good, goodLeaf := selfCert(t, "localhost")
	hostile, _ := selfCert(t, hostileSAN)
	base := tlsControlPlane(t, func(n int) *tls.Certificate {
		if n == 0 {
			return &good
		}
		return &hostile
	})
	roots := x509.NewCertPool()
	roots.AddCert(goodLeaf)
	client := noRedirectClient()
	client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DisableKeepAlives: true}

	src := filepath.Join(t.TempDir(), "page.html")
	writeFile(t, src, "<h1>one</h1>")
	stubOpeners(t)
	p := &publisher{client: client, base: base, filename: "page.html"}
	waitIdle := watchIdleSignal(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			done := make(chan error, 1)
			go func() { done <- runWatch(ctx, p, src, false) }()
			if !waitIdle() {
				t.Error("watchLoop never recorded its baseline")
			}
			writeFile(t, src, "<h1>two</h1>")
			later := time.Now().Add(time.Hour)
			_ = os.Chtimes(src, later, later)
			if !waitIdle() {
				t.Error("the republish callback never returned")
			}
			cancel()
			if err := <-done; err != context.Canceled {
				t.Errorf("runWatch = %v, want context.Canceled", err)
			}
		})
	})

	_, after, ok := strings.Cut(stderr, "(Ctrl-C to stop)\n")
	if !ok {
		t.Fatalf("stderr has no watching line: %q", stderr)
	}
	if !strings.Contains(after, "republish failed") || !strings.Contains(after, "certificate is valid for") {
		t.Fatalf("no certificate error reported for the republish: %q", stderr)
	}
	for _, line := range strings.SplitAfter(after, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "demiplane publish: ") {
			t.Errorf("a forged line reached stderr: %q", line)
		}
		{
			assertNoRawControl(t, "watch republish TLS error", line, true)
		}
	}
}

// TestClientCommandsHideTLSErrorText: the other client commands report a
// failed request with fixed text, so a hostile certificate's names never
// reach the terminal from them either.
func TestClientCommandsHideTLSErrorText(t *testing.T) {
	t.Setenv("DEMIPLANE_TOKEN", "tok")
	hostile, _ := selfCert(t, hostileSAN)
	base := tlsControlPlane(t, func(int) *tls.Certificate { return &hostile })
	cases := []struct {
		name string
		run  func(out io.Writer) error
	}{
		{"list", func(out io.Writer) error { return runList([]string{"--url", base}, out) }},
		{"list --json", func(out io.Writer) error { return runList([]string{"--url", base, "--json"}, out) }},
		{"delete slug", func(out io.Writer) error {
			return runDelete([]string{"--url", base, "--yes", "x"}, out, strings.NewReader(""), false)
		}},
		{"delete --match", func(out io.Writer) error {
			return runDelete([]string{"--url", base, "--match", "*", "--yes"}, out, strings.NewReader(""), false)
		}},
		{"ttl", func(out io.Writer) error { return runPatch("ttl", []string{"--url", base, "x", "1h"}, out) }},
		{"pin", func(out io.Writer) error { return runPatch("pin", []string{"--url", base, "x"}, out) }},
		{"unpin", func(out io.Writer) error { return runPatch("unpin", []string{"--url", base, "x"}, out) }},
		{"delete-site", func(io.Writer) error { return runDeleteSite([]string{"--url", base, "site"}) }},
	}
	for _, tc := range cases {
		var out strings.Builder
		var err error
		stderr := captureStderr(t, func() { err = tc.run(&out) })
		if err == nil {
			t.Errorf("%s: no error from a bad certificate", tc.name)
			continue
		}
		t.Logf("%s: %v", tc.name, err)
		for label, s := range map[string]string{"error": err.Error(), "stdout": out.String(), "stderr": stderr} {
			if strings.Contains(s, "PWNED") || strings.Contains(s, "published-ok") {
				t.Errorf("%s %s quotes the certificate: %q", tc.name, label, s)
			}
			assertTerminalSafe(t, tc.name+" "+label, s, true)
		}
	}
}
