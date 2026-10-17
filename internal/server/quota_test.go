// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	arts, err := st.List(store.DefaultOwner)
	if err != nil {
		t.Fatal(err)
	}
	entries := siteEntryCount(t, st)
	if want := storeDiskBytes(t, st) + (int64(len(arts))+entries)*store.MetaAllowance; used != want {
		t.Errorf("usage = %d, bytes on disk + %d row and %d site-entry allowances = %d", used, len(arts), entries, want)
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
	ts, st := newQuotaServer(t, 1000+store.MetaAllowance, Config{})
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
	ts, st := newQuotaServer(t, 1000+store.MetaAllowance, Config{MaxUpload: 700})
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
	// The bytes plus two entry allowances: the site's directory and its file.
	if used, _ := st.Usage(); used != 2000+2*store.MetaAllowance {
		t.Errorf("usage after site re-publishes = %d, want %d", used, 2000+2*store.MetaAllowance)
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

// TestPublishMetadataBounded: a filename hint over the cap is a 400, and
// empty artifacts count against the quota through their row allowance, so a
// flood of them gets 507 instead of growing the metadata database.
func TestPublishMetadataBounded(t *testing.T) {
	const rows = 5
	ts, st := newQuotaServer(t, rows*store.MetaAllowance, Config{})
	long := strings.Repeat("f", 64<<10) + ".txt"
	if code, b := do(t, http.MethodPost, ts.URL+"/publish?slug=long&filename="+long, "", ""); code != http.StatusBadRequest {
		t.Fatalf("64 KiB filename hint = %d %q, want 400", code, b)
	}
	for i := 0; i < rows; i++ {
		if code, b := do(t, http.MethodPost, ts.URL+"/publish", "", ""); code != http.StatusCreated {
			t.Fatalf("empty artifact %d = %d %q, want 201", i, code, b)
		}
	}
	if code, b := do(t, http.MethodPost, ts.URL+"/publish", "", ""); code != http.StatusInsufficientStorage {
		t.Fatalf("empty artifact past the quota = %d %q, want 507", code, b)
	}
	assertStoreAccounted(t, st)
}

// siteEntryCount counts the files and directories of the published sites.
func siteEntryCount(t *testing.T, st *store.Store) int64 {
	t.Helper()
	root := filepath.Join(st.Root(), "sites")
	var n int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if p != root {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk sites: %v", err)
	}
	return n
}

// emptyFileArchive is n empty files, each under its own nested directories.
func emptyFileArchive(t *testing.T, n int) []byte {
	t.Helper()
	var files []srcFile
	for i := 0; i < n; i++ {
		files = append(files, srcFile{fmt.Sprintf("p%03d/a/b/c/d/e/f/g/index.html", i), ""})
	}
	return makeTar(t, files)
}

// TestSitePublishEntriesCountAgainstQuota: a site's files and directories
// count against the quota even when empty, so archives of empty files under
// fresh site names cannot be published without limit, and the charge
// survives a recount and a restart.
func TestSitePublishEntriesCountAgainstQuota(t *testing.T) {
	const limit = 64 << 10
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuota(limit); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(st, Config{}).Handler())

	flood := emptyFileArchive(t, 100)
	t.Logf("flood archive: %d bytes", len(flood))
	refused := 0
	for i := 0; i < 10; i++ {
		resp, rb := postSite(t, ts.URL, fmt.Sprintf("flood%d", i), "fmt=tar", "application/x-tar", flood)
		switch resp.StatusCode {
		case http.StatusInsufficientStorage:
			refused++
		case http.StatusCreated:
		default:
			t.Fatalf("flood publish %d = %d %q", i, resp.StatusCode, rb)
		}
	}
	used, _ := st.Usage()
	t.Logf("after 10 flood publishes: %d refused, usage %d, %d site entries on disk", refused, used, siteEntryCount(t, st))
	if refused != 10 {
		t.Fatalf("%d of 10 archives of 100 empty files refused under a %d-byte quota, want all", refused, limit)
	}
	if n := siteEntryCount(t, st); n != 0 {
		t.Fatalf("refused publishes left %d site entries", n)
	}

	// One empty file one directory down: the site, its directory and the
	// file, three allowances.
	small := makeTar(t, []srcFile{{"a/index.html", ""}})
	const per = 3 * store.MetaAllowance
	ok := 0
	for i := 0; i < 10; i++ {
		resp, rb := postSite(t, ts.URL, fmt.Sprintf("small%d", i), "fmt=tar", "application/x-tar", small)
		if resp.StatusCode == http.StatusInsufficientStorage {
			break
		}
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("small publish %d = %d %q", i, resp.StatusCode, rb)
		}
		ok++
	}
	if ok != limit/per {
		t.Fatalf("%d small sites fit, want %d", ok, limit/per)
	}
	assertStoreAccounted(t, st)
	if err := st.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	if used, _ := st.Usage(); used != int64(ok)*per {
		t.Fatalf("usage after a recount = %d, want %d", used, int64(ok)*per)
	}
	if resp, rb := postSite(t, ts.URL, "after-recount", "fmt=tar", "application/x-tar", small); resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("publish after a recount = %d %q, want 507", resp.StatusCode, rb)
	}

	ts.Close()
	st.Close()
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SetQuota(limit); err != nil {
		t.Fatal(err)
	}
	if used, _ := st.Usage(); used != int64(ok)*per {
		t.Fatalf("usage after a restart = %d, want %d", used, int64(ok)*per)
	}
	ts = httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)
	if resp, rb := postSite(t, ts.URL, "after-restart", "fmt=tar", "application/x-tar", small); resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("publish after a restart = %d %q, want 507", resp.StatusCode, rb)
	}

	// Deleting a site frees its entries. A replacement stages beside the
	// tree it replaces, and afterwards only the new, flatter tree counts.
	if err := st.DeleteSite("small1"); err != nil {
		t.Fatal(err)
	}
	if used, _ := st.Usage(); used != int64(ok-1)*per {
		t.Fatalf("usage after a delete = %d, want %d", used, int64(ok-1)*per)
	}
	flat := makeTar(t, []srcFile{{"index.html", ""}})
	if resp, rb := postSite(t, ts.URL, "small0", "fmt=tar", "application/x-tar", flat); resp.StatusCode != http.StatusCreated {
		t.Fatalf("replace = %d %q", resp.StatusCode, rb)
	}
	want := int64(ok-1)*per - store.MetaAllowance
	if used, _ := st.Usage(); used != want {
		t.Fatalf("usage after a replace = %d, want %d", used, want)
	}
	assertStoreAccounted(t, st)
}

