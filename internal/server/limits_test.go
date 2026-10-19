// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

func limitServer(t *testing.T, cfg Config) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, cfg), st
}

// rawPost opens a connection, sends a POST whose headers promise length
// bytes of body, then calls send to write (some of) the body. It returns the
// connection for the caller to read from.
func rawPost(t *testing.T, addr, path string, length int, send func(net.Conn)) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	fmt.Fprintf(c, "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: text/html\r\nContent-Length: %d\r\n\r\n", path, addr, length)
	if send != nil {
		send(c)
	}
	return c
}

// waitClosedOrAnswered reads until the server answers or drops the
// connection, failing if neither happens within limit. It returns the time
// taken and whatever was read.
func waitClosedOrAnswered(t *testing.T, c net.Conn, limit time.Duration) (time.Duration, string) {
	t.Helper()
	start := time.Now()
	_ = c.SetReadDeadline(start.Add(limit))
	b, err := io.ReadAll(c)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("connection still open %v after the body stalled", limit)
	}
	return time.Since(start), string(b)
}

func TestStalledUploadIsCut(t *testing.T) {
	s, st := limitServer(t, Config{Requests: RequestLimits{BodyTimeout: 300 * time.Millisecond}})
	ts := httptest.NewServer(s.ControlHandler())
	t.Cleanup(ts.Close)
	addr := ts.Listener.Addr().String()

	c := rawPost(t, addr, "/publish?slug=stall", 4096, func(c net.Conn) {
		io.WriteString(c, "<html>only the start")
	})
	el, resp := waitClosedOrAnswered(t, c, 5*time.Second)
	if el > 3*time.Second {
		t.Fatalf("stalled upload held for %v with a 300ms body timeout", el)
	}
	if !strings.HasPrefix(resp, "HTTP/1.1 408") {
		t.Fatalf("stalled upload answered %q, want 408", firstLine(resp))
	}
	if _, _, err := st.Get("stall"); err == nil {
		t.Fatal("a stalled upload was stored")
	}
}

func TestSlowUploadBelowMinRateIsCut(t *testing.T) {
	s, st := limitServer(t, Config{Requests: RequestLimits{
		BodyTimeout:   300 * time.Millisecond,
		UploadMinRate: 1000, // bytes a second
	}})
	ts := httptest.NewServer(s.ControlHandler())
	t.Cleanup(ts.Close)
	addr := ts.Listener.Addr().String()

	// 10 bytes every 50ms is 200 B/s: never a stall, but under the floor.
	stop := make(chan struct{})
	defer close(stop)
	c := rawPost(t, addr, "/publish?slug=slow", 100000, func(c net.Conn) {
		go func() {
			for {
				select {
				case <-stop:
					return
				case <-time.After(50 * time.Millisecond):
					if _, err := c.Write(bytes.Repeat([]byte("x"), 10)); err != nil {
						return
					}
				}
			}
		}()
	})
	el, _ := waitClosedOrAnswered(t, c, 10*time.Second)
	if el > 4*time.Second {
		t.Fatalf("a 200 B/s upload ran %v under a 1000 B/s floor", el)
	}
	if _, _, err := st.Get("slow"); err == nil {
		t.Fatal("a too-slow upload was stored")
	}

	// An upload above the floor completes.
	resp, err := http.Post(ts.URL+"/publish?slug=fast", "text/html", strings.NewReader(strings.Repeat("y", 200000)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("fast upload = %d, want 201", resp.StatusCode)
	}
}

func TestUploadStallAfterFastStartIsCut(t *testing.T) {
	s, _ := limitServer(t, Config{Requests: RequestLimits{
		BodyTimeout:   300 * time.Millisecond,
		UploadMinRate: 1000,
	}})
	ts := httptest.NewServer(s.ControlHandler())
	t.Cleanup(ts.Close)
	// 20 KB up front earns 20s of rate credit; the stall limit still applies.
	c := rawPost(t, ts.Listener.Addr().String(), "/publish?slug=burst", 100000, func(c net.Conn) {
		c.Write(bytes.Repeat([]byte("z"), 20000))
	})
	el, _ := waitClosedOrAnswered(t, c, 10*time.Second)
	if el > 3*time.Second {
		t.Fatalf("stall after a fast start held for %v with a 300ms stall limit", el)
	}
}

func TestNonUploadBodyHasAWholeBodyDeadline(t *testing.T) {
	s, _ := limitServer(t, Config{Requests: RequestLimits{BodyTimeout: 300 * time.Millisecond}})
	read := make(chan error, 1)
	h := s.withRequestLimits(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		read <- err
		if err != nil {
			http.Error(w, "slow body", http.StatusRequestTimeout)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	// Steady trickling does not extend a non-upload deadline.
	stop := make(chan struct{})
	defer close(stop)
	rawPost(t, ts.Listener.Addr().String(), "/answer/x", 1000, func(c net.Conn) {
		go func() {
			for {
				select {
				case <-stop:
					return
				case <-time.After(20 * time.Millisecond):
					if _, err := c.Write([]byte("a")); err != nil {
						return
					}
				}
			}
		}()
	})
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("a trickled body was read to the end")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler still reading a trickled body after 5s")
	}
}

func TestBodyDeadlineLiftsAfterTheBody(t *testing.T) {
	s, _ := limitServer(t, Config{Requests: RequestLimits{BodyTimeout: 200 * time.Millisecond}})
	h := s.withRequestLimits(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		time.Sleep(500 * time.Millisecond) // work past the body deadline
		select {
		case <-r.Context().Done():
			http.Error(w, "cancelled", http.StatusServiceUnavailable)
		default:
			io.WriteString(w, "done")
		}
	}))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	resp, err := http.Post(ts.URL+"/x", "text/plain", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(b) != "done" {
		t.Fatalf("handler working past the body deadline got %d %q: the deadline must lift once the body is read", resp.StatusCode, b)
	}
}

func TestSlowReaderIsCutAfterItsAllowance(t *testing.T) {
	s, st := limitServer(t, Config{Requests: RequestLimits{
		// The timeout is also the longest one write may wait (pacedWriter);
		// on loopback a full socket can pause about 200ms before it drains.
		ResponseTimeout: time.Second,
		DownloadMinRate: 32 << 20, // 32 MiB/s: a 32 MiB body gets about 1s more
	}})
	big := bytes.Repeat([]byte("q"), 32<<20)
	if _, err := st.Put(store.PutOptions{Slug: "big", Filename: "big.bin"}, bytes.NewReader(big)); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.ContentHandler())
	t.Cleanup(ts.Close)
	c, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "GET /big HTTP/1.1\r\nHost: x\r\n\r\n")
	time.Sleep(3 * time.Second) // read nothing past the allowance
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	got, _ := io.Copy(io.Discard, bufio.NewReader(c))
	if got >= int64(len(big)) {
		t.Fatalf("a reader that stalled past its allowance still received all %d bytes", got)
	}

	// A reader that keeps up receives everything.
	resp, err := http.Get(ts.URL + "/big")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if n != int64(len(big)) {
		t.Fatalf("a prompt reader got %d of %d bytes", n, len(big))
	}
}

