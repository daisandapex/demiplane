// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runLoop feeds newline-delimited input through Serve and returns the decoded
// responses in order.
func runLoop(t *testing.T, c *Client, input string) []response {
	t.Helper()
	var out bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(input), &out, c, "1.2.3"); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resps []response
	dec := json.NewDecoder(&out)
	for dec.More() {
		var r response
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		resps = append(resps, r)
	}
	return resps
}

func TestInitializeHandshake(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "")
	resps := runLoop(t, c, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`+"\n")
	if len(resps) != 1 {
		t.Fatalf("got %d responses", len(resps))
	}
	var res initializeResult
	mustReDecode(t, resps[0].Result, &res)
	if res.ProtocolVersion != "2025-06-18" {
		t.Errorf("protocolVersion = %q", res.ProtocolVersion)
	}
	if _, ok := res.Capabilities["tools"]; !ok {
		t.Errorf("tools capability not advertised: %+v", res.Capabilities)
	}
	if res.ServerInfo["name"] != "demiplane" || res.ServerInfo["version"] != "1.2.3" {
		t.Errorf("serverInfo = %+v", res.ServerInfo)
	}
	// id must be echoed.
	if string(resps[0].ID) != "1" {
		t.Errorf("id = %s", resps[0].ID)
	}
}

func TestInitializeDefaultsVersion(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "")
	resps := runLoop(t, c, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n")
	var res initializeResult
	mustReDecode(t, resps[0].Result, &res)
	if res.ProtocolVersion != protocolVersion {
		t.Errorf("default protocolVersion = %q, want %q", res.ProtocolVersion, protocolVersion)
	}
}

func TestToolsList(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "")
	resps := runLoop(t, c, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n")
	var res struct {
		Tools []tool `json:"tools"`
	}
	mustReDecode(t, resps[0].Result, &res)
	names := map[string]bool{}
	for _, tl := range res.Tools {
		names[tl.Name] = true
		if tl.InputSchema == nil {
			t.Errorf("tool %q has nil inputSchema", tl.Name)
		}
	}
	for _, want := range []string{"publish", "list", "delete", "get"} {
		if !names[want] {
			t.Errorf("tools/list missing %q", want)
		}
	}
}

func TestNotificationsProduceNoResponse(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "")
	// initialized notification (no id) then a real request.
	in := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":5,"method":"ping"}` + "\n"
	resps := runLoop(t, c, in)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1 (notification must be silent)", len(resps))
	}
	if string(resps[0].ID) != "5" {
		t.Errorf("id = %s", resps[0].ID)
	}
}

func TestUnknownMethod(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "")
	resps := runLoop(t, c, `{"jsonrpc":"2.0","id":9,"method":"does/notexist"}`+"\n")
	if resps[0].Error == nil || resps[0].Error.Code != codeMethodNotFound {
		t.Errorf("error = %+v, want method-not-found", resps[0].Error)
	}
}

func TestParseError(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "")
	resps := runLoop(t, c, "{not json}\n")
	if resps[0].Error == nil || resps[0].Error.Code != codeParseError {
		t.Errorf("error = %+v, want parse error", resps[0].Error)
	}
}

func TestToolsCall_Publish_RoundTrip(t *testing.T) {
	f := newFakeControl(t)
	f.status = http.StatusCreated
	f.reply = `{"url":"http://content:8081/shadow-specter","slug":"shadow-specter"}`
	c := NewClient(f.URL, "", "")

	in := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"publish","arguments":{"content":"<h1>hi</h1>"}}}` + "\n"
	resps := runLoop(t, c, in)
	if resps[0].Error != nil {
		t.Fatalf("unexpected error: %+v", resps[0].Error)
	}
	var res toolResult
	mustReDecode(t, resps[0].Result, &res)
	if len(res.Content) != 1 || res.Content[0].Text != "http://content:8081/shadow-specter" {
		t.Errorf("content = %+v", res.Content)
	}
	if f.lastBody != "<h1>hi</h1>" {
		t.Errorf("published body = %q", f.lastBody)
	}
}

func TestToolsCall_HTTPErrorBecomesRPCError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusRequestEntityTooLarge, http.StatusBadRequest} {
		f := newFakeControl(t)
		f.status = status
		f.reply = "denied"
		c := NewClient(f.URL, "", "")
		in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{"content":"x"}}}` + "\n"
		resps := runLoop(t, c, in)
		if resps[0].Error == nil {
			t.Fatalf("status %d: expected rpc error", status)
		}
		if resps[0].Error.Code != codeServerError {
			t.Errorf("status %d: code = %d", status, resps[0].Error.Code)
		}
	}
}

