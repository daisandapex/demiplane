// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// publishPathCall runs one publish tools/call for path against a fake control
// plane and returns the JSON-RPC error (nil on success) plus the body the fake
// received, so a test can assert both the refusal and that nothing was sent.
func publishPathCall(t *testing.T, c *Client, f *fakeControl, path string) (*rpcError, string) {
	t.Helper()
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{"path":` + jsonStr(path) + `}}}` + "\n"
	resps := runLoop(t, c, in)
	return resps[0].Error, f.lastBody
}

func newGuardClient(t *testing.T, token string) (*Client, *fakeControl) {
	t.Helper()
	f := newFakeControl(t)
	f.status = http.StatusCreated
	f.reply = `{"url":"http://content/p","slug":"p"}`
	return NewClient(f.URL, "", token), f
}

// TestPublishPathGuard_EnvTokenProcEnviron: with the token taken from the
// environment there is no token file, and the process environment itself must
// still be unreadable through `path`. The root is "/" so the refusal has to come
// from the pseudo-filesystem check, not from root confinement.
func TestPublishPathGuard_EnvTokenProcEnviron(t *testing.T) {
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no /proc on this platform")
	}
	c, f := newGuardClient(t, "ENV-TOKEN")
	setPublishRoot(t, c, "/")
	for _, p := range []string{"/proc/self/environ", "/proc/self/cmdline", "/sys/kernel/hostname"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		f.lastBody = ""
		rerr, body := publishPathCall(t, c, f, p)
		if rerr == nil || rerr.Code != codeInvalidParams {
			t.Errorf("%s: expected refusal, got %+v", p, rerr)
		}
		if body != "" {
			t.Errorf("%s: bytes reached the server (%d bytes)", p, len(body))
		}
	}
}

// TestPublishPathGuard_TokenFileAliases: a symlink and a hardlink to the token
// file are both refused. The hardlink has a different path, so only a
// file-identity comparison catches it.
func TestPublishPathGuard_TokenFileAliases(t *testing.T) {
	dir := t.TempDir()
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("SECRET-TOKEN"), 0o600); err != nil {
		t.Fatal(err)
	}
	sym := filepath.Join(dir, "sym.html")
	if err := os.Symlink(tf, sym); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	hard := filepath.Join(dir, "hard.html")
	if err := os.Link(tf, hard); err != nil {
		t.Skipf("hardlink unsupported: %v", err)
	}
	c, f := newGuardClient(t, "SECRET-TOKEN")
	if err := c.SetTokenFile(tf); err != nil {
		t.Fatal(err)
	}
	setPublishRoot(t, c, dir)
	for _, p := range []string{tf, sym, hard} {
		f.lastBody = ""
		rerr, body := publishPathCall(t, c, f, p)
		if rerr == nil || rerr.Code != codeInvalidParams {
			t.Errorf("%s: expected refusal, got %+v", filepath.Base(p), rerr)
		}
		if strings.Contains(body, "SECRET-TOKEN") {
			t.Errorf("%s: token file contents were published", filepath.Base(p))
		}
	}
}

// TestPublishPathGuard_Confinement: `path` must resolve inside the publish root.
// A file outside it, a relative escape, and an in-root symlinked directory that
// points outside are all refused; a plain file inside is published.
func TestPublishPathGuard_Confinement(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("OUTSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "page.html")
	if err := os.WriteFile(inside, []byte("INSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	c, f := newGuardClient(t, "")
	setPublishRoot(t, c, root)

	for _, p := range []string{
		secret,
		filepath.Join(root, "..", filepath.Base(outside), "secret.txt"),
		filepath.Join(root, "linkdir", "secret.txt"),
	} {
		f.lastBody = ""
		rerr, body := publishPathCall(t, c, f, p)
		if rerr == nil || rerr.Code != codeInvalidParams {
			t.Errorf("%s: expected refusal, got %+v", p, rerr)
		}
		if body == "OUTSIDE" {
			t.Errorf("%s: file outside the publish root was published", p)
		}
	}

	f.lastBody = ""
	if rerr, body := publishPathCall(t, c, f, inside); rerr != nil || body != "INSIDE" {
		t.Fatalf("in-root file: err=%+v body=%q", rerr, body)
	}
}

// TestPublishPathGuard_NoRootRefuses: a client with no publish root refuses
// every `path` publish instead of reading anywhere.
func TestPublishPathGuard_NoRootRefuses(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "page.html")
	if err := os.WriteFile(fp, []byte("BODY"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, f := newGuardClient(t, "")
	rerr, body := publishPathCall(t, c, f, fp)
	if rerr == nil || rerr.Code != codeInvalidParams {
		t.Fatalf("expected refusal with no publish root, got %+v", rerr)
	}
	if body != "" {
		t.Fatalf("bytes reached the server: %q", body)
	}
}

// TestPublishPathGuard_TokenFileReplaced: the token file's identity is taken
// at startup, so a hardlink to it is still refused after the original path is
// renamed away and a different file put in its place.
func TestPublishPathGuard_TokenFileReplaced(t *testing.T) {
	dir := t.TempDir()
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("SECRET-TOKEN"), 0o600); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(dir, "hard.html")
	if err := os.Link(tf, hard); err != nil {
		t.Skipf("hardlink unsupported: %v", err)
	}
	c, f := newGuardClient(t, "SECRET-TOKEN")
	if err := c.SetTokenFile(tf); err != nil {
		t.Fatal(err)
	}
	setPublishRoot(t, c, dir)
	if err := os.Rename(tf, filepath.Join(dir, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tf, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	rerr, body := publishPathCall(t, c, f, hard)
	if rerr == nil || strings.Contains(body, "SECRET-TOKEN") {
		t.Fatalf("hardlink to the original token file was published after the path was replaced (err %+v)", rerr)
	}
}

// TestPublishPathGuard_RootReplaced: the publish root is opened at startup.
// Replacing the directory at that path with a symlink elsewhere does not move
// the root.
func TestPublishPathGuard_RootReplaced(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("OUTSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, f := newGuardClient(t, "")
	setPublishRoot(t, c, root)
	if err := os.Rename(root, root+".orig"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	rerr, body := publishPathCall(t, c, f, filepath.Join(root, "secret.txt"))
	if rerr == nil || body == "OUTSIDE" {
		t.Fatalf("file outside the original root was published after the root path was replaced (err %+v)", rerr)
	}
}
