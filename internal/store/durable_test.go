// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordSyncs records the files and directories synced, failing those for
// which fail reports true.
func recordSyncs(t *testing.T, fail func(path string) bool) (files, dirs func() []string) {
	t.Helper()
	var (
		mu     sync.Mutex
		fs, ds []string
	)
	fileSync = func(f *os.File) error {
		mu.Lock()
		fs = append(fs, f.Name())
		mu.Unlock()
		if fail(f.Name()) {
			return errors.New("injected sync failure")
		}
		return f.Sync()
	}
	contentSync = func(dir string) error {
		mu.Lock()
		ds = append(ds, dir)
		mu.Unlock()
		if fail(dir) {
			return errors.New("injected sync failure")
		}
		return syncDir(dir)
	}
	t.Cleanup(func() { fileSync, contentSync = (*os.File).Sync, syncDir })
	get := func(p *[]string) func() []string {
		return func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), *p...)
		}
	}
	return get(&fs), get(&ds)
}

// TestPublishIsDurableBeforeItsRow: a publish syncs its blob and source and
// their directories before the row commits, and a sync failure leaves no row.
func TestPublishIsDurableBeforeItsRow(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	files, dirs := recordSyncs(t, func(string) bool { return false })
	if _, err := s.Put(PutOptions{Slug: "page", Source: []byte("# src")}, bytes.NewReader([]byte("bytes"))); err != nil {
		t.Fatal(err)
	}
	if n := len(files()); n != 2 {
		t.Errorf("files synced = %d, want 2 (blob and source)", n)
	}
	got := strings.Join(dirs(), " ")
	if !strings.Contains(got, s.blobDir) || !strings.Contains(got, s.sourceDir) {
		t.Errorf("directories synced = %v; want the blob and source directories", dirs())
	}

	for _, which := range []string{"file", "dir"} {
		recordSyncs(t, func(p string) bool {
			if which == "file" {
				return strings.HasPrefix(p, s.blobDir+string(filepath.Separator))
			}
			return p == s.blobDir
		})
		slug := "fails-" + which
		if _, err := s.Put(PutOptions{Slug: slug}, bytes.NewReader([]byte("bytes"))); err == nil {
			t.Fatalf("%s sync failure: publish succeeded", which)
		}
		if _, f, err := s.Get(slug); !errors.Is(err, ErrNotFound) {
			if f != nil {
				f.Close()
			}
			t.Fatalf("%s sync failure: Get = %v, want ErrNotFound", which, err)
		}
		assertAccounted(t, s)
	}
}

