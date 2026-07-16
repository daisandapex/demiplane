// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// passwordHeader mirrors server.PasswordHeader: the per-artifact view password
// travels in a header, never the URL query (query strings leak into logs). The
// MCP client duplicates the constant rather than importing the server package to
// keep the "thin HTTP client, no server coupling" boundary.
const passwordHeader = "X-Demiplane-Password"

// maxGetBytes bounds how much of an artifact body the `get` tool will read back
// into an MCP text block, so a large artifact cannot balloon the response.
const maxGetBytes = 1 << 20 // 1 MiB

// Client is a thin HTTP client of a running demiplane instance. ControlURL hosts
// the write/list/delete surface; ContentURL hosts artifact bodies (GET /{slug})
// which live on a separate origin under ADR 0003 (falls back to ControlURL for
// --unsafe-same-origin deployments). Token is the bearer credential; it is set
// on every request's Authorization header and is NEVER logged or echoed.
type Client struct {
	ControlURL string
	ContentURL string
	Token      string
	// TokenFile, when set, is the resolved absolute path of the file the bearer
	// token was read from. The publish tool refuses to read-and-publish this path
	// so a prompt-injected model cannot use `path` to exfiltrate the token file to
	// a public slug (invariant 4: the token is never rendered to a page).
	TokenFile string
	HTTP      *http.Client
}

