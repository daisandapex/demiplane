// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package reply is demiplane's inline-reply module: viewers respond to a
// published page (Approve / Defer / free-text) and an agent later lists and
// acks those replies. It is the first RouteModule on the ADR 0001 seam and is
// opt-in — compiled in only with `go build -tags reply` (see
// cmd/demiplane/modules_reply.go). Design: docs/adr/0002-inline-reply-module.md.
//
// Auth is asymmetric and reuses core's posture: submitting a reply is mesh-only
// (a replier is a viewer), while listing/acking replies rides core's bearer
// publish-auth (a publisher action). The module owns its own SQLite store under
// the module data dir; core knows nothing about replies.
package reply

import (
	"encoding/json"
	"errors"
	"html"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/logsafe"
	"github.com/daisandapex/demiplane/v3/internal/module"
	"github.com/daisandapex/demiplane/v3/internal/store"
)

const (
	// maxReplyBody bounds a single reply's free-text (anti-abuse on the open
	// submit endpoint). Over-limit → 413.
	maxReplyBody = 8 << 10 // 8 KiB
	// maxRequestBody caps the whole submit request (form fields + overhead).
	maxRequestBody = 16 << 10 // 16 KiB
)

// validKinds is the allowed set of reply kinds.
var validKinds = map[string]bool{"approve": true, "defer": true, "comment": true}

// std is the package singleton the server mounts (registered below) and the
// instance ConfigureHook targets at startup.
var std = &Module{}

func init() { module.Register(std) }

// Module implements module.RouteModule for inline replies.
type Module struct {
	host module.Host
	rs   *replyStore
	// hook is the reply-event hook configuration (see hook.go). Written once at
	// startup (ConfigureHook), read on every recorded reply.
	hook hookConfig
	// hookDone, when non-nil, is invoked after a hook dispatch completes — a
	// test seam for synchronizing with the asynchronous pool.
	hookDone func(Reply)
	// pool runs hook events; created on first use from hook (hook.go).
	poolOnce sync.Once
	pool     *hookPool
	// adm holds the admission and storage limits (admission.go).
	adm *admission
}

// Name identifies the module (and its data-dir key).
func (*Module) Name() string { return "reply" }

// Reserved claims the module's two top-level segments so no artifact slug can
// shadow them: "replies" (the agent read API) and "reply" (the submit/form
// routes). The submit/form routes are /reply/{slug} — a literal-first pattern,
// deliberately NOT /{slug}/reply: a wildcard-first multi-segment pattern is
// ambiguous with core's literal-first routes (e.g. GET /docs/{page}) under Go's
// ServeMux and panics at registration. See ADR 0002.
func (*Module) Reserved() []string { return []string{"reply", "replies"} }

// Routes opens the module's store and mounts its CONTROL-plane handlers. A
// storage failure is logged and degrades to 503 on every route rather than
// taking down core — a broken module must not break publishing (ADR 0001).
func (m *Module) Routes(mux *http.ServeMux, host module.Host) {
	m.ensureStore(host)

	mux.HandleFunc("GET /reply/{slug}", m.handleForm)
	mux.HandleFunc("POST /reply/{slug}", m.handleSubmit)
	mux.HandleFunc("GET /replies", host.RequireAuth(m.handleList))
	mux.HandleFunc("POST /replies/{id}/ack", host.RequireAuth(m.handleAck))
	mux.HandleFunc("DELETE /replies/{id}", host.RequireAuth(m.handleDelete))
	mux.HandleFunc("DELETE /replies", host.RequireAuth(m.handleDeleteSlug))
}

// ContentReserved returns "answer": the module's content-origin submit route is
// POST /answer/{slug}. A literal-first shape (like the control plane's
// /reply/{slug}) keeps it unambiguous with core's routes and, unlike a bare
// POST /{slug}, it neither shadows the GET /{slug} 405 behaviour of control
// literals in the combined build nor collides with POST /reply/{slug}.
func (*Module) ContentReserved() []string { return []string{"answer"} }

