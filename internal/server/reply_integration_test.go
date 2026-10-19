// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build reply

package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/limit"
	"github.com/daisandapex/demiplane/v3/internal/store"

	// Compile the reply module into this test binary so Handler() mounts its
	// routes alongside the real core routes. This is the regression guard for the
	// mux-conflict class of bug: a wildcard-first module pattern (/{slug}/reply)
	// is ambiguous with core's literal-first routes (GET /docs/{page}) and panics
	// at registration. Run with: go test -tags reply ./internal/server/
	"github.com/daisandapex/demiplane/v3/internal/modules/reply"
)

// The registered module is one instance for the whole test binary, so its
// per-client submission buckets would carry over between tests that all
// submit from 127.0.0.1. Lift the rate limits here; the reply package tests
// cover them.
func init() {
	l := reply.DefaultLimits()
	l.RateClient, l.RateTarget, l.RateGlobal = limit.Rate{}, limit.Rate{}, limit.Rate{}
	if err := reply.ConfigureLimits(l); err != nil {
		panic(err)
	}
}

// TestHandlerMountsReplyModule verifies the full handler (core + reply module)
// builds without a ServeMux pattern conflict, and that the asymmetric auth wiring
// holds end to end against the real Server.Handler.
func TestHandlerMountsReplyModule(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Put(store.PutOptions{Slug: "doc", Replies: true}, strings.NewReader("<html>x</html>")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Handler() panics on a mux conflict — building it at all is half the test.
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)

	// Core /docs route still resolves (the route that conflicted with the naive
	// /{slug}/reply shape).
	if resp, err := http.Get(ts.URL + "/docs"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /docs: err=%v status=%v", err, resp)
	}

	// Submit is mesh-only (open); list is bearer-gated (open here, no token set).
	resp, err := http.Post(ts.URL+"/reply/doc", "application/json", strings.NewReader(`{"kind":"approve"}`))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /reply/doc: err=%v status=%v", err, resp)
	}
	if resp, err := http.Get(ts.URL + "/replies"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /replies: err=%v status=%v", err, resp)
	}

	// A slug named "replies" is reserved (the module route owns it).
	resp, err = http.Post(ts.URL+"/publish?slug=replies", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("publish replies: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("publish slug=replies = %d, want 400 (reserved)", resp.StatusCode)
	}
}

// TestContentPlaneSameOriginReplySubmit verifies the reply module's content-
// origin route (POST /answer/{slug}) mounts alongside the GET /{slug} artifact
// catch-all without a mux conflict, and that a plain same-origin form post
// records an answer and lands on a server-rendered confirmation — the honest
// reply path that replaces the cross-origin, JS-timer form from the incident.
func TestContentPlaneSameOriginReplySubmit(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Put(store.PutOptions{Slug: "lesson", Replies: true}, strings.NewReader("<html>x</html>")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// ContentHandler() panics on a mux conflict — building it is half the test.
	ts := httptest.NewServer(New(st, Config{}).ContentHandler())
	t.Cleanup(ts.Close)

	// GET /{slug} still serves the artifact body.
	if resp, err := http.Get(ts.URL + "/lesson"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /lesson: err=%v status=%v", err, resp)
	}

	// Same-origin answer submit: the baked box posts here (/answer/<slug>).
	resp, err := http.Post(ts.URL+"/answer/lesson", "application/x-www-form-urlencoded",
		strings.NewReader("body=my+worked+answer"))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /answer/lesson: err=%v status=%v", err, resp)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	if body := string(buf[:n]); !strings.Contains(body, "✓ Recorded") {
		t.Errorf("expected server-rendered confirmation, got:\n%s", body)
	}
}