// TestStagedSiteIsDurableBeforeTheSwap: every staged file and directory is
// synced before the site is swapped in, and a sync failure leaves the old
// site in place with nothing recorded.
func TestStagedSiteIsDurableBeforeTheSwap(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	if err := publishSiteBody(s, "site", "index.html", "v1"); err != nil {
		t.Fatal(err)
	}
	files, dirs := recordSyncs(t, func(string) bool { return false })
	sw, err := s.NewSiteWriter("site")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"index.html", "a/b/c.txt", "a/d.txt"} {
		if err := sw.AddFile(p, strings.NewReader("v2")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sw.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := len(files()); n != 3 {
		t.Errorf("files synced = %d, want 3", n)
	}
	want := map[string]bool{"": false, "a": false, "a/b": false}
	for _, d := range dirs() {
		base := filepath.Base(sw.tmpDir)
		if i := strings.Index(d, base); i >= 0 {
			rel := strings.TrimPrefix(d[i+len(base):], "/")
			if _, ok := want[rel]; ok {
				want[rel] = true
			}
		}
	}
	for d, ok := range want {
		if !ok {
			t.Errorf("staged directory %q was not synced (synced: %v)", d, dirs())
		}
	}

	recordSyncs(t, func(p string) bool { return strings.HasSuffix(p, "/a") })
	sw, err = s.NewSiteWriter("site")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.AddFile("a/x.txt", strings.NewReader("v3")); err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Commit(); err == nil {
		t.Fatal("commit succeeded although the staged tree could not be synced")
	}
	if got := readSite(t, s, "site", "index.html"); got != "v2" {
		t.Fatalf("site = %q, want v2", got)
	}
	if n := swapRows(t, s); n != 0 {
		t.Fatalf("%d swap records after a refused commit", n)
	}
	assertAccounted(t, s)
}

// TestReplaceKeepsTheBackupUntilDurable: a replace whose renames cannot be
// synced keeps the previous tree, still charged and recorded, until
// recovery syncs; only then is it removed.
func TestReplaceKeepsTheBackupUntilDurable(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	if err := publishSiteBody(s, "site", "index.html", "v1"); err != nil {
		t.Fatal(err)
	}
	restore := failSitesSyncAfterRecord(t)
	if err := publishSiteBody(s, "site", "index.html", "v2"); err == nil {
		t.Fatal("a replace that is not durable was acknowledged")
	}
	backups := func() []string {
		ents, err := os.ReadDir(s.siteRoot())
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), tempPrefix+"old-") {
				out = append(out, e.Name())
			}
		}
		return out
	}
	if b := backups(); len(b) != 1 {
		t.Fatalf("backups after a replace that is not durable = %v, want one kept", b)
	}
	if n := swapRows(t, s); n != 1 {
		t.Fatalf("%d swap records, want 1", n)
	}
	// The kept tree is charged: a recount agrees with the running total.
	before, _ := s.Usage()
	if err := s.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	if after, _ := s.Usage(); after != before {
		t.Fatalf("usage %d, recount %d: the kept backup is not charged as it is", before, after)
	}
	restore()
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if n := swapRows(t, s); n != 0 {
		t.Fatalf("%d swap records after recovery", n)
	}
	if _, err := s.Reconcile(time.Now().Add(4 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if b := backups(); len(b) != 0 {
		t.Fatalf("backups left after recovery and reconcile = %v", b)
	}
	if got := readSite(t, s, "site", "index.html"); got != "v2" {
		t.Fatalf("site = %q, want v2", got)
	}
	assertAccounted(t, s)
}

// TestFirstSitePublishIsRecoverableUntilDurable: a first publish whose
// install cannot be synced keeps a record, so recovery syncs it rather than
// the scavenger losing it; one a crash cut off before its install is
// finished from the complete staged tree, and a delete meanwhile removes
// only that site's trees.
func TestFirstSitePublishIsRecoverableUntilDurable(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	restore := failSitesSyncAfterRecord(t)
	if err := publishSiteBody(s, "fresh", "index.html", "v1"); err == nil {
		t.Fatal("a first publish that is not durable was acknowledged")
	}
	if n := swapRows(t, s); n != 1 {
		t.Fatalf("%d swap records after a first publish that is not durable, want 1", n)
	}
	restore()
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if n := swapRows(t, s); n != 0 {
		t.Fatalf("%d swap records after recovery", n)
	}
	if got := readSite(t, s, "fresh", "index.html"); got != "v1" {
		t.Fatalf("site = %q, want v1", got)
	}

	// A crash between the record and the install: the staged tree is
	// complete and only its record names it.
	staging := filepath.Join(s.siteRoot(), tempPrefix+"site-crashed")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "index.html"), []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO site_swaps (site, id, backup, staging) VALUES ('second', 'x', '', ?)`, filepath.Base(staging)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if got := readSite(t, s, "second", "index.html"); got != "staged" {
		t.Fatalf("site after recovery = %q, want the staged tree", got)
	}

	// A delete retiring a record that names no backup removes that site's
	// trees only.
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO site_swaps (site, id, backup, staging) VALUES ('second', 'y', '', ?)`, filepath.Base(staging)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSite("second"); err != nil {
		t.Fatal(err)
	}
	if got := readSite(t, s, "fresh", "index.html"); got != "v1" {
		t.Fatalf("other site after the delete = %q, want v1", got)
	}
	if s.SiteExists("second") {
		t.Fatal("deleted site still exists")
	}
	if n := swapRows(t, s); n != 0 {
		t.Fatalf("%d swap records after the delete", n)
	}
	if err := s.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	assertAccounted(t, s)
}

