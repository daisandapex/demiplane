// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/store"
)

// TestStalledRenderUploadsDoNotHoldMemory: render publishes whose bodies are
// still arriving hold neither a render slot nor their bodies in memory, so
// many slow uploads cost disk (charged to the store) rather than heap.
func TestStalledRenderUploadsDoNotHoldMemory(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)

	const uploads = 16
	chunk := bytes.Repeat([]byte("word "), (6<<20)/5) // 6 MiB of markdown, shared
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	writers := make([]*io.PipeWriter, uploads)
	done := make(chan struct{}, uploads)
	for i := range writers {
		pr, pw := io.Pipe()
		writers[i] = pw
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/publish?render=md", pr)
		go func() {
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
			done <- struct{}{}
		}()
		go func() { _, _ = pw.Write(chunk) }()
	}
	// Let the server take in what has been sent; the uploads then stall.
	time.Sleep(3 * time.Second)
	var during runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&during)
	for _, pw := range writers {
		pw.CloseWithError(io.ErrClosedPipe)
	}
	for range writers {
		<-done
	}
	held := int64(during.HeapInuse) - int64(before.HeapInuse)
	t.Logf("%d stalled render uploads of %d MiB: heap grew %d MiB", uploads, len(chunk)>>20, held>>20)
	if held > 32<<20 {
		t.Errorf("%d stalled render uploads hold %d MiB of heap, want under 32 MiB", uploads, held>>20)
	}
}
