// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/store"
)

func newTestServer(t *testing.T, baseURL string) *httptest.Server {
	t.Helper()
	return newAuthTestServer(t, baseURL, "")
}

func newAuthTestServer(t *testing.T, baseURL, token string) *httptest.Server {
	t.Helper()
	return newConfiguredServer(t, Config{BaseURL: baseURL, AuthToken: token})
}

func newConfiguredServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, cfg).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// publish POSTs body and returns the artifact URL from the text response.
func publish(t *testing.T, ts *httptest.Server, query, body string) string {
	t.Helper()
	resp, err := http.Post(ts.URL+"/publish"+query, "application/octet-stream", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /publish: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish status = %d, body=%q", resp.StatusCode, b)
	}
	return strings.TrimSpace(string(b))
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestPublishGetRoundTrip(t *testing.T) {
	ts := newTestServer(t, "")
	html := "<!DOCTYPE html><html><body>round trip</body></html>"

	url := publish(t, ts, "", html)
	if !strings.HasPrefix(url, ts.URL+"/") {
		t.Fatalf("returned URL %q lacks server prefix %q", url, ts.URL)
	}

	resp, body := get(t, url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	if string(body) != html {
		t.Errorf("body mismatch:\n got %q\nwant %q", body, html)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type = %q, want text/html", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "inline" {
		t.Errorf("disposition = %q, want inline", cd)
	}
}

func TestNamedSlugOverwrite(t *testing.T) {
	ts := newTestServer(t, "")
	v1 := "<html>v1</html>"
	v2 := "<html>v2</html>"

	u1 := publish(t, ts, "?slug=reports", v1)
	u2 := publish(t, ts, "?slug=reports", v2)
	if u1 != u2 {
		t.Fatalf("named slug URL changed: %q vs %q", u1, u2)
	}
	if !strings.HasSuffix(u1, "/reports") {
		t.Fatalf("named URL = %q, want .../reports", u1)
	}

	_, body := get(t, u1)
	if string(body) != v2 {
		t.Errorf("after overwrite body = %q, want %q", body, v2)
	}
}

func TestPublishMultipart(t *testing.T) {
	ts := newTestServer(t, "")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := "body { color: rebeccapurple; }"
	io.WriteString(fw, css)
	mw.Close()

	resp, err := http.Post(ts.URL+"/publish", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatalf("multipart POST: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body=%q", resp.StatusCode, body)
	}
	url := strings.TrimSpace(string(body))

	getResp, got := get(t, url)
	if string(got) != css {
		t.Errorf("multipart body mismatch: got %q, want %q", got, css)
	}
	if ct := getResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("content type = %q, want text/css (from .css extension)", ct)
	}
}

func TestAttachmentDisposition(t *testing.T) {
	ts := newTestServer(t, "")
	url := publish(t, ts, "?filename=archive.bin", "\x00\x01\x02not-web-content")

	resp, _ := get(t, url)
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment") {
		t.Errorf("disposition = %q, want attachment", cd)
	}
	if !strings.Contains(cd, "archive.bin") {
		t.Errorf("disposition %q missing filename", cd)
	}
}

func TestBaseURLOverridesHost(t *testing.T) {
	ts := newTestServer(t, "https://reports.example.com/")
	url := publish(t, ts, "?slug=fixed", "<html>x</html>")
	if url != "https://reports.example.com/fixed" {
		t.Errorf("URL = %q, want https://reports.example.com/fixed", url)
	}
}

