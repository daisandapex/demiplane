// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// discardControl is a control plane that reads and discards the publish body
// and reports how many bytes it got.
func discardControl(t *testing.T, got *int64) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		*got = n
		if err != nil {
			http.Error(w, "short body", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"url":"http://content/p","slug":"p"}`)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func publishPath(t *testing.T, c *Client, fp string) *rpcError {
	t.Helper()
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{"path":` + jsonStr(fp) + `}}}` + "\n"
	resps := runLoop(t, c, in)
	return resps[0].Error
}

// TestPublishPathStreams: a file published by path is sent from its
// descriptor as the request goes, not read whole into memory first.
func TestPublishPathStreams(t *testing.T) {
	var got int64
	ts := discardControl(t, &got)
	dir := t.TempDir()
	fp := filepath.Join(dir, "big.bin")
	const size = 32 << 20
	if err := os.WriteFile(fp, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewClient(ts.URL, "", "")
	setPublishRoot(t, c, dir)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if err := publishPath(t, c, fp); err != nil {
		t.Fatalf("publish: %+v", err)
	}
	runtime.ReadMemStats(&after)
	if got != size {
		t.Fatalf("control plane got %d bytes, want %d", got, size)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 8<<20 {
		t.Errorf("publishing a 32 MiB file allocated %d MiB, want it streamed", alloc>>20)
	}
}

// TestPublishPathCapped: a file over the cap is refused, before or while it
// is sent, and is never truncated into a smaller publish.
func TestPublishPathCapped(t *testing.T) {
	saved := maxPublishPathBytes
	t.Cleanup(func() { maxPublishPathBytes = saved })
	maxPublishPathBytes = 1 << 20
	var got int64
	ts := discardControl(t, &got)
	dir := t.TempDir()
	fp := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(fp, make([]byte, 1<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewClient(ts.URL, "", "")
	setPublishRoot(t, c, dir)
	err := publishPath(t, c, fp)
	if err == nil || !strings.Contains(err.Message, "larger than") {
		t.Fatalf("a file over the cap: err %+v, want a size refusal", err)
	}
}

// errReader fails every read with a non-EOF error.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// TestCappedFileProbeErrorFails: once the cap is reached, a read error on the
// probe for more data fails the stream; it must not read as a clean end,
// which would upload the first part of a growing file as complete.
func TestCappedFileProbeErrorFails(t *testing.T) {
	c := &cappedFile{r: errReader{}, left: 0}
	if _, err := c.Read(make([]byte, 8)); err == nil || err == io.EOF {
		t.Fatalf("probe error read as %v, want a failure", err)
	}
}
