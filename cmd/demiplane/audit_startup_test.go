// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build audit

package main

import (
	"testing"
)

// TestAuditBlankTokenEnvIsRefused: an empty token file is a startup error,
// but DEMIPLANE_TOKEN set to an empty or blank value (a failed command
// substitution, a missing secret) starts the server with no authentication.
// audit/08-blank-token-env-starts-open.md.
func TestAuditBlankTokenEnvIsRefused(t *testing.T) {
	for _, v := range []string{"", "  \n"} {
		t.Setenv("DEMIPLANE_TOKEN", v)
		tok, err := resolveToken("")
		if err == nil {
			t.Errorf("resolveToken with DEMIPLANE_TOKEN=%q = %q, nil: want an error, as for an empty token file", v, tok)
		}
	}
}
