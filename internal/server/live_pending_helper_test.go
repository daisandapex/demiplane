// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import "testing"

// assertNoPending fails if any pending reservation outlived its request.
func assertNoPending(t *testing.T, h *liveHub) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending != 0 || len(h.pendingClient) != 0 {
		t.Errorf("pending reservations leaked: %d in all, %v by client", h.pending, h.pendingClient)
	}
}
