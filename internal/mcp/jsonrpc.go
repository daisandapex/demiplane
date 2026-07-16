// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package mcp implements a stdio Model Context Protocol server for demiplane.
// It speaks JSON-RPC 2.0 over stdin/stdout and is a THIN CLIENT of a running
// demiplane control plane: each tools/call is forwarded to the HTTP API
// (POST /publish, GET /list, DELETE /{slug}, GET /{slug}) and the result is
// returned as MCP content. It has no store or filesystem coupling beyond
// optionally reading a local file named by the publish `path` argument, and it
// is stdlib-only so it ships in the core build with no build tag.
//
// The transport is newline-delimited JSON: one JSON-RPC message per line, no
// embedded newlines (json.Encoder guarantees this). stdout is reserved for
// protocol traffic only — diagnostics go to stderr via the standard logger.
package mcp

import "encoding/json"

// JSON-RPC 2.0 error codes. The four reserved codes are per the spec; -32000 is
// the implementation-defined "server error" we use for a failed upstream HTTP
// call (the demiplane control plane returned non-2xx).
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	codeServerError    = -32000
)

// request is an incoming JSON-RPC 2.0 request or notification. A message with no
// id member is a notification and MUST NOT be answered.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification reports whether the message omits the id member (JSON-RPC
// notification — no response is emitted). An explicit `"id":null` is a request.
func (r *request) isNotification() bool { return len(r.ID) == 0 }

// response is an outgoing JSON-RPC 2.0 response. Exactly one of Result or Error
// is populated; the other is nil and omitted by the encoder.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is a JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return e.Message }

// resultResponse builds a success response echoing the request id.
func resultResponse(id json.RawMessage, result any) *response {
	return &response{JSONRPC: "2.0", ID: id, Result: result}
}

// errorResponse builds an error response echoing the request id.
func errorResponse(id json.RawMessage, err *rpcError) *response {
	return &response{JSONRPC: "2.0", ID: id, Error: err}
}