// ContentRoutes mounts the same-origin answer-submit handler on the CONTENT
// origin. A ?reply=question page's baked form posts to /answer/<slug> on the same
// origin that served it, so the confirmation the viewer sees is a real server
// response — never a client-side timer.
func (m *Module) ContentRoutes(mux *http.ServeMux, host module.Host) {
	m.ensureStore(host)
	mux.HandleFunc("POST /answer/{slug}", m.handleContentSubmit)
	// The forward-flow wait endpoint (?next= publish param): the confirmation
	// page refreshes here until the follow-up slug exists, then redirects to it.
	// Same literal-first shape as the submit route; needs no reply storage.
	mux.HandleFunc("GET /answer/{slug}/next", m.handleNextWait)
}

// ensureStore binds the module's SQLite store to host, opening it once per host.
// The module is a package-global singleton, so Routes (control plane) and
// ContentRoutes (content origin) of the SAME server share one handle (host
// identity matches → reuse), while a fresh server — notably every test — rebinds
// rather than keep a stale handle to a previous server's now-deleted database.
func (m *Module) ensureStore(host module.Host) {
	if m.host == host && m.rs != nil {
		return // same server, store already open — reuse across planes
	}
	if m.rs != nil {
		m.rs.close() // rebinding to a new host; drop the old handle
		m.rs = nil
	}
	m.host = host
	dir, err := host.ModuleDataDir(m.Name())
	if err == nil {
		m.rs, err = openReplyStore(dir)
	}
	if err != nil {
		log.Printf("reply module: storage unavailable, serving 503: %s", logsafe.Err(err))
		return
	}
	// An artifact's replies go with it, deleted while its slug is still
	// locked, so a later artifact published under the slug starts with none.
	// A deletion that fails is logged and not retried: those replies stay
	// until DELETE /replies?slug= removes them.
	rs := m.rs
	host.Store().OnRemove(func(slug string) {
		if _, err := rs.deleteSlug(slug); err != nil {
			log.Printf("reply: could not remove a deleted artifact's replies (DELETE /replies?slug= removes them): %s", logsafe.Err(err))
		}
	})
	// Retention (reply_retention, off by default) runs here, never on the
	// submit path.
	m.rs.startSweeper(func() time.Duration { return m.admission().lim.Retention }, retentionEvery)
}

