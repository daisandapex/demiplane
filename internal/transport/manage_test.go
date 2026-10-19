// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

func TestManageReceiveUntarDefaultTTL(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.SetDefaultTTL(time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)
		if err = tw.WriteHeader(&tar.Header{Name: "entry.txt", Mode: 0600, Size: 1}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte("x"))
		tw.Close()
		if err = Receive(st, ReceiveOptions{Untar: true, PutOptions: store.PutOptions{TTLSet: explicit}}, &archive, io.Discard); err != nil {
			t.Fatal(err)
		}
		// Use the same flat identifier as the untar ingestion path.
		slug, err := pathToSlug("entry.txt")
		if err != nil {
			t.Fatal(err)
		}
		a, err := st.Patch(slug, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if a.ExpiresAt.IsZero() != explicit {
			t.Fatalf("untar TTL presence lost: explicit=%t expires=%v", explicit, a.ExpiresAt)
		}
	}
}