// TestStagedTreeNameIsDurableBeforeItsRecord: the staged tree's entry in
// the sites directory is synced before a swap record names it, and a
// failure there stops the publish before anything is recorded or moved.
func TestStagedTreeNameIsDurableBeforeItsRecord(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	var synced int
	sitesSync = func(dir string) error { synced++; return syncDir(dir) }
	afterSwapRecord = func() {
		if synced == 0 {
			t.Error("swap recorded before the sites directory was synced")
		}
	}
	t.Cleanup(func() { sitesSync, afterSwapRecord = syncDir, nil })
	if err := publishSiteBody(s, "site", "index.html", "v1"); err != nil {
		t.Fatal(err)
	}
	failSitesSync(t)
	if err := publishSiteBody(s, "site", "index.html", "v2"); err == nil {
		t.Fatal("publish succeeded although the staged tree's name could not be synced")
	}
	if n := swapRows(t, s); n != 0 {
		t.Fatalf("%d swap records after a refused publish", n)
	}
	sitesSync = syncDir
	if got := readSite(t, s, "site", "index.html"); got != "v1" {
		t.Fatalf("site = %q, want v1", got)
	}
	assertAccounted(t, s)
}

// TestFailedFirstInstallKeepsItsRecordedTree: a first install whose rename
// fails, and whose record cannot then be cleared, keeps the complete staged
// tree for recovery instead of dismantling it under the record.
func TestFailedFirstInstallKeepsItsRecordedTree(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	dir := s.root
	restore := failInstall(t, dir, "fresh")
	clearSwapErr = func() error { return errors.New("injected clear failure") }
	t.Cleanup(func() { clearSwapErr = nil })
	sw, err := s.NewSiteWriter("fresh")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"index.html", "a/b.txt"} {
		if err := sw.AddFile(p, strings.NewReader("v1 "+p)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sw.Commit(); err == nil {
		t.Fatal("commit with a failing install reported success")
	}
	restore()
	clearSwapErr = nil
	if n := swapRows(t, s); n != 1 {
		t.Fatalf("%d swap records, want 1", n)
	}
	for _, p := range []string{"index.html", "a/b.txt"} {
		b, err := os.ReadFile(filepath.Join(sw.tmpDir, p))
		if err != nil || string(b) != "v1 "+p {
			t.Fatalf("kept staged %s = %q, %v", p, b, err)
		}
	}
	before, _ := s.Usage()
	if err := s.RecountUsage(); err != nil {
		t.Fatal(err)
	}
	if after, _ := s.Usage(); after != before {
		t.Fatalf("usage %d, recount %d: the kept tree is not charged as it is", before, after)
	}
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if got := readSite(t, s, "fresh", "a/b.txt"); got != "v1 a/b.txt" {
		t.Fatalf("site after recovery = %q", got)
	}
	if n := swapRows(t, s); n != 0 {
		t.Fatalf("%d swap records after recovery", n)
	}
	assertAccounted(t, s)
}

// TestScavengerSparesATreeKeptMidPass: a staged tree a writer gives up to
// recovery (its first install failed and its record could not be cleared)
// while a scavenging pass is under way is not removed by that pass.
func TestScavengerSparesATreeKeptMidPass(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	sw, err := s.NewSiteWriter("fresh")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.AddFile("index.html", strings.NewReader("v1")); err != nil {
		t.Fatal(err)
	}
	scanned, resume := make(chan struct{}), make(chan struct{})
	afterStagingScan = func() {
		close(scanned)
		<-resume
	}
	t.Cleanup(func() { afterStagingScan = nil })
	done := make(chan error, 1)
	go func() {
		_, err := s.reclaimStagingTrees(time.Now().Add(2 * orphanGrace))
		done <- err
	}()
	select {
	case <-scanned:
	case <-time.After(10 * time.Second):
		t.Fatal("the scavenging pass never reached its scan hook")
	}
	restore := failInstall(t, s.root, "fresh")
	clearSwapErr = func() error { return errors.New("injected clear failure") }
	if _, err := sw.Commit(); err == nil {
		t.Fatal("commit with a failing install reported success")
	}
	clearSwapErr = nil
	restore()
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	afterStagingScan = nil
	if b, err := os.ReadFile(filepath.Join(sw.tmpDir, "index.html")); err != nil || string(b) != "v1" {
		t.Fatalf("kept tree after the pass: %q, %v", b, err)
	}
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if got := readSite(t, s, "fresh", "index.html"); got != "v1" {
		t.Fatalf("site after recovery = %q", got)
	}
	assertAccounted(t, s)
}

// TestSiteDeleteIsDurableBeforeRemoval: a delete takes the site's name
// away durably before it removes anything, and if that cannot be synced the
// site stays whole and the delete fails.
func TestSiteDeleteIsDurableBeforeRemoval(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	for _, name := range []string{"a", "b"} {
		if err := publishSiteBody(s, name, "index.html", "v1 "+name); err != nil {
			t.Fatal(err)
		}
	}
	var checked bool
	sitesSync = func(dir string) error {
		if _, err := os.Stat(filepath.Join(dir, "a")); err == nil {
			t.Error("sites directory synced for the delete with the site still named")
		}
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), tempPrefix) {
				if b, err := os.ReadFile(filepath.Join(dir, e.Name(), "index.html")); err == nil && string(b) == "v1 a" {
					checked = true
				}
			}
		}
		return syncDir(dir)
	}
	t.Cleanup(func() { sitesSync = syncDir })
	if err := s.DeleteSite("a"); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Error("the delete did not sync the sites directory while the tree was still whole")
	}
	sitesSync = func(string) error { return errors.New("injected sync failure") }
	if err := s.DeleteSite("b"); err == nil {
		t.Fatal("delete succeeded although it could not be synced")
	}
	sitesSync = syncDir
	if got := readSite(t, s, "b", "index.html"); got != "v1 b" {
		t.Fatalf("site after a failed delete = %q, want it whole", got)
	}
	if s.SiteExists("a") {
		t.Fatal("deleted site still exists")
	}
	assertAccounted(t, s)
}