// TestSitePublishEntryLimit507: past the store-wide cap on site files and
// directories a site publish is refused with 507, quota or not.
func TestSitePublishEntryLimit507(t *testing.T) {
	ts, st := newQuotaServer(t, 0, Config{})
	st.SetSiteEntryLimit(10)
	small := makeTar(t, []srcFile{{"a/index.html", ""}})
	ok := 0
	for i := 0; i < 10; i++ {
		resp, rb := postSite(t, ts.URL, fmt.Sprintf("s%d", i), "fmt=tar", "application/x-tar", small)
		if resp.StatusCode == http.StatusInsufficientStorage {
			break
		}
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("publish %d = %d %q", i, resp.StatusCode, rb)
		}
		ok++
	}
	if ok != 3 {
		t.Fatalf("%d three-entry sites fit a 10-entry limit, want 3", ok)
	}
	if n := siteEntryCount(t, st); n != 9 {
		t.Fatalf("%d site entries on disk, want 9", n)
	}
}

// TestStalledDownloadKeepsReplacedBytesCharged: while a slow reader holds an
// old generation open its bytes stay on disk, so they stay charged, and
// repeated replacement cannot take the store past its quota.
func TestStalledDownloadKeepsReplacedBytesCharged(t *testing.T) {
	const size = 32 << 20
	limit := int64(size*5/2) + store.MetaAllowance
	ts, st := newQuotaServer(t, limit, Config{})
	body := strings.Repeat("z", size)
	if code, b := do(t, http.MethodPost, ts.URL+"/publish?slug=big", "", body); code != http.StatusCreated {
		t.Fatalf("publish = %d %q", code, b)
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Get(ts.URL + "/big")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if _, err := io.ReadFull(resp.Body, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	// The handler is still sending the first generation.
	if code, b := do(t, http.MethodPost, ts.URL+"/publish?slug=big", "", body); code != http.StatusCreated {
		t.Fatalf("first replace = %d %q", code, b)
	}
	used, _ := st.Usage()
	t.Logf("usage with the first generation still being served: %d (quota %d)", used, limit)
	code, b := do(t, http.MethodPost, ts.URL+"/publish?slug=big", "", body)
	if code != http.StatusInsufficientStorage {
		t.Fatalf("replace with an open old generation = %d %q, want 507", code, b)
	}
	resp.Body.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		used, _ := st.Usage()
		if used == size+store.MetaAllowance {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("usage after the reader went away = %d, want %d", used, size+store.MetaAllowance)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code, b := do(t, http.MethodPost, ts.URL+"/publish?slug=big", "", body); code != http.StatusCreated {
		t.Fatalf("replace after the reader went away = %d %q", code, b)
	}
}
