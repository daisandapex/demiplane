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

	"github.com/daisandapex/demiplane/internal/mcp"
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
// DEMIPLANE_TOKEN (reuse resolveToken). The token is taken from the file/env
// only — never from argv, never echoed to stdout or logs. stdout is reserved for
// JSON-RPC traffic; diagnostics go to stderr.
func runMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "demiplane control-plane base URL (publish/list/delete); overrides DEMIPLANE_URL")
	contentURL := fs.String("content-url", "", "demiplane content origin base URL for GET (artifact bodies); overrides DEMIPLANE_CONTENT_URL, defaults to --url host")
	tokenFile := fs.String("token-file", "", "file holding the bearer token; overrides DEMIPLANE_TOKEN")
	if err := fs.Parse(args); err != nil {
		return err
	}

	controlURL := firstNonEmpty(*urlFlag, os.Getenv("DEMIPLANE_URL"), "http://127.0.0.1:8080")
	content := firstNonEmpty(*contentURL, os.Getenv("DEMIPLANE_CONTENT_URL"))

	// Token from --token-file (preferred) or DEMIPLANE_TOKEN. resolveToken never
	// reads argv and we never print the returned value.
	token, err := resolveToken(*tokenFile)
	if err != nil {
		return err
	}

	client := mcp.NewClient(controlURL, content, token)
	// Record the resolved token-file path so the publish tool can refuse to
	// read-and-publish it (token-exfiltration guard; see guardPublishPath). A
	// DEMIPLANE_TOKEN env token has no backing file, so nothing to guard.
	if *tokenFile != "" {
		if abs, err := filepath.Abs(*tokenFile); err == nil {
			if real, rerr := filepath.EvalSymlinks(abs); rerr == nil {
				abs = real
			}
			client.TokenFile = abs
		}
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
