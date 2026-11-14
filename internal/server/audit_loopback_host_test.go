// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build audit

package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestAuditLoopbackListenerRefusesForeignHost: the default instance (no token,
// no --allowed-hosts, loopback listeners) answers a request whatever its Host
// header says. A web page whose own host name is re-pointed at 127.0.0.1 (DNS
// rebinding) is then same-origin with the control plane in the visitor's
// browser: the browser labels its requests Sec-Fetch-Site: same-origin, so the
// cross-origin write guard lets them through, and it may read the responses.
// A listener bound to loopback must refuse a Host that does not name loopback
// (or a configured base URL). audit/03-loopback-host-rebinding.md.
func TestAuditLoopbackListenerRefusesForeignHost(t *testing.T) {
	control, content := newSplitServers(t, Config{})
	publish(t, control, "?slug=notes", "<p>internal notes</p>")

	const foreign = "rebind.attacker.example:8080"
	send := func(method, url, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		// What a browser sends from a page it loaded from http://<foreign>/
		// once that name resolves to this listener.
		req.Host = foreign
		req.Header.Set("Origin", "http://"+foreign)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}

	for _, tc := range []struct {
		name, method, url, body string
	}{
		{"publish", http.MethodPost, control.URL + "/publish?slug=planted", "<p>planted</p>"},
		{"list", http.MethodGet, control.URL + "/list", ""},
		{"read artifact", http.MethodGet, content.URL + "/notes", ""},
		{"delete", http.MethodDelete, control.URL + "/notes", ""},
	} {
		code, body := send(tc.method, tc.url, tc.body)
		if code != http.StatusMisdirectedRequest {
			t.Errorf("%s with Host %s on a loopback listener = %d (%.80q), want 421", tc.name, foreign, code, body)
		}
	}
}
