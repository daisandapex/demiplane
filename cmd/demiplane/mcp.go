// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/daisandapex/demiplane/v3/internal/mcp"
)

// runMCP runs the stdio MCP server: a JSON-RPC 2.0 loop over stdin/stdout that
// advertises publish/list/delete/get as MCP tools and forwards each tools/call
// to the demiplane control plane over HTTP. It is a THIN CLIENT of the running
// server (no store/filesystem coupling), stdlib-only, so it ships in the core
// build with no tag.
//
// Config: --url / DEMIPLANE_URL (control plane), --content-url /
// DEMIPLANE_CONTENT_URL (artifact origin; defaults to the control host so the
// `get` tool works in --unsafe-same-origin deployments), --token-file /
// DEMIPLANE_TOKEN (reuse resolveToken), --publish-root / DEMIPLANE_PUBLISH_ROOT
// (the directory the publish tool's `path` must resolve inside). The token is taken from the file/env
// only — never from argv, never echoed to stdout or logs. stdout is reserved for
// JSON-RPC traffic; diagnostics go to stderr.
func runMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "demiplane control-plane base URL (publish/list/delete); overrides DEMIPLANE_URL")
	contentURL := fs.String("content-url", "", "demiplane content origin base URL for GET (artifact bodies); overrides DEMIPLANE_CONTENT_URL, defaults to --url host")
	tokenFile := fs.String("token-file", "", "file holding the bearer token; overrides DEMIPLANE_TOKEN")
	publishRoot := fs.String("publish-root", "", "directory the publish tool's path argument must resolve inside; overrides DEMIPLANE_PUBLISH_ROOT, defaults to the working directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	controlURL := firstNonEmpty(*urlFlag, os.Getenv("DEMIPLANE_URL"), "http://127.0.0.1:8080")
	content := firstNonEmpty(*contentURL, os.Getenv("DEMIPLANE_CONTENT_URL"))

	client, err := newMCPClient(controlURL, content, *tokenFile)
	if err != nil {
		return err
	}
	root, err := mcpPublishRoot(*publishRoot)
	if err != nil {
		return err
	}
	if root != "" {
		if err := client.SetPublishRoot(root); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(os.Stderr, "demiplane mcp: publishing by path is disabled (no usable publish root; set --publish-root to enable it)")
	}

	// Terminate the read loop cleanly on SIGINT/SIGTERM (in addition to stdin
	// EOF, which is how a well-behaved MCP host signals shutdown).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := mcp.Serve(ctx, os.Stdin, os.Stdout, client, version); err != nil {
		if ctx.Err() != nil {
			return nil // signalled shutdown is not an error
		}
		return fmt.Errorf("mcp server: %w", err)
	}
	return nil
}

// mcpTokenLoaded, when set by a test, runs right after the bearer token is
// read, to replace the token file in the window before setup finishes.
var mcpTokenLoaded func()

// newMCPClient builds the MCP client with its bearer token, taken from
// tokenFile (preferred) or DEMIPLANE_TOKEN; never from argv, never printed. A
// token file is read through LoadTokenFile, which records the file's identity
// from the same descriptor the token came from, so the publish tool can refuse
// to read-and-publish it (token-exfiltration guard; see readPublishPath). A
// DEMIPLANE_TOKEN env token has no backing file; the pseudo-filesystem check
// covers the process environment instead.
func newMCPClient(controlURL, contentURL, tokenFile string) (*mcp.Client, error) {
	if tokenFile == "" {
		token, err := resolveToken("")
		if err != nil {
			return nil, err
		}
		return mcp.NewClient(controlURL, contentURL, token), nil
	}
	client := mcp.NewClient(controlURL, contentURL, "")
	if err := client.LoadTokenFile(tokenFile); err != nil {
		return nil, err
	}
	if mcpTokenLoaded != nil {
		mcpTokenLoaded()
	}
	return client, nil
}

// mcpPublishRoot picks the directory the publish tool may read `path` from:
// --publish-root, then DEMIPLANE_PUBLISH_ROOT, then the working directory. An
// explicit root must exist and be a directory. A working directory of / is not
// taken as a default, since that would confine nothing; it yields "" (path
// publishing disabled) unless the operator sets / explicitly.
func mcpPublishRoot(flagVal string) (string, error) {
	explicit := firstNonEmpty(flagVal, os.Getenv("DEMIPLANE_PUBLISH_ROOT"))
	dir := explicit
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", nil
		}
		dir = wd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("publish root: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if explicit != "" {
			return "", fmt.Errorf("publish root: %w", err)
		}
		return "", nil
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.IsDir() {
		if explicit != "" {
			return "", fmt.Errorf("publish root %q is not a directory", explicit)
		}
		return "", nil
	}
	if explicit == "" && real == string(filepath.Separator) {
		return "", nil
	}
	return real, nil
}

// firstNonEmpty returns the first non-empty, whitespace-trimmed argument, or ""
// if all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
