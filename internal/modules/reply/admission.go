// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/daisandapex/demiplane/internal/limit"
	"github.com/daisandapex/demiplane/internal/module"
	"github.com/daisandapex/demiplane/internal/store"
)

// Admission for the open submit endpoints. A reply is accepted only when all
// of these hold, checked in this order:
//
//  1. The target is a visible (public, unexpired) artifact whose publish
//     enabled replies (?reply=form or ?reply=question, stored with it).
//  2. The request carries the artifact's view password, if it has one
//     (core's rate-limited check, Host.CheckView).
//  3. The client, the target and the instance each have a token left in
//     their submission buckets.
//  4. The body arrives within replyBodyTimeout.
//  5. The hook pool, when a hook is configured, has room (hook.go).
//  6. The per-artifact and aggregate storage limits have room, checked and
//     inserted atomically (storage.go).
//
// Origin checks are core's: the content listener rejects cross-origin
// browser writes. They cannot tell apart two pages served from the same
// content origin; the view password and the per-artifact opt-in are what
// keep a hostile page from writing to an artifact it should not.

// replyBodyTimeout bounds how long a submit request may take to deliver its
// (at most maxRequestBody) body. A browser form post needs well under a second.
const replyBodyTimeout = 10 * time.Second

// Limits are the reply_* admission and storage limits.
type Limits struct {
	PerArtifact int           // reply_max_per_artifact: replies stored against one artifact
	Total       int           // reply_max_total: replies stored in all
	TotalBytes  int64         // reply_max_bytes: reply text stored in all
	Retention   time.Duration // reply_retention: replies older than this are deleted; 0 keeps them
	RateClient  limit.Rate    // reply_rate_client: submissions per client
	RateTarget  limit.Rate    // reply_rate_target: submissions per artifact
	RateGlobal  limit.Rate    // reply_rate_global: submissions in all
}

// DefaultLimits returns the defaults for unset reply_* limit keys.
func DefaultLimits() Limits {
	return Limits{
		PerArtifact: 1000,
		Total:       100_000,
		TotalBytes:  64 << 20,
		Retention:   90 * 24 * time.Hour,
		RateClient:  limit.PerMinute(10, 10),
		RateTarget:  limit.PerMinute(60, 60),
		RateGlobal:  limit.PerMinute(300, 300),
	}
}

// ConfigureLimits sets the admission and storage limits on the registered
// module. Called at startup by the build-tagged config wiring.
func ConfigureLimits(l Limits) error {
	if l.PerArtifact < 1 || l.Total < 1 || l.TotalBytes < maxReplyBody {
		return fmt.Errorf("reply limits: per-artifact and total counts must be positive and total bytes at least %d", maxReplyBody)
	}
	if l.Retention < 0 {
		return errors.New("reply_retention: must not be negative")
	}
	std.setLimits(l)
	return nil
}

// admission holds the module's limits and their buckets.
type admission struct {
	lim                    Limits
	client, target, global *limit.Keyed
}

func newAdmission(l Limits) *admission {
	return &admission{
		lim:    l,
		client: limit.NewKeyed(l.RateClient, 0),
		target: limit.NewKeyed(l.RateTarget, 0),
		global: limit.NewKeyed(l.RateGlobal, 1),
	}
}

// admissionMu guards Module.adm across ConfigureLimits and first use.
var admissionMu sync.Mutex

func (m *Module) setLimits(l Limits) {
	admissionMu.Lock()
	m.adm = newAdmission(l)
	admissionMu.Unlock()
}

func (m *Module) admission() *admission {
	admissionMu.Lock()
	defer admissionMu.Unlock()
	if m.adm == nil {
		m.adm = newAdmission(DefaultLimits())
	}
	return m.adm
}

// refusal is why a submission was not admitted; status and message go to the
// replier, header (if any) is set first.
type refusal struct {
	status        int
	msg           string
	header, value string
	capReached    bool // the artifact's reply limit, not a rate limit
}

func (r *refusal) write(w http.ResponseWriter) {
	if r.header != "" {
		w.Header().Set(r.header, r.value)
	}
}

var errNoReplies = errors.New("replies are not enabled for this artifact")

// target resolves slug to an artifact that accepts replies and checks the
// view password. It returns errNoReplies (answer 404) when the artifact is
// missing, private, expired or not reply-enabled, so a disabled page and a
// missing one look the same.
func (m *Module) target(r *http.Request, slug string) (store.Artifact, *refusal, error) {
	art, err := m.host.Store().LookupPublic(slug)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !art.Replies) {
		return store.Artifact{}, nil, errNoReplies
	}
	if err != nil {
		return store.Artifact{}, nil, err
	}
	switch res, wait := m.host.CheckView(r, art); res {
	case module.ViewOK:
		return art, nil, nil
	case module.ViewThrottled:
		return art, &refusal{status: http.StatusTooManyRequests, msg: "too many password attempts; try again later",
			header: "Retry-After", value: limit.RetryAfter(wait)}, nil
	case module.ViewBusy:
		return art, &refusal{status: http.StatusServiceUnavailable, msg: "the server is busy; try again shortly",
			header: "Retry-After", value: limit.RetryAfter(wait)}, nil
	default:
		return art, &refusal{status: http.StatusUnauthorized, msg: "this page needs its password",
			header: "WWW-Authenticate", value: module.PasswordChallenge}, nil
	}
}

// rate takes a submission token for the client, the target and the instance.
func (m *Module) rate(r *http.Request, slug string) *refusal {
	a := m.admission()
	ok, wait := limit.AllowAll(
		limit.Charge{L: a.global, Key: ""},
		limit.Charge{L: a.client, Key: limit.ClientKey(r)},
		limit.Charge{L: a.target, Key: slug},
	)
	if ok {
		return nil
	}
	return &refusal{status: http.StatusTooManyRequests, msg: "too many replies right now; try again later",
		header: "Retry-After", value: limit.RetryAfter(wait)}
}

// bodyDeadline bounds the time left to read the request body. A transport
// without deadlines (a test recorder) is left alone.
func bodyDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(replyBodyTimeout))
}

// record stores a validated reply under the hook and storage limits, firing
// the hook on success. The refusal covers every way it can decline.
func (m *Module) record(slug, kind, body string) (Reply, *refusal, error) {
	ticket, ok := m.reserveHook()
	if !ok {
		return Reply{}, &refusal{status: http.StatusServiceUnavailable, msg: "the server is busy; try again shortly",
			header: "Retry-After", value: "5"}, nil
	}
	rep, err := m.rs.addLimited(slug, kind, body, time.Now(), m.admission().lim)
	switch {
	case errors.Is(err, errReplyCap):
		ticket.release()
		return Reply{}, &refusal{status: http.StatusTooManyRequests, msg: "this artifact has reached its reply limit", capReached: true}, nil
	case errors.Is(err, errReplyStoreFull):
		ticket.release()
		return Reply{}, &refusal{status: http.StatusInsufficientStorage, msg: "reply storage is full"}, nil
	case err != nil:
		ticket.release()
		return Reply{}, nil, err
	}
	ticket.fire(rep)
	return rep, nil, nil
}
