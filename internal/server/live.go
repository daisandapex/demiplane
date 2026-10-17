// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/daisandapex/demiplane/internal/limit"
	"github.com/daisandapex/demiplane/internal/logsafe"
	"github.com/daisandapex/demiplane/internal/module"
	"github.com/daisandapex/demiplane/internal/store"
)

// live.go owns feature T2.1: SSE live-reload. On the CONTENT origin it serves
// GET /_events/{slug} — an SSE stream that emits `event: reload` when the slug
// is republished — and, for a ?live view of GET /{slug}, serves a first-party
// wrapper that reloads on that event. The in-process pub/sub is driven by the
// store Notifier seam (internal/store/notify.go): Put -> Notify(slug) -> fan-out.
//
// URL shape: the SSE endpoint is /_events/{slug} (literal-first), NOT the
// /{slug}/_events shape from the design sketch. Go 1.22's ServeMux rejects
// /{slug}/_events because it conflicts with the existing control route
// /docs/{page} (wildcard-first vs literal-first cross, neither more specific) on
// the combined --unsafe-same-origin mux. Namespacing under a reserved _events
// prefix is conflict-free in split AND combined mode.
//
// The ?live wrapper does NOT embed or rewrite the artifact's own bytes: it
// serves a tiny first-party shell that loads the artifact in a same-origin
// iframe (its normal, unmodified GET /{slug}) and carries the reload script
// under its OWN nonce-CSP. The artifact renders under its own headers/CSP,
// untouched — and a plain (non-live) GET is byte-identical to the stored bytes
// because serveLiveView returns false the instant ?live is absent.
//
// Idle/lifetime: http.Server.IdleTimeout governs keep-alive BETWEEN requests,
// not an in-flight request, so it cannot by itself close a live SSE stream. The
// stream instead self-maintains under it: a heartbeat comment every ~25s keeps
// the connection (and any intermediary) from going idle, a per-write deadline
// drops a stalled/slow consumer, and the request context ends the loop the
// moment the client disconnects. Per-slug, per-client and global subscriber
// caps bound the FD/goroutine/memory a flood of EventSource connections can
// hold.
//
// Authorization: a stream is a view of its artifact, so it is opened only for
// an artifact that exists, has not expired, and whose password (if any) the
// request satisfies with HTTP Basic, exactly like GET /{slug}. Knowing a
// private artifact's capability slug is its authorization. The grant is
// re-checked on every republish and every heartbeat: a deleted or expired
// artifact, or a password the stream's credentials no longer match, ends the
// stream, and the browser's EventSource has to reconnect and pass the check
// again.

// liveHeartbeat is the SSE comment-ping cadence, and the interval at which an
// open stream's authorization is re-checked. Kept below the default
// --idle-timeout (120s) so the stream never looks idle to the server or an
// intermediary proxy, and so a dead peer is detected within one interval. A
// var only so tests can shorten it.
var liveHeartbeat = 25 * time.Second

// liveWriteTimeout bounds a single SSE write/flush. A consumer that cannot
// absorb the bytes within it is dropped (its goroutine + FD reclaimed) rather
// than allowed to wedge the handler — the "drop slow consumers" guard. A var
// only so tests can shorten it.
var liveWriteTimeout = 10 * time.Second

const (
	// Default live-reload caps (LiveLimits). liveMaxPerSlug: streams watching
	// one slug. liveMaxPerClient: streams from one client (an IPv4 address or
	// IPv6 /64, or the forwarded client behind a --trusted-proxy) on all slugs.
	// liveMaxPerClientSlug: streams from one client on one slug.
	// liveMaxTotal: streams in all, the ceiling on the FDs and goroutines the
	// endpoint can hold. liveMaxPending and liveMaxPendingPerClient: requests
	// waiting for an ended stream's slot, in all and from one client; they
	// hold no request-admission slot while they wait.
	liveMaxPerSlug          = 16
	liveMaxPerClient        = 16
	liveMaxPerClientSlug    = 2
	liveMaxTotal            = 256
	liveMaxPending          = 32
	liveMaxPendingPerClient = 1
	// liveNonceBytes is the entropy of the per-response CSP nonce for the wrapper
	// reload script (128 bits, base64-encoded).
	liveNonceBytes = 16
)

