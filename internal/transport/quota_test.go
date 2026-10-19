// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/v3/internal/logsafe"
	"github.com/daisandapex/demiplane/v3/internal/store"
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

// TestDirectorySyncErrorOmitsTheSlug: a directory-sync entry that fails to
// publish is reported by its slug tag, never by its name, which is the slug
// of the artifact it would replace and may be a capability.
func TestDirectorySyncErrorOmitsTheSlug(t *testing.T) {
	st := newStore(t)
	const name = "Zk3pQ9vXr2LmNw8TbYc4Hd"
	if err := st.SetQuota(1000); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	tarball := makeTar(t, map[string]string{name: strings.Repeat("x", 1500)})
	err := Receive(st, ReceiveOptions{Untar: true}, bytes.NewReader(tarball), &bytes.Buffer{})
	if !errors.Is(err, store.ErrQuota) {
		t.Fatalf("receive past the quota: %v, want ErrQuota", err)
	}
	if msg := logsafe.Err(err); strings.Contains(msg, name) {
		t.Fatalf("error names the entry: %s", msg)
	}
}