// ready reports whether storage opened; handlers 503 when it did not.
func (m *Module) ready(w http.ResponseWriter) bool {
	if m.rs == nil {
		http.Error(w, "reply storage unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// artifactExists reports whether the target slug names a currently-visible
// (public, unexpired) artifact. It uses HasPublic, not a bare existence check, so
// the reply form never reveals the existence of a private capability-slug artifact
// via a 200-vs-404 oracle (matching the GET /list privacy filter).
func (m *Module) artifactExists(slug string) bool {
	ok, err := m.host.Store().HasPublic(slug)
	if err != nil {
		log.Printf("reply: existence check failed for slug: %s", logsafe.Err(err))
		return false
	}
	return ok
}

// handleForm renders the JS-free reply form for an artifact that accepts
// replies.
func (m *Module) handleForm(w http.ResponseWriter, r *http.Request) {
	if !m.ready(w) {
		return
	}
	slug := r.PathValue("slug")
	art, ref, err := m.target(r, slug)
	switch {
	case errors.Is(err, errNoReplies):
		http.NotFound(w, r)
		return
	case err != nil:
		log.Printf("reply: target lookup failed: %s", logsafe.Err(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	case ref != nil:
		ref.write(w)
		http.Error(w, ref.msg, ref.status)
		return
	}
	writeHTML(w, http.StatusOK, formPage(slug, "", m.host.ReplyFormToken(art)))
}

// handleSubmit stores a reply. Open to any viewer of a reply-enabled artifact
// (with its password, if it has one), under the admission limits in
// admission.go. Accepts a browser form post or a JSON body; the response form
// follows Accept.
func (m *Module) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if !m.ready(w) {
		return
	}
	slug := r.PathValue("slug")
	art, ref, err := m.target(r, slug)
	switch {
	case errors.Is(err, errNoReplies):
		http.NotFound(w, r)
		return
	case err != nil:
		log.Printf("reply: target lookup failed: %s", logsafe.Err(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if ref == nil {
		ref = m.rate(r, slug)
	}
	if ref != nil {
		ref.write(w)
		http.Error(w, ref.msg, ref.status)
		return
	}

	// Cap the whole request body, in bytes and in time, before parsing.
	bodyDeadline(w)
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	kind, body, token, isJSON, err := parseSubmission(r)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "reply too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if bad := m.checkFormToken(r, art, token); bad != formTokenOK {
		http.Error(w, bad.message(), http.StatusForbidden)
		return
	}
	if !validKinds[kind] {
		http.Error(w, "kind must be one of: approve, defer, comment", http.StatusBadRequest)
		return
	}
	if kind == "comment" && strings.TrimSpace(body) == "" {
		http.Error(w, "a comment reply needs non-empty text", http.StatusBadRequest)
		return
	}
	if len(body) > maxReplyBody {
		http.Error(w, "reply text too large", http.StatusRequestEntityTooLarge)
		return
	}

	rep, ref, err := m.record(art, kind, body)
	if err != nil {
		log.Printf("reply: add failed: %s", logsafe.Err(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if ref != nil {
		ref.write(w)
		http.Error(w, ref.msg, ref.status)
		return
	}

	if isJSON || wantsJSON(r) {
		writeJSON(w, http.StatusCreated, rep)
		return
	}
	// Browser form post → re-render the form with a confirmation.
	writeHTML(w, http.StatusCreated, formPage(slug, kind, m.host.ReplyFormToken(art)))
}

// handleContentSubmit records a single free-text answer submitted from a page's
// baked inline reply box (the ?reply=question mode). It runs on the CONTENT
// origin so the post is same-origin to the page that carried the form.
//
// Honesty is structural: this handler renders a "recorded" confirmation ONLY on
// the success branch, AFTER the row is durably inserted. Every failure path —
// unknown/private/disabled slug, missing password, rate limit, empty answer,
// storage down, a limit reached: renders an explicit "not recorded" page with
// the matching status. Because the browser simply navigates to whatever this
// returns (no JS), the viewer cannot be shown success on a failure. Kind is
// fixed to "comment": a question answer is a comment-kind reply (Approve/Defer
// sign-off chrome does not fit a classroom).
func (m *Module) handleContentSubmit(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if m.rs == nil {
		m.writeAnswer(w, slug, http.StatusServiceUnavailable, false,
			"The server could not reach reply storage, so your answer was not recorded. Please try again shortly.", "")
		return
	}
	art, ref, err := m.target(r, slug)
	switch {
	case errors.Is(err, errNoReplies):
		// Unknown, private, expired, or not taking answers: no page here to answer.
		http.NotFound(w, r)
		return
	case err != nil:
		log.Printf("reply: target lookup failed: %s", logsafe.Err(err))
		m.writeAnswer(w, slug, http.StatusInternalServerError, false,
			"The server hit an error and your answer was not recorded. Please try again.", "")
		return
	}
	if ref == nil {
		ref = m.rate(r, slug)
	}
	if ref != nil {
		ref.write(w)
		m.writeAnswer(w, slug, ref.status, false, answerRefusal(ref), "")
		return
	}

	bodyDeadline(w)
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := r.ParseForm(); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			m.writeAnswer(w, slug, http.StatusRequestEntityTooLarge, false,
				"Your answer was too large to record. Please shorten it and try again.", "")
			return
		}
		m.writeAnswer(w, slug, http.StatusBadRequest, false,
			"Your submission could not be read, so nothing was recorded. Please try again.", "")
		return
	}

	if bad := m.checkFormToken(r, art, r.PostFormValue("token")); bad != formTokenOK {
		m.writeAnswer(w, slug, http.StatusForbidden, false, bad.answerText(), "")
		return
	}

	body := r.PostFormValue("body")
	if strings.TrimSpace(body) == "" {
		m.writeAnswer(w, slug, http.StatusBadRequest, false,
			"Your answer was empty, so nothing was recorded. Please go back and write an answer.", "")
		return
	}
	if len(body) > maxReplyBody {
		m.writeAnswer(w, slug, http.StatusRequestEntityTooLarge, false,
			"Your answer was too long to record. Please shorten it and try again.", "")
		return
	}

	_, ref, err = m.record(art, "comment", body)
	if err != nil {
		log.Printf("reply: add failed: %s", logsafe.Err(err))
		m.writeAnswer(w, slug, http.StatusInternalServerError, false,
			"The server could not save your answer, so it was not recorded. Please try again.", "")
		return
	}
	if ref != nil {
		ref.write(w)
		m.writeAnswer(w, slug, ref.status, false, answerRefusal(ref), "")
		return
	}

	// Forward flow (?next= publish param): the baked form carries the follow-up
	// slug as a hidden field. Re-validated here (a hidden field is
	// client-tamperable); anything malformed degrades to the plain confirmation.
	next := forwardSlug(slug, r.PostFormValue("next"))

	// Reached only after a durable insert — this is the one place success is told.
	writeHTML(w, http.StatusCreated,
		answerPage(slug, true, "Your answer was recorded.", body, next))
}

// answerRefusal is the reader-facing "not recorded" text for a refusal.
func answerRefusal(ref *refusal) string {
	switch ref.status {
	case http.StatusUnauthorized:
		return "This page needs its password, so your answer was not recorded. Open the page with its password and try again."
	case http.StatusTooManyRequests:
		if ref.capReached {
			return "This page has reached its answer limit, so your answer was not recorded."
		}
		return "Too many answers are arriving right now, so yours was not recorded. Please wait a minute and try again."
	case http.StatusInsufficientStorage:
		return "The server has no room for more answers, so yours was not recorded."
	case http.StatusConflict:
		return "This page was updated while your answer was being sent, so it was not recorded. Reload the page and try again."
	default:
		return "The server is busy, so your answer was not recorded. Please try again shortly."
	}
}

// handleNextWait is the forward-flow wait endpoint the success confirmation
// refreshes into: GET /answer/{slug}/next?to=<next>. While the follow-up slug
// does not resolve publicly it renders an honest "being prepared" page that
// meta-refreshes itself; the moment the slug exists it redirects there. It
// never claims readiness it has not verified, and it always offers the way
// back to the source page.
func (m *Module) handleNextWait(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !m.artifactExists(slug) {
		http.NotFound(w, r)
		return
	}
	next := forwardSlug(slug, r.URL.Query().Get("to"))
	if next == "" {
		http.Error(w, "?to= must name a valid slug different from the page's own", http.StatusBadRequest)
		return
	}
	if m.artifactExists(next) {
		http.Redirect(w, r, "/"+next, http.StatusFound)
		return
	}
	writeHTML(w, http.StatusOK, waitPage(slug, next))
}

// handleList returns replies as JSON (auth). Filters: ?slug=, ?status=.
func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	if !m.ready(w) {
		return
	}
	q := r.URL.Query()
	status := strings.ToLower(q.Get("status"))
	if status == "" {
		status = "pending"
	}
	switch status {
	case "pending", "read", "all":
	default:
		http.Error(w, "status must be one of: pending, read, all", http.StatusBadRequest)
		return
	}
	var after *listCursor
	if v := q.Get("after"); v != "" {
		c, err := parseListCursor(v)
		if err != nil {
			http.Error(w, "after must be a cursor from a previous response's next", http.StatusBadRequest)
			return
		}
		after = &c
	}
	// One page at a time: a response holds at most listPageSize replies (each
	// at most the reply text cap), whatever the store holds.
	replies, next, err := m.rs.list(q.Get("slug"), status, after, listPageSize)
	if err != nil {
		log.Printf("reply: list failed: %s", logsafe.Err(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if replies == nil {
		replies = []Reply{}
	}
	resp := map[string]any{"replies": replies, "count": len(replies)}
	if next != nil {
		resp["next"] = next.String()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAck marks a reply read (auth). 204 on success, 404 for an unknown id.
func (m *Module) handleAck(w http.ResponseWriter, r *http.Request) {
	if !m.ready(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "reply id must be an integer", http.StatusBadRequest)
		return
	}
	if err := m.rs.ack(id); err != nil {
		if errors.Is(err, errNoReply) {
			http.NotFound(w, r)
			return
		}
		log.Printf("reply: ack failed: %s", logsafe.Err(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDelete deletes one reply (auth). 204 on success, 404 for an unknown
// id. Deleting is how an operator frees reply capacity.
func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !m.ready(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "reply id must be an integer", http.StatusBadRequest)
		return
	}
	if err := m.rs.deleteID(id); err != nil {
		if errors.Is(err, errNoReply) {
			http.NotFound(w, r)
			return
		}
		log.Printf("reply: delete failed: %s", logsafe.Err(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteSlug deletes every reply to ?slug= (auth) and answers how many.
func (m *Module) handleDeleteSlug(w http.ResponseWriter, r *http.Request) {
	if !m.ready(w) {
		return
	}
	slug := r.URL.Query().Get("slug")
	if slug == "" {
		http.Error(w, "slug is required", http.StatusBadRequest)
		return
	}
	n, err := m.rs.deleteSlug(slug)
	if err != nil {
		log.Printf("reply: delete failed for %s: %s", logsafe.Slug(slug), logsafe.Err(err, slug))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// parseSubmission extracts (kind, body) from a JSON body or a form post. The
// bool reports whether the request was JSON (drives the response format).
func parseSubmission(r *http.Request) (kind, body, token string, isJSON bool, err error) {
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	if strings.TrimSpace(ct) == "application/json" {
		var in struct {
			Kind  string `json:"kind"`
			Body  string `json:"body"`
			Token string `json:"token"`
		}
		dec := json.NewDecoder(r.Body)
		derr := dec.Decode(&in)
		if derr == nil {
			// The whole body counts against the request cap and must hold one
			// JSON value: read on to its end, so trailing data past the cap is
			// a 413 and any trailing value is malformed.
			var extra json.RawMessage
			if terr := dec.Decode(&extra); terr != io.EOF {
				derr = terr
				if derr == nil {
					derr = errors.New("trailing data after the JSON value")
				}
			}
		}
		if derr != nil {
			// Keep the size limit's error, so an oversized JSON body is a
			// 413 like an oversized form, not a malformed one.
			var maxErr *http.MaxBytesError
			if errors.As(derr, &maxErr) {
				return "", "", "", true, derr
			}
			return "", "", "", true, errors.New("invalid JSON body")
		}
		return strings.ToLower(strings.TrimSpace(in.Kind)), in.Body, in.Token, true, nil
	}
	if perr := r.ParseForm(); perr != nil {
		// MaxBytesReader surfaces here for an oversized form body.
		return "", "", "", false, perr
	}
	return strings.ToLower(strings.TrimSpace(r.PostFormValue("kind"))), r.PostFormValue("body"),
		r.PostFormValue("token"), false, nil
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// writeAnswer renders the server-truth result page for an inline-answer
// submission and writes it with status. ok=true is emitted by exactly one
// caller — the post-persist success branch — so this page can never claim
// success on a failure. On success the recorded answer is echoed back
// (HTML-escaped) so the viewer sees exactly what the server stored; failure
// pages carry no success wording, only the reason and a way back to the page.
func (m *Module) writeAnswer(w http.ResponseWriter, slug string, status int, ok bool, message, recorded string) {
	writeHTML(w, status, answerPage(slug, ok, message, recorded, ""))
}

// forwardSlug validates a next-lesson pointer against slug. The value travels
// through the baked form as a hidden field (or the wait endpoint's ?to=), both
// client-tamperable surfaces, so it is re-validated at use: it must be a
// well-formed named slug and differ from the page itself. Anything else
// degrades to "" (no forward flow). It deliberately does NOT require the slug
// to exist yet — waiting for it to appear is the whole point.
func forwardSlug(slug, next string) string {
	next = strings.TrimSpace(next)
	if next == "" || next == slug || store.ValidateNamedSlug(next) != nil {
		return ""
	}
	return next
}

// waitURL is the forward-flow wait endpoint for a validated slug/next pair.
// Both components are slug-charset by construction (URL-safe as-is).
func waitURL(slug, next string) string {
	return "/answer/" + slug + "/next?to=" + next
}

// answerPage builds the standalone result page. It is fully server-composed and
// reflects only the (escaped) slug, message, and — on success — the stored
// answer text; it runs no script. A non-empty next (validated by the caller;
// success path only) adds the forward flow: an honest "being prepared" note, a
// link to the wait endpoint, and a JS-free meta-refresh that carries the
// student there — where they are redirected to the next lesson once it exists.
func answerPage(slug string, ok bool, message, recorded, next string) string {
	esc := html.EscapeString(slug)
	cls, badge, title := "err", "Not recorded", "Answer not recorded"
	if ok {
		cls, badge, title = "ok", "✓ Recorded", "Answer recorded"
	}
	var echo string
	if ok && recorded != "" {
		echo = `<figure class="ans"><figcaption>Your answer</figcaption><blockquote>` +
			html.EscapeString(recorded) + `</blockquote></figure>`
	}
	var refresh, forward string
	if ok && next != "" {
		wait := html.EscapeString(waitURL(slug, next))
		refresh = `<meta http-equiv="refresh" content="4;url=` + wait + `">` + "\n"
		forward = `<p class="msg">Your next lesson is being prepared — you will be taken there as soon as it is ready.</p>
<p><a href="` + wait + `">Continue to your next lesson →</a></p>
`
	}
	return `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
` + refresh + `<title>` + html.EscapeString(title) + ` · ` + esc + `</title>
<style>
body{max-width:34rem;margin:3rem auto;padding:0 1rem;font:16px/1.6 -apple-system,Segoe UI,Roboto,sans-serif;color:#1a1a1a}
h1{font-size:1.3rem;margin:.2rem 0 .1rem}.sub{color:#666;margin-top:0}
.badge{display:inline-block;font-weight:700;font-size:.8rem;letter-spacing:.02em;padding:.25rem .6rem;border-radius:999px}
.badge.ok{background:#e7f5ec;color:#1e7a3d;border:1px solid #9ad3b0}
.badge.err{background:#fdecec;color:#a12020;border:1px solid #efb4b4}
.msg{margin:1rem 0}
.ans{margin:1.2rem 0;padding:0}.ans figcaption{color:#666;font-size:.8rem;margin-bottom:.3rem}
.ans blockquote{margin:0;padding:.7rem .9rem;border-left:3px solid #9ad3b0;background:#f4faf6;
  border-radius:0 6px 6px 0;white-space:pre-wrap;word-break:break-word}
a{color:#0a58ca}
</style></head><body>
<span class="badge ` + cls + `">` + badge + `</span>
<h1>` + html.EscapeString(title) + `</h1>
<p class="sub">On <code>` + esc + `</code></p>
<p class="msg">` + html.EscapeString(message) + `</p>
` + echo + `
` + forward + `<p><a href="/` + esc + `">← back to the page</a></p>
</body></html>`
}

// waitPage is the forward-flow holding page: rendered by handleNextWait while
// the follow-up slug does not yet resolve. JS-free; a meta-refresh re-checks
// every few seconds (the handler redirects the moment the lesson exists). It
// makes no claim about the answer or about readiness — only that the check is
// running — and always offers the way back.
func waitPage(slug, next string) string {
	esc := html.EscapeString(slug)
	return `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="5">
<title>Preparing your next lesson · ` + esc + `</title>
<style>
body{max-width:34rem;margin:3rem auto;padding:0 1rem;font:16px/1.6 -apple-system,Segoe UI,Roboto,sans-serif;color:#1a1a1a}
h1{font-size:1.3rem;margin:.2rem 0 .1rem}.sub{color:#666;margin-top:0}
.badge{display:inline-block;font-weight:700;font-size:.8rem;letter-spacing:.02em;padding:.25rem .6rem;border-radius:999px;
  background:#e8f0fe;color:#1a56b0;border:1px solid #a9c6f5}
.msg{margin:1rem 0}
a{color:#0a58ca}
</style></head><body>
<span class="badge">Preparing</span>
<h1>Your next lesson is being prepared</h1>
<p class="sub">Next up: <code>` + html.EscapeString(next) + `</code></p>
<p class="msg">This page checks every few seconds and will take you there as soon as it is ready.
You can leave it open, or go back and review while you wait.</p>
<p><a href="/` + esc + `">← back to the page</a></p>
</body></html>`
}

// formPage renders the self-contained reply form for slug, carrying the
// submission token. When confirmKind is non-empty it shows a confirmation
// banner for that just-submitted kind. The slug is HTML-escaped defensively
// (it is already URL-safe by construction).
func formPage(slug, confirmKind, token string) string {
	esc := html.EscapeString(slug)
	var banner string
	if confirmKind != "" {
		banner = `<div class="ok">Thanks — your <strong>` + html.EscapeString(confirmKind) +
			`</strong> reply was recorded.</div>`
	}
	return `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Reply · ` + esc + `</title>
<style>
body{max-width:34rem;margin:3rem auto;padding:0 1rem;font:16px/1.6 -apple-system,Segoe UI,Roboto,sans-serif;color:#1a1a1a}
h1{font-size:1.3rem}.sub{color:#666;margin-top:-.4rem}
textarea{width:100%;min-height:6rem;font:inherit;padding:.6rem;border:1px solid #ccc;border-radius:6px;box-sizing:border-box}
.row{display:flex;gap:.5rem;margin-top:.8rem;flex-wrap:wrap}
button{font:inherit;padding:.5rem 1rem;border:1px solid #0a58ca;border-radius:6px;background:#0a58ca;color:#fff;cursor:pointer}
button.alt{background:#fff;color:#0a58ca}
.ok{background:#e7f5ec;border:1px solid #9ad3b0;padding:.6rem .8rem;border-radius:6px;margin-bottom:1rem}
a{color:#0a58ca}
</style></head><body>
` + banner + `
<h1>Reply</h1>
<p class="sub">Responding to <code>` + esc + `</code></p>
<form method="post" action="/reply/` + esc + `">
  <input type="hidden" name="token" value="` + html.EscapeString(token) + `">
  <textarea name="body" placeholder="Optional note…"></textarea>
  <div class="row">
    <button type="submit" name="kind" value="approve">Approve</button>
    <button type="submit" name="kind" value="defer" class="alt">Defer</button>
    <button type="submit" name="kind" value="comment" class="alt">Comment</button>
  </div>
</form>
<p class="sub"><a href="/` + esc + `">← back to the page</a></p>
</body></html>`
}
