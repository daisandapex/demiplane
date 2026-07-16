// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/daisandapex/demiplane/internal/module"
	"github.com/daisandapex/demiplane/internal/store"
)

// fakeHost is a minimal module.Host for testing: a real temp store, a temp
// per-module data dir, and a RequireAuth that enforces a fixed token so the
// asymmetric-auth wiring (open submit, gated read) can be asserted.
type fakeHost struct {
	st      *store.Store
	dataDir string
}

const testToken = "secret"

func (h *fakeHost) Store() *store.Store { return h.st }
func (h *fakeHost) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
func (h *fakeHost) RequestBase(*http.Request) string     { return "http://test" }
func (h *fakeHost) BaseURL() string                      { return "" }
func (h *fakeHost) ModuleDataDir(string) (string, error) { return h.dataDir, nil }

// setup mounts the reply module's CONTROL-plane routes on a test server and
// seeds one artifact slug, returning the server and the module.
func setup(t *testing.T) (*httptest.Server, *Module) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Put(store.PutOptions{Slug: "report"}, strings.NewReader("<html>r</html>")); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	h := &fakeHost{st: st, dataDir: t.TempDir()}
	m := &Module{}
	mux := http.NewServeMux()
	m.Routes(mux, h)
	if m.rs == nil {
		t.Fatal("reply store did not open")
	}
	t.Cleanup(func() { m.rs.close() })
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, m
}

// controlSetup is setup under the name that mirrors contentSetup.
func controlSetup(t *testing.T) (*httptest.Server, *Module) { return setup(t) }