func TestLiveStreamOutlivesTheResponseTimeout(t *testing.T) {
	s, st := limitServer(t, Config{Requests: RequestLimits{ResponseTimeout: 200 * time.Millisecond}})
	if _, err := st.Put(store.PutOptions{Slug: "watched"}, strings.NewReader("v1")); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.ContentHandler())
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/_events/watched")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscribe = %d", resp.StatusCode)
	}
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	time.Sleep(time.Second) // five response timeouts
	if _, err := st.Put(store.PutOptions{Slug: "watched"}, strings.NewReader("v2")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("live stream closed before the reload event")
			}
			if l == "event: reload" {
				return
			}
		case <-deadline:
			t.Fatal("no reload event on a stream older than the response timeout")
		}
	}
}

func TestRequestAdmissionLimits(t *testing.T) {
	s, _ := limitServer(t, Config{Requests: RequestLimits{MaxRequests: 2, MaxRequestsPerClient: 1}})
	release := make(chan struct{})
	var entered sync.WaitGroup
	h := s.withRequestLimits(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered.Done()
		<-release
	}))
	do := func(remote string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	entered.Add(2)
	var wg sync.WaitGroup
	for _, remote := range []string{"192.0.2.1:1", "192.0.2.2:1"} {
		wg.Add(1)
		go func(remote string) { defer wg.Done(); do(remote) }(remote)
	}
	entered.Wait()
	// Per client: a second request from a busy client is refused.
	// Per listener: a third client finds no room either.
	for _, remote := range []string{"192.0.2.1:2", "192.0.2.3:1"} {
		w := do(remote)
		if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
			t.Errorf("request from %s with the limits full = %d, want 503 with Retry-After", remote, w.Code)
		}
	}
	close(release)
	wg.Wait()
	entered.Add(1)
	if w := do("192.0.2.1:3"); w.Code != http.StatusOK {
		t.Fatalf("request after the others finished = %d, want 200", w.Code)
	}
}

func TestPerClientRequestLimitSparesOtherClients(t *testing.T) {
	s, _ := limitServer(t, Config{Requests: RequestLimits{MaxRequestsPerClient: 1}})
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	h := s.withRequestLimits(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		if r.RemoteAddr == "192.0.2.1:1" {
			<-release
		}
	}))
	go func() {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.RemoteAddr = "192.0.2.1:1"
		h.ServeHTTP(httptest.NewRecorder(), r)
	}()
	<-entered
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.RemoteAddr = "192.0.2.2:1"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	close(release)
	if w.Code != http.StatusOK {
		t.Fatalf("another client while one is at its limit = %d, want 200", w.Code)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\r'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestRefusedRequestWithStalledBodyIsClosed: a request refused at admission
// whose body never arrives must not hold the connection while the server
// drains it.
func TestRefusedRequestWithStalledBodyIsClosed(t *testing.T) {
	s, _ := limitServer(t, Config{Requests: RequestLimits{
		BodyTimeout: 300 * time.Millisecond,
		MaxRequests: 1,
	}})
	release := make(chan struct{})
	entered := make(chan struct{})
	h := s.withRequestLimits(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	}))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(release) })
	go http.Get(ts.URL + "/hold")
	<-entered
	c := rawPost(t, ts.Listener.Addr().String(), "/x", 100000, func(c net.Conn) {
		io.WriteString(c, "a little")
	})
	el, resp := waitClosedOrAnswered(t, c, 5*time.Second)
	if !strings.HasPrefix(resp, "HTTP/1.1 503") {
		t.Fatalf("refused request answered %q, want 503", firstLine(resp))
	}
	if el > 3*time.Second {
		t.Fatalf("refused request with a stalled body held its connection %v", el)
	}
}