func TestPublishJSONResponse(t *testing.T) {
	ts := newTestServer(t, "")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/publish", strings.NewReader("<html>j</html>"))
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out struct {
		URL         string `json:"url"`
		Slug        string `json:"slug"`
		ContentType string `json:"content_type"`
		Size        int64  `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if out.Slug == "" || !strings.HasSuffix(out.URL, "/"+out.Slug) {
		t.Errorf("JSON url/slug inconsistent: %+v", out)
	}
	if out.Size != int64(len("<html>j</html>")) {
		t.Errorf("size = %d, want %d", out.Size, len("<html>j</html>"))
	}
}

func TestRedactSlug(t *testing.T) {
	r1 := redactSlug("super-secret-capability-slug")
	if strings.Contains(r1, "super-secret") || !strings.HasPrefix(r1, "slug#") {
		t.Errorf("redactSlug leaked or malformed: %q", r1)
	}
	// Deterministic and distinct per slug.
	if r1 != redactSlug("super-secret-capability-slug") {
		t.Error("redactSlug not deterministic")
	}
	if r1 == redactSlug("different-slug") {
		t.Error("redactSlug collided on different inputs")
	}
}

func TestMaxUploadCap(t *testing.T) {
	ts := newConfiguredServer(t, Config{MaxUpload: 16})
	// Under the cap: OK.
	if code, _ := do(t, http.MethodPost, ts.URL+"/publish", "", "tiny"); code != http.StatusCreated {
		t.Errorf("small upload = %d, want 201", code)
	}
	// Over the cap: 413.
	big := strings.Repeat("x", 64)
	if code, _ := do(t, http.MethodPost, ts.URL+"/publish", "", big); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize upload = %d, want 413", code)
	}
}

// TestMaxUploadCapRenderPath ensures the markdown render path also returns 413
// (not 500) when --max-upload fires — it reads the body separately from the
// store.Put streaming path.
func TestMaxUploadCapRenderPath(t *testing.T) {
	ts := newConfiguredServer(t, Config{MaxUpload: 16})
	big := "# " + strings.Repeat("x", 64)
	if code, body := do(t, http.MethodPost, ts.URL+"/publish?render=md", "", big); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize render upload = %d (body %q), want 413", code, body)
	}
	// Under the cap renders fine.
	if code, _ := do(t, http.MethodPost, ts.URL+"/publish?render=md", "", "# ok"); code != http.StatusCreated {
		t.Errorf("small render upload = %d, want 201", code)
	}
}

func TestGetUnknownSlug404(t *testing.T) {
	ts := newTestServer(t, "")
	resp, _ := get(t, ts.URL+"/nope-nope-nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestPublishBadSlug400(t *testing.T) {
	ts := newTestServer(t, "")
	resp, err := http.Post(ts.URL+"/publish?slug=../escape", "application/octet-stream", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// do sends a request with an optional bearer token and returns status + body.
func do(t *testing.T, method, url, token, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestAuthRequiredWhenTokenSet(t *testing.T) {
	const tok = "s3cret-token"
	ts := newAuthTestServer(t, "", tok)

	// No token → 401 on each protected endpoint.
	for _, ep := range []struct {
		method, path string
	}{
		{http.MethodPost, "/publish"},
		{http.MethodGet, "/list"},
		{http.MethodDelete, "/anything"},
	} {
		if code, _ := do(t, ep.method, ts.URL+ep.path, "", "x"); code != http.StatusUnauthorized {
			t.Errorf("%s %s without token: status = %d, want 401", ep.method, ep.path, code)
		}
	}

	// Wrong token → 401.
	if code, _ := do(t, http.MethodGet, ts.URL+"/list", "wrong", ""); code != http.StatusUnauthorized {
		t.Errorf("bad token: status = %d, want 401", code)
	}

	// Correct token → success.
	if code, _ := do(t, http.MethodPost, ts.URL+"/publish", tok, "<html>ok</html>"); code != http.StatusCreated {
		t.Errorf("good token publish: status = %d, want 201", code)
	}

	// GET /{slug} is always open — publish one (authed), fetch without a token.
	code, body := do(t, http.MethodPost, ts.URL+"/publish?slug=open", tok, "<html>open</html>")
	if code != http.StatusCreated {
		t.Fatalf("setup publish: status = %d", code)
	}
	_ = body
	if code, _ := do(t, http.MethodGet, ts.URL+"/open", "", ""); code != http.StatusOK {
		t.Errorf("GET /{slug} without token: status = %d, want 200 (view auth is open)", code)
	}
}

func TestNoAuthWhenTokenUnset(t *testing.T) {
	ts := newAuthTestServer(t, "", "") // open mode
	if code, _ := do(t, http.MethodPost, ts.URL+"/publish", "", "<html>x</html>"); code != http.StatusCreated {
		t.Errorf("open-mode publish: status = %d, want 201", code)
	}
	if code, _ := do(t, http.MethodGet, ts.URL+"/list", "", ""); code != http.StatusOK {
		t.Errorf("open-mode list: status = %d, want 200", code)
	}
}

func TestDelete(t *testing.T) {
	ts := newTestServer(t, "")
	url := publish(t, ts, "?slug=trash", "<html>byebye</html>")

	// Present before delete.
	if resp, _ := get(t, url); resp.StatusCode != http.StatusOK {
		t.Fatalf("pre-delete GET status = %d", resp.StatusCode)
	}
	// Delete → 204.
	if code, _ := do(t, http.MethodDelete, url, "", ""); code != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204", code)
	}
	// Gone → 404.
	if resp, _ := get(t, url); resp.StatusCode != http.StatusNotFound {
		t.Errorf("post-delete GET status = %d, want 404", resp.StatusCode)
	}
	// Deleting again → 404.
	if code, _ := do(t, http.MethodDelete, url, "", ""); code != http.StatusNotFound {
		t.Errorf("re-DELETE status = %d, want 404", code)
	}
}

func TestList(t *testing.T) {
	ts := newTestServer(t, "")

	// Empty list.
	code, body := do(t, http.MethodGet, ts.URL+"/list", "", "")
	if code != http.StatusOK {
		t.Fatalf("list status = %d", code)
	}
	var empty struct {
		Artifacts []listItem `json:"artifacts"`
		Count     int        `json:"count"`
	}
	if err := json.Unmarshal(body, &empty); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if empty.Count != 0 || len(empty.Artifacts) != 0 {
		t.Errorf("empty list: count=%d len=%d", empty.Count, len(empty.Artifacts))
	}

	// Publish two, then list.
	publish(t, ts, "?slug=alpha", "<html>a</html>")
	publish(t, ts, "?slug=beta", "<html>b</html>")

	code, body = do(t, http.MethodGet, ts.URL+"/list", "", "")
	if code != http.StatusOK {
		t.Fatalf("list status = %d", code)
	}
	var out struct {
		Artifacts []listItem `json:"artifacts"`
		Count     int        `json:"count"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Count != 2 || len(out.Artifacts) != 2 {
		t.Fatalf("list count = %d, want 2", out.Count)
	}
	slugs := map[string]listItem{}
	for _, it := range out.Artifacts {
		slugs[it.Slug] = it
	}
	for _, want := range []string{"alpha", "beta"} {
		it, ok := slugs[want]
		if !ok {
			t.Errorf("missing %q in list", want)
			continue
		}
		if !strings.HasSuffix(it.URL, "/"+want) {
			t.Errorf("%q URL = %q, want suffix /%s", want, it.URL, want)
		}
		if it.Owner != store.DefaultOwner {
			t.Errorf("%q owner = %q, want %q", want, it.Owner, store.DefaultOwner)
		}
	}
}

func TestPrivatePublishHighEntropySlug(t *testing.T) {
	ts := newTestServer(t, "")
	url := publish(t, ts, "?private=true", "<html>secret</html>")
	slug := url[strings.LastIndexByte(url, '/')+1:]
	// Capability slug: 20+ url-safe chars, NOT a friendly adjective-creature pair.
	if len(slug) < 20 {
		t.Errorf("private slug %q too short to be a capability secret", slug)
	}
	if regexp.MustCompile(`^[a-z]+-[a-z]+(-\d{4})?$`).MatchString(slug) {
		t.Errorf("private slug %q looks like a friendly slug, want high-entropy", slug)
	}
	// Still fetchable with the capability URL, no extra gate.
	if resp, body := get(t, url); resp.StatusCode != http.StatusOK || string(body) != "<html>secret</html>" {
		t.Errorf("private GET: status=%d body=%q", resp.StatusCode, body)
	}
}

// publishPassword publishes with a view password via the header (not the URL).
func publishPassword(t *testing.T, ts *httptest.Server, query, password, body string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/publish"+query, strings.NewReader(body))
	req.Header.Set(PasswordHeader, password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish status = %d, body=%q", resp.StatusCode, b)
	}
	return strings.TrimSpace(string(b))
}

func TestPasswordGate(t *testing.T) {
	ts := newTestServer(t, "")
	url := publishPassword(t, ts, "?slug=locked", "letmein", "<html>classified</html>")

	// No credentials → 401 with a Basic challenge.
	resp, _ := get(t, url)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-password GET = %d, want 401", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic") {
		t.Errorf("missing Basic challenge: %q", resp.Header.Get("WWW-Authenticate"))
	}

	// Wrong password → 401.
	if code := basicGet(t, url, "x", "nope"); code != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", code)
	}

	// Correct password (any username) → 200.
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.SetBasicAuth("ignored", "letmein")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	body, _ := io.ReadAll(r2.Body)
	if r2.StatusCode != http.StatusOK || string(body) != "<html>classified</html>" {
		t.Errorf("correct password: status=%d body=%q", r2.StatusCode, body)
	}
}

