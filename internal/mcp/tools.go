// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	return append(manageToolDefs(), []tool{
		{
			Name: "publish",
			Description: "Publish content to demiplane and get back a shareable URL. Supply either " +
				"`content` (the text/HTML to publish) or `path` (a local file to read). Optional: " +
				"`slug` for a stable name that overwrites in place, `private` for an unguessable " +
				"capability URL, `ttl` to auto-expire (e.g. 30m, 24h, 7d), `render`=md to render " +
				"markdown to HTML, `filename` as a content-type hint, `password` to gate the view, " +
				"`series` to group artifacts into an explicit prev/next family.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content":  map[string]any{"type": "string", "description": "the content to publish (mutually exclusive with path)"},
					"path":     map[string]any{"type": "string", "description": "a local file to read and publish (mutually exclusive with content)"},
					"slug":     map[string]any{"type": "string", "description": "stable name that overwrites in place; omit for a generated slug"},
					"private":  map[string]any{"type": "boolean", "description": "mint an unguessable capability URL instead of a guessable name"},
					"ttl":      map[string]any{"type": "string", "description": "auto-expire after a duration, e.g. 30m, 2h, 7d"},
					"series":   map[string]any{"type": "string", "description": "explicit series family for colophon prev/next and gallery grouping"},
					"render":   map[string]any{"type": "string", "enum": []string{"md", "markdown"}, "description": "render markdown source to an HTML page"},
					"filename": map[string]any{"type": "string", "description": "filename hint used to pick the content-type"},
					"password": map[string]any{"type": "string", "description": "require this password to view (sent as a header, never the URL)"},
				},
			},
		},
		{
			Name:        "list",
			Description: "List the non-private artifacts published on this demiplane instance, newest first, 200 at a time (JSON: slug, url, type, size, timestamps; `next` is the cursor for the following page).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"after": map[string]any{"type": "string", "description": "the `next` cursor from a previous list call, for the following page"},
				},
			},
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
	}...)
}

// callTool dispatches a tools/call to the matching handler. A missing/unknown
// tool or invalid arguments yield an invalid-params error; a failed upstream
// HTTP call yields a server error carrying the status. Neither the token nor any
// password is ever placed in an error.
func callTool(ctx context.Context, c *Client, name string, args json.RawMessage) (*toolResult, *rpcError) {
	switch name {
	case "delete_many", "set_ttl", "pin":
		return callManage(ctx, c, name, args)
	case "publish":
		return callPublish(ctx, c, args)
	case "list":
		return callList(ctx, c, args)
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
	Series   string  `json:"series"`
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

	var (
		body   []byte
		stream io.Reader
	)
	filename := a.Filename
	if hasContent {
		body = []byte(*a.Content)
	} else {
		f, rerr := openPublishPath(c, a.Path)
		if rerr != nil {
			return nil, rerr
		}
		defer f.Close()
		stream = f
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
		Body:     stream,
		Slug:     a.Slug,
		Private:  a.Private,
		TTL:      a.TTL,
		Series:   a.Series,
		Render:   a.Render,
		Filename: filename,
		Password: a.Password,
	})
	if errors.Is(err, errPublishPathTooLarge) {
		return nil, &rpcError{Code: codeInvalidParams, Message: errPublishPathTooLarge.Error()}
	}
	if err != nil {
		return nil, upstreamError(err)
	}
	return textResult(res.URL), nil
}

// maxPublishPathBytes caps a file published by `path`; it matches the
// server's default upload cap. A var so tests can lower it.
var maxPublishPathBytes int64 = 100 << 20

var errPublishPathTooLarge = fmt.Errorf("path is larger than %d bytes", maxPublishPathBytes)

// cappedFile streams an opened, checked file into the upload, at most
// maxPublishPathBytes of it. A file that is (or grows) larger fails the read,
// and so the upload, instead of being truncated.
type cappedFile struct {
	f    *os.File  // the opened, checked file (closed by Close)
	r    io.Reader // what is read: f
	left int64
}

// Read passes the file through until the cap; there it probes for one more
// byte, and only a clean end of file ends the stream. More data, or any read
// error, fails it, so a growing or failing file is never uploaded truncated.
func (c *cappedFile) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var probe [1]byte
		n, err := c.r.Read(probe[:])
		switch {
		case n > 0:
			return 0, errPublishPathTooLarge
		case err == io.EOF:
			return 0, io.EOF
		case err != nil:
			return 0, err
		default:
			return 0, io.ErrNoProgress
		}
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func (c *cappedFile) Close() error {
	if c.f == nil {
		return nil
	}
	return c.f.Close()
}