// TestForwardFlowEndToEnd drives the whole classroom loop against the real
// combined handler: publish lesson A with ?reply&next=B, answer it BEFORE B
// exists (honest waiting state), then publish B and watch the wait endpoint
// carry the student onto it — the auto-advance demiplane-tb7 ships.
func TestForwardFlowEndToEnd(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	read := func(resp *http.Response) string {
		t.Helper()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		resp.Body.Close()
		return string(b)
	}

	// Publish lesson A pointing at the (not yet existing) lesson B.
	resp, err := http.Post(ts.URL+"/publish?render=md&slug=fw-a&reply=question&next=fw-b",
		"text/markdown", strings.NewReader("# Lesson A\n\nAnswer me."))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish A: err=%v status=%v", err, resp)
	}
	resp.Body.Close()
	resp, err = http.Get(ts.URL + "/fw-a")
	if err != nil {
		t.Fatal(err)
	}
	if page := read(resp); !strings.Contains(page, `name="next" value="fw-b"`) {
		t.Fatalf("lesson A missing the baked next pointer:\n%s", page)
	}

	// Answer A while B does not exist: honest confirmation + forward chrome.
	resp, err = http.Post(ts.URL+"/answer/fw-a", "application/x-www-form-urlencoded",
		strings.NewReader("body=because&next=fw-b"))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("answer A: err=%v status=%v", err, resp)
	}
	conf := read(resp)
	if !strings.Contains(conf, "✓ Recorded") || !strings.Contains(conf, "/answer/fw-a/next?to=fw-b") {
		t.Fatalf("confirmation missing recorded badge or forward link:\n%s", conf)
	}

	// The wait endpoint holds honestly while B is unpublished.
	resp, err = client.Get(ts.URL + "/answer/fw-a/next?to=fw-b")
	if err != nil {
		t.Fatal(err)
	}
	if wait := read(resp); resp.StatusCode != http.StatusOK || !strings.Contains(wait, "being prepared") {
		t.Fatalf("wait before publish = %d, want honest holding page:\n%s", resp.StatusCode, wait)
	}

	// Publish B → the same wait URL now moves the student forward.
	resp, err = http.Post(ts.URL+"/publish?render=md&slug=fw-b",
		"text/markdown", strings.NewReader("# Lesson B"))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish B: err=%v status=%v", err, resp)
	}
	resp.Body.Close()
	resp, err = client.Get(ts.URL + "/answer/fw-a/next?to=fw-b")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/fw-b" {
		t.Fatalf("wait after publish = %d loc=%q, want 302 /fw-b",
			resp.StatusCode, resp.Header.Get("Location"))
	}
}

// TestContentPlaneRejectsCrossOriginAnswers: the content listener's write
// guard refuses browser posts from another origin (including the control
// origin, which is same-site) and records nothing, even with a valid
// submission token; a same-origin post and a non-browser client still record.
func TestContentPlaneRejectsCrossOriginAnswers(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	art, err := st.Put(store.PutOptions{Slug: "lesson", Replies: true}, strings.NewReader("<html>x</html>"))
	if err != nil {
		t.Fatal(err)
	}
	srv := New(st, Config{})
	ts := httptest.NewServer(srv.ContentHandler())
	t.Cleanup(ts.Close)
	token := srv.ReplyFormToken(art)
	post := func(hdr map[string]string) int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/answer/lesson",
			strings.NewReader(url.Values{"body": {"answer"}, "token": {token}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for name, hdr := range map[string]map[string]string{
		"cross-site":     {"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"},
		"same-site":      {"Sec-Fetch-Site": "same-site"},
		"foreign origin": {"Origin": "https://evil.example"},
	} {
		if code := post(hdr); code != http.StatusForbidden {
			t.Errorf("%s answer = %d, want 403", name, code)
		}
	}
	if code := post(map[string]string{"Sec-Fetch-Site": "same-origin"}); code != http.StatusCreated {
		t.Errorf("same-origin answer = %d, want 201", code)
	}
	// A sandboxed page (opaque origin) on this content origin still posts.
	self := strings.TrimPrefix(ts.URL, "http://")
	for name, hdr := range map[string]map[string]string{
		"sandboxed, own referer": {"Sec-Fetch-Site": "cross-site", "Origin": "null", "Referer": "http://" + self + "/lesson"},
		"sandboxed, no referer":  {"Sec-Fetch-Site": "cross-site", "Origin": "null"},
	} {
		if code := post(hdr); code != http.StatusCreated {
			t.Errorf("%s answer = %d, want 201", name, code)
		}
	}
	if code := post(map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "null",
		"Referer": "https://evil.example/page"}); code != http.StatusForbidden {
		t.Errorf("opaque origin with a foreign referer = %d, want 403", code)
	}
	if code := post(nil); code != http.StatusCreated {
		t.Errorf("non-browser answer = %d, want 201", code)
	}
	resp, err := http.Get(ts.URL + "/lesson")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET still serves: %v %v", err, resp)
	}
	resp.Body.Close()
}

