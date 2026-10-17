// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"github.com/daisandapex/demiplane/internal/formtoken"
	"github.com/daisandapex/demiplane/internal/store"
)

// Reply-form submission tokens (internal/formtoken). A token is bound to its
// artifact's publish identifier (store.Artifact.PublishID), which every
// publish replaces and a rebake keeps. A ?reply=question page's baked reply
// box carries the token for the publish being written: the publish handler
// picks the identifier before rendering and hands the same one to the store.
// The reply module's own form carries the token for the publish it was
// served from. Either way a republish, including one that only adds a
// password, invalidates every earlier token.

// ReplyFormToken is the module.Host token minter.
func (s *Server) ReplyFormToken(art store.Artifact) string {
	return formtoken.Mint(s.store.FormKey(), art.Slug, art.PublishID)
}

// CheckReplyFormToken is the module.Host token check.
func (s *Server) CheckReplyFormToken(art store.Artifact, tok string) bool {
	return formtoken.Check(s.store.FormKey(), art.Slug, art.PublishID, tok)
}

// replyTokenFor is the token a page of publish publishID carries for its
// reply box posting to replySlug ("" when the page has no reply box).
func (s *Server) replyTokenFor(replySlug, publishID string) string {
	if replySlug == "" {
		return ""
	}
	return formtoken.Mint(s.store.FormKey(), replySlug, publishID)
}
