// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/store"
)

// TestPublishRefusalsAnswerWithoutDrainingTheBody: a publish refused after
// the server has started reading it is answered at once, not after the rest
// of a slow body has been read: the multipart prefix limit, a multipart
// filename over the limit, and a render document over the size limit.
func TestPublishRefusalsAnswerWithoutDrainingTheBody(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)

	prefix := func(n int) string {
		h := "--B\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\nX-Pad: "
		return h + strings.Repeat("p", n-len(h)-4) + "\r\n\r\n"
	}
	for _, tc := range []struct {
		name string
		path string
		ct   string
		head []byte
		want int
	}{
		{"multipart prefix over 16 KiB", "/publish", "multipart/form-data; boundary=B",
			[]byte(prefix(maxMultipartHeaderBytes + 1)), http.StatusRequestHeaderFieldsTooLarge},
		{"multipart filename too long", "/publish", "multipart/form-data; boundary=B",
			[]byte("--B\r\nContent-Disposition: form-data; name=\"file\"; filename=\"" +
				strings.Repeat("f", store.MaxFilenameBytes+1) + "\"\r\n\r\n"), http.StatusBadRequest},
		{"render document too large", "/publish?render=md", "text/markdown",
			bytes.Repeat([]byte("x"), maxRenderBytes+1), http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr, pw := io.Pipe()
			req, _ := http.NewRequest(http.MethodPost, ts.URL+tc.path, pr)
			req.Header.Set("Content-Type", tc.ct)
			got := make(chan int, 1)
			go func() {
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					got <- 0
					return
				}
				resp.Body.Close()
				got <- resp.StatusCode
			}()
			stop := make(chan struct{})
			go func() {
				// The head, then a slow tail that would take a minute.
				if _, err := pw.Write(tc.head); err != nil {
					return
				}
				for i := 0; i < 600; i++ {
					select {
					case <-stop:
						pw.Close()
						return
					case <-time.After(100 * time.Millisecond):
					}
					if _, err := pw.Write(bytes.Repeat([]byte("t"), 1024)); err != nil {
						return
					}
				}
				pw.Close()
			}()
			start := time.Now()
			select {
			case code := <-got:
				if code != tc.want {
					t.Errorf("status %d, want %d", code, tc.want)
				}
				if el := time.Since(start); el > 5*time.Second {
					t.Errorf("answered after %s, want promptly", el.Round(time.Second))
				}
			case <-time.After(10 * time.Second):
				t.Errorf("no answer within 10s: the server is reading the rest of the body")
			}
			close(stop)
		})
	}
}
