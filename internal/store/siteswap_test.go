// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const swapCrashEnv = "DEMIPLANE_TEST_SITE_SWAP_CRASH"

// ageSite backdates a published site's directory past the orphan grace, as
// an old site's is: a rename keeps it.
func ageSite(t *testing.T, dir, site string) {
	t.Helper()
	old := time.Now().Add(-2 * orphanGrace)
	if err := os.Chtimes(filepath.Join(dir, "data", "sites", site), old, old); err != nil {
		t.Fatal(err)
	}
}

// readSite returns a published site file's bytes.
func readSite(t *testing.T, s *Store, site, name string) string {
	t.Helper()
	f, _, _, err := s.OpenSiteFile(site, name)
	if err != nil {
		t.Fatalf("open %s/%s: %v", site, name, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSiteSwapCrashChild is the child of TestSiteSwapCrashRecovers: it starts
// a replace and exits right after the live site has been moved aside.
func TestSiteSwapCrashChild(t *testing.T) {
	dir := os.Getenv(swapCrashEnv)
	if dir == "" {
		t.Skip("run by TestSiteSwapCrashRecovers")
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	afterSiteBackup = func() { os.Exit(3) }
	publishSiteBody(s, "site", "index.html", "new")
	t.Fatal("the replace finished; the crash hook did not run")
}

// TestSiteSwapCrashRecovers: a process that dies between moving the live site
// aside and moving the new tree into place leaves the old site recoverable,
// and the next Open restores it instead of reclaiming it.
func TestSiteSwapCrashRecovers(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishSiteBody(s, "site", "index.html", "old"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	ageSite(t, dir, "site")

	cmd := exec.Command(os.Args[0], "-test.run=^TestSiteSwapCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), swapCrashEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("child: %v\n%s", err, out)
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if got := readSite(t, s, "site", "index.html"); got != "old" {
		t.Fatalf("site after a crashed replace = %q, want the committed %q", got, "old")
	}
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if got := readSite(t, s, "site", "index.html"); got != "old" {
		t.Fatalf("site after Reconcile = %q, want %q", got, "old")
	}
	if err := s.SetQuota(1 << 20); err != nil {
		t.Fatal(err)
	}
	assertAccounted(t, s)
	if n := siteEntries(t, s); n != 2 {
		t.Fatalf("%d site entries after recovery and Reconcile, want 2 (the site and its file)", n)
	}
	// A replace now works as usual.
	if err := publishSiteBody(s, "site", "index.html", "new"); err != nil {
		t.Fatal(err)
	}
	if got := readSite(t, s, "site", "index.html"); got != "new" {
		t.Fatalf("site after a replace = %q, want %q", got, "new")
	}
	assertAccounted(t, s)
}

// TestSiteSwapFailedRollbackRecovers: when moving the new tree into place
// fails and so does moving the old site back, the old site survives as the
// only copy, is never reclaimed, and is restored by the next Open and by
// Reconcile.
func TestSiteSwapFailedRollbackRecovers(t *testing.T) {
	for _, recover := range []string{"open", "reconcile"} {
		t.Run(recover, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetQuota(1 << 20); err != nil {
				t.Fatal(err)
			}
			if err := publishSiteBody(s, "site", "index.html", strings.Repeat("o", 300)); err != nil {
				t.Fatal(err)
			}
			ageSite(t, dir, "site")
			final := filepath.Join(dir, "data", "sites", "site")
			siteRename = func(from, to string) error {
				if to == final {
					return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
				}
				return os.Rename(from, to)
			}
			t.Cleanup(func() { siteRename = os.Rename })
			if err := publishSiteBody(s, "site", "index.html", "new"); err == nil {
				t.Fatal("replace with failing renames reported success")
			}
			siteRename = os.Rename
			if recover == "open" {
				s.Close()
				if s, err = Open(dir); err != nil {
					t.Fatal(err)
				}
				if err := s.SetQuota(1 << 20); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			if got := readSite(t, s, "site", "index.html"); got != strings.Repeat("o", 300) {
				t.Fatalf("site after recovery = %q, want the committed bytes", got)
			}
			if used, _ := s.Usage(); used != 2*metaAllowance+300 {
				t.Fatalf("usage after recovery = %d, want %d", used, 2*metaAllowance+300)
			}
			assertAccounted(t, s)
		})
	}
}
