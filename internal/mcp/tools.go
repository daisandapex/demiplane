// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// contentBlock is one MCP content item. Only the text type is used.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolResult is the payload of a successful tools/call response.
type toolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

func textResult(text string) *toolResult {
	return &toolResult{Content: []contentBlock{{Type: "text", Text: text}}}
}

// tool describes an MCP tool for tools/list.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// toolDefs returns the advertised tool set. Schemas are JSON Schema objects the
// harness surfaces to the model.
func toolDefs() []tool {
	str := map[string]any{"type": "string"}
	return []tool{
		{
			Name: "publish",
			Description: "Publish content to demiplane and get back a shareable URL. Supply either " +
				"`content` (the text/HTML to publish) or `path` (a local file to read). Optional: " +
				"`slug` for a stable name that overwrites in place, `private` for an unguessable " +
				"capability URL, `ttl` to auto-expire (e.g. 30m, 24h, 7d), `render`=md to render " +
				"markdown to HTML, `filename` as a content-type hint, `password` to gate the view.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content":  map[string]any{"type": "string", "description": "the content to publish (mutually exclusive with path)"},
					"path":     map[string]any{"type": "string", "description": "a local file to read and publish (mutually exclusive with content)"},
					"slug":     map[string]any{"type": "string", "description": "stable name that overwrites in place; omit for a generated slug"},
					"private":  map[string]any{"type": "boolean", "description": "mint an unguessable capability URL instead of a guessable name"},
					"ttl":      map[string]any{"type": "string", "description": "auto-expire after a duration, e.g. 30m, 2h, 7d"},
					"render":   map[string]any{"type": "string", "enum": []string{"md", "markdown"}, "description": "render markdown source to an HTML page"},
					"filename": map[string]any{"type": "string", "description": "filename hint used to pick the content-type"},
					"password": map[string]any{"type": "string", "description": "require this password to view (sent as a header, never the URL)"},
				},
			},
		},
		{
			Name:        "list",
			Description: "List the non-private artifacts published on this demiplane instance (JSON: slug, url, type, size, timestamps).",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "delete",
			Description: "Delete an artifact by slug.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"slug": str},
				"required":   []string{"slug"},
			},
		},
		{
			Name: "get",
			Description: "Fetch an artifact's contents. Provide `slug` (fetched from the content origin) " +
				"or `url` (a full demiplane artifact URL, e.g. the one returned by publish).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"slug": map[string]any{"type": "string", "description": "artifact slug to fetch"},
					"url":  map[string]any{"type": "string", "description": "full artifact URL on this demiplane instance"},
				},
			},
		},
	}
}

// callTool dispatches a tools/call to the matching handler. A missing/unknown
// tool or invalid arguments yield an invalid-params error; a failed upstream
// HTTP call yields a server error carrying the status. Neither the token nor any
// password is ever placed in an error.
func callTool(ctx context.Context, c *Client, name string, args json.RawMessage) (*toolResult, *rpcError) {
	switch name {
	case "publish":
		return callPublish(ctx, c, args)
	case "list":
		return callList(ctx, c)
	case "delete":
		return callDelete(ctx, c, args)
	case "get":
		return callGet(ctx, c, args)
	default:
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("unknown tool %q", name)}
	}
}

type publishArgs struct {
	Content  *string `json:"content"`
	Path     string  `json:"path"`
	Slug     string  `json:"slug"`
	Private  bool    `json:"private"`
	TTL      string  `json:"ttl"`
	Render   string  `json:"render"`
	Filename string  `json:"filename"`
	Password string  `json:"password"`
}

func callPublish(ctx context.Context, c *Client, raw json.RawMessage) (*toolResult, *rpcError) {
	var a publishArgs
	if err := unmarshalArgs(raw, &a); err != nil {
		return nil, err
	}

	hasContent := a.Content != nil
	hasPath := a.Path != ""
	if hasContent == hasPath { // both set or neither set
		return nil, &rpcError{Code: codeInvalidParams, Message: "provide exactly one of `content` or `path`"}
	}

	var body []byte
	filename := a.Filename
	if hasContent {
		body = []byte(*a.Content)
	} else {
		if rerr := guardPublishPath(c, a.Path); rerr != nil {
			return nil, rerr
		}
		b, err := os.ReadFile(a.Path)
		if err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("read path: %v", err)}
		}
		body = b
		if filename == "" {
			filename = baseName(a.Path)
		}
	}

	// A named (guessable) slug cannot also be private — the server rejects the
	// combination; catch it client-side for a clearer error.
	if a.Private && a.Slug != "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "private artifacts cannot use a named slug — omit `slug` to get a capability URL"}
	}
	if err := validateTTL(a.TTL); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	if a.Render != "" && a.Render != "md" && a.Render != "markdown" {
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("invalid render %q (want md)", a.Render)}
	}

	res, err := c.Publish(ctx, PublishParams{
		Content:  body,
		Slug:     a.Slug,
		Private:  a.Private,
		TTL:      a.TTL,
		Render:   a.Render,
		Filename: filename,
		Password: a.Password,
	})
	if err != nil {
		return nil, upstreamError(err)
	}
	return textResult(res.URL), nil
}

