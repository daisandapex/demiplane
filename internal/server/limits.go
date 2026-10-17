// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/daisandapex/demiplane/internal/limit"
)

// limits.go bounds how long a request may hold the server and how many may
// do so at once, on both listeners:
//
//   - Request bodies. An upload (POST /publish) must keep up a minimum average
//     rate after a grace period and may not stall for longer than the body
//     timeout; every other body must arrive whole within the body timeout.
//     The deadline is lifted once the body has been read, so it never cuts
//     off the response.
//   - Responses. Every response gets a write deadline; an artifact or site
//     file gets extra time for its size at a minimum rate. The SSE stream is
//     exempt and keeps its own per-write deadline (live.go).
//   - Admission. At most MaxRequests requests are in progress per listener,
//     and at most MaxRequestsPerClient from one client; the excess is
//     answered 503 at once. The SSE stream is exempt (live.go caps it).
//
// Connection counts are bounded at the listener (limit.Listener, wired in
// cmd/demiplane). A client is its TCP peer address (limit.ClientKey); behind
// a reverse proxy every request shares the proxy's address, so per-client
// limits there belong at the proxy.

// RequestLimits configures limits.go. A zero field takes its default; a
// negative one disables that limit.
type RequestLimits struct {
	BodyTimeout          time.Duration // whole-body deadline, upload grace and stall limit (30s)
	UploadMinRate        int64         // upload bytes a second after the grace (16 KiB)
	ResponseTimeout      time.Duration // base write deadline per response (60s)
	DownloadMinRate      int64         // artifact bytes a second the write deadline allows for (16 KiB)
	MaxRequests          int           // requests in progress per listener (256)
	MaxRequestsPerClient int           // requests in progress per client per listener (32)
}

// Request-limit defaults.
const (
	defaultBodyTimeout          = 30 * time.Second
	defaultUploadMinRate        = 16 << 10
	defaultResponseTimeout      = 60 * time.Second
	defaultDownloadMinRate      = 16 << 10
	defaultMaxRequests          = 256
	defaultMaxRequestsPerClient = 32
)

func pickDuration(v, def time.Duration) time.Duration {
	switch {
	case v < 0:
		return 0
	case v == 0:
		return def
	}
	return v
}

func pickInt64(v, def int64) int64 {
	switch {
	case v < 0:
		return 0
	case v == 0:
		return def
	}
	return v
}

func (l RequestLimits) resolved() RequestLimits {
	return RequestLimits{
		BodyTimeout:          pickDuration(l.BodyTimeout, defaultBodyTimeout),
		UploadMinRate:        pickInt64(l.UploadMinRate, defaultUploadMinRate),
		ResponseTimeout:      pickDuration(l.ResponseTimeout, defaultResponseTimeout),
		DownloadMinRate:      pickInt64(l.DownloadMinRate, defaultDownloadMinRate),
		MaxRequests:          int(pickInt64(int64(l.MaxRequests), defaultMaxRequests)),
		MaxRequestsPerClient: int(pickInt64(int64(l.MaxRequestsPerClient), defaultMaxRequestsPerClient)),
	}
}

// isSSE reports whether r is the live-reload event stream, which is
// long-lived by design and bounded by live.go instead.
func isSSE(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/_events/")
}

// isUpload reports whether r is a publish upload, the one route whose body
// may legitimately be large.
func isUpload(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/publish"
}

// hasBody reports whether r may carry a body.
func hasBody(r *http.Request) bool {
	return r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0
}

// withRequestLimits wraps a listener's handler with the body, response and
// admission limits. One wrapper per listener: the in-progress counts are per
// listener.
func (s *Server) withRequestLimits(next http.Handler) http.Handler {
	l := s.reqLimits
	inflight := limit.NewGate(l.MaxRequests)
	perClient := newClientGates(l.MaxRequestsPerClient)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		now := time.Now()
		sse := isSSE(r)
		// The body deadline goes on first, before any refusal, so that even a
		// refused request cannot hold the connection while the server drains
		// the body it left unread.
		bodied := hasBody(r) && l.BodyTimeout > 0
		upload := bodied && isUpload(r)
		if bodied {
			br := &bodyDeadline{r: r.Body, w: w, rc: rc, start: now, grace: l.BodyTimeout}
			if upload {
				br.rate = l.UploadMinRate
				// An upload may legitimately outlast the response timeout,
				// so the write deadline starts when the body is done.
				br.respTimeout = l.ResponseTimeout
			}
			br.arm()
			r.Body = br
		}
		// Every response sets (or clears) its write deadline: one left by an
		// earlier request on a kept-alive connection must not carry over.
		if sse || upload || l.ResponseTimeout <= 0 {
			_ = rc.SetWriteDeadline(time.Time{})
		} else {
			_ = rc.SetWriteDeadline(now.Add(l.ResponseTimeout))
		}
		if sse {
			next.ServeHTTP(w, r)
			return
		}
		if !inflight.TryAcquire() {
			refuseBusy(w, "server busy; try again shortly")
			return
		}
		defer inflight.Release()
		key := limit.ClientKey(r)
		if !perClient.acquire(key) {
			refuseBusy(w, "too many requests in progress from this client")
			return
		}
		defer perClient.release(key)
		next.ServeHTTP(w, r)
	})
}

