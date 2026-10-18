// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestServerSpeaksHTTP1OverTLS: a TLS listener does not negotiate HTTP/2,
// whose streams could outlive request admission.
func TestServerSpeaksHTTP1OverTLS(t *testing.T) {
	certSrc := httptest.NewTLSServer(http.NotFoundHandler())
	certs := certSrc.TLS.Certificates
	certSrc.Close()
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	// Offering h2 in the config (as autocert's does) must not bring it back.
	srv := newHTTPServer("", ok, &tls.Config{Certificates: certs, NextProtos: []string{"h2", "http/1.1"}}, 0, time.Minute)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close() })
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		ForceAttemptHTTP2: true,
	}}
	resp, err := client.Get("https://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ProtoMajor != 1 {
		t.Fatalf("negotiated %s, want HTTP/1.1", resp.Proto)
	}
}