// guardPublishPath vets a model-supplied `path` before the publish tool reads it.
// Tool arguments are attacker-influenceable (a prompt-injected model chooses this
// value, unlike the human-chosen CLI path), so the file read here is a potential
// arbitrary-file-read-and-publish primitive. Two defenses:
//
//   - Reject non-regular files (symlinks, devices, fifos, sockets) via Lstat, so
//     `path` cannot follow a symlink to a secret or block on a device.
//   - Refuse the configured token file. Publishing it would render the bearer
//     token to a world-readable slug, defeating invariant 4. The comparison is on
//     the fully symlink-resolved absolute path of both sides, so an alias to the
//     token file is caught too.
//
// `path` still reads any OTHER regular file the process can read; that mirrors the
// CLI's file publish and is the operator's responsibility to scope (documented in
// README/SECURITY). The token file is the one path we hard-refuse.
func guardPublishPath(c *Client, p string) *rpcError {
	fi, err := os.Lstat(p)
	if err != nil {
		return &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("read path: %v", err)}
	}
	if !fi.Mode().IsRegular() {
		return &rpcError{Code: codeInvalidParams, Message: "path must be a regular file (symlinks and special files are refused)"}
	}
	if c.TokenFile == "" {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("resolve path: %v", err)}
	}
	if abs, err := filepath.Abs(resolved); err == nil {
		resolved = abs
	}
	if resolved == c.TokenFile {
		return &rpcError{Code: codeInvalidParams, Message: "refusing to publish the bearer token file"}
	}
	return nil
}

func callList(ctx context.Context, c *Client) (*toolResult, *rpcError) {
	body, err := c.List(ctx)
	if err != nil {
		return nil, upstreamError(err)
	}
	return textResult(body), nil
}

func callDelete(ctx context.Context, c *Client, raw json.RawMessage) (*toolResult, *rpcError) {
	var a struct {
		Slug string `json:"slug"`
	}
	if err := unmarshalArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.Slug == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "`slug` is required"}
	}
	if err := c.Delete(ctx, a.Slug); err != nil {
		return nil, upstreamError(err)
	}
	return textResult("deleted " + a.Slug), nil
}

func callGet(ctx context.Context, c *Client, raw json.RawMessage) (*toolResult, *rpcError) {
	var a struct {
		Slug string `json:"slug"`
		URL  string `json:"url"`
	}
	if err := unmarshalArgs(raw, &a); err != nil {
		return nil, err
	}
	if (a.Slug == "") == (a.URL == "") {
		return nil, &rpcError{Code: codeInvalidParams, Message: "provide exactly one of `slug` or `url`"}
	}

	var (
		res *GetResult
		err error
	)
	if a.URL != "" {
		res, err = c.GetByURL(ctx, a.URL)
	} else {
		res, err = c.GetBySlug(ctx, a.Slug)
	}
	if err != nil {
		return nil, upstreamError(err)
	}

	if !isTextual(res.ContentType) {
		return textResult(fmt.Sprintf("binary artifact (%s, %d bytes) — not shown as text", res.ContentType, len(res.Body))), nil
	}
	text := string(res.Body)
	if res.Truncated {
		text += "\n\n[truncated at 1 MiB]"
	}
	return textResult(text), nil
}

// unmarshalArgs decodes tool arguments, tolerating an absent arguments member
// (treated as empty object). A malformed arguments blob is an invalid-params
// error.
func unmarshalArgs(raw json.RawMessage, v any) *rpcError {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &rpcError{Code: codeInvalidParams, Message: "invalid arguments: " + err.Error()}
	}
	return nil
}

// upstreamError maps a client error to a JSON-RPC error. A typed httpError
// carries the control plane's status; anything else is a transport/internal
// failure. The message never includes request material (token/password).
func upstreamError(err error) *rpcError {
	if he, ok := err.(*httpError); ok {
		return &rpcError{Code: codeServerError, Message: he.Error(), Data: map[string]any{"status": he.Status}}
	}
	return &rpcError{Code: codeServerError, Message: err.Error()}
}

// baseName returns the final path element of p (client-local; used only as a
// filename hint for the content-type).
func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}