// openPublishPath opens a model-supplied `path` for the publish tool and
// returns it as a capped stream: the bytes uploaded are the bytes checked,
// read from the checked descriptor as the request is sent, never held whole.
// Tool arguments are attacker-influenceable (a prompt-injected model chooses this
// value, unlike the human-chosen CLI path), so this read is a potential
// arbitrary-file-read-and-publish primitive. Every check below applies whether
// the token came from a file or from the environment:
//
//   - A client with no publish root refuses `path` outright.
//   - The given path must not itself be a symlink or special file (Lstat).
//   - The symlink-resolved path must lie under the publish root, and the file is
//     opened through the os.Root opened on that directory at startup, so neither
//     a component swapped for a symlink after the check nor a replaced root
//     directory can move the read outside it.
//   - The file is opened non-blocking, so a FIFO swapped in after the checks
//     cannot stall the request loop before the descriptor checks refuse it.
//   - The opened file must be a regular file that does not live on the /proc,
//     /sys or /dev filesystems (compared by device, so an alias path does not
//     help), which keeps the process environment and kernel state out.
//   - The opened file must not be the configured token file, compared by file
//     identity (device and inode) against both the file recorded at startup and
//     whatever the path names now, so a symlink or hardlink to it is refused
//     even after the original path is renamed or replaced.
//
// All identity checks run on the opened descriptor, so the bytes read are the
// bytes that were checked.
func openPublishPath(c *Client, p string) (*cappedFile, *rpcError) {
	refuse := func(format string, args ...any) *rpcError {
		return &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf(format, args...)}
	}
	if c.publishRoot == nil {
		return nil, refuse("publishing by `path` is disabled: no publish root is configured (use `content`, or start `demiplane mcp` with --publish-root)")
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, refuse("read path: %v", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, refuse("path must be a regular file (symlinks and special files are refused)")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, refuse("resolve path: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, refuse("resolve path: %v", err)
	}
	rel, err := filepath.Rel(c.publishRootPath, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, refuse("path is outside the publish root")
	}

	// Opened non-blocking: the file at rel can be swapped for a FIFO after the
	// checks above, and a blocking open would then wait for a writer forever.
	// The descriptor checks below refuse anything but a regular file, and
	// reads from a regular file never return EAGAIN, so the flag is harmless
	// for the read itself. os.Root already opens the final component with
	// O_NOFOLLOW and resolves any symlink inside the root.
	f, err := c.publishRoot.OpenFile(rel, os.O_RDONLY|openNoBlock, 0)
	if err != nil {
		return nil, refuse("read path: %v", err)
	}
	keep := false
	defer func() {
		if !keep {
			f.Close()
		}
	}()
	st, err := f.Stat()
	if err != nil {
		return nil, refuse("read path: %v", err)
	}
	if !st.Mode().IsRegular() {
		return nil, refuse("path must be a regular file (symlinks and special files are refused)")
	}
	if onPseudoFS(st) {
		return nil, refuse("refusing to publish a file from /proc, /sys or /dev")
	}
	if c.isTokenFile(st) {
		return nil, refuse("refusing to publish the bearer token file")
	}
	if st.Size() > maxPublishPathBytes {
		return nil, refuse("%s", errPublishPathTooLarge.Error())
	}
	keep = true
	return &cappedFile{f: f, r: f, left: maxPublishPathBytes}, nil
}

// isTokenFile reports whether fi is the token file recorded at startup or the
// file its path names now.
func (c *Client) isTokenFile(fi os.FileInfo) bool {
	if c.tokenID != nil && os.SameFile(fi, c.tokenID) {
		return true
	}
	if c.tokenFile != "" {
		if cur, err := os.Stat(c.tokenFile); err == nil && os.SameFile(fi, cur) {
			return true
		}
	}
	return false
}

// resolveDir returns dir as an absolute, symlink-free path.
func resolveDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func callList(ctx context.Context, c *Client, raw json.RawMessage) (*toolResult, *rpcError) {
	var a struct {
		After string `json:"after"`
	}
	if err := unmarshalArgs(raw, &a); err != nil {
		return nil, err
	}
	body, err := c.List(ctx, a.After)
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
