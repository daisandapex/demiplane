// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// wideTableDoc is the 10,000-column, 100,000-row table payload (about 420 KB).
func wideTableDoc() string {
	return "|" + strings.Repeat("h|", 10000) + "\n|---|\n" + strings.Repeat("|x|\n", 100000)
}

func postRaw(t *testing.T, url, body string) (int, string, http.Header) {
	t.Helper()
	resp, err := http.Post(url, "text/markdown", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestPublishRefusesOverBudgetRender(t *testing.T) {
	ts := newTestServer(t, "")
	url := publish(t, ts, "?render=md&slug=wide", "# Before\n\nThe page that must survive.\n")
	_, before := get(t, url)

	code, body, _ := postRaw(t, ts.URL+"/publish?render=md&slug=wide", wideTableDoc())
	if code != http.StatusRequestEntityTooLarge || !strings.Contains(body, "table columns") {
		t.Fatalf("publish = %d %q, want 413 naming the table columns limit", code, body)
	}
	resp, after := get(t, url)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(before, after) {
		t.Fatalf("refused publish changed the stored page (status %d)", resp.StatusCode)
	}
	code, _, _ = postRaw(t, ts.URL+"/publish?render=md&slug=fresh", wideTableDoc())
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("fresh slug publish = %d, want 413", code)
	}
	if resp, _ := get(t, ts.URL+"/fresh"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("refused publish stored an artifact: GET status %d", resp.StatusCode)
	}
}

func TestRebakeRefusedKeepsStoredBytes(t *testing.T) {
	st := newSharedStore(t)
	first := newStoreServer(t, st, Config{})
	url := publish(t, first, "?render=md&slug=doc", rerenderSource+strings.Repeat("More text. ", 400)+"\n")
	_, before := get(t, url)

	// A second instance over the same store with a far smaller output cap
	// cannot rebake the page; the stored bytes must stay as they were.
	tight := newStoreServer(t, st, Config{RenderMaxOutput: 2048})
	code, body, _ := postRaw(t, tight.URL+"/rerender/doc", "")
	if code != http.StatusRequestEntityTooLarge || !strings.Contains(body, "output bytes") {
		t.Fatalf("rerender = %d %q, want 413 naming the output limit", code, body)
	}
	var sweep struct {
		Failed map[string]string `json:"failed"`
	}
	postJSON(t, tight.URL+"/rerender", &sweep)
	if !strings.Contains(sweep.Failed["doc"], "output bytes") {
		t.Fatalf("bulk rerender failures = %v, want doc refused on output bytes", sweep.Failed)
	}
	_, after := get(t, url)
	if !bytes.Equal(before, after) {
		t.Fatal("a refused rebake changed the stored page")
	}
}

func TestRenderSlotsBounded(t *testing.T) {
	if got, want := cap(newRenderSem(0)), min(runtime.GOMAXPROCS(0), maxDefaultRenderConcurrency); got != want {
		t.Fatalf("default render slots = %d, want %d", got, want)
	}

	st := newSharedStore(t)
	srv := New(st, Config{RenderConcurrency: 2})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	var inFlight, peak int32
	renderHeld = func() {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
	}
	t.Cleanup(func() { renderHeld = nil })

	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _, _ = postRaw(t, ts.URL+"/publish?render=md", "# hi\n")
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusCreated {
			t.Fatalf("publish %d = %d, want 201 (waits within renderWait)", i, c)
		}
	}
	if peak > 2 {
		t.Fatalf("%d renders ran at once, want at most 2", peak)
	}
	if peak < 2 {
		t.Logf("peak concurrency only reached %d", peak)
	}

	// With every slot held past the wait, a render is refused with 503.
	renderHeld = nil
	old := renderWait
	renderWait = 50 * time.Millisecond
	t.Cleanup(func() { renderWait = old })
	srv.renderSem <- struct{}{}
	srv.renderSem <- struct{}{}
	code, _, hdr := postRaw(t, ts.URL+"/publish?render=md", "# hi\n")
	if code != http.StatusServiceUnavailable || hdr.Get("Retry-After") == "" {
		t.Fatalf("publish with no free slot = %d (Retry-After %q), want 503 with Retry-After", code, hdr.Get("Retry-After"))
	}
	code, _, _ = postRaw(t, ts.URL+"/rerender", "")
	if code != http.StatusOK {
		t.Fatalf("bulk rerender with no free slot = %d, want 200 with per-slug failures", code)
	}
	<-srv.renderSem
	<-srv.renderSem
	if code, _, _ := postRaw(t, ts.URL+"/publish?render=md", "# hi\n"); code != http.StatusCreated {
		t.Fatalf("publish after release = %d, want 201", code)
	}
}