// TestReplyEnablementFollowsThePublish: replies are accepted only after a
// publish that asked for them, and the next publish without ?reply turns
// them off again.
func TestReplyEnablementFollowsThePublish(t *testing.T) {
	ts := newTestServer(t, "")
	reply := func(slug string) int {
		resp, err := http.Post(ts.URL+"/reply/"+slug, "application/json",
			strings.NewReader(`{"kind":"approve","body":"ok"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	publish(t, ts, "?slug=plain", "<html>a</html>")
	if code := reply("plain"); code != http.StatusNotFound {
		t.Fatalf("reply to a page published without ?reply = %d, want 404", code)
	}
	publish(t, ts, "?slug=plain&reply=form", "<html>a</html>")
	if code := reply("plain"); code != http.StatusCreated {
		t.Fatalf("reply after ?reply=form = %d, want 201", code)
	}
	publish(t, ts, "?slug=plain", "<html>b</html>")
	if code := reply("plain"); code != http.StatusNotFound {
		t.Fatalf("reply after republishing without ?reply = %d, want 404", code)
	}
	publish(t, ts, "?render=md&slug=lesson-x&reply=question", "# Q")
	if code := reply("lesson-x"); code != http.StatusCreated {
		t.Fatalf("reply to a ?reply=question page = %d, want 201", code)
	}
	resp, err := http.Post(ts.URL+"/publish?reply=form&private=true", "text/html", strings.NewReader("<html>p</html>"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("?reply=form with ?private = %d, want 400", resp.StatusCode)
	}
}

// TestAnswerNeedsThePagePassword runs the real view-password gate: an answer
// to a password-protected lesson needs the password, as viewing it does.
func TestAnswerNeedsThePagePassword(t *testing.T) {
	ts := newTestServer(t, "")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/publish?render=md&slug=secret-lesson&reply=question",
		strings.NewReader("# Q"))
	req.Header.Set(PasswordHeader, "letmein")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish = %d", resp.StatusCode)
	}
	answer := func(pass string) int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/answer/secret-lesson",
			strings.NewReader("body=mine"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if pass != "" {
			req.SetBasicAuth("", pass)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := answer(""); code != http.StatusUnauthorized {
		t.Fatalf("answer without the password = %d, want 401", code)
	}
	if code := answer("nope"); code != http.StatusUnauthorized {
		t.Fatalf("answer with a wrong password = %d, want 401", code)
	}
	if code := answer("letmein"); code != http.StatusCreated {
		t.Fatalf("answer with the password = %d, want 201", code)
	}
}

// replyTokenOf extracts the reply box's submission token from a page.
func replyTokenOf(t *testing.T, page string) string {
	t.Helper()
	const marker = `name="token" value="`
	i := strings.Index(page, marker)
	if i < 0 {
		t.Fatalf("page has no reply token: %s", page)
	}
	rest := page[i+len(marker):]
	return rest[:strings.IndexByte(rest, '"')]
}

// TestBakedReplyTokenLifecycle: the reply box baked into a ?reply=question page
// carries a token its answer route accepts from a sandboxed (opaque-origin)
// post; a rebake bakes the same token; a republish (here adding a password)
// invalidates the previous publish's token; and a post from another origin is
// refused by the content guard whatever token it carries.
func TestBakedReplyTokenLifecycle(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)

	publish := func(password string) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/publish?slug=quiz&render=md&reply=question",
			strings.NewReader("# Q\n\nWhy?\n"))
		if password != "" {
			req.Header.Set(PasswordHeader, password)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("publish: %v %v", err, resp)
		}
		resp.Body.Close()
	}
	page := func(password string) string {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/quiz", nil)
		if password != "" {
			req.SetBasicAuth("u", password)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	answer := func(token, password string, hdr map[string]string) int {
		v := url.Values{"body": {"an answer"}, "token": {token}}
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/answer/quiz", strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, val := range hdr {
			req.Header.Set(k, val)
		}
		if password != "" {
			req.SetBasicAuth("u", password)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	sandboxed := map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site"}

	publish("")
	first := replyTokenOf(t, page(""))
	if code := answer(first, "", sandboxed); code != http.StatusCreated {
		t.Fatalf("sandboxed answer with the page's token: %d", code)
	}
	if code := answer(first, "", map[string]string{"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site"}); code != http.StatusForbidden {
		t.Errorf("answer from another origin with a valid token: %d, want 403", code)
	}

	resp, err := http.Post(ts.URL+"/rerender/quiz", "", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("rerender: %v %v", err, resp)
	}
	resp.Body.Close()
	if again := replyTokenOf(t, page("")); again != first {
		t.Errorf("a rebake changed the token: %q -> %q", first, again)
	}
	if code := answer(first, "", sandboxed); code != http.StatusCreated {
		t.Errorf("the token after a rebake: %d, want 201", code)
	}

	publish("pw-2")
	second := replyTokenOf(t, page("pw-2"))
	if second == first {
		t.Fatal("a republish kept the previous token")
	}
	if code := answer(first, "pw-2", sandboxed); code != http.StatusForbidden {
		t.Errorf("the previous publish's token after a republish: %d, want 403", code)
	}
	if code := answer(second, "pw-2", sandboxed); code != http.StatusCreated {
		t.Errorf("the current token: %d, want 201", code)
	}
}

// formTokenOf fetches the reply module's form for slug and returns its token.
func formTokenOf(t *testing.T, base, slug string) string {
	t.Helper()
	resp, err := http.Get(base + "/reply/" + slug)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /reply/%s: %d %s", slug, resp.StatusCode, b)
	}
	return replyTokenOf(t, string(b))
}

// postAnswerAs posts an answer to /answer/{slug} from a sandboxed page,
// with the viewer's password when set, and returns the status.
func postAnswerAs(t *testing.T, base, slug, token, password string) int {
	t.Helper()
	v := url.Values{"body": {"an answer"}, "token": {token}}
	req, _ := http.NewRequest(http.MethodPost, base+"/answer/"+slug, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "null")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if password != "" {
		req.SetBasicAuth("u", password)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestTokenMintedDuringRepublishRenderIsRefused: while a protected republish
// renders, the previous public page is still served, and its reply form can
// be fetched. A token taken from it then must not work once the protected
// publish commits.
func TestTokenMintedDuringRepublishRenderIsRefused(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)
	publish := func(password string) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/publish?slug=quiz&render=md&reply=question",
			strings.NewReader("# Q\n\nWhy?\n"))
		if password != "" {
			req.Header.Set(PasswordHeader, password)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("publish: %v %v", err, resp)
		}
		resp.Body.Close()
	}
	publish("")

	var (
		once   sync.Once
		minted string
	)
	renderHeld = func() {
		once.Do(func() { minted = formTokenOf(t, ts.URL, "quiz") })
	}
	t.Cleanup(func() { renderHeld = nil })
	publish("pw-locked")
	renderHeld = nil
	if minted == "" {
		t.Fatal("the render hook did not run")
	}
	if code := postAnswerAs(t, ts.URL, "quiz", minted, "pw-locked"); code != http.StatusForbidden {
		t.Errorf("a token minted from the public page during the protected republish's render: %d, want 403", code)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/quiz", nil)
	req.SetBasicAuth("u", "pw-locked")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if code := postAnswerAs(t, ts.URL, "quiz", replyTokenOf(t, string(page)), "pw-locked"); code != http.StatusCreated {
		t.Errorf("the protected page's baked token: %d, want 201", code)
	}
}

// TestSameSecondRepublishInvalidatesToken: a republish without rendering, in
// the same second a token was issued, still invalidates it.
func TestSameSecondRepublishInvalidatesToken(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)
	publish := func(password string) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/publish?slug=plain&reply=form",
			strings.NewReader("<html>p</html>"))
		if password != "" {
			req.Header.Set(PasswordHeader, password)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("publish: %v %v", err, resp)
		}
		resp.Body.Close()
	}
	// Start just after a second boundary so the whole sequence shares a second.
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second + 20*time.Millisecond)))
	start := time.Now()
	publish("")
	tok := formTokenOf(t, ts.URL, "plain")
	publish("pw-same")
	if time.Now().Truncate(time.Second) != start.Truncate(time.Second) {
		t.Skip("the sequence crossed a second boundary; the same-second case was not exercised")
	}
	if code := postAnswerAs(t, ts.URL, "plain", tok, "pw-same"); code != http.StatusForbidden {
		t.Errorf("a token from the previous publish in the same second: %d, want 403", code)
	}
}
