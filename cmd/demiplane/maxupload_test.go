// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"
	"time"
)

// TestReceiveDefaultMaxUploadBounded locks the pre-public hardening: `receive`
// must cap uploads by default so a fresh SSH forced-command install is not an
// unbounded disk-fill target, while `--max-upload=0` stays the explicit unbounded
// opt-out. `serve` shares the same defaultMaxUpload constant, and its enforcement
// path is covered by internal/server TestMaxUploadCap.
func TestReceiveDefaultMaxUploadBounded(t *testing.T) {
	if defaultMaxUpload != 100<<20 {
		t.Errorf("defaultMaxUpload = %d, want 100 MiB (%d)", defaultMaxUpload, 100<<20)
	}

	fs, c := newReceiveFlagSet()
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	if c.maxUpload != defaultMaxUpload {
		t.Errorf("default --max-upload = %d, want %d (defaultMaxUpload)", c.maxUpload, defaultMaxUpload)
	}
	if c.maxUpload <= 0 {
		t.Errorf("default --max-upload = %d, want a bounded (>0) cap", c.maxUpload)
	}

	// The documented "unlimited" escape hatch must still parse to 0.
	fs0, c0 := newReceiveFlagSet()
	if err := fs0.Parse([]string{"--max-upload=0"}); err != nil {
		t.Fatalf("parse opt-out: %v", err)
	}
	if c0.maxUpload != 0 {
		t.Errorf("--max-upload=0 = %d, want 0 (unbounded opt-out)", c0.maxUpload)
	}
}

// TestStoreQuotaDefaultUnlimited pins the documented default: no aggregate
// quota unless the operator sets one.
func TestStoreQuotaDefaultUnlimited(t *testing.T) {
	fs, c := newReceiveFlagSet()
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	if c.storeQuota != 0 {
		t.Errorf("default --store-quota = %d, want 0 (unlimited)", c.storeQuota)
	}
	fs2, c2 := newReceiveFlagSet()
	if err := fs2.Parse([]string{"--store-quota=1048576"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c2.storeQuota != 1<<20 {
		t.Errorf("--store-quota = %d, want 1048576", c2.storeQuota)
	}
}

// TestOffIfZero pins the flag mapping: a flag's 0 ("no limit") reaches the
// server as negative (disabled), never as 0 (which the server reads as
// "use the default").
func TestOffIfZero(t *testing.T) {
	if offIfZero(0) != -1 || offIfZero(int64(0)) != -1 || offIfZero(time.Duration(0)) != -1 {
		t.Fatal("0 must map to -1")
	}
	if offIfZero(5) != 5 || offIfZero(30*time.Second) != 30*time.Second {
		t.Fatal("non-zero values must pass through")
	}
}