func init() {
	// One hub per Server, created and subscribed to the store at mount time
	// (runs once at startup, before serving). The route handler closes over the
	// same hub the notifier fans out to. Only one notifier may be set on the
	// store; live.go is the sole subscriber (see notify.go).
	registerCoreContentRoute([]string{"_events"}, func(mux *http.ServeMux, s *Server) {
		hub := newLiveHub(s.liveLimits)
		s.liveHub = hub
		s.store.SetNotifier(hub)
		mux.HandleFunc("GET /_events/{slug}", func(w http.ResponseWriter, r *http.Request) {
			s.handleLiveEvents(hub, w, r)
		})
	})
	// ?live wrapper hook (routes.go). Non-live GETs (hook returns false) fall
	// through to handleGet's normal, byte-identical serving.
	liveView = serveLiveView
}

// LiveLimits caps live-reload streams. Zero fields take the defaults.
type LiveLimits struct {
	PerSlug       int // streams watching one slug
	PerClient     int // streams from one client on all slugs
	PerClientSlug int // streams from one client on one slug
	Total         int // streams in all
	// Pending and PendingPerClient cap requests waiting for an ended
	// stream's slot, in all and from one client.
	Pending          int
	PendingPerClient int
}

func (l LiveLimits) resolved() LiveLimits {
	pick := func(v, def int) int {
		if v > 0 {
			return v
		}
		return def
	}
	return LiveLimits{
		PerSlug:       pick(l.PerSlug, liveMaxPerSlug),
		PerClient:     pick(l.PerClient, liveMaxPerClient),
		PerClientSlug: pick(l.PerClientSlug, liveMaxPerClientSlug),
		Total:         pick(l.Total, liveMaxTotal),

		Pending:          pick(l.Pending, liveMaxPending),
		PendingPerClient: pick(l.PendingPerClient, liveMaxPendingPerClient),
	}
}

// liveHub is the in-process, per-slug pub/sub backing the SSE endpoint. It
// implements store.Notifier. All state is guarded by mu; the notify path never
// blocks (non-blocking sends into buffered per-subscriber channels), honoring
// the Notifier contract that Notify runs on the publish goroutine.
//
// Admission is fair rather than first come: a client's own caps (per slug,
// and in all) refuse its further streams, and when a shared cap (the slug's,
// or the total) is full, a newcomer takes the place of the oldest stream of
// the client holding the most, provided the move improves balance: that
// client holds at least two more than the newcomer. Otherwise the newcomer
// is refused, so a stream is never moved between clients holding about as
// many, which its reconnect would only move back. An ended stream keeps its
// slot until its handler returns (at most liveWriteTimeout, if it is blocked
// in a write); the newcomer waits for that, so the caps are never exceeded.
// Waiting newcomers hold no request-admission slot, so their number is
// bounded separately (LiveLimits.Pending, PendingPerClient), and each waits
// at most drainWait in all.
type liveHub struct {
	mu        sync.Mutex
	limits    LiveLimits
	subs      map[string]map[*liveSub]struct{} // by slug
	byClient  map[string]map[*liveSub]struct{}
	total     int
	seq       uint64
	heartbeat time.Duration // liveHeartbeat, captured when the hub is built
	// drainWait bounds how long a newcomer waits, in all, for ended
	// streams' handlers to return; a field so tests can shorten it.
	drainWait time.Duration
	// pending counts newcomers waiting for a slot, in all and by client.
	pending       int
	pendingClient map[string]int
}

// liveSub is one stream. ch carries reload signals; gone is closed when the
// hub ends the stream to make room for another client, and done when its
// handler has unsubscribed and its slot is free.
type liveSub struct {
	ch     chan struct{}
	gone   chan struct{}
	done   chan struct{}
	slug   string
	client string
	seq    uint64 // admission order: lower is older
	ended  bool   // gone is closed; the slot is held until done
}

