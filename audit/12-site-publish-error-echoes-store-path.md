# 12. A failed site publish answers with the store's absolute path

**Severity: Low.** Only a caller who may publish sees it, and what it learns
is where the store lives on the server and the name of the staging
directory. It is listed because the rest of the server is careful never to
send a store path to a client, and because it reports a server-side fault as
`400`.

## Affected code

- `internal/server/site.go:228-229` (`writeSiteExtractError`, default
  branch): `http.Error(w, "invalid archive: "+err.Error(), 400)`.
- `internal/server/site.go:130`: `"read upload: "+err.Error()`.

`SiteWriter.AddFile` returns filesystem errors as `*fs.PathError`
(`internal/store/site.go`), so any I/O failure during extraction (a path the
filesystem refuses, a full disk, an I/O error) reaches that branch with the
full path in its text.

## Impact

The body reads, for example, `invalid archive: stat site dir: lstat
/var/lib/demiplane/data/sites/.tmp-s-123456789/aaa…: file name too long`.
Every other handler logs such errors through `logsafe` and answers
`internal error`. The length of the echoed path is also what makes finding 04
easy to calibrate.

## Failing test

`internal/server/audit_site_error_path_test.go` (build tags `audit linux`).

```go
func TestAuditSitePublishErrorDoesNotNameStorePaths(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)

	// 32 segments of 128 bytes: a valid entry name whose staged path is
	// longer than PATH_MAX.
	name := strings.TrimSuffix(strings.Repeat(strings.Repeat("a", 128)+"/", 32), "/")
	body := tarOf(t, map[string]string{name: "x"}, false)
	resp, err := http.Post(ts.URL+"/publish?site=s", "application/x-tar", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(msg), dir) {
		t.Errorf("publish = %d, and its body names the store directory: %.160s …", resp.StatusCode, msg)
	}
}
```

```
$ go test -tags audit -run TestAuditSitePublishError -count=1 ./internal/server/
--- FAIL: TestAuditSitePublishErrorDoesNotNameStorePaths (0.03s)
    audit_site_error_path_test.go:47: publish = 400, and its body names the store directory: invalid archive: stat site dir: lstat /…/TestAuditSitePublishErrorDoesNotNameStorePaths4242883381/001/data/site …
FAIL
```

## Smallest fix

In `writeSiteExtractError`, send `err.Error()` only for errors that come from
the archive itself (`tar.ErrHeader`, `zip.ErrFormat`, `zip.ErrInsecurePath`,
the wrapped "read tar"/"open zip" errors); for anything else log
`logsafe.Err(err)` and answer `500 internal error`, as `handleGet` does. The
same for the `read upload` branch.
