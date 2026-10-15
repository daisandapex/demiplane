// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// conditionalGet sends a GET carrying the validators a browser cached from an
// earlier response.
func conditionalGet(t *testing.T, url string, cached http.Header) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lm := cached.Get("Last-Modified"); lm != "" {
		req.Header.Set("If-Modified-Since", lm)
	}
	if et := cached.Get("ETag"); et != "" {
		req.Header.Set("If-None-Match", et)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// TestRebakeReachesCachedBrowser: a browser holding the old rendering
// revalidates with the validators it was given. After a rebake that
// revalidation must return the new bytes, not 304; revalidating again with the
// new validators must return 304.
func TestRebakeReachesCachedBrowser(t *testing.T) {
	st := newSharedStore(t)
	before := newStoreServer(t, st, renderConfig("https://example.com/old-renderer"))
	url := publish(t, before, "?render=md&slug=field-notes", rerenderSource)

	first, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, first.Body)
	first.Body.Close()
	cached := first.Header.Clone()

	if resp, _ := conditionalGet(t, url, cached); resp.StatusCode != http.StatusNotModified {
		t.Fatalf("unchanged revalidation = %d, want 304", resp.StatusCode)
	}

	after := newStoreServer(t, st, renderConfig("https://example.com/new-renderer"))
	var res rerenderResult
	if resp := postJSON(t, after.URL+"/rerender/field-notes", &res); resp.StatusCode != http.StatusOK || !res.Rerendered {
		t.Fatalf("rerender: status %d, %+v", resp.StatusCode, res)
	}

	resp, body := conditionalGet(t, after.URL+"/field-notes", cached)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revalidation after rebake = %d, want 200 (a cached browser keeps the old rendering)", resp.StatusCode)
	}
	if !strings.Contains(body, "https://example.com/new-renderer") {
		t.Fatalf("revalidation after rebake did not return the new rendering")
	}
	if resp.Header.Get("ETag") == "" {
		t.Error("no ETag on GET")
	}

	if again, _ := conditionalGet(t, after.URL+"/field-notes", resp.Header); again.StatusCode != http.StatusNotModified {
		t.Errorf("revalidation with the new validators = %d, want 304", again.StatusCode)
	}
}

// TestRepublishChangesValidators: a republish inside the same second as the
// first publish must still invalidate a cached copy.
func TestRepublishChangesValidators(t *testing.T) {
	ts := newTestServer(t, "")
	url := publish(t, ts, "?slug=same-second", "<p>one</p>")
	first, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, first.Body)
	first.Body.Close()
	publish(t, ts, "?slug=same-second", "<p>two</p>")
	resp, body := conditionalGet(t, url, first.Header)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "two") {
		t.Fatalf("revalidation after republish = %d %q, want 200 with new bytes", resp.StatusCode, body)
	}
}

// TestRepublishChangesLastModified: a client that revalidates with
// If-Modified-Since alone (no ETag) still gets new bytes after a republish in
// the same second.
func TestRepublishChangesLastModified(t *testing.T) {
	ts := newTestServer(t, "")
	url := publish(t, ts, "?slug=ims-only", "<p>one</p>")
	first, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, first.Body)
	first.Body.Close()
	publish(t, ts, "?slug=ims-only", "<p>two</p>")
	cached := http.Header{}
	cached.Set("Last-Modified", first.Header.Get("Last-Modified"))
	resp, body := conditionalGet(t, url, cached)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "two") {
		t.Fatalf("If-Modified-Since revalidation after republish = %d %q, want 200 with new bytes", resp.StatusCode, body)
	}
}

// TestLastModifiedNotAheadAfterRecreate: rapid writes must not push
// Last-Modified past the response's Date, and a client holding the
// Last-Modified of a deleted artifact must get the recreated artifact's bytes,
// not a 304 for bytes it never saw.
func TestLastModifiedNotAheadAfterRecreate(t *testing.T) {
	ts := newTestServer(t, "")
	var url string
	for i := 0; i < 5; i++ {
		url = publish(t, ts, "?slug=churn", fmt.Sprintf("<p>old %d</p>", i))
	}
	resp, _ := get(t, url)
	lm, err := http.ParseTime(resp.Header.Get("Last-Modified"))
	if err != nil {
		t.Fatalf("Last-Modified %q: %v", resp.Header.Get("Last-Modified"), err)
	}
	date, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		t.Fatalf("Date %q: %v", resp.Header.Get("Date"), err)
	}
	if lm.After(date) {
		t.Errorf("Last-Modified %v is later than Date %v", lm, date)
	}
	cached := http.Header{}
	cached.Set("Last-Modified", resp.Header.Get("Last-Modified"))

	if code, _ := do(t, http.MethodDelete, url, "", ""); code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d", code)
	}
	url = publish(t, ts, "?slug=churn", "<p>fresh</p>")
	got, body := conditionalGet(t, url, cached)
	if got.StatusCode != http.StatusOK || !strings.Contains(body, "fresh") {
		t.Fatalf("If-Modified-Since after delete and recreate = %d %q, want 200 with the new bytes", got.StatusCode, body)
	}
}

// rangeGet sends a Range request for bytes 3- with the given If-Range value.
func rangeGet(t *testing.T, url, ifRange string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=3-")
	req.Header.Set("If-Range", ifRange)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestIfRangeDateAfterSameSecondRepublish: a resumed download that carries
// If-Range with the old Last-Modified must not get a partial response of
// different bytes when the republish lands in the same second, so both writes
// are retried until their Last-Modified dates match. An If-Range with the
// current strong ETag still gets the range; a stale or weak one gets the whole
// body.
func TestIfRangeDateAfterSameSecondRepublish(t *testing.T) {
	ts := newTestServer(t, "")
	var url string
	var first, second *http.Response
	for attempt := 0; ; attempt++ {
		url = publish(t, ts, "?slug=resume", "<p>aaaaaaaa</p>")
		first, _ = get(t, url)
		publish(t, ts, "?slug=resume", "<p>bbbbbbbb</p>")
		second, _ = get(t, url)
		if first.Header.Get("Last-Modified") == second.Header.Get("Last-Modified") {
			break
		}
		if attempt == 5 {
			t.Fatal("could not land two writes in one second")
		}
	}
	if code, body := rangeGet(t, url, first.Header.Get("Last-Modified")); code != http.StatusOK || body != "<p>bbbbbbbb</p>" {
		t.Fatalf("If-Range with a same-second date = %d %q, want 200 with the whole new body", code, body)
	}
	if code, body := rangeGet(t, url, first.Header.Get("ETag")); code != http.StatusOK || body != "<p>bbbbbbbb</p>" {
		t.Fatalf("If-Range with the old ETag = %d %q, want 200 with the whole new body", code, body)
	}
	if code, body := rangeGet(t, url, "W/"+second.Header.Get("ETag")); code != http.StatusOK || body != "<p>bbbbbbbb</p>" {
		t.Fatalf("If-Range with a weak ETag = %d %q, want 200 with the whole new body", code, body)
	}
	if code, body := rangeGet(t, url, second.Header.Get("ETag")); code != http.StatusPartialContent || body != "bbbbbbbb</p>" {
		t.Fatalf("If-Range with the current ETag = %d %q, want 206 with the range", code, body)
	}
}