func newLiveHub(l LiveLimits) *liveHub {
	return &liveHub{
		limits:        l.resolved(),
		subs:          make(map[string]map[*liveSub]struct{}),
		byClient:      make(map[string]map[*liveSub]struct{}),
		heartbeat:     liveHeartbeat,
		drainWait:     liveWriteTimeout + time.Second,
		pendingClient: make(map[string]int),
	}
}

// Notify implements store.Notifier: fan a reload signal out to every subscriber
// on slug. Non-blocking: a subscriber whose 1-slot buffer is already full
// keeps its pending signal (one queued reload is as good as two), so a slow
// consumer can never stall the publish that called Put.
func (h *liveHub) Notify(slug string) {
	h.mu.Lock()
	for sub := range h.subs[slug] {
		select {
		case sub.ch <- struct{}{}:
		default:
		}
	}
	h.mu.Unlock()
}

// subscribe admits a stream for slug on behalf of client. It returns
// ok=false when the client is at one of its own caps, or a shared cap is full
// and no other client holds at least two more streams than this one, or the
// pending caps are full. When it must wait for an ended stream's handler to
// return, it holds a pending reservation, released however the wait ends,
// and waits at most drainWait in all (and no longer than ctx).
func (h *liveHub) subscribe(ctx context.Context, slug, client string) (*liveSub, bool) {
	var deadline <-chan time.Time
	reserved := false
	defer func() {
		if reserved {
			h.unreserve(client)
		}
	}()
	// A slug cap and the total cap may each need a stream ended; the last
	// pass only admits.
	for pass := 0; pass < 3; pass++ {
		sub, wait, ok := h.admit(slug, client, pass < 2, reserved)
		if sub != nil {
			return sub, true
		}
		if !ok {
			return nil, false
		}
		reserved = true
		if deadline == nil {
			t := time.NewTimer(h.drainWait)
			defer t.Stop()
			deadline = t.C
		}
		select {
		case <-wait.done:
		case <-ctx.Done():
			return nil, false
		case <-deadline:
			return nil, false
		}
	}
	return nil, false
}

// admit takes a slot for client on slug if the caps allow and returns it.
// If a shared cap is full and mayWait is set, it instead picks a stream to
// wait for (ending it if it is live) and returns that with ok; unless
// reserved, it first takes a pending reservation, and refuses when the
// pending caps are full. ok=false is a refusal.
func (h *liveHub) admit(slug, client string, mayWait, reserved bool) (sub, wait *liveSub, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// A client's own caps count its live streams; ended ones are leaving.
	mine, onSlug := 0, 0
	for s := range h.byClient[client] {
		if !s.ended {
			mine++
			if s.slug == slug {
				onSlug++
			}
		}
	}
	if mine >= h.limits.PerClient || onSlug >= h.limits.PerClientSlug {
		return nil, nil, false
	}
	// The shared caps count every slot held, ended streams included.
	var set map[*liveSub]struct{}
	held := mine
	switch {
	case len(h.subs[slug]) >= h.limits.PerSlug:
		set, held = h.subs[slug], onSlug
	case h.total >= h.limits.Total:
	default:
		h.seq++
		sub = &liveSub{
			ch: make(chan struct{}, 1), gone: make(chan struct{}), done: make(chan struct{}),
			slug: slug, client: client, seq: h.seq,
		}
		h.add(sub)
		return sub, nil, true
	}
	if !mayWait {
		return nil, nil, false
	}
	wait = h.slotToWaitFor(set, client, held, len(h.subs[slug]) >= h.limits.PerSlug)
	if wait == nil {
		return nil, nil, false
	}
	if !reserved {
		if h.pending >= h.limits.Pending || h.pendingClient[client] >= h.limits.PendingPerClient {
			return nil, nil, false
		}
		h.pending++
		h.pendingClient[client]++
	}
	if !wait.ended {
		wait.ended = true
		close(wait.gone)
	}
	return nil, wait, true
}

func (h *liveHub) unreserve(client string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pending--
	if h.pendingClient[client]--; h.pendingClient[client] <= 0 {
		delete(h.pendingClient, client)
	}
}