// refuseBusy answers an admission refusal and closes the connection rather
// than reading on through the request's unread body.
func refuseBusy(w http.ResponseWriter, msg string) {
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Connection", "close")
	http.Error(w, msg, http.StatusServiceUnavailable)
}

// extendWriteDeadline gives a response of size bytes time to reach a client
// reading at the minimum download rate, on top of the base response timeout.
func (s *Server) extendWriteDeadline(w http.ResponseWriter, size int64) {
	l := s.reqLimits
	if l.ResponseTimeout <= 0 {
		return
	}
	d := l.ResponseTimeout
	if l.DownloadMinRate > 0 && size > 0 {
		d += time.Duration(float64(size) / float64(l.DownloadMinRate) * float64(time.Second))
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
}

// bodyDeadline enforces the body limits on reads. With rate == 0 the whole
// body is due grace after the request started. With rate > 0 (uploads) the
// deadline moves with progress: after n bytes, byte n+1 must arrive by
// start + grace + (n+1)/rate, and no later than grace after now, so the
// average rate cannot fall below rate (after the grace) and the body cannot
// stall for longer than grace. Reaching the end of the body lifts the
// deadline.
type bodyDeadline struct {
	r     io.ReadCloser
	w     http.ResponseWriter
	rc    *http.ResponseController
	start time.Time
	grace time.Duration
	rate  int64
	n     int64
	done  bool
	// respTimeout, when set, starts the response write deadline once the
	// body is finished (uploads).
	respTimeout time.Duration
}

func (b *bodyDeadline) arm() {
	var d time.Time
	if b.rate <= 0 {
		d = b.start.Add(b.grace)
	} else {
		budget := time.Duration(float64(b.n+1) / float64(b.rate) * float64(time.Second))
		d = b.start.Add(b.grace + budget)
		if stall := time.Now().Add(b.grace); stall.Before(d) {
			d = stall
		}
	}
	_ = b.rc.SetReadDeadline(d)
}

func (b *bodyDeadline) Read(p []byte) (int, error) {
	if b.done {
		return b.r.Read(p)
	}
	if b.rate > 0 {
		b.arm()
	}
	n, err := b.r.Read(p)
	b.n += int64(n)
	if err != nil && !b.done {
		b.done = true
		if err == io.EOF {
			_ = b.rc.SetReadDeadline(time.Time{})
		} else {
			// The rest of the body may still be in flight; the connection
			// cannot be reused for another request.
			b.w.Header().Set("Connection", "close")
		}
		if b.respTimeout > 0 {
			_ = b.rc.SetWriteDeadline(time.Now().Add(b.respTimeout))
		}
	}
	return n, err
}

// bodyTimeoutMessage answers a request whose body missed its deadline.
const bodyTimeoutMessage = "request body arrived too slowly"

// isBodyTimeout reports whether err is a request body that missed its read
// deadline (answer 408).
func isBodyTimeout(err error) bool {
	return err != nil && errors.Is(err, os.ErrDeadlineExceeded)
}

func (b *bodyDeadline) Close() error { return b.r.Close() }

// clientGates counts in-progress requests per client.
type clientGates struct {
	max int
	mu  sync.Mutex
	n   map[string]int
}

func newClientGates(max int) *clientGates {
	return &clientGates{max: max, n: map[string]int{}}
}

func (c *clientGates) acquire(key string) bool {
	if c.max <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[key] >= c.max {
		return false
	}
	c.n[key]++
	return true
}

func (c *clientGates) release(key string) {
	if c.max <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[key]--; c.n[key] <= 0 {
		delete(c.n, key)
	}
}
