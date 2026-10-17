// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

// TestReceiveRefusesRouteNames runs the real `demiplane receive` (the SSH
// ingest path, which never builds an HTTP handler) and checks it refuses the
// same route names HTTP publishing refuses, core and module alike. Before
// reservations were established at registration, receive accepted these and
// created artifacts that a route then shadowed.
func TestReceiveRefusesRouteNames(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "reserved-token"})
	for _, name := range []string{"gallery", "connect", "rerender", "favicon.svg", "reply", "replies", "answer"} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(binPath, "receive", "--store", t.TempDir(), "--slug", name)
			cmd.Stdin = strings.NewReader("shadowed")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("receive --slug %s succeeded: %s", name, out)
			}
			if !strings.Contains(string(out), "reserved") {
				t.Errorf("receive --slug %s failed without naming the reservation: %s", name, out)
			}

			res := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug="+name,
				strings.NewReader("shadowed"), srv.authHeader())
			if res.Status != http.StatusBadRequest {
				t.Errorf("HTTP publish ?slug=%s = %d, want 400", name, res.Status)
			}
		})
	}
}