// slotToWaitFor picks the stream whose slot the requester may wait for, in
// slug's set (bySlug) or in all: the oldest live stream of the client
// holding the most live streams there, if that client holds at least two
// more than held, the requester's own count; otherwise a stream already
// ended and still draining, if any; otherwise nil. It ends nothing. Caller
// holds mu.
func (h *liveHub) slotToWaitFor(set map[*liveSub]struct{}, requester string, held int, bySlug bool) *liveSub {
	counts := map[string]int{}
	oldest := map[string]*liveSub{}
	var draining *liveSub
	consider := func(s *liveSub) {
		if s.ended {
			draining = s
			return
		}
		if s.client == requester {
			return
		}
		counts[s.client]++
		if o := oldest[s.client]; o == nil || s.seq < o.seq {
			oldest[s.client] = s
		}
	}
	if bySlug {
		for s := range set {
			consider(s)
		}
	} else {
		for _, subs := range h.byClient {
			for s := range subs {
				consider(s)
			}
		}
	}
	most := 0
	for _, n := range counts {
		most = max(most, n)
	}
	if most < held+2 {
		return draining
	}
	var victim *liveSub
	for c, n := range counts {
		if n == most && (victim == nil || oldest[c].seq < victim.seq) {
			victim = oldest[c]
		}
	}
	return victim
}

func (h *liveHub) add(sub *liveSub) {
	if h.subs[sub.slug] == nil {
		h.subs[sub.slug] = make(map[*liveSub]struct{})
	}
	h.subs[sub.slug][sub] = struct{}{}
	if h.byClient[sub.client] == nil {
		h.byClient[sub.client] = make(map[*liveSub]struct{})
	}
	h.byClient[sub.client][sub] = struct{}{}
	h.total++
}

// unsubscribe frees sub's slot; its handler calls it on the way out.
func (h *liveHub) unsubscribe(sub *liveSub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.subs[sub.slug]
	if _, ok := set[sub]; !ok {
		return
	}
	delete(set, sub)
	if len(set) == 0 {
		delete(h.subs, sub.slug)
	}
	mine := h.byClient[sub.client]
	delete(mine, sub)
	if len(mine) == 0 {
		delete(h.byClient, sub.client)
	}
	h.total--
	close(sub.done)
}

// liveClientKey is the client a stream counts against for the per-client cap:
// limit.ClientKey, the IPv4 address or IPv6 /64 the server resolved for the
// request (the TCP peer, or the forwarded client behind a --trusted-proxy).
func liveClientKey(r *http.Request) string { return limit.ClientKey(r) }

// liveGrant records the password hash a stream's credentials were last
// verified against. The credentials themselves stay on the stream's request.
type liveGrant struct {
	hash string
}

// authorizeLive checks that slug names a live artifact the request may view:
// it exists, has not expired, and any password matches the request's HTTP Basic
// credentials, through the same rate and concurrency limits as GET /{slug}
// (viewauth.go). On failure it writes the response and returns ok=false.
func (s *Server) authorizeLive(w http.ResponseWriter, r *http.Request, slug string) (liveGrant, bool) {
	art, f, err := s.store.Get(slug)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
		} else {
			log.Printf("live events %s: %s", redactSlug(slug), logsafe.Err(err, slug))
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return liveGrant{}, false
	}
	f.Close()
	if !art.HasPassword() {
		return liveGrant{}, true
	}
	// A refusal challenges with the artifact's own realm: a browser that holds
	// credentials for it answers from its cache. The ?live wrapper is gated on
	// the same password, so by the time it opens the stream the viewer has
	// entered it.
	setProtectedHeaders(w)
	if res, wait := s.CheckView(r, art); writeViewDenied(w, slug, res, wait) {
		return liveGrant{}, false
	}
	return liveGrant{hash: art.PasswordHash}, true
}