func basicGet(t *testing.T, url, user, pass string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.SetBasicAuth(user, pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestTTLPublishExpiresAndBadTTL(t *testing.T) {
	ts := newTestServer(t, "")

	// Invalid TTL → 400.
	resp, err := http.Post(ts.URL+"/publish?ttl=notaduration", "application/octet-stream", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad ttl status = %d, want 400", resp.StatusCode)
	}

	// Very short TTL expires; GET 404s after it passes (lazy reclaim).
	url := publish(t, ts, "?slug=blip&ttl=1ms", "<html>fleeting</html>")
	time.Sleep(10 * time.Millisecond)
	if resp, _ := get(t, url); resp.StatusCode != http.StatusNotFound {
		t.Errorf("expired GET = %d, want 404", resp.StatusCode)
	}
}

func TestPrivateNamedSlugRejected(t *testing.T) {
	ts := newTestServer(t, "")
	resp, err := http.Post(ts.URL+"/publish?slug=reports&private=true", "application/octet-stream", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("private+named status = %d, want 400 (no-op privacy must be rejected)", resp.StatusCode)
	}
}

func TestPasswordInQueryRejected(t *testing.T) {
	ts := newTestServer(t, "")
	// Password via URL must be refused so it can't silently land in logs.
	resp, err := http.Post(ts.URL+"/publish?slug=x&password=leaky", "application/octet-stream", strings.NewReader("y"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("password-in-query status = %d, want 400", resp.StatusCode)
	}
	// Even an empty ?password= is rejected (presence, not value).
	resp2, _ := http.Post(ts.URL+"/publish?password=", "application/octet-stream", strings.NewReader("y"))
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("empty password-in-query status = %d, want 400", resp2.StatusCode)
	}
}

func TestPublishJSONReportsPrivacyTTL(t *testing.T) {
	ts := newTestServer(t, "")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/publish?private=true&ttl=1h", strings.NewReader("<html>x</html>"))
	req.Header.Set("Accept", "application/json")
	req.Header.Set(PasswordHeader, "pw")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Private   bool   `json:"private"`
		Password  bool   `json:"password"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Private || !out.Password || out.ExpiresAt == "" {
		t.Errorf("JSON did not reflect private/password/ttl: %+v", out)
	}
}

func TestMarkdownRenderOnPublish(t *testing.T) {
	ts := newTestServer(t, "")
	url := publish(t, ts, "?slug=doc&render=md", "# Hello\n\nsome **bold** text")

	resp, body := get(t, url)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("rendered content-type = %q, want text/html", ct)
	}
	if !strings.Contains(string(body), `<h1 id="hello">Hello`) || !strings.Contains(string(body), "<strong>bold</strong>") {
		t.Errorf("markdown not rendered:\n%s", body)
	}
	// The rendered page carries the house style by default (demiplane-7fp), not
	// the old bare system-sans stylesheet.
	if !strings.Contains(string(body), "--accent:oklch(0.485") || !strings.Contains(string(body), "var(--serif)") {
		t.Errorf("rendered page missing house-style markers:\n%s", body)
	}
	// The print sheet forces light hex values inside @media print; scope the
	// old-bare-stylesheet check to the on-screen CSS before that block.
	screen := string(body)
	if i := strings.Index(screen, "@media print"); i >= 0 {
		screen = screen[:i]
	}
	if strings.Contains(screen, "color:#1a1a1a") {
		t.Errorf("rendered page still uses the old bare stylesheet:\n%s", body)
	}

	// Without ?render, markdown is stored verbatim.
	raw := publish(t, ts, "?slug=raw", "# Hello")
	_, rawBody := get(t, raw)
	if strings.Contains(string(rawBody), "<h1>") {
		t.Errorf("unrendered publish should be verbatim, got:\n%s", rawBody)
	}
}

// TestRenderThemeFlag verifies --theme dark re-skins ?render=md output.
func TestRenderThemeFlag(t *testing.T) {
	ts := newConfiguredServer(t, Config{RenderTheme: "dark"})
	url := publish(t, ts, "?slug=doc&render=md", "# Hello")
	_, body := get(t, url)
	if !strings.Contains(string(body), "--bg:oklch(0.225") {
		t.Errorf("dark theme not applied to rendered page:\n%s", body)
	}
}

// TestRenderCSSFlag verifies --css replaces the built-in theme entirely.
func TestRenderCSSFlag(t *testing.T) {
	const custom = "body{background:hotpink}"
	ts := newConfiguredServer(t, Config{RenderCSS: custom})
	url := publish(t, ts, "?slug=doc&render=md", "# Hello")
	_, body := get(t, url)
	if !strings.Contains(string(body), custom) {
		t.Errorf("custom CSS not applied:\n%s", body)
	}
	if strings.Contains(string(body), "--accent:oklch(0.555") {
		t.Errorf("custom CSS should replace the built-in theme:\n%s", body)
	}
}

// TestRenderChromeConfig verifies the masthead + vanity footer honor the
// server's render-chrome config on ?render=md pages.
func TestRenderChromeConfig(t *testing.T) {
	on := newConfiguredServer(t, Config{RenderHeader: true, RenderFooter: true, RenderFooterLink: "https://repo.example"})
	u := publish(t, on, "?slug=d&render=md", "# Doc Title\n\nbody text")
	_, body := get(t, u)
	s := string(body)
	if !strings.Contains(s, `class="docbar"`) || !strings.Contains(s, `class="doctitle">Doc Title</h1>`) {
		t.Errorf("masthead with lifted H1 missing:\n%s", s)
	}
	if !strings.Contains(s, "Generated by") || !strings.Contains(s, `href="https://repo.example"`) {
		t.Errorf("vanity footer with configured link missing:\n%s", s)
	}

	off := newConfiguredServer(t, Config{}) // header+footer default false here
	u2 := publish(t, off, "?slug=d&render=md", "# Doc Title\n\nbody text")
	_, body2 := get(t, u2)
	s2 := string(body2)
	if strings.Contains(s2, `class="docbar"`) || strings.Contains(s2, "Generated by") {
		t.Errorf("chrome should be absent when header/footer are off:\n%s", s2)
	}
	if !strings.Contains(s2, `<h1 id="doc-title">Doc Title`) {
		t.Errorf("H1 should remain in the body when the masthead is off:\n%s", s2)
	}
}

// TestRenderMetaHeaderConfig verifies a frontmatter doc gets the localized date
// + per-field meta-header when RenderMetaHeader is on, and that frontmatter is
// stripped (no leak, no <header>) when it's off.
func TestRenderMetaHeaderConfig(t *testing.T) {
	const doc = "---\ndate: 2026-06-20T13:46:00Z\nrepo: demiplane\nbranch: feat/render-theme\n---\n# Overnight\n\nbody"

	on := newConfiguredServer(t, Config{RenderHeader: true, RenderMetaHeader: true})
	_, body := get(t, publish(t, on, "?slug=m&render=md", doc))
	s := string(body)
	for _, want := range []string{
		`class="metahead"`,
		`datetime="2026-06-20T13:46:00Z"`,
		"2026-06-20 · 13:46 UTC",
		"<dt>Repo</dt><dd>demiplane</dd>",
		"<dt>Branch</dt><dd>feat/render-theme</dd>",
		"timeZoneName", // localize script shipped
	} {
		if !strings.Contains(s, want) {
			t.Errorf("meta-header missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "date: 2026-06-20") {
		t.Errorf("raw frontmatter leaked into the page:\n%s", s)
	}

	off := newConfiguredServer(t, Config{RenderHeader: true, RenderMetaHeader: false})
	_, body2 := get(t, publish(t, off, "?slug=m&render=md", doc))
	s2 := string(body2)
	if strings.Contains(s2, "metahead") || strings.Contains(s2, "date: 2026-06-20") {
		t.Errorf("meta-header should be absent and frontmatter stripped when off:\n%s", s2)
	}
	if !strings.Contains(s2, `class="doctitle">Overnight</h1>`) {
		t.Errorf("masthead title should still render when meta-header is off:\n%s", s2)
	}
}

// TestChromeHonorsTheme verifies --theme dark darkens the CHROME too (landing +
// docs), not just rendered content — one setting skins everything.
func TestChromeHonorsTheme(t *testing.T) {
	dark := newConfiguredServer(t, Config{RenderTheme: "dark"})
	light := newConfiguredServer(t, Config{}) // default house style

	for _, path := range []string{"/", "/docs", "/docs/readme"} {
		_, dbody := get(t, dark.URL+path)
		if !strings.Contains(string(dbody), "--bg:oklch(0.225") {
			t.Errorf("dark chrome at %s missing dark background token:\n%.300s", path, dbody)
		}
		if strings.Contains(string(dbody), "--bg:oklch(0.972") {
			t.Errorf("dark chrome at %s leaked the light background token", path)
		}
		// Chrome-only classes are still present (theme swaps colors, not structure).
		if !strings.Contains(string(dbody), "header.top") {
			t.Errorf("dark chrome at %s lost the chrome classes", path)
		}

		_, lbody := get(t, light.URL+path)
		if !strings.Contains(string(lbody), "--bg:oklch(0.972") {
			t.Errorf("default chrome at %s should be the light house style:\n%.300s", path, lbody)
		}
	}
}

func TestLandingOrientsAndDoesNotLeakWithoutBrowse(t *testing.T) {
	ts := newTestServer(t, "") // no Browse
	url := publish(t, ts, "?slug=secret-name", "<html>x</html>")
	slug := url[strings.LastIndexByte(url, '/')+1:]

	resp, body := get(t, ts.URL+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("landing status = %d, want 200", resp.StatusCode)
	}
	page := string(body)
	// Orients a newcomer: tagline + access-model ethos + pointers + publish hint.
	for _, want := range []string{
		"Your private publishing plane.",
		"Sealed by default, shared by choice.",
		"/docs", "/llms.txt", "/publish",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("landing missing %q", want)
		}
	}
	// Without --browse, artifact slugs must NOT be listed.
	if strings.Contains(page, slug) {
		t.Errorf("landing leaked artifact slug %q without --browse", slug)
	}
}

func TestDocsRoutes(t *testing.T) {
	ts := newTestServer(t, "")
	// Index.
	resp, body := get(t, ts.URL+"/docs")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("/docs status=%d ct=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, slug := range []string{"readme", "security", "contributing", "changelog"} {
		if !strings.Contains(string(body), "/docs/"+slug) {
			t.Errorf("/docs index missing link to %s", slug)
		}
	}
	// A real page renders embedded markdown (README → contains the project name as a heading).
	resp, body = get(t, ts.URL+"/docs/readme")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/docs/readme status=%d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "<h1 id=") || !strings.Contains(string(body), "demiplane") {
		t.Errorf("/docs/readme did not render markdown:\n%.300s", body)
	}
	// Unknown page → 404.
	if resp, _ := get(t, ts.URL+"/docs/nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("/docs/nope = %d, want 404", resp.StatusCode)
	}
}

func TestLLMsTxt(t *testing.T) {
	ts := newTestServer(t, "")
	resp, body := get(t, ts.URL+"/llms.txt")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/llms.txt status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("/llms.txt content-type=%q, want text/plain", ct)
	}
	for _, want := range []string{"POST", "/publish", "?private", "?ttl", "X-Demiplane-Password", "413"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/llms.txt missing %q", want)
		}
	}
	// Examples use the instance base URL.
	if !strings.Contains(string(body), ts.URL+"/publish") {
		t.Errorf("/llms.txt examples don't use the instance base URL")
	}
}

func TestHelpJSON(t *testing.T) {
	ts := newTestServer(t, "")
	resp, body := get(t, ts.URL+"/help.json")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("/help.json status=%d ct=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var help struct {
		Service   string `json:"service"`
		Endpoints []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"endpoints"`
		Errors []struct {
			Code int `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &help); err != nil {
		t.Fatalf("decode /help.json: %v", err)
	}
	if help.Service != "demiplane" || len(help.Endpoints) == 0 || len(help.Errors) == 0 {
		t.Errorf("/help.json incomplete: %+v", help)
	}
	var hasPublish bool
	for _, e := range help.Endpoints {
		if e.Method == "POST" && e.Path == "/publish" {
			hasPublish = true
		}
	}
	if !hasPublish {
		t.Error("/help.json missing POST /publish")
	}
}

// TestHelpHumanPage verifies /help now serves the human getting-started HTML
// (not JSON), carries a Help nav entry, and links out to the API reference and
// Connect rather than duplicating them.
func TestHelpHumanPage(t *testing.T) {
	ts := newTestServer(t, "")
	resp, body := get(t, ts.URL+"/help")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/help status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("/help Content-Type = %q, want text/html", ct)
	}
	page := string(body)
	for _, want := range []string{
		"demiplane help",     // page heading
		">Help<",             // nav entry
		`href="/docs/api"`,   // links out to the API reference
		`href="/connect"`,    // links out to Connect
		`href="/help.json"`,  // points agents at the JSON
		"Install", "Theming", // section coverage
		"reply module", "TLS module", "SSH ingest",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("/help page missing %q", want)
		}
	}
}

// TestHelpPageNeverEmitsToken is the security assertion for the help surface: the
// live bearer token must never appear, only the token FILE PATH.
func TestHelpPageNeverEmitsToken(t *testing.T) {
	const secret = "SUPER-SECRET-BEARER-help-abc123"
	ts := newConfiguredServer(t, Config{AuthToken: secret})
	_, body := get(t, ts.URL+"/help")
	if strings.Contains(string(body), secret) {
		t.Fatal("/help page LEAKED the bearer token value")
	}
}

// TestDocsAPIPage verifies the embedded API reference renders at /docs/api with
// the multi-language examples and marks the API nav entry active.
func TestDocsAPIPage(t *testing.T) {
	ts := newTestServer(t, "")
	resp, body := get(t, ts.URL+"/docs/api")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/docs/api status=%d", resp.StatusCode)
	}
	page := string(body)
	for _, want := range []string{
		"API reference",           // rendered H1
		"/publish",                // the core endpoint
		`class="language-python"`, // Python example, syntax-highlighted (demiplane-rwj)
		`class="language-go"`,     // Go example, syntax-highlighted
		"NewRequest",              // Go example token (fn-highlighted; text preserved)
		"await fetch(",            // JavaScript example — unsupported lang, stays plain
		"curl",                    // curl example
		"/_events/",               // SSE endpoint documented (literal-first shape)
		"content origin",          // two-plane model
	} {
		if !strings.Contains(page, want) {
			t.Errorf("/docs/api missing %q", want)
		}
	}
	// The API nav entry (href /docs/api) is marked active on this page.
	if !strings.Contains(page, `href="/docs/api" class="active"`) {
		t.Error("/docs/api should mark the API nav entry active")
	}
}

func TestReservedRouteNamesNotPublishable(t *testing.T) {
	ts := newTestServer(t, "")
	// Every name that collides with a built-in route must be rejected as a slug,
	// so an artifact can never shadow (or be shadowed by) a route.
	for _, name := range []string{"docs", "help", "help.json", "llms.txt", "publish", "list"} {
		resp, _ := http.Post(ts.URL+"/publish?slug="+name, "application/octet-stream", strings.NewReader("x"))
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("publish ?slug=%s = %d, want 400 (reserved)", name, resp.StatusCode)
		}
		// The route itself still resolves (not shadowed).
		if name == "docs" || name == "help" || name == "help.json" || name == "llms.txt" {
			if resp2, _ := get(t, ts.URL+"/"+name); resp2.StatusCode != http.StatusOK {
				t.Errorf("route /%s = %d after reserved-publish attempt, want 200", name, resp2.StatusCode)
			}
		}
	}
}

// TestReplyBoxPublishParam covers the ?reply=question publish option (core render
// path, no module needed): a valid request bakes the same-origin box into the
// page, and each invalid combination is rejected loudly rather than shipping a
// box that could never record an answer.
func TestReplyBoxPublishParam(t *testing.T) {
	ts := newTestServer(t, "")

	// Valid: render=md + named slug → page carries the box posting to /answer/<slug>.
	url := publish(t, ts, "?render=md&slug=q-01&reply=question", "# Q\n\nAnswer below.")
	resp, body := get(t, url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET rendered page = %d", resp.StatusCode)
	}
	if s := string(body); !strings.Contains(s, `action="/answer/q-01"`) || !strings.Contains(s, `name="body"`) {
		t.Errorf("rendered page missing same-origin reply box:\n%s", s)
	}

	// Invalid combinations → 400, no artifact created.
	cases := []struct {
		name, query string
	}{
		{"no render", "?slug=q-02&reply=question"},
		{"no slug", "?render=md&reply=question"},
		{"private", "?render=md&private=true&reply=question"},
		{"next without reply", "?render=md&slug=q-02&next=q-03"},
		{"next invalid slug", "?render=md&slug=q-02&reply=question&next=..%2Fetc"},
		{"next equals slug", "?render=md&slug=q-02&reply=question&next=q-02"},
	}
	for _, c := range cases {
		if code, _ := do(t, http.MethodPost, ts.URL+"/publish"+c.query, "", "# x"); code != http.StatusBadRequest {
			t.Errorf("%s: publish %s = %d, want 400", c.name, c.query, code)
		}
	}
}

// TestReplyNextPublishParam covers the ?next= forward-flow publish option: a
// valid next is baked into the box as a hidden field so the confirmation page
// can carry the student to the follow-up lesson once it is published.
func TestReplyNextPublishParam(t *testing.T) {
	ts := newTestServer(t, "")
	url := publish(t, ts, "?render=md&slug=fw-01&reply=question&next=fw-02", "# Q1")
	resp, body := get(t, url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET rendered page = %d", resp.StatusCode)
	}
	if s := string(body); !strings.Contains(s, `<input type="hidden" name="next" value="fw-02">`) {
		t.Errorf("rendered page missing hidden next field:\n%s", s)
	}
}

func TestMethodNotAllowedDocumented(t *testing.T) {
	ts := newTestServer(t, "")
	// 405 is real behavior (ServeMux) and must be documented in both surfaces.
	if code, _ := do(t, http.MethodPost, ts.URL+"/list", "", "x"); code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /list = %d, want 405", code)
	}
	if _, body := get(t, ts.URL+"/llms.txt"); !strings.Contains(string(body), "405") {
		t.Error("/llms.txt does not document 405")
	}
	_, hbody := get(t, ts.URL+"/help.json")
	if !strings.Contains(string(hbody), "405") {
		t.Error("/help.json does not document 405")
	}
}

func TestBrowsePageExcludesPrivate(t *testing.T) {
	ts := newConfiguredServer(t, Config{Browse: true})
	publish(t, ts, "?slug=public-one", "<html>a</html>")
	privURL := publish(t, ts, "?private=true", "<html>secret</html>")
	privSlug := privURL[strings.LastIndexByte(privURL, '/')+1:]

	resp, body := get(t, ts.URL+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("browse status = %d", resp.StatusCode)
	}
	page := string(body)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("browse content-type = %q", ct)
	}
	if !strings.Contains(page, "public-one") {
		t.Error("browse page missing public artifact")
	}
	if strings.Contains(page, privSlug) {
		t.Errorf("browse page leaked a PRIVATE capability slug %q:\n%s", privSlug, page)
	}
}

// TestCollapseSeries is the unit for the landing table's series folding
// (demiplane-0pw): a slug family collapses to one reference on its newest member;
// a lone artifact stays a plain row.
func TestCollapseSeries(t *testing.T) {
	base := time.Now()
	pub := []store.Artifact{
		{Slug: "dispatch-08", CreatedAt: base},                                  // newest member
		{Slug: "dispatch-01", CreatedAt: base.Add(-time.Hour)},                  // older member
		{Slug: "design-lab", CreatedAt: base.Add(-2 * time.Hour)},               // singleton
		{Slug: "notes", CreatedAt: base.Add(-3 * time.Hour), PasswordHash: "x"}, // locked singleton
	}
	rows := collapseSeries(pub)
	if len(rows) != 3 {
		t.Fatalf("collapseSeries len = %d, want 3 (one series + two singletons)", len(rows))
	}
	// The series folds to the prefix, counts its members, links to the newest.
	if rows[0].label != "dispatch" || rows[0].count != 2 || rows[0].slug != "dispatch-08" {
		t.Errorf("series row = %+v, want label=dispatch count=2 slug=dispatch-08", rows[0])
	}
	if rows[0].locked {
		t.Errorf("a mixed series must not carry a lock glyph: %+v", rows[0])
	}
	// A lone artifact stays a plain row (no forced count) and keeps its own lock.
	if rows[1].label != "design-lab" || rows[1].count != 1 {
		t.Errorf("singleton row = %+v, want design-lab count=1", rows[1])
	}
	if rows[2].label != "notes" || rows[2].count != 1 || !rows[2].locked {
		t.Errorf("locked singleton row = %+v, want notes count=1 locked", rows[2])
	}
}

// TestLandingTableSlugOnlyAndCollapsed asserts the served landing table
// (demiplane-0pw): two columns only (slug + published, no type/size), and a
// slug-series rendered as one "(N)" reference to the newest member.
func TestLandingTableSlugOnlyAndCollapsed(t *testing.T) {
	ts := newConfiguredServer(t, Config{Browse: true})
	publish(t, ts, "?slug=dispatch-01", "<h1>1</h1>")
	publish(t, ts, "?slug=dispatch-02", "<h1>2</h1>")
	publish(t, ts, "?slug=dispatch-03", "<h1>3</h1>")
	publish(t, ts, "?slug=design-lab", "<h1>d</h1>")

	_, body := get(t, ts.URL+"/")
	page := string(body)

	// Two-column header, no type/size clutter.
	if !strings.Contains(page, "<th>artifact</th><th>published</th>") {
		t.Errorf("landing table is not the slim two-column shape")
	}
	if strings.Contains(page, "<th>type</th>") || strings.Contains(page, "<th>size</th>") {
		t.Errorf("landing table still carries a type/size column")
	}
	// The dispatch family is one reference with a (3) count, linking to a single
	// member (its newest — collapseSeries' unit test pins that selection; here we
	// only require a single collapsed row, robust to same-second publish order).
	if !strings.Contains(page, "dispatch") || !strings.Contains(page, "(3)") {
		t.Errorf("dispatch series did not collapse to one (3) reference")
	}
	if n := strings.Count(page, "dispatch-0"); n != 1 {
		t.Errorf("collapsed series links %d members, want exactly 1 (the newest)", n)
	}
	// The lone artifact stays a plain single row.
	if !strings.Contains(page, "design-lab") {
		t.Errorf("singleton design-lab missing from landing table")
	}
}

// TestTopNavReconciled asserts demiplane-6va: Gallery and Connect are both
// primary top-nav entries, and the landing no longer duplicates them as a
// "Learn more" tile row.
func TestTopNavReconciled(t *testing.T) {
	ts := newTestServer(t, "")
	_, body := get(t, ts.URL+"/")
	page := string(body)
	for _, want := range []string{`href="/gallery">Gallery`, `href="/connect">Connect`} {
		if !strings.Contains(page, want) {
			t.Errorf("top nav missing %q", want)
		}
	}
	if strings.Contains(page, "Learn more") {
		t.Errorf("landing still renders the redundant Learn more tile row")
	}
}

// TestListRouteDoesNotShadowSlugs guards the ServeMux precedence: GET /list hits
// the list handler, while GET /{other} still serves artifacts.
func TestListRouteDoesNotShadowSlugs(t *testing.T) {
	ts := newTestServer(t, "")
	url := publish(t, ts, "?slug=notlist", "<html>real</html>")
	if resp, body := get(t, url); resp.StatusCode != http.StatusOK || string(body) != "<html>real</html>" {
		t.Errorf("GET /notlist: status=%d body=%q", resp.StatusCode, body)
	}
	// And a literal /list is JSON, not an artifact lookup.
	code, body := do(t, http.MethodGet, ts.URL+"/list", "", "")
	if code != http.StatusOK || !strings.Contains(string(body), "artifacts") {
		t.Errorf("GET /list: status=%d body=%q", code, body)
	}
}

// TestSecurityHeaders covers the stored-XSS + clickjacking hardening: nosniff
// and same-origin framing headers on every response, a no-script CSP on SVG
// (scriptable-but-inert content), and no script restriction on HTML (hosting
// executable pages is an intended feature).
func TestSecurityHeaders(t *testing.T) {
	ts := newTestServer(t, "")

	htmlURL := publish(t, ts, "", "<html><body>page</body></html>")
	resp, _ := get(t, htmlURL)
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want SAMEORIGIN", got)
	}
	// HTML pages carry ONLY the anti-clickjacking directive — no script
	// restriction (executable pages are the product).
	if csp := resp.Header.Get("Content-Security-Policy"); csp != "frame-ancestors 'self'" {
		t.Errorf("HTML artifact CSP = %q, want frame-ancestors 'self' only", csp)
	}
	// Referrer-Policy keeps capability slugs out of the Referer on cross-origin
	// navigation (demiplane-hgp); present on every response.
	if got := resp.Header.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Errorf("Referrer-Policy = %q, want strict-origin-when-cross-origin", got)
	}
	// HSTS is TLS-only: this test server is plain HTTP, so it must be ABSENT
	// (advertising HSTS over plain HTTP is meaningless/harmful).
	if got := resp.Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("Strict-Transport-Security = %q over plain HTTP, want absent", got)
	}

	svgURL := publish(t, ts, "?filename=x.svg",
		`<svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>`)
	resp2, _ := get(t, svgURL)
	if csp := resp2.Header.Get("Content-Security-Policy"); csp != "script-src 'none'; frame-ancestors 'self'" {
		t.Errorf("SVG CSP = %q, want script-src 'none'; frame-ancestors 'self'", csp)
	}
	if got := resp2.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("SVG X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := resp2.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("SVG X-Frame-Options = %q, want SAMEORIGIN", got)
	}
}

// TestHSTSHeader proves the HSTS policy is emitted exactly when the request
// arrived over TLS (demiplane-5ce), and that its value is the conservative
// two-year max-age WITHOUT includeSubDomains or preload (a public instance must
// not commit sibling subdomains of its parent zone to HTTPS-only).
func TestHSTSHeader(t *testing.T) {
	h := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Over TLS: HSTS present, exact conservative value.
	rrTLS := httptest.NewRecorder()
	reqTLS := httptest.NewRequest(http.MethodGet, "https://demiplane.example/x", nil)
	reqTLS.TLS = &tls.ConnectionState{}
	h.ServeHTTP(rrTLS, reqTLS)
	if got := rrTLS.Header().Get("Strict-Transport-Security"); got != "max-age=63072000" {
		t.Errorf("HSTS over TLS = %q, want max-age=63072000 (no includeSubDomains/preload)", got)
	}
	if got := rrTLS.Header().Get("Strict-Transport-Security"); strings.Contains(got, "includeSubDomains") || strings.Contains(got, "preload") {
		t.Errorf("HSTS = %q, must NOT carry includeSubDomains or preload", got)
	}

	// Plain HTTP (r.TLS == nil): HSTS absent.
	rrPlain := httptest.NewRecorder()
	reqPlain := httptest.NewRequest(http.MethodGet, "http://demiplane.example/x", nil)
	h.ServeHTTP(rrPlain, reqPlain)
	if got := rrPlain.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS over plain HTTP = %q, want absent", got)
	}
}

// TestFrameHeadersOnControlPlane proves every control-plane surface refuses
// cross-origin framing (clickjacking; demiplane-t0j): X-Frame-Options SAMEORIGIN
// plus the equivalent CSP frame-ancestors directive.
func TestFrameHeadersOnControlPlane(t *testing.T) {
	ts := newTestServer(t, "")
	for _, path := range []string{"/", "/docs", "/llms.txt", "/help"} {
		resp, _ := get(t, ts.URL+path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, resp.StatusCode)
		}
		if got := resp.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
			t.Errorf("%s X-Frame-Options = %q, want SAMEORIGIN", path, got)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'self'") {
			t.Errorf("%s CSP = %q, want frame-ancestors 'self'", path, csp)
		}
	}
}

// TestSanitizeHost pins the host-reflection sanitizer (demiplane-6y9):
// legitimate hosts pass through untouched; injection characters are stripped.
func TestSanitizeHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"example.com", "example.com"},
		{"example.com:8890", "example.com:8890"},
		{"[::1]:8890", "[::1]:8890"},
		{"under_score.host", "under_score.host"},
		{"203.0.113.7:8891", "203.0.113.7:8891"},
		{"evil.com\r\nX-Injected: 1", "evil.comX-Injected:1"},
		{`e.com"><script>x</script>`, "e.comscriptxscript"},
		{"a b\tc", "abc"},
	}
	for _, c := range cases {
		if got := sanitizeHost(c.in); got != c.want {
			t.Errorf("sanitizeHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestLLMsHostReflectionSanitized drives the actual /llms.txt handler with a
// hostile Host header (set directly — Go's transport would refuse it on the
// wire; this is defense-in-depth for any path that lets one through) and
// asserts the reflected base URL cannot carry injected content.
func TestLLMsHostReflectionSanitized(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	srv := New(st, Config{})

	req := httptest.NewRequest(http.MethodGet, "http://placeholder/llms.txt", nil)
	req.Host = `evil.example "><script>alert(1)</script>` + "\r\nInjected: header"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, bad := range []string{`"><script>`, "alert(1)", "\r\nInjected:", `evil.example "`} {
		if strings.Contains(body, bad) {
			t.Errorf("llms.txt reflected hostile Host content %q:\n%s", bad, body)
		}
	}
	// The sanitized remnant is still a harmless single token on the Base URL line.
	if !strings.Contains(body, "Base URL: http://") {
		t.Errorf("llms.txt lost its Base URL line:\n%s", body)
	}
}

// TestExplicitBaseURLWinsOverHost pins the other half of the demiplane-6y9 fix:
// with --base-url configured, the request Host is never consulted at all.
func TestExplicitBaseURLWinsOverHost(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	srv := New(st, Config{BaseURL: "http://configured.example:8891"})

	req := httptest.NewRequest(http.MethodGet, "http://placeholder/llms.txt", nil)
	req.Host = "attacker.example"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "Base URL: http://configured.example:8891") {
		t.Errorf("llms.txt did not use the configured base URL:\n%s", body)
	}
	if strings.Contains(body, "attacker.example") {
		t.Errorf("llms.txt reflected the request Host despite --base-url:\n%s", body)
	}
}
