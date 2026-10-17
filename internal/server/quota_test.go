// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/store"
)

// newQuotaServer serves a fresh store whose aggregate quota is limit.
func newQuotaServer(t *testing.T, limit int64, cfg Config) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SetQuota(limit); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	ts := httptest.NewServer(New(st, cfg).Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

// storeDiskBytes sums every regular file under the store's content dirs.
func storeDiskBytes(t *testing.T, st *store.Store) int64 {
	t.Helper()
	var n int64
	for _, dir := range []string{"blobs", "sources", "sites"} {
		err := filepath.WalkDir(filepath.Join(st.Root(), dir), func(p string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				fi, err := d.Info()
				if err != nil {
					return err
				}
				n += fi.Size()
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
	}
	return n
}

// assertStoreAccounted checks the quota counter against the disk and that no
// temp or scratch file survived.
func assertStoreAccounted(t *testing.T, st *store.Store) {
	t.Helper()
	used, _ := st.Usage()
	if disk := storeDiskBytes(t, st); used != disk {
		t.Errorf("usage = %d, bytes on disk = %d", used, disk)
	}
	for _, dir := range []string{"blobs", "sources", "sites"} {
		ents, _ := os.ReadDir(filepath.Join(st.Root(), dir))
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".tmp-") {
				t.Errorf("temp entry left behind: %s/%s", dir, e.Name())
			}
		}
	}
}

func TestPublishOverQuota507(t *testing.T) {
	ts, st := newQuotaServer(t, 1000, Config{})
	if code, b := do(t, http.MethodPost, ts.URL+"/publish?slug=a", "", strings.Repeat("x", 600)); code != http.StatusCreated {
		t.Fatalf("first publish = %d %q", code, b)
	}
	code, b := do(t, http.MethodPost, ts.URL+"/publish?slug=b", "", strings.Repeat("x", 600))
	if code != http.StatusInsufficientStorage {
		t.Fatalf("publish past the quota = %d %q, want 507", code, b)
	}
	if code, _ := do(t, http.MethodGet, ts.URL+"/b", "", ""); code != http.StatusNotFound {
		t.Errorf("rejected artifact is served: %d", code)
	}
	code, b = do(t, http.MethodPost, ts.URL+"/publish?render=md&slug=c", "", "# "+strings.Repeat("x", 600))
	if code != http.StatusInsufficientStorage {
		t.Fatalf("rendered publish past the quota = %d %q, want 507", code, b)
	}
	assertStoreAccounted(t, st)
}

func TestPublishOverMaxUploadReleasesQuota(t *testing.T) {
	ts, st := newQuotaServer(t, 1000, Config{MaxUpload: 700})
	if code, _ := do(t, http.MethodPost, ts.URL+"/publish", "", strings.Repeat("x", 900)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize publish = %d, want 413", code)
	}
	if code, b := do(t, http.MethodPost, ts.URL+"/publish", "", strings.Repeat("x", 700)); code != http.StatusCreated {
		t.Fatalf("publish that fits after a 413 = %d %q", code, b)
	}
	assertStoreAccounted(t, st)
}

func TestSitePublishQuotaCountsScratchAndFiles(t *testing.T) {
	// The archive (about 1.5 KiB of tar for 600 bytes of file) and its
	// extracted file must fit together: the scratch copy is held until the
	// publish ends.
	files := []srcFile{{"index.html", strings.Repeat("A", 600)}}
	archive := makeTar(t, files)
	ts, st := newQuotaServer(t, int64(len(archive))+300, Config{})
	resp, rb := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", archive)
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("site publish past the quota = %d %q, want 507", resp.StatusCode, rb)
	}
	if used, _ := st.Usage(); used != 0 {
		t.Errorf("usage after a refused site publish = %d, want 0", used)
	}
	assertStoreAccounted(t, st)

	// An archive larger than the quota by itself is refused while it uploads.
	big := makeTar(t, []srcFile{{"index.html", strings.Repeat("B", 4096)}})
	resp, rb = postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", big)
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("archive past the quota = %d %q, want 507", resp.StatusCode, rb)
	}
	assertStoreAccounted(t, st)
}

func TestSitePublishQuotaReleasedOnFailureAndReplace(t *testing.T) {
	saved := siteDecompressBudget
	siteDecompressBudget = 4096
	defer func() { siteDecompressBudget = saved }()

	ts, st := newQuotaServer(t, 64<<10, Config{})
	bomb := makeTar(t, []srcFile{{"a.html", strings.Repeat("A", 3000)}, {"b.html", strings.Repeat("B", 3000)}})
	resp, rb := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", bomb)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-budget site = %d %q, want 413", resp.StatusCode, rb)
	}
	if used, _ := st.Usage(); used != 0 {
		t.Errorf("usage after a decompress-budget failure = %d, want 0", used)
	}

	site := makeTar(t, []srcFile{{"index.html", strings.Repeat("C", 2000)}})
	for i := 0; i < 4; i++ {
		resp, rb := postSite(t, ts.URL, "demo", "fmt=tar", "application/x-tar", site)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("site publish %d = %d %q", i, resp.StatusCode, rb)
		}
	}
	if used, _ := st.Usage(); used != 2000 {
		t.Errorf("usage after site re-publishes = %d, want 2000", used)
	}
	assertStoreAccounted(t, st)
}

func TestRerenderOverQuota507KeepsPage(t *testing.T) {
	ts, st := newQuotaServer(t, 0, Config{})
	publish(t, ts, "?render=md&slug=field-notes", rerenderSource)
	used, _ := st.Usage()
	if used != 0 {
		t.Fatalf("usage with no quota = %d, want 0", used)
	}
	// Leave room for less than a second copy of the page: a rebake writes the
	// new bytes before it drops the old ones.
	if err := st.SetQuota(storeDiskBytes(t, st) + 100); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	resp := postJSON(t, ts.URL+"/rerender/field-notes", nil)
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("rerender past the quota = %d, want 507", resp.StatusCode)
	}
	if code, body := do(t, http.MethodGet, ts.URL+"/field-notes", "", ""); code != http.StatusOK || !strings.Contains(string(body), "must survive every rebake") {
		t.Errorf("page after a refused rerender: %d", code)
	}
	assertStoreAccounted(t, st)
}
