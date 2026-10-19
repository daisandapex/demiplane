// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// TestStalledMultipartUploadsDoNotHoldHeaders: a multipart publish whose file
// part carries megabytes of MIME headers is refused before they are held, so
// many such uploads, stalled mid-body, cost no heap.
func TestStalledMultipartUploadsDoNotHoldHeaders(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)

	const uploads = 16
	head := []byte("--B\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\n" +
		"X-Pad: " + strings.Repeat("p", 9<<20) + "\r\n\r\nstart of the file")
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	writers := make([]*io.PipeWriter, uploads)
	done := make(chan int, uploads)
	for i := range writers {
		pr, pw := io.Pipe()
		writers[i] = pw
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/publish", pr)
		req.Header.Set("Content-Type", "multipart/form-data; boundary=B")
		go func() {
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				done <- 0
				return
			}
			resp.Body.Close()
			done <- resp.StatusCode
		}()
		go func() { _, _ = pw.Write(head) }()
	}
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
	t.Logf("%d stalled multipart uploads with %d MiB headers: heap grew %d MiB", uploads, len(head)>>20, held>>20)
	if held > 32<<20 {
		t.Errorf("%d stalled multipart uploads hold %d MiB of heap, want under 32 MiB", uploads, held>>20)
	}
}

// TestMultipartHeaderLimit: a multipart publish with ordinary headers and a
// form field before the file still works; one whose headers in front of the
// file's body exceed the limit is refused with 431.
func TestMultipartHeaderLimit(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)
	post := func(body string) (int, string) {
		resp, err := http.Post(ts.URL+"/publish", "multipart/form-data; boundary=B", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	fileBody := strings.Repeat("x", 100000)
	ok := "preamble\r\n--B\r\nContent-Disposition: form-data; name=\"note\"\r\n\r\nhello\r\n" +
		"--B\r\nContent-Disposition: form-data; name=\"file\"; filename=\"page.html\"\r\nContent-Type: text/html\r\n\r\n" +
		fileBody + "\r\n--B--\r\n"
	code, body := post(ok)
	if code != http.StatusCreated {
		t.Fatalf("ordinary multipart publish: %d %s", code, body)
	}
	resp, err := http.Get(strings.TrimSpace(body))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, []byte(fileBody)) {
		t.Errorf("stored file is %d bytes, want %d", len(got), len(fileBody))
	}

	// headers builds a file part whose bytes in front of its body total n.
	headers := func(n int) string {
		h := "--B\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\nX-Pad: "
		return h + strings.Repeat("p", n-len(h)-4) + "\r\n\r\n"
	}
	if h := headers(maxMultipartHeaderBytes); len(h) != maxMultipartHeaderBytes {
		t.Fatalf("built %d bytes of headers", len(h))
	}
	if code, body := post(headers(maxMultipartHeaderBytes) + fileBody + "\r\n--B--\r\n"); code != http.StatusCreated {
		t.Errorf("multipart publish with exactly %d bytes in front of the file: %d %s, want 201", maxMultipartHeaderBytes, code, body)
	}
	over := maxMultipartHeaderBytes + multipartReadAhead + 1
	if code, body := post(headers(over) + "x\r\n--B--\r\n"); code != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("multipart publish with %d bytes in front of the file: %d %s, want 431", over, code, body)
	}
}

// TestMultipartPrefixLimitIsExact: exactly 16 KiB in front of the file
// part's body is accepted and one byte more is refused, however the body is
// split into reads, with and without a form field in front of the file.
func TestMultipartPrefixLimitIsExact(t *testing.T) {
	// build returns a multipart body whose bytes in front of the file
	// part's body total n.
	build := func(n int, field bool) string {
		var pre string
		if field {
			pre = "--B\r\nContent-Disposition: form-data; name=\"note\"\r\n\r\n" + strings.Repeat("n", 3000) + "\r\n"
		}
		h := pre + "--B\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\nX-Pad: "
		s := h + strings.Repeat("p", n-len(h)-4) + "\r\n\r\n"
		if len(s) != n {
			panic("bad length")
		}
		return s + strings.Repeat("b", 9000) + "\r\n--B--\r\n"
	}
	readers := map[string]func(string) io.Reader{
		"whole":    func(s string) io.Reader { return strings.NewReader(s) },
		"one byte": func(s string) io.Reader { return iotest.OneByteReader(strings.NewReader(s)) },
		"half":     func(s string) io.Reader { return iotest.HalfReader(strings.NewReader(s)) },
	}
	for name, rd := range readers {
		for _, field := range []bool{false, true} {
			for _, extra := range []int{0, 1} {
				req := httptest.NewRequest(http.MethodPost, "/publish", rd(build(maxMultipartHeaderBytes+extra, field)))
				req.Header.Set("Content-Type", "multipart/form-data; boundary=B")
				body, filename, err := openMultipart(req)
				if extra == 0 {
					if err != nil {
						t.Errorf("%s reads, field %v: exactly %d bytes in front of the file: %v", name, field, maxMultipartHeaderBytes, err)
						continue
					}
					got, rerr := io.ReadAll(body)
					body.Close()
					if rerr != nil || filename != "a.txt" || string(got) != strings.Repeat("b", 9000) {
						t.Errorf("%s reads, field %v: body %d bytes, filename %q, err %v", name, field, len(got), filename, rerr)
					}
				} else if !errors.Is(err, errMultipartHeaders) {
					t.Errorf("%s reads, field %v: %d bytes in front of the file: %v, want errMultipartHeaders", name, field, maxMultipartHeaderBytes+1, err)
				}
			}
		}
	}
}
