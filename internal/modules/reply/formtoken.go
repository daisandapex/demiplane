// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"net/http"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// Submission tokens. Every reply form demiplane serves carries a token bound
// to its artifact (module.Host.ReplyFormToken; the baked inline box gets one
// at render). A browser submission must present a valid token for exactly the
// artifact it posts to. That is what ties a submission to the target's own
// page: the cross-origin write guards cannot, because every sandboxed page on
// the content origin posts with "Origin: null", and the browser may attach the
// viewer's cached password for the target to a post another page sends.
//
// A browser submission is one carrying Origin or Sec-Fetch-Site, which
// browsers send on every form post and cross-site fetch. Requests with
// neither (curl, agents, SSH tooling) are not browser-driven: they carry only
// the credentials their caller chose, so they need no token.

// formTokenState is the outcome of a token check.
type formTokenState int

const (
	formTokenOK formTokenState = iota
	// formTokenMissing: a browser submission without a token, typically a
	// page baked before forms carried one.
	formTokenMissing
	// formTokenInvalid: a token that is malformed, for another artifact, or
	// from before the artifact's current publish.
	formTokenInvalid
)

// browserSubmission reports whether r was sent by a browser.
func browserSubmission(r *http.Request) bool {
	return r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != ""
}

// checkFormToken checks a submission's token against art.
func (m *Module) checkFormToken(r *http.Request, art store.Artifact, token string) formTokenState {
	if !browserSubmission(r) {
		return formTokenOK
	}
	if token == "" {
		return formTokenMissing
	}
	if !m.host.CheckReplyFormToken(art, token) {
		return formTokenInvalid
	}
	return formTokenOK
}

// message is the plain-text refusal for the control-plane submit route.
func (s formTokenState) message() string {
	if s == formTokenMissing {
		return "this reply form is missing its submission token; reload the form and try again"
	}
	return "this reply form is out of date or was not issued for this page; reload it and try again"
}

// answerText is the reader-facing "not recorded" text for the inline box.
func (s formTokenState) answerText() string {
	if s == formTokenMissing {
		return "This page was published before its answer form was updated, so your answer was not recorded. Its publisher needs to rebuild the page."
	}
	return "This answer form is out of date or did not come from this page, so your answer was not recorded. Reload the page and try again."
}