func TestToolsCall_ContentXorPath(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "")
	// both content and path → invalid params
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{"content":"a","path":"/tmp/x"}}}` + "\n"
	resps := runLoop(t, c, in)
	if resps[0].Error == nil || resps[0].Error.Code != codeInvalidParams {
		t.Errorf("both set: error = %+v", resps[0].Error)
	}
	// neither → invalid params
	in = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{}}}` + "\n"
	resps = runLoop(t, c, in)
	if resps[0].Error == nil || resps[0].Error.Code != codeInvalidParams {
		t.Errorf("neither set: error = %+v", resps[0].Error)
	}
}

func TestToolsCall_PublishFromPath(t *testing.T) {
	f := newFakeControl(t)
	f.status = http.StatusCreated
	f.reply = `{"url":"http://content/p","slug":"p"}`
	dir := t.TempDir()
	fp := filepath.Join(dir, "page.html")
	if err := os.WriteFile(fp, []byte("FILE-BODY"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewClient(f.URL, "", "")
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{"path":` + jsonStr(fp) + `}}}` + "\n"
	resps := runLoop(t, c, in)
	if resps[0].Error != nil {
		t.Fatalf("error: %+v", resps[0].Error)
	}
	if f.lastBody != "FILE-BODY" {
		t.Errorf("body = %q", f.lastBody)
	}
	// filename hint derived from the path.
	if !strings.Contains(f.lastQuery, "filename=page.html") {
		t.Errorf("query = %q", f.lastQuery)
	}
}

// TestToolsCall_PublishRefusesTokenFile proves the publish tool refuses to
// read-and-publish the configured token file, so a prompt-injected model cannot
// use `path` to exfiltrate the bearer token to a public slug (invariant 4).
func TestToolsCall_PublishRefusesTokenFile(t *testing.T) {
	f := newFakeControl(t)
	f.status = http.StatusCreated
	f.reply = `{"url":"http://content/p","slug":"p"}`
	dir := t.TempDir()
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("SECRET-TOKEN"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewClient(f.URL, "", "SECRET-TOKEN")
	abs, _ := filepath.Abs(tf)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	c.TokenFile = abs

	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{"path":` + jsonStr(tf) + `}}}` + "\n"
	resps := runLoop(t, c, in)
	if resps[0].Error == nil || resps[0].Error.Code != codeInvalidParams {
		t.Fatalf("expected invalid-params refusal, got %+v", resps[0].Error)
	}
	if f.lastBody == "SECRET-TOKEN" {
		t.Fatal("token file was published — exfiltration guard failed")
	}
}

// TestToolsCall_PublishRefusesSymlink proves the publish tool refuses a
// non-regular path (a symlink), so `path` cannot follow a link to a secret.
func TestToolsCall_PublishRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret")
	if err := os.WriteFile(target, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.html")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	c := NewClient("http://127.0.0.1:1", "", "")
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{"path":` + jsonStr(link) + `}}}` + "\n"
	resps := runLoop(t, c, in)
	if resps[0].Error == nil || resps[0].Error.Code != codeInvalidParams {
		t.Fatalf("expected invalid-params refusal for symlink, got %+v", resps[0].Error)
	}
}

func TestToolsCall_PrivateWithSlugRejected(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "")
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{"content":"x","private":true,"slug":"named"}}}` + "\n"
	resps := runLoop(t, c, in)
	if resps[0].Error == nil || resps[0].Error.Code != codeInvalidParams {
		t.Errorf("error = %+v", resps[0].Error)
	}
}

func TestToolsCall_UnknownTool(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "")
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"frobnicate","arguments":{}}}` + "\n"
	resps := runLoop(t, c, in)
	if resps[0].Error == nil || resps[0].Error.Code != codeInvalidParams {
		t.Errorf("error = %+v", resps[0].Error)
	}
}

func TestToolsCall_GetSlugXorURL(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "")
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get","arguments":{}}}` + "\n"
	resps := runLoop(t, c, in)
	if resps[0].Error == nil || resps[0].Error.Code != codeInvalidParams {
		t.Errorf("error = %+v", resps[0].Error)
	}
}

func TestToolsCall_GetBinarySummarized(t *testing.T) {
	f := newFakeControl(t)
	f.contentType = "image/png"
	f.reply = "\x89PNGbinary"
	c := NewClient(f.URL, f.URL, "")
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get","arguments":{"slug":"img"}}}` + "\n"
	resps := runLoop(t, c, in)
	var res toolResult
	mustReDecode(t, resps[0].Result, &res)
	if !strings.Contains(res.Content[0].Text, "binary artifact") {
		t.Errorf("binary get text = %q", res.Content[0].Text)
	}
}

// TestNoStdoutLeakOnError verifies error text carries no token even when the
// client is configured with one and the upstream fails.
func TestNoTokenLeakInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "", "SUPER-SECRET-TOKEN")
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list","arguments":{}}}` + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(in), &out, c, "dev"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "SUPER-SECRET-TOKEN") {
		t.Fatalf("token leaked into response: %s", out.String())
	}
}

func mustReDecode(t *testing.T, v any, into any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatal(err)
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