// NewClient builds a Client with sane defaults. controlURL is required;
// contentURL falls back to controlURL when empty (same-origin deployments).
func NewClient(controlURL, contentURL, token string) *Client {
	controlURL = strings.TrimRight(controlURL, "/")
	contentURL = strings.TrimRight(contentURL, "/")
	if contentURL == "" {
		contentURL = controlURL
	}
	return &Client{
		ControlURL: controlURL,
		ContentURL: contentURL,
		Token:      token,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
			// The control plane answers publish/list/delete with 2xx/4xx, never a
			// 3xx. Refuse to follow redirects so a redirect response can never
			// forward the bearer token (or, on a token-less GET, the request) to a
			// different origin. Defense-in-depth; the stdlib already strips
			// Authorization across hosts, this closes the same-host downgrade too.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// httpError is a non-2xx response from the control plane, carrying the status
// and a bounded snippet of the server's error body (never the request, so no
// token or password can appear here).
type httpError struct {
	Status int
	Body   string
}

func (e *httpError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("demiplane returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("demiplane returned HTTP %d: %s", e.Status, e.Body)
}

// auth attaches the bearer token if one is configured. The token is only ever
// placed in the Authorization header of an outbound request.
func (c *Client) auth(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

// PublishParams are the validated inputs to a publish call.
type PublishParams struct {
	Content  []byte
	Slug     string
	Private  bool
	TTL      string
	Render   string
	Filename string
	Password string
}

// PublishResult is the control plane's JSON publish response.
type PublishResult struct {
	URL         string `json:"url"`
	Slug        string `json:"slug"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Private     bool   `json:"private"`
	Password    bool   `json:"password"`
	ExpiresAt   string `json:"expires_at"`
}

// Publish POSTs the content to /publish and returns the parsed JSON response.
// The password (if any) is sent via the X-Demiplane-Password header; it never
// enters the query string.
func (c *Client) Publish(ctx context.Context, p PublishParams) (*PublishResult, error) {
	q := url.Values{}
	if p.Slug != "" {
		q.Set("slug", p.Slug)
	}
	if p.Private {
		q.Set("private", "true")
	}
	if p.TTL != "" {
		q.Set("ttl", p.TTL)
	}
	if p.Render != "" {
		q.Set("render", p.Render)
	}
	if p.Filename != "" {
		q.Set("filename", p.Filename)
	}
	u := c.ControlURL + "/publish"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(p.Content))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if p.Password != "" {
		req.Header.Set(passwordHeader, p.Password)
	}
	c.auth(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, readHTTPError(resp)
	}
	var out PublishResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxGetBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode publish response: %w", err)
	}
	return &out, nil
}

// List GETs /list and returns the raw JSON body (already filtered to non-private
// artifacts by the server).
func (c *Client) List(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ControlURL+"/list", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	c.auth(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", readHTTPError(resp)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxGetBytes))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Delete DELETEs /{slug}. The slug is path-escaped so a caller-supplied value
// cannot inject extra path segments or query.
func (c *Client) Delete(ctx context.Context, slug string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.ControlURL+"/"+url.PathEscape(slug), nil)
	if err != nil {
		return err
	}
	c.auth(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return readHTTPError(resp)
	}
	return nil
}

// GetResult is the outcome of fetching an artifact body.
type GetResult struct {
	ContentType string
	Body        []byte
	Truncated   bool
}

// GetBySlug fetches an artifact body from the content origin by slug.
func (c *Client) GetBySlug(ctx context.Context, slug string) (*GetResult, error) {
	return c.get(ctx, c.ContentURL+"/"+url.PathEscape(slug))
}

// GetByURL fetches an artifact body from an explicit URL, after verifying the
// URL's host matches the configured control or content origin. This bounds the
// `get` tool so an LLM cannot coax it into fetching an arbitrary internal host
// (SSRF): only the demiplane instance this client is pointed at is reachable.
func (c *Client) GetByURL(ctx context.Context, raw string) (*GetResult, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("url scheme %q not allowed (http/https only)", u.Scheme)
	}
	if !c.originAllowed(u) {
		return nil, fmt.Errorf("url %q is not this demiplane instance (host:port must match the control or content origin)", u.Host)
	}
	return c.get(ctx, u.String())
}

// originAllowed reports whether target's host AND port match the control or
// content origin exactly. Comparing host:port — not hostname alone — is what
// bounds the get tool to THIS instance's own listeners: demiplane's default
// topology binds the control and content planes to the same loopback host on
// different ports (ADR 0003), so a hostname-only check would also admit every
// OTHER service on that host (a co-located admin panel, a socket-proxy, redis)
// — a same-host loopback port pivot. Both sides resolve an absent port to the
// scheme default (http 80 / https 443) so an implicit and an explicit-default
// port compare equal.
func (c *Client) originAllowed(target *url.URL) bool {
	th, tp := hostPort(target)
	if th == "" {
		return false
	}
	for _, base := range []string{c.ControlURL, c.ContentURL} {
		u, err := url.Parse(base)
		if err != nil {
			continue
		}
		if bh, bp := hostPort(u); bh != "" && bp == tp && strings.EqualFold(bh, th) {
			return true
		}
	}
	return false
}

// hostPort returns u's hostname and effective port, resolving an absent port to
// the scheme default so http://h and http://h:80 (or https://h and
// https://h:443) compare equal. A scheme without a known default and no explicit
// port yields an empty port, which still compares by equality.
func hostPort(u *url.URL) (host, port string) {
	host = u.Hostname()
	port = u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return host, port
}

func (c *Client) get(ctx context.Context, u string) (*GetResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// Artifact bodies are public (GET /{slug} is always open); no auth needed.
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGetBytes+1))
	if err != nil {
		return nil, err
	}
	res := &GetResult{ContentType: resp.Header.Get("Content-Type"), Body: body}
	if int64(len(body)) > maxGetBytes {
		res.Body = body[:maxGetBytes]
		res.Truncated = true
	}
	return res, nil
}

// readHTTPError reads a bounded snippet of an error response body and returns a
// typed httpError. Only the response is read — the request (which carries the
// bearer token / password) is never touched here.
func readHTTPError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return &httpError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
}

// validateTTL mirrors store.ParseTTL's accepted forms (Go durations plus a "d"
// day suffix) so a malformed ttl is rejected client-side before any HTTP call,
// per the "validate params before forwarding" requirement. It does not import
// the store package (thin-client boundary).
func validateTTL(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if rest, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.ParseFloat(rest, 64)
		if err != nil {
			return fmt.Errorf("invalid ttl %q", s)
		}
		if days <= 0 {
			return fmt.Errorf("ttl %q must be positive", s)
		}
		return nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid ttl %q", s)
	}
	if d <= 0 {
		return fmt.Errorf("ttl %q must be positive", s)
	}
	return nil
}

// isTextual reports whether a content type is safe to inline as an MCP text
// block. Binary types are summarized instead of dumped as mojibake.
func isTextual(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/json", "application/xml", "application/xhtml+xml",
		"image/svg+xml", "application/javascript", "application/yaml":
		return true
	}
	return strings.HasSuffix(ct, "+json") || strings.HasSuffix(ct, "+xml")
}
