// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestReplyModule_AnswerFlow exercises the reply module (built into the test
// binary via -tags reply) against a real server: publish a page with an
// inline reply box (?render=md&reply=question&slug=...), submit an answer to
// POST /answer/{slug} on the CONTENT origin exactly like the baked form does,
// and verify both the server-rendered confirmation AND durable storage
// (GET /replies, bearer-gated) agree.
func TestReplyModule_AnswerFlow(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "reply-token"})

	md := "# Question\n\nWhat is 2+2?\n"
	pub := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug=lesson-1&render=md&reply=question",
		strings.NewReader(md), srv.authHeader())
	if pub.Status != http.StatusCreated {
		t.Fatalf("publish with reply box: status=%d body=%s", pub.Status, pub.Body)
	}

	// The published page itself carries a same-origin form; confirm the reply
	// box is actually baked in before trusting the submit below.
	page := srv.do(t, http.MethodGet, srv.ContentURL+"/lesson-1", nil, nil)
	if page.Status != http.StatusOK || !strings.Contains(string(page.Body), "/answer/lesson-1") {
		t.Fatalf("published page missing reply form action /answer/lesson-1:\n%s", page.Body)
	}

	form := url.Values{"body": {"four"}}.Encode()
	submit := srv.do(t, http.MethodPost, srv.ContentURL+"/answer/lesson-1",
		strings.NewReader(form), map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		})
	if submit.Status != http.StatusCreated {
		t.Fatalf("submit answer: status=%d body=%s", submit.Status, submit.Body)
	}
	if !strings.Contains(string(submit.Body), "Your answer was recorded.") {
		t.Fatalf("confirmation page missing success message:\n%s", submit.Body)
	}
	if !strings.Contains(string(submit.Body), "four") {
		t.Fatalf("confirmation page does not echo the submitted answer:\n%s", submit.Body)
	}

	// Durable storage: the agent-facing list, bearer-gated on the control plane.
	list := srv.do(t, http.MethodGet, srv.ControlURL+"/replies", nil, srv.authHeader())
	if list.Status != http.StatusOK {
		t.Fatalf("GET /replies: status=%d body=%s", list.Status, list.Body)
	}
	if !strings.Contains(string(list.Body), "lesson-1") || !strings.Contains(string(list.Body), "four") {
		t.Fatalf("recorded reply missing from /replies: %s", list.Body)
	}

	// An empty answer is honestly rejected — never a false "recorded".
	emptyForm := url.Values{"body": {"   "}}.Encode()
	emptySubmit := srv.do(t, http.MethodPost, srv.ContentURL+"/answer/lesson-1",
		strings.NewReader(emptyForm), map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		})
	if emptySubmit.Status != http.StatusBadRequest {
		t.Fatalf("empty answer: status=%d (want 400)", emptySubmit.Status)
	}
	// NOTE: don't assert on the ABSENCE of "was recorded" — the honest failure
	// page itself says "...so nothing was recorded", which contains that
	// substring. Assert the POSITIVE success string is absent instead; that's
	// the one the earlier successful submit checked FOR.
	if strings.Contains(string(emptySubmit.Body), "Your answer was recorded.") {
		t.Fatalf("empty answer falsely claims success:\n%s", emptySubmit.Body)
	}

	// Answering an unknown slug is a clean 404, not a silent 200.
	unknown := srv.do(t, http.MethodPost, srv.ContentURL+"/answer/does-not-exist",
		strings.NewReader(form), map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		})
	if unknown.Status != http.StatusNotFound {
		t.Fatalf("answer on unknown slug: status=%d (want 404)", unknown.Status)
	}
}
