// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"time"

	"github.com/daisandapex/demiplane/internal/formtoken"
	"github.com/daisandapex/demiplane/internal/store"
)

// Reply-form submission tokens (internal/formtoken). A ?reply=question page's
// baked reply box carries a token issued at the page's render time, which the
// render spec records as Published and a rebake replays, so a rebake bakes the
// same token. The reply module's own form is issued a fresh token when it is
// served. A token is accepted for the artifact's current publish only: one
// issued before that publish's render time is refused.

// mintReplyToken returns the token for slug issued at the given time.
func (s *Server) mintReplyToken(slug string, issued time.Time) string {
	return formtoken.Mint(s.store.FormKey(), slug, issued)
}

// ReplyFormToken is the module.Host token minter, issuing a token now.
func (s *Server) ReplyFormToken(slug string) string {
	return s.mintReplyToken(slug, time.Now())
}

// CheckReplyFormToken is the module.Host token check.
func (s *Server) CheckReplyFormToken(art store.Artifact, tok string) bool {
	return formtoken.Check(s.store.FormKey(), art.Slug, tok, replyTokenNotBefore(art))
}

// replyTokenNotBefore is the earliest issue time accepted for art: its current
// publish's render time when a render spec records one, else its publish time.
func replyTokenNotBefore(art store.Artifact) time.Time {
	if art.RenderSpec != "" {
		var spec renderSpec
		if json.Unmarshal([]byte(art.RenderSpec), &spec) == nil && !spec.Published.IsZero() {
			return spec.Published
		}
	}
	return art.CreatedAt
}

// replyTokenFor is the token a page baked at published carries for its reply
// box posting to replySlug ("" when the page has no reply box).
func (s *Server) replyTokenFor(replySlug string, published time.Time) string {
	if replySlug == "" {
		return ""
	}
	return s.mintReplyToken(replySlug, published)
}
