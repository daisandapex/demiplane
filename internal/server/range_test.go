// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/store"
)

// TestMultiRangeIsServedWhole: a single byte range still gets 206; a request
// for many ranges, or with an over-long Range header, gets the whole body
// (200) without the server sizing a multipart response for it.
func TestMultiRangeIsServedWhole(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Put(store.PutOptions{Slug: "doc", Filename: "doc.txt"}, strings.NewReader(strings.Repeat("x", 4096))); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(st, Config{}).ContentHandler())
	t.Cleanup(ts.Close)
	get := func(rng string) (int, int) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/doc", nil)
		req.Header.Set("Range", rng)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		n, _ := io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, int(n)
	}
	if code, n := get("bytes=0-9"); code != http.StatusPartialContent || n != 10 {
		t.Errorf("single range: %d with %d bytes, want 206 with 10", code, n)
	}
	many := "bytes=" + strings.TrimSuffix(strings.Repeat("0-0,", 2000), ",")
	if code, n := get(many); code != http.StatusOK || n != 4096 {
		t.Errorf("2000 ranges: %d with %d bytes, want 200 with the whole body", code, n)
	}
	if code, _ := get("bytes=0-1,5-6"); code != http.StatusOK {
		t.Errorf("two ranges: %d, want 200", code)
	}
}