// stillAuthorized re-checks an open stream's grant against the artifact's
// current state. A republish re-salts the stored hash even when the password is
// unchanged, so a changed hash is re-verified against the credentials of the
// stream's request r rather than treated as a revocation. That check goes
// through the view-password gate like every other one, so republishing cannot
// make open streams run unbounded KDF work; a check the gate refuses for load
// ends the stream, and the client's reconnect is checked again.
func (s *Server) stillAuthorized(r *http.Request, slug string, g *liveGrant) bool {
	art, f, err := s.store.Get(slug)
	if err != nil {
		return false
	}
	f.Close()
	switch {
	case !art.HasPassword():
		g.hash = ""
		return true
	case art.PasswordHash == g.hash:
		return true
	}
	if res, _ := s.CheckView(r, art); res == module.ViewOK {
		g.hash = art.PasswordHash
		return true
	}
	return false
}

// handleLiveEvents streams reload events for a slug over Server-Sent Events. It
// authorizes the request against the artifact, subscribes to the hub, emits
// `event: reload` on each republish, a heartbeat comment every liveHeartbeat,
// and returns when the client disconnects, a write stalls past
// liveWriteTimeout, a subscriber cap rejects the connection, or the stream's
// authorization lapses.
func (s *Server) handleLiveEvents(hub *liveHub, w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	grant, ok := s.authorizeLive(w, r, slug)
	if !ok {
		return
	}

	client := liveClientKey(r)
	// Leave the request-admission limits before any wait for a slot: the
	// hub's pending caps bound waiting newcomers instead.
	releaseAdmission(r)
	sub, ok := hub.subscribe(r.Context(), slug, client)
	if !ok {
		http.Error(w, "too many live subscribers", http.StatusServiceUnavailable)
		return
	}
	defer hub.unsubscribe(sub)
	// Subscribed: the stream is bounded by the subscriber caps and per-write
	// deadlines now, not by the request limits or the response deadline.
	detachLongLived(w, r)

	rc := http.NewResponseController(w)

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream; charset=utf-8")
	hdr.Set("Cache-Control", "no-store, no-transform")
	hdr.Set("Connection", "keep-alive")
	// Defeat response buffering by a reverse proxy (nginx honors this) so events
	// are delivered promptly rather than coalesced.
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Open the stream immediately so the client's EventSource connects without
	// waiting for the first republish.
	if err := writeFlush(rc, w, ": connected\n\n"); err != nil {
		return
	}

	heartbeat := time.NewTicker(hub.heartbeat)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.gone: // ended to make room for another client
			return
		case <-sub.ch:
			if !s.stillAuthorized(r, slug, &grant) {
				return
			}
			if err := writeFlush(rc, w, "event: reload\ndata: 1\n\n"); err != nil {
				return
			}
		case <-heartbeat.C:
			if !s.stillAuthorized(r, slug, &grant) {
				return
			}
			if err := writeFlush(rc, w, ": ping\n\n"); err != nil {
				return
			}
		}
	}
}

// writeFlush writes an SSE frame and flushes it, under a write deadline so a
// stalled consumer is dropped rather than allowed to block the handler. A
// deadline the transport does not support is a no-op (best-effort guard); a
// write/flush error is returned so the caller drops the subscriber.
//
// The deadline is cleared once the frame is flushed: HTTP/2 resets a stream
// whose write deadline passes even with nothing to write, so one left armed
// would end a quiet stream between heartbeats. (Under --write-timeout,
// clearing restores that cap; cappedWriteDeadline.)
func writeFlush(rc *http.ResponseController, w http.ResponseWriter, frame string) error {
	_ = rc.SetWriteDeadline(time.Now().Add(liveWriteTimeout))
	if _, err := io.WriteString(w, frame); err != nil {
		return err
	}
	if err := rc.Flush(); err != nil {
		return err
	}
	_ = rc.SetWriteDeadline(time.Time{})
	return nil
}