func do(t *testing.T, method, url, ctype, body string, auth bool) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestSubmitJSONAndList(t *testing.T) {
	ts, _ := setup(t)

	code, _ := do(t, http.MethodPost, ts.URL+"/reply/report", "application/json",
		`{"kind":"approve","body":"ship it"}`, false)
	if code != http.StatusCreated {
		t.Fatalf("submit = %d, want 201", code)
	}

	// List requires auth.
	if code, _ := do(t, http.MethodGet, ts.URL+"/replies", "", "", false); code != http.StatusUnauthorized {
		t.Fatalf("list without auth = %d, want 401", code)
	}
	code, body := do(t, http.MethodGet, ts.URL+"/replies", "", "", true)
	if code != http.StatusOK {
		t.Fatalf("list = %d, want 200", code)
	}
	var out struct {
		Replies []Reply `json:"replies"`
		Count   int     `json:"count"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	if out.Count != 1 || len(out.Replies) != 1 {
		t.Fatalf("count = %d, want 1", out.Count)
	}
	r := out.Replies[0]
	if r.Slug != "report" || r.Kind != "approve" || r.Body != "ship it" || r.Status != "pending" {
		t.Errorf("unexpected reply: %+v", r)
	}
}

func TestSubmitForm(t *testing.T) {
	ts, _ := setup(t)
	form := url.Values{"kind": {"defer"}, "body": {"later"}}.Encode()
	code, body := do(t, http.MethodPost, ts.URL+"/reply/report",
		"application/x-www-form-urlencoded", form, false)
	if code != http.StatusCreated {
		t.Fatalf("form submit = %d, want 201", code)
	}
	if !strings.Contains(string(body), "was recorded") {
		t.Errorf("missing confirmation banner:\n%s", body)
	}
}

func TestSubmitUnknownSlug(t *testing.T) {
	ts, _ := setup(t)
	code, _ := do(t, http.MethodPost, ts.URL+"/reply/nope", "application/json",
		`{"kind":"approve"}`, false)
	if code != http.StatusNotFound {
		t.Errorf("unknown slug submit = %d, want 404", code)
	}
}

func TestInvalidKind(t *testing.T) {
	ts, _ := setup(t)
	code, _ := do(t, http.MethodPost, ts.URL+"/reply/report", "application/json",
		`{"kind":"nuke"}`, false)
	if code != http.StatusBadRequest {
		t.Errorf("invalid kind = %d, want 400", code)
	}
}

func TestCommentRequiresBody(t *testing.T) {
	ts, _ := setup(t)
	code, _ := do(t, http.MethodPost, ts.URL+"/reply/report", "application/json",
		`{"kind":"comment","body":"   "}`, false)
	if code != http.StatusBadRequest {
		t.Errorf("empty comment = %d, want 400", code)
	}
}

func TestBodyTooLarge(t *testing.T) {
	ts, _ := setup(t)
	big := strings.Repeat("x", maxReplyBody+100)
	payload, _ := json.Marshal(map[string]string{"kind": "comment", "body": big})
	code, _ := do(t, http.MethodPost, ts.URL+"/reply/report", "application/json",
		string(payload), false)
	// Over maxRequestBody → 413 (MaxBytesReader); the explicit body-length check
	// is the backstop for inputs under the request cap.
	if code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize reply = %d, want 413", code)
	}
}

func TestAckLifecycle(t *testing.T) {
	ts, _ := setup(t)
	do(t, http.MethodPost, ts.URL+"/reply/report", "application/json", `{"kind":"approve"}`, false)

	// Grab the id from the pending list.
	_, body := do(t, http.MethodGet, ts.URL+"/replies?status=pending", "", "", true)
	var out struct {
		Replies []Reply `json:"replies"`
	}
	json.Unmarshal(body, &out)
	if len(out.Replies) != 1 {
		t.Fatalf("want 1 pending, got %d", len(out.Replies))
	}
	id := out.Replies[0].ID

	// Ack it.
	if code, _ := do(t, http.MethodPost, ts.URL+"/replies/"+strconv.FormatInt(id, 10)+"/ack", "", "", true); code != http.StatusNoContent {
		t.Fatalf("ack = %d, want 204", code)
	}
	// Now pending is empty, read has it.
	_, body = do(t, http.MethodGet, ts.URL+"/replies?status=pending", "", "", true)
	json.Unmarshal(body, &out)
	if len(out.Replies) != 0 {
		t.Errorf("pending after ack = %d, want 0", len(out.Replies))
	}
	_, body = do(t, http.MethodGet, ts.URL+"/replies?status=read", "", "", true)
	json.Unmarshal(body, &out)
	if len(out.Replies) != 1 || out.Replies[0].Status != "read" {
		t.Errorf("read list wrong: %+v", out.Replies)
	}

	// Ack of unknown id → 404.
	if code, _ := do(t, http.MethodPost, ts.URL+"/replies/99999/ack", "", "", true); code != http.StatusNotFound {
		t.Errorf("ack unknown = %d, want 404", code)
	}
}

func TestForm(t *testing.T) {
	ts, _ := setup(t)
	code, body := do(t, http.MethodGet, ts.URL+"/reply/report", "", "", false)
	if code != http.StatusOK {
		t.Fatalf("form = %d, want 200", code)
	}
	if !strings.Contains(string(body), `action="/reply/report"`) {
		t.Errorf("form missing post action:\n%s", body)
	}
	if code, _ := do(t, http.MethodGet, ts.URL+"/reply/nope", "", "", false); code != http.StatusNotFound {
		t.Errorf("form for unknown slug = %d, want 404", code)
	}
}

// contentSetup mounts the module's CONTENT-origin route (POST /answer/{slug}) and seeds
// one public artifact, returning the server and the module so a test can inspect
// the shared store directly.
func contentSetup(t *testing.T) (*httptest.Server, *Module) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Put(store.PutOptions{Slug: "report"}, strings.NewReader("<html>r</html>")); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	h := &fakeHost{st: st, dataDir: t.TempDir()}
	m := &Module{}
	mux := http.NewServeMux()
	m.ContentRoutes(mux, h)
	if m.rs == nil {
		t.Fatal("reply store did not open")
	}
	t.Cleanup(func() { m.rs.close() })
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, m
}

const formCT = "application/x-www-form-urlencoded"

// TestContentSubmitRecordsAnswer is the happy path: a same-origin form post lands
// a comment-kind reply and the viewer sees a server-rendered confirmation that
// echoes the stored answer — only after the row is persisted.
func TestContentSubmitRecordsAnswer(t *testing.T) {
	ts, m := contentSetup(t)
	form := url.Values{"body": {"because option 1 is targeted"}}.Encode()
	code, body := do(t, http.MethodPost, ts.URL+"/answer/report", formCT, form, false)
	if code != http.StatusCreated {
		t.Fatalf("submit = %d, want 201\n%s", code, body)
	}
	s := string(body)
	if !strings.Contains(s, "✓ Recorded") || strings.Contains(s, "Not recorded") {
		t.Errorf("confirmation not an honest success page:\n%s", s)
	}
	if !strings.Contains(s, "because option 1 is targeted") {
		t.Errorf("confirmation should echo the stored answer:\n%s", s)
	}
	// Persisted as a comment-kind reply.
	rs, err := m.rs.list("report", "all")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rs) != 1 || rs[0].Kind != "comment" || rs[0].Body != "because option 1 is targeted" {
		t.Fatalf("stored reply wrong: %+v", rs)
	}
}

// TestContentSubmitEmptyIsHonest: an empty answer records nothing and the viewer
// is told so — never a false success.
func TestContentSubmitEmptyIsHonest(t *testing.T) {
	ts, m := contentSetup(t)
	form := url.Values{"body": {"   "}}.Encode()
	code, body := do(t, http.MethodPost, ts.URL+"/answer/report", formCT, form, false)
	if code != http.StatusBadRequest {
		t.Fatalf("empty answer = %d, want 400", code)
	}
	s := string(body)
	if strings.Contains(s, "✓ Recorded") {
		t.Errorf("empty answer must NOT show success:\n%s", s)
	}
	if !strings.Contains(s, "Not recorded") {
		t.Errorf("empty answer should say not recorded:\n%s", s)
	}
	if n, _ := m.rs.count("report"); n != 0 {
		t.Errorf("empty answer stored %d replies, want 0", n)
	}
}

// TestContentSubmitUnknownSlug: nothing to answer on a slug with no public page.
func TestContentSubmitUnknownSlug(t *testing.T) {
	ts, _ := contentSetup(t)
	form := url.Values{"body": {"hi"}}.Encode()
	code, _ := do(t, http.MethodPost, ts.URL+"/answer/nope", formCT, form, false)
	if code != http.StatusNotFound {
		t.Errorf("unknown slug = %d, want 404", code)
	}
}

// TestContentSubmitStorageDownIsHonest: with storage unavailable the viewer sees
// an explicit failure, not a success — the incident this feature exists to
// prevent. rs is nil (no ensureStore), so the guard fires before any write.
func TestContentSubmitStorageDownIsHonest(t *testing.T) {
	m := &Module{} // rs nil, host nil
	mux := http.NewServeMux()
	mux.HandleFunc("POST /answer/{slug}", m.handleContentSubmit)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	form := url.Values{"body": {"my answer"}}.Encode()
	code, body := do(t, http.MethodPost, ts.URL+"/answer/report", formCT, form, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("storage down = %d, want 503", code)
	}
	if s := string(body); strings.Contains(s, "✓ Recorded") || !strings.Contains(s, "Not recorded") {
		t.Errorf("storage-down page must be an honest failure:\n%s", s)
	}
}

// --- forward flow (?next=) ---

// noRedirect returns a client that reports redirects instead of following them,
// so the wait endpoint's 302 is observable.
func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// TestContentSubmitWithNextPointsForward: an answer carrying a valid hidden
// next field gets the honest "Recorded" confirmation PLUS the forward flow —
// a link and a meta-refresh into the wait endpoint. Recording still happens
// exactly as without next.
func TestContentSubmitWithNextPointsForward(t *testing.T) {
	ts, m := contentSetup(t)
	form := url.Values{"body": {"my answer"}, "next": {"lesson-02"}}.Encode()
	code, body := do(t, http.MethodPost, ts.URL+"/answer/report", formCT, form, false)
	if code != http.StatusCreated {
		t.Fatalf("submit = %d, want 201", code)
	}
	s := string(body)
	if !strings.Contains(s, "✓ Recorded") {
		t.Errorf("next must not weaken the honest confirmation:\n%s", s)
	}
	if !strings.Contains(s, "/answer/report/next?to=lesson-02") {
		t.Errorf("confirmation missing forward link to the wait endpoint:\n%s", s)
	}
	if !strings.Contains(s, `http-equiv="refresh"`) {
		t.Errorf("confirmation missing the JS-free meta-refresh:\n%s", s)
	}
	if !strings.Contains(s, "being prepared") {
		t.Errorf("confirmation should say the next lesson is being prepared:\n%s", s)
	}
	if n, _ := m.rs.count("report"); n != 1 {
		t.Errorf("answer with next recorded %d replies, want 1", n)
	}
}

// TestContentSubmitBadNextDegrades: a tampered/invalid hidden next (bad charset,
// or pointing at the page itself) records the answer normally and renders the
// plain confirmation — no forward chrome, no refresh.
func TestContentSubmitBadNextDegrades(t *testing.T) {
	ts, _ := contentSetup(t)
	for _, next := range []string{"../etc/passwd", "has space", "report"} {
		form := url.Values{"body": {"my answer"}, "next": {next}}.Encode()
		code, body := do(t, http.MethodPost, ts.URL+"/answer/report", formCT, form, false)
		if code != http.StatusCreated {
			t.Fatalf("next=%q: submit = %d, want 201", next, code)
		}
		s := string(body)
		if !strings.Contains(s, "✓ Recorded") {
			t.Errorf("next=%q: answer should still record:\n%s", next, s)
		}
		if strings.Contains(s, `http-equiv="refresh"`) || strings.Contains(s, "next lesson") {
			t.Errorf("next=%q: invalid next must degrade to the plain confirmation:\n%s", next, s)
		}
	}
}

// TestNextWaitFlow drives the wait endpoint through its whole life: honest
// waiting while the follow-up is unpublished, redirect the moment it exists.
func TestNextWaitFlow(t *testing.T) {
	ts, m := contentSetup(t)
	client := noRedirect()

	// Not yet published → an honest holding page that refreshes itself.
	resp, err := client.Get(ts.URL + "/answer/report/next?to=lesson-02")
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 8192)
	n, _ := resp.Body.Read(b)
	resp.Body.Close()
	s := string(b[:n])
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("wait (unpublished) = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(s, "being prepared") || !strings.Contains(s, `http-equiv="refresh" content="5"`) {
		t.Errorf("wait page not an honest self-refreshing holding page:\n%s", s)
	}
	if !strings.Contains(s, `href="/report"`) {
		t.Errorf("wait page must keep the way back to the source page:\n%s", s)
	}
	if strings.Contains(s, "Recorded") || strings.Contains(s, "ready.</h1>") {
		t.Errorf("wait page must not claim recording or readiness:\n%s", s)
	}

	// Publish the follow-up → the same URL now redirects to it.
	if _, err := m.host.Store().Put(store.PutOptions{Slug: "lesson-02"}, strings.NewReader("<html>2</html>")); err != nil {
		t.Fatalf("publish next: %v", err)
	}
	resp, err = client.Get(ts.URL + "/answer/report/next?to=lesson-02")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("wait (published) = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/lesson-02" {
		t.Errorf("redirect Location = %q, want /lesson-02", loc)
	}
}

// TestNextWaitRejectsBadTargets: the wait endpoint validates both the source
// slug and the ?to= pointer.
func TestNextWaitRejectsBadTargets(t *testing.T) {
	ts, _ := contentSetup(t)
	cases := []struct {
		name, path string
		want       int
	}{
		{"missing to", "/answer/report/next", http.StatusBadRequest},
		{"self to", "/answer/report/next?to=report", http.StatusBadRequest},
		{"invalid to", "/answer/report/next?to=..%2Fetc", http.StatusBadRequest},
		{"unknown source slug", "/answer/nope/next?to=lesson-02", http.StatusNotFound},
	}
	for _, c := range cases {
		if code, _ := do(t, http.MethodGet, ts.URL+c.path, "", "", false); code != c.want {
			t.Errorf("%s: GET %s = %d, want %d", c.name, c.path, code, c.want)
		}
	}
}

// TestImplementsContentRouteModule pins the optional-interface wiring so the
// content-origin submit path is actually mounted by the server.
func TestImplementsContentRouteModule(t *testing.T) {
	var _ module.ContentRouteModule = (*Module)(nil)
	for _, rm := range module.ContentRouteModules() {
		if rm.Name() == "reply" {
			return
		}
	}
	t.Fatal("reply module not registered as a ContentRouteModule")
}

func TestModuleMetadata(t *testing.T) {
	m := &Module{}
	if m.Name() != "reply" {
		t.Errorf("Name = %q, want reply", m.Name())
	}
	if got := m.Reserved(); len(got) != 2 || got[0] != "reply" || got[1] != "replies" {
		t.Errorf("Reserved = %v, want [reply replies]", got)
	}
}

func TestRegisteredAsRouteModule(t *testing.T) {
	for _, rm := range module.RouteModules() {
		if rm.Name() == "reply" {
			return
		}
	}
	t.Fatal("reply module not registered as a RouteModule")
}