// TestSlowValidUploadOutlastsResponseTimeout: an upload that keeps above the
// rate floor but takes longer than the response timeout still gets its 201.
func TestSlowValidUploadOutlastsResponseTimeout(t *testing.T) {
	s, st := limitServer(t, Config{Requests: RequestLimits{
		BodyTimeout:   time.Second,
		UploadMinRate: 1000,
		// The body takes three seconds, three times the response timeout; the
		// second left after it is room for the store write under load.
		ResponseTimeout: time.Second,
	}})
	ts := httptest.NewServer(s.ControlHandler())
	t.Cleanup(ts.Close)
	const total = 9000
	c := rawPost(t, ts.Listener.Addr().String(), "/publish?slug=steady", total, func(c net.Conn) {
		// 300 bytes every 100ms: 3000 B/s for three seconds.
		for i := 0; i < total/300; i++ {
			if _, err := c.Write(bytes.Repeat([]byte("s"), 300)); err != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("no response to a steady upload: %v", err)
	}
	if !strings.HasPrefix(line, "HTTP/1.1 201") {
		t.Fatalf("steady upload answered %q, want 201", strings.TrimSpace(line))
	}
	if _, _, err := st.Get("steady"); err != nil {
		t.Fatalf("steady upload not stored: %v", err)
	}
}

// TestUploadResponseHasAWriteDeadlineWithoutBodyEOF: an upload answered
// before its body is read (an early refusal) still gets a write deadline, so
// a client that never reads cannot hold the response.
func TestUploadResponseHasAWriteDeadlineWithoutBodyEOF(t *testing.T) {
	s, _ := limitServer(t, Config{Requests: RequestLimits{
		BodyTimeout:     200 * time.Millisecond,
		UploadMinRate:   1 << 20,
		ResponseTimeout: 200 * time.Millisecond,
	}})
	big := bytes.Repeat([]byte("r"), 32<<20)
	wrote := make(chan error, 1)
	h := s.withRequestLimits(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write(big) // answer without reading the body
		wrote <- err
	}))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	rawPost(t, ts.Listener.Addr().String(), "/publish?site=x", 10, nil)
	select {
	case err := <-wrote:
		if err == nil {
			t.Fatal("a 32 MiB response to a client that reads nothing completed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("response write still blocked after 10s with a 200ms response timeout")
	}
}

// TestSubscribedStreamLeavesAdmission: an open live stream does not hold a
// request slot.
func TestSubscribedStreamLeavesAdmission(t *testing.T) {
	s, st := limitServer(t, Config{Requests: RequestLimits{MaxRequests: 1, MaxRequestsPerClient: 1}})
	if _, err := st.Put(store.PutOptions{Slug: "watched"}, strings.NewReader("v1")); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.ContentHandler())
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/_events/watched")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscribe = %d", resp.StatusCode)
	}
	bufio.NewReader(resp.Body).ReadString('\n') // connected
	r2, err := http.Get(ts.URL + "/watched")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("GET with a stream open and one request slot = %d, want 200", r2.StatusCode)
	}
}

// TestStalledReaderIsCutDespiteALongAllowance: a download's allowance at the
// minimum rate does not let a reader that has stopped reading hold its
// connection (and admission slot) for the whole allowance: each write must
// make progress within the response timeout.
func TestStalledReaderIsCutDespiteALongAllowance(t *testing.T) {
	s, st := limitServer(t, Config{Requests: RequestLimits{
		ResponseTimeout: time.Second,
		DownloadMinRate: 1 << 10, // 1 KiB/s: hours of allowance for this body
	}})
	big := bytes.Repeat([]byte("q"), 32<<20)
	if _, err := st.Put(store.PutOptions{Slug: "big", Filename: "big.bin"}, bytes.NewReader(big)); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.ContentHandler())
	t.Cleanup(ts.Close)
	c, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "GET /big HTTP/1.1\r\nHost: x\r\n\r\n")
	time.Sleep(3 * time.Second) // stop reading
	_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
	got, _ := io.Copy(io.Discard, bufio.NewReader(c))
	if got >= int64(len(big)) {
		t.Fatalf("a reader that stalled still received all %d bytes", got)
	}

	// A reader that keeps up receives everything.
	resp, err := http.Get(ts.URL + "/big")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if n != int64(len(big)) {
		t.Fatalf("a prompt reader got %d of %d bytes", n, len(big))
	}
}