// serveLiveView is the liveView hook: for a ?live GET /{slug} it serves a
// first-party reload wrapper and returns true; for every other GET it returns
// false so handleGet serves the stored bytes verbatim (the byte-identical
// promise). The wrapper NEVER emits the artifact's own bytes — it iframes the
// artifact's normal same-origin URL, so the artifact renders under its own
// headers/CSP while the wrapper carries only the reload script under its own
// nonce-CSP.
func serveLiveView(s *Server, w http.ResponseWriter, r *http.Request) bool {
	if !queryFlag(r.URL.Query(), "live") {
		return false
	}
	slug := r.PathValue("slug")

	// Confirm the artifact resolves before serving a wrapper; otherwise fall
	// through so handleGet renders the proper 404/500 in one place. Existence is
	// already observable via GET /{slug}, so this gate leaks nothing new. A
	// password-protected artifact's wrapper takes the same password (falling
	// through serves handleGet's challenge), so the viewer authenticates once,
	// at the top level, before the frame and the event stream ask. The bytes are
	// not needed here (the iframe fetches them through the normal path), so
	// close the handle immediately.
	art, f, err := s.store.Get(slug)
	if err != nil {
		return false
	}
	f.Close()
	if art.HasPassword() && !passwordOK(r, art.PasswordHash) {
		return false
	}

	nonce, err := liveNonce()
	if err != nil {
		// crypto/rand failure is effectively impossible; if it happens, fall
		// back to normal (non-live) serving rather than ship a script with a
		// weak/empty nonce.
		return false
	}

	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	// Lock the wrapper down to exactly what it needs: its inline reload script
	// and style (nonce), the same-origin artifact iframe (frame-src 'self'), and
	// the EventSource (connect-src 'self'). default-src 'none' denies everything
	// else. This governs only the wrapper shell — the artifact in the iframe
	// keeps its own headers/CSP. frame-ancestors has no default-src fallback,
	// so the baseline anti-clickjacking directive is re-stated explicitly.
	hdr.Set("Content-Security-Policy",
		"default-src 'none'; frame-src 'self'; connect-src 'self'; "+
			"script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'; "+
			"base-uri 'none'; "+frameAncestorsSelf)
	hdr.Set("Referrer-Policy", "no-referrer")
	// The shell names the slug, which for a private artifact is the secret.
	hdr.Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, liveWrapperHTML(slug, nonce))
	return true
}

// liveNonce returns a fresh base64 CSP nonce.
func liveNonce() (string, error) {
	b := make([]byte, liveNonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// liveWrapperHTML builds the first-party live-preview shell for slug: a
// full-viewport iframe of the artifact's normal URL plus a reload script bound
// to the SSE stream, both under the per-response CSP nonce. slug is a validated,
// already-stored slug (URL-safe by construction); it is still HTML-escaped for
// the attribute context and JSON-encoded for the script-string context as
// defense-in-depth.
func liveWrapperHTML(slug, nonce string) string {
	slugAttr := html.EscapeString(slug)
	slugJS, _ := json.Marshal(slug)

	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\"><head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s (live)</title>\n", slugAttr)
	fmt.Fprintf(&b,
		"<style nonce=\"%s\">html,body{margin:0;height:100%%}iframe{border:0;display:block;width:100%%;height:100vh}</style>\n",
		nonce)
	b.WriteString("</head>\n<body>\n")
	fmt.Fprintf(&b, "<iframe src=\"/%s\" id=\"live\" title=\"live preview\"></iframe>\n", slugAttr)
	fmt.Fprintf(&b, "<script nonce=\"%s\">\n", nonce)
	// The stream is authorized like the artifact itself and opens once the
	// frame has loaded. EventSource retries a dropped stream on its own but gives
	// up on a refused one (an HTTP error, such as a password that changed), so a
	// closed stream is reopened with backoff, a bounded number of times so a
	// declined password prompt does not come back forever. Both the named
	// reload event and the default message event reload the page.
	fmt.Fprintf(&b,
		"(function(){var s=%s,u='/_events/'+encodeURIComponent(s),d=2000,n=0,on=false;"+
			"function r(){location.reload();}"+
			"function c(){var es=new EventSource(u);es.addEventListener('reload',r);es.onmessage=r;"+
			"es.onopen=function(){d=2000;n=0;};"+
			"es.onerror=function(){if(es.readyState===2&&++n<=8){setTimeout(c,d);d=Math.min(d*2,60000);}};}"+
			"document.getElementById('live').addEventListener('load',function(){if(!on){on=true;c();}});})();\n",
		slugJS)
	b.WriteString("</script>\n</body></html>\n")
	return b.String()
}
