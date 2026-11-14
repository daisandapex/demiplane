// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fuzzToken   = "SECRET-TOKEN-fuzz-0123456789"
	fuzzOutside = "OUTSIDE-SECRET-fuzz-9876543210"
	fuzzControl = "127.0.0.1:18080"
	fuzzContent = "127.0.0.1:18081"
)

// recordingTransport stands in for the network: it answers every request
// itself and records where each one was addressed and what it carried, so no
// fuzz input can reach a real address.
type recordingTransport struct {
	mu   sync.Mutex
	reqs []recorded
}

type recorded struct {
	host, auth string
	body       []byte
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
		r.Body.Close()
	}
	rt.mu.Lock()
	rt.reqs = append(rt.reqs, recorded{host: r.URL.Host, auth: r.Header.Get("Authorization"), body: body})
	rt.mu.Unlock()
	status, out := http.StatusOK, "ok"
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publish") {
		status, out = http.StatusCreated, `{"url":"http://`+fuzzContent+`/x"}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"text/plain"}},
		Body:       io.NopCloser(strings.NewReader(out)),
		Request:    r,
	}, nil
}

func (rt *recordingTransport) take() []recorded {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	r := rt.reqs
	rt.reqs = nil
	return r
}

// fuzzClient builds a client confined to a publish root under base, with
// the token file inside the root and a secret file and a symlink to it
// beside and inside the root.
func fuzzClient(tb testing.TB, base string) (*Client, *recordingTransport) {
	root := filepath.Join(base, "root")
	must := func(err error) {
		if err != nil {
			tb.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "sub"), 0o700))
	must(os.WriteFile(filepath.Join(root, "ok.txt"), []byte("public"), 0o600))
	must(os.WriteFile(filepath.Join(root, "sub", "ok.md"), []byte("# public"), 0o600))
	must(os.WriteFile(filepath.Join(root, "token"), []byte(fuzzToken+"\n"), 0o600))
	must(os.WriteFile(filepath.Join(base, "outside.txt"), []byte(fuzzOutside), 0o600))
	must(os.Symlink(filepath.Join(base, "outside.txt"), filepath.Join(root, "link")))
	must(os.Symlink("..", filepath.Join(root, "sub", "up")))
	must(os.Link(filepath.Join(root, "token"), filepath.Join(root, "hardtoken")))
	c := NewClient("http://"+fuzzControl, "http://"+fuzzContent, "")
	must(c.LoadTokenFile(filepath.Join(root, "token")))
	must(c.SetPublishRoot(root))
	rt := &recordingTransport{}
	c.HTTP.Transport = rt
	return c, rt
}

// checkRequests holds the guards SECURITY.md gives the MCP server: it talks
// only to this instance's listeners and never publishes the token file or a
// file outside the publish root.
func checkRequests(t *testing.T, reqs []recorded) {
	t.Helper()
	for _, r := range reqs {
		if r.host != fuzzControl && r.host != fuzzContent {
			t.Fatalf("request left the instance: host %q", r.host)
		}
		if r.auth != "" && r.auth != "Bearer "+fuzzToken {
			t.Fatalf("request carried Authorization %q", r.auth)
		}
		if bytes.Contains(r.body, []byte(fuzzToken)) {
			t.Fatal("the token file was published")
		}
		if bytes.Contains(r.body, []byte(fuzzOutside)) {
			t.Fatal("a file outside the publish root was published")
		}
	}
}

// fuzzServeBound is the longest one fuzzed MCP session may take.
const fuzzServeBound = 20 * time.Second

// FuzzServe feeds hostile JSON-RPC streams to the stdio server. "$ROOT" in
// the input stands for the publish root's parent, so the fuzzer can reach
// the path guard with absolute paths. Every response is one JSON-RPC 2.0
// object, there is never more than one per line in, the token never reaches
// stdout, and the guards in checkRequests hold.
func FuzzServe(f *testing.F) {
	for _, s := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"x"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":"a","method":"ping"}`,
		`{"jsonrpc":"1.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"publish","arguments":{"content":"hi","slug":"a"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"publish","arguments":{"path":"$ROOT/root/ok.txt"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"publish","arguments":{"path":"$ROOT/root/token"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"publish","arguments":{"path":"$ROOT/root/hardtoken"}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"publish","arguments":{"path":"$ROOT/root/link"}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"publish","arguments":{"path":"$ROOT/root/../outside.txt"}}}`,
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"publish","arguments":{"path":"$ROOT/root/sub/up/ok.txt"}}}`,
		`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"get","arguments":{"url":"http://127.0.0.1:18081/x"}}}`,
		`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"get","arguments":{"url":"http://127.0.0.1:6379/"}}}`,
		`{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"get","arguments":{"url":"http://127.0.0.1:18080@evil.example/"}}}`,
		`{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"get","arguments":{"slug":"../../list"}}}`,
		`{"jsonrpc":"2.0","id":15,"method":"tools/call","params":{"name":"list","arguments":{"after":"x&all=1"}}}`,
		`{"jsonrpc":"2.0","id":16,"method":"tools/call","params":{"name":"delete","arguments":{"slug":"a/../b"}}}`,
		`{"jsonrpc":"2.0","id":17,"method":"tools/call","params":{"name":"delete_many","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":18,"method":"tools/call","params":{"name":"publish","arguments":{"content":"x","ttl":"1e3d"}}}`,
		"{\"jsonrpc\":\"2.0\",\"id\":19}\n\n{not json\n[1,2]",
	} {
		f.Add([]byte(s))
	}
	base := f.TempDir()
	c, rt := fuzzClient(f, base)
	f.Fuzz(func(t *testing.T, in []byte) {
		in = bytes.ReplaceAll(in, []byte("$ROOT"), []byte(base))
		var out bytes.Buffer
		// Serve checks its context between messages and passes it to every
		// request it makes, so the time bound is a context deadline inside
		// the call: a session past it returns DeadlineExceeded. It is
		// generous, as plain `go test` replays the seeds on loaded machines.
		ctx, cancel := context.WithTimeout(context.Background(), fuzzServeBound)
		defer cancel()
		if err := Serve(ctx, bytes.NewReader(in), &out, c, "fuzz"); err != nil {
			t.Fatalf("Serve: %v (bound %v)", err, fuzzServeBound)
		}
		checkRequests(t, rt.take())
		if bytes.Contains(out.Bytes(), []byte(fuzzToken)) {
			t.Fatal("the bearer token reached stdout")
		}
		lines := 0
		sc := bufio.NewScanner(&out)
		sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for sc.Scan() {
			lines++
			var resp struct {
				JSONRPC string          `json:"jsonrpc"`
				Result  json.RawMessage `json:"result"`
				Error   *rpcError       `json:"error"`
			}
			if err := json.Unmarshal(sc.Bytes(), &resp); err != nil || resp.JSONRPC != "2.0" ||
				(resp.Result == nil) == (resp.Error == nil) {
				t.Fatalf("not a JSON-RPC 2.0 response: %q", sc.Bytes())
			}
		}
		if in := bytes.Count(in, []byte("\n")) + 1; lines > in {
			t.Fatalf("%d responses to %d lines", lines, in)
		}
	})
}

// FuzzOpenPublishPath fuzzes the `path` guard directly: whatever path the
// model picks, an opened file is a regular file inside the publish root and
// is not the token file, and a path the guard's rules refuse is refused.
func FuzzOpenPublishPath(f *testing.F) {
	for _, s := range []string{
		"root/ok.txt", "root/sub/ok.md", "root/token", "root/hardtoken", "root/link",
		"root/../outside.txt", "root/sub/up/ok.txt", "root/sub/up/../outside.txt",
		"root//ok.txt", "root/./ok.txt", "outside.txt", "root", "root/sub", "/etc/passwd",
		"root/ok.txt/", "root/sub/../ok.txt", "root/\x00",
	} {
		f.Add(s)
	}
	base := f.TempDir()
	c, _ := fuzzClient(f, base)
	rootPath := c.publishRootPath
	f.Fuzz(func(t *testing.T, p string) {
		full := base + "/" + p
		if strings.HasPrefix(p, "/") {
			full = p
		}
		cf, rerr := openPublishPath(c, full)
		// The reject side, judged with os and path/filepath: a path with a
		// NUL, one whose last element is not a regular file (a symlink, a
		// directory, missing), or one that resolves outside the publish
		// root is refused.
		refuse := strings.ContainsRune(full, 0)
		if fi, err := os.Lstat(full); err != nil || !fi.Mode().IsRegular() {
			refuse = true
		}
		if res, err := filepath.EvalSymlinks(full); err != nil {
			refuse = true
		} else if rel, err := filepath.Rel(rootPath, res); err != nil || !filepath.IsLocal(rel) {
			refuse = true
		}
		if refuse && rerr == nil {
			cf.Close()
			t.Fatalf("opened %q, which the guard must refuse", p)
		}
		if rerr != nil {
			return
		}
		defer cf.Close()
		st, err := cf.f.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if !st.Mode().IsRegular() {
			t.Fatalf("opened a non-regular file for %q", p)
		}
		if c.isTokenFile(st) {
			t.Fatalf("opened the token file for %q", p)
		}
		b, _ := io.ReadAll(cf)
		if bytes.Contains(b, []byte(fuzzOutside)) || bytes.Contains(b, []byte(fuzzToken)) {
			t.Fatalf("read a protected file for %q", p)
		}
		// The file opened is one of the files under the root.
		found := false
		filepath.Walk(rootPath, func(q string, fi os.FileInfo, err error) error {
			if err == nil && fi.Mode().IsRegular() && os.SameFile(fi, st) {
				found = true
			}
			return nil
		})
		if !found {
			t.Fatalf("opened a file outside the publish root for %q", p)
		}
	})
}
