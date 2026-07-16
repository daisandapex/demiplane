// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
)

// protocolVersion is the MCP revision this server implements. When a client
// requests a different version at initialize we echo the client's value (lenient
// interop) rather than hard-failing, but default to this when none is supplied.
const protocolVersion = "2025-06-18"

// serverName is the MCP serverInfo.name.
const serverName = "demiplane"

// initializeResult is the initialize response payload.
type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      map[string]any `json:"serverInfo"`
}

// Serve runs the JSON-RPC 2.0 stdio loop until in reaches EOF or ctx is done.
// It reads newline-delimited requests from in, dispatches them against the
// control-plane client c, and writes responses to out (one JSON object per
// line). Notifications produce no response. Nothing except protocol messages is
// ever written to out.
func Serve(ctx context.Context, in io.Reader, out io.Writer, c *Client, serverVersion string) error {
	br := bufio.NewReaderSize(in, 1<<20)
	enc := json.NewEncoder(out)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := readLine(br)
		if len(bytes.TrimSpace(line)) > 0 {
			if resp := dispatch(ctx, c, serverVersion, line); resp != nil {
				if werr := enc.Encode(resp); werr != nil {
					return werr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// dispatch parses one message and returns the response, or nil for a
// notification (no reply per JSON-RPC 2.0).
func dispatch(ctx context.Context, c *Client, serverVersion string, line []byte) *response {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		// A parse error has no id to echo (JSON-RPC uses null).
		return errorResponse(nil, &rpcError{Code: codeParseError, Message: "parse error"})
	}
	if req.JSONRPC != "2.0" {
		if req.isNotification() {
			return nil
		}
		return errorResponse(req.ID, &rpcError{Code: codeInvalidRequest, Message: "jsonrpc must be \"2.0\""})
	}

	switch req.Method {
	case "initialize":
		return resultResponse(req.ID, buildInitialize(serverVersion, req.Params))
	case "notifications/initialized", "initialized", "notifications/cancelled":
		return nil // client notifications — no reply
	case "ping":
		return resultResponse(req.ID, map[string]any{})
	case "tools/list":
		return resultResponse(req.ID, map[string]any{"tools": toolDefs()})
	case "tools/call":
		if req.isNotification() {
			return nil
		}
		return handleToolsCall(ctx, c, &req)
	default:
		if req.isNotification() {
			return nil // ignore unknown notifications
		}
		return errorResponse(req.ID, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method})
	}
}

// buildInitialize composes the initialize result, echoing the client's requested
// protocol version when present.
func buildInitialize(serverVersion string, params json.RawMessage) initializeResult {
	pv := protocolVersion
	if len(params) > 0 {
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
			pv = p.ProtocolVersion
		}
	}
	if serverVersion == "" {
		serverVersion = "dev"
	}
	return initializeResult{
		ProtocolVersion: pv,
		Capabilities:    map[string]any{"tools": map[string]any{}},
		ServerInfo:      map[string]any{"name": serverName, "version": serverVersion},
	}
}

// handleToolsCall parses the call params and dispatches to the tool.
func handleToolsCall(ctx context.Context, c *Client, req *request) *response {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(req.Params) == 0 {
		return errorResponse(req.ID, &rpcError{Code: codeInvalidParams, Message: "missing params"})
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResponse(req.ID, &rpcError{Code: codeInvalidParams, Message: "invalid params: " + err.Error()})
	}
	if p.Name == "" {
		return errorResponse(req.ID, &rpcError{Code: codeInvalidParams, Message: "tool name is required"})
	}
	result, rerr := callTool(ctx, c, p.Name, p.Arguments)
	if rerr != nil {
		return errorResponse(req.ID, rerr)
	}
	return resultResponse(req.ID, result)
}

// readLine reads a single newline-delimited message, transparently rejoining a
// line that spans the buffer. The returned slice excludes the newline. On EOF it
// returns any trailing partial line along with io.EOF.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		buf = append(buf, chunk...)
		if err != nil {
			return buf, err
		}
		if !isPrefix {
			return buf, nil
		}
	}
}