// TestSyncSkippedOnlyByTests: nothing but test files turns the content
// syncs off.
func TestSyncSkippedOnlyByTests(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || filepath.Base(p) == "durable.go" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "SkipSyncForTest(") {
			t.Errorf("%s calls SkipSyncForTest", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRetireSyncsBeforeDismantling: deleting a site whose failed replace
// left it moved aside first makes that move durable; if it cannot, the
// delete fails with the record and the moved-aside tree intact.
func TestRetireSyncsBeforeDismantling(t *testing.T) {
	s := newQuotaStore(t, 1<<20)
	if err := publishSiteBody(s, "site", "index.html", "v1"); err != nil {
		t.Fatal(err)
	}
	restore := failInstall(t, s.root, "site")
	if err := publishSiteBody(s, "site", "index.html", "v2"); err == nil {
		t.Fatal("replace with failing renames reported success")
	}
	restore()
	if s.SiteExists("site") {
		t.Fatal("setup: the site should be moved aside")
	}
	ents, err := filepath.Glob(filepath.Join(s.siteRoot(), tempPrefix+"old-*", "index.html"))
	if err != nil || len(ents) != 1 {
		t.Fatalf("setup: moved-aside tree = %v, %v", ents, err)
	}
	failSitesSync(t)
	if err := s.DeleteSite("site"); err == nil {
		t.Fatal("delete succeeded although the moved-aside site could not be synced")
	}
	if n := swapRows(t, s); n != 1 {
		t.Fatalf("%d swap records after a refused delete, want 1", n)
	}
	if b, err := os.ReadFile(ents[0]); err != nil || string(b) != "v1" {
		t.Fatalf("moved-aside tree after a refused delete: %q, %v", b, err)
	}
	sitesSync = syncDir
	if err := s.DeleteSite("site"); err != nil {
		t.Fatal(err)
	}
	if n := swapRows(t, s); n != 0 {
		t.Fatalf("%d swap records after the delete", n)
	}
	if _, err := s.Reconcile(time.Now().Add(2 * orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if s.SiteExists("site") {
		t.Fatal("deleted site came back")
	}
	assertAccounted(t, s)
}
