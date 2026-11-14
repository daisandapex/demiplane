# 04. Replacing or deleting a site with near-limit paths leaves a tree the store cannot remove, and the store stops opening

**Status: Fixed** in `fix(store): bound site paths so a renamed tree stays removable` and `fix(store): walk and remove site trees by descriptor`. `AddFile` refuses (400) an entry whose path would pass the platform limit under the longest name the tree is ever given. That bound counts the store root as spelled, so site trees are now measured and removed by descriptor (`os.Root`, one name per call relative to an open directory), and the root's spelling no longer matters: a store published under a relative `--store` and reopened through a longer absolute spelling opens, counts and deletes the site. A leftover `.tmp-` tree that cannot be measured is logged and skipped, and a live site that cannot be is logged and quarantined (counted in part) instead of failing Open. Tests: `internal/store/site_pathmax_linux_test.go`, `TestSiteWithLongPathsCanBeRemoved`, `TestOpenToleratesUnmeasurableLeftoverTree`, `TestSiteTreeSurvivesALongerRootSpelling` and `TestOpenQuarantinesAnUnmeasurableLiveSite`, outside the `audit` tag.

**Severity: Medium.** One site publish followed by one replace or delete
makes the store unusable until someone removes a directory by hand: `serve`
will not start again after its next restart, every `demiplane receive` fails
at once, and `delete-site --store` fails. It needs the publish token (or an
instance with none), and loses no data, so it is not rated High. This is the
"what survives a restart" case: the damage is permanent and is only noticed
at the next start.

## Affected code

- `internal/store/site.go:55-94` (`cleanSiteRelPath`): an entry may have 32
  segments of 128 bytes, 4,127 bytes in all, more than Linux's 4,095.
  `AddFile` only needs the path to fit under the staging directory.
- `internal/store/site.go:166`, `:357`, `:563`: the staging name is
  `.tmp-<site>-<n>`; a replace renames the live tree to `.tmp-old-<site>-<n>`
  and a delete to `.tmp-del-<16 hex>`, both longer.
- `internal/store/readers.go:125` (`unlinkFile`) and
  `internal/store/quota.go:503-541` (`removeTreeCounted`) remove by full
  path; `internal/store/quota.go:372-403` (`treeStats`) measures by full path.
- `internal/store/store.go:233` (`measureSiteEntries`) runs at every open and
  fails on the first error.

## Impact

A file whose staged path is just under `PATH_MAX` is accepted and served.
When the site is next replaced or deleted its tree is renamed to the longer
backup or tombstone name, the file's path is now over the limit, `lstat`
fails with `ENAMETOOLONG`, the file cannot be unlinked and its parents are
"not empty". The failure is logged and retried every hour, and fails the
same way every time. From then on every open of the store walks that
leftover tree and returns the error. A running server keeps serving, and
with `--store-quota` its recount fails on every sweep; a stopped server does
not come back. No API reaches the tree, because it is not a site any more.
Each repeat strands another tree of up to 512 MiB that stays charged to the
quota.

## Failing test

`internal/store/audit_site_pathmax_test.go` (build tags `audit linux`). It
uses the delete path because the tombstone name has a fixed length; the
replace path differs only in the name.

```go
func auditDeepPath(n int) string {
	var segs []string
	for n > 0 {
		l := min(n, 128)
		segs = append(segs, strings.Repeat("a", l))
		n -= l + 1 // the segment and the separator after it
	}
	return strings.Join(segs, "/")
}

func TestAuditSiteWithLongPathsCanBeRemoved(t *testing.T) {
	const pathMax = 4095 // Linux PATH_MAX without the terminating NUL
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// The longest staging name for site "s" is ".tmp-s-" plus 10 digits.
	rel := auditDeepPath(pathMax - len(s.siteRoot()) - len("/.tmp-s-4294967295/"))
	sw, err := s.NewSiteWriter("s")
	if err != nil {
		t.Fatalf("NewSiteWriter: %v", err)
	}
	if err := sw.AddFile(rel, strings.NewReader("x")); err != nil {
		sw.Abort()
		t.Fatalf("AddFile(%d-byte path): %v", len(rel), err)
	}
	if _, err := sw.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := s.DeleteSite("s"); err != nil {
		t.Errorf("DeleteSite: %v", err)
	}
	left, _ := filepath.Glob(filepath.Join(s.siteRoot(), ".tmp-*"))
	if len(left) != 0 {
		t.Errorf("the deleted site's tree is still on disk: %s", filepath.Base(left[0]))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(dir)
	if err != nil {
		t.Fatalf("the store no longer opens: %v", shortenAuditErr(err))
	}
	again.Close()
}
```

```
$ go test -tags audit -run TestAuditSiteWithLongPaths -count=1 ./internal/store/
store: could not remove a deleted site's tree, will retry: lstat [path]: file name too long\nremove [path]: directory not empty\n…
--- FAIL: TestAuditSiteWithLongPathsCanBeRemoved (0.05s)
    audit_site_pathmax_test.go:61: the deleted site's tree is still on disk: .tmp-del-852b8a50146aebf7
    audit_site_pathmax_test.go:69: the store no longer opens: count site entries: lstat …/data/sites/.tmp-del-852b8a50146aebf7/aaaa … aaaa: file name too long
FAIL
```

## Smallest fix

Refuse the entry at publish: in `AddFile`, reject (as an `InputError`, so
`400`) a cleaned path for which the longest name the tree can ever have
would pass the limit, that is `len(siteRoot) + 1 + len(longest of staging,
backup and tombstone names) + 1 + len(path) > 4095` (1,023 on macOS). That
keeps every later rename inside the limit. Independently, `measureSiteEntries`
should not make `open` fail for a leftover `.tmp-` tree it cannot measure:
log it and carry on, so that one bad tree cannot stop the store.
