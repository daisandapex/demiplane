// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/daisandapex/demiplane/internal/render"
)

// renderlimit.go bounds markdown rendering on the server: each render runs
// under render.Limits (work and output, see internal/render/budget.go), and at
// most renderConcurrency renders run at once, so a burst of large publishes
// or a bulk rebake cannot take every core or hold many large outputs in
// memory together. A render waits briefly for a slot and then gives up with
// 503, rather than queueing without bound.

// maxDefaultRenderConcurrency caps the default slot count on large hosts.
const maxDefaultRenderConcurrency = 4

// renderWait is how long a render waits for a free slot before the request is
// answered 503. A var so tests can shorten it.
var renderWait = 5 * time.Second

// renderHeld, when set by a test, runs while a publish render holds its slot.
var renderHeld func()

// renderRetryAfter is the Retry-After (seconds) sent with a busy 503.
const renderRetryAfter = 5

// errRenderBusy means every render slot stayed taken for renderWait.
var errRenderBusy = errors.New("markdown rendering is at capacity; retry shortly")

// defaultRenderConcurrency is GOMAXPROCS, capped at maxDefaultRenderConcurrency.
func defaultRenderConcurrency() int {
	return min(max(runtime.GOMAXPROCS(0), 1), maxDefaultRenderConcurrency)
}

// newRenderSem returns the render slot semaphore for n slots (0 = default).
func newRenderSem(n int) chan struct{} {
	if n <= 0 {
		n = defaultRenderConcurrency()
	}
	return make(chan struct{}, n)
}

// acquireRender takes a render slot, waiting at most renderWait and no longer
// than ctx allows. The returned release must be called exactly once.
func (s *Server) acquireRender(ctx context.Context) (release func(), err error) {
	select {
	case s.renderSem <- struct{}{}:
		return func() { <-s.renderSem }, nil
	default:
	}
	t := time.NewTimer(renderWait)
	defer t.Stop()
	select {
	case s.renderSem <- struct{}{}:
		return func() { <-s.renderSem }, nil
	case <-t.C:
		return nil, errRenderBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// renderDoc renders src under a slot and the configured limits.
func (s *Server) renderDoc(ctx context.Context, src []byte, opts render.Options) ([]byte, error) {
	release, err := s.acquireRender(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if renderHeld != nil {
		renderHeld()
	}
	opts.Limits = s.renderLimits
	return render.Render(ctx, src, opts)
}

// writeRenderError answers a failed render: a limit is the client's document
// (413, naming the limit), a full slot pool is 503 with Retry-After, and a
// cancelled request gets 503 too (the client has usually gone).
func writeRenderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, render.ErrBudget):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	case errors.Is(err, errRenderBusy):
		w.Header().Set("Retry-After", strconv.Itoa(renderRetryAfter))
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "render cancelled", http.StatusServiceUnavailable)
	default:
		log.Printf("render failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
