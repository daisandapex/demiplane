// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/store"
)

func TestReceiveHonorsQuota(t *testing.T) {
	st := newStore(t)
	if err := st.SetQuota(1000); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	var out bytes.Buffer
	opts := ReceiveOptions{PutOptions: store.PutOptions{Slug: "big"}}
	err := Receive(st, opts, strings.NewReader(strings.Repeat("x", 1500)), &out)
	if !errors.Is(err, store.ErrQuota) {
		t.Fatalf("receive past the quota: %v, want ErrQuota", err)
	}
	if used, _ := st.Usage(); used != 0 {
		t.Errorf("usage after a refused receive = %d, want 0", used)
	}
}
