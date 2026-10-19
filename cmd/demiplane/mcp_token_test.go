// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/daisandapex/demiplane/v3/internal/mcp"
)

// TestMCPTokenIdentityMatchesBytesRead: the token file is atomically replaced
// right after the token is read. The identity the publish guard compares
// against must be that of the file whose bytes became the token, so an in-root
// hardlink to that original file is still refused.
func TestMCPTokenIdentityMatchesBytesRead(t *testing.T) {
	var mu sync.Mutex
	var published string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		published += string(b)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"url":"http://content/p","slug":"p"}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("SECRET-TOKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(dir, "hard.html")
	if err := os.Link(tf, hard); err != nil {
		t.Skipf("hardlink unsupported: %v", err)
	}
	mcpTokenLoaded = func() {
		repl := filepath.Join(dir, "token.new")
		if err := os.WriteFile(repl, []byte("OTHER-TOKEN\n"), 0o600); err != nil {
			t.Error(err)
		}
		if err := os.Rename(repl, tf); err != nil {
			t.Error(err)
		}
	}
	defer func() { mcpTokenLoaded = nil }()

	client, err := newMCPClient(srv.URL, "", tf)
	if err != nil {
		t.Fatal(err)
	}
	if client.Token != "SECRET-TOKEN" {
		t.Fatalf("token = %q, want the bytes read before the replacement", client.Token)
	}
	if err := client.SetPublishRoot(dir); err != nil {
		t.Fatal(err)
	}
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{"path":"` + hard + `"}}}` + "\n"
	var out strings.Builder
	if err := mcp.Serve(context.Background(), strings.NewReader(in), &out, client, "test"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(published, "SECRET-TOKEN") {
		t.Fatalf("hardlink to the token file that was read got published: %s", out.String())
	}
	if !strings.Contains(out.String(), "token file") {
		t.Fatalf("want a token-file refusal, got %s", out.String())
	}
}
