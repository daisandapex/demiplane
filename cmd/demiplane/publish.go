// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// passwordHeader mirrors internal/server.PasswordHeader. The view password
// travels in this header, never the URL query (logged surface) and never argv
// (process list). It is sourced only from the DEMIPLANE_PASSWORD environment
// variable — the same model as `demiplane receive`.
const passwordHeader = "X-Demiplane-Password"

// defaultControlURL matches `serve`'s default control-plane bind, so a local
// zero-config install can `demiplane publish index.html` with no flags.
const defaultControlURL = "http://127.0.0.1:8080"

// watchInterval bounds the --watch republish rate: the file mtime is polled at
// this cadence (stdlib-only, no fsnotify dependency), so a busy editor cannot
// drive more than ~2 republishes/sec.
const watchInterval = 500 * time.Millisecond

// runPublish is the client CLI: a thin HTTP client that POSTs a file (or stdin)
// to a running instance's control plane, prints the returned URL, and best-effort
// copies it to the clipboard. --watch polls the file mtime and re-publishes to a
// stable slug (so an SSE-live tab auto-updates); --open launches a browser. It is
// stdlib-only and ships in the core build with no tag.
//
// Token discipline: the bearer token comes from --token-file / DEMIPLANE_TOKEN
// (via resolveToken) and is sent as an Authorization header — never argv, never
// printed. The view password comes from DEMIPLANE_PASSWORD only.
func runPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	var (
		urlFlag   = fs.String("url", os.Getenv("DEMIPLANE_URL"), "control-plane base URL of the target instance (env DEMIPLANE_URL; default "+defaultControlURL+")")
		tokenFile = fs.String("token-file", "", "file holding the bearer token; overrides DEMIPLANE_TOKEN")
		slug      = fs.String("slug", "", "named slug (overwrites in place); empty lets the server generate one")
		private   = fs.Bool("private", false, "mint an unguessable capability URL instead of a public slug")
		ttl       = fs.String("ttl", "", "auto-expire after a duration, e.g. 30m, 2h, 7d")
		render    = fs.String("render", "", "render the body from markdown to HTML (\"md\")")
		filename  = fs.String("filename", "", "filename hint for content-type; defaults to the input file's basename")
		watch     = fs.Bool("watch", false, "re-publish to a stable slug whenever the file changes (edit-save-see loop)")
		open      = fs.Bool("open", false, "open the resulting URL in a browser (xdg-open/open)")
	)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `demiplane publish — publish a file (or stdin) to a running instance

Usage:
  demiplane publish [flags] <file>
  demiplane publish [flags] -          # read from stdin
  cat page.html | demiplane publish    # read from stdin

The view password, when the artifact needs one, is read from the
DEMIPLANE_PASSWORD environment variable (never a flag — argv is world-readable).

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	base, err := resolveBaseURL(*urlFlag)
	if err != nil {
		return err
	}
	if err := validateRender(*render); err != nil {
		return err
	}
	if *private && *slug != "" {
		return fmt.Errorf("--private cannot be combined with --slug: a private artifact is an unguessable capability URL, not a named one")
	}

	token, err := resolveToken(*tokenFile)
	if err != nil {
		return err
	}

	path := inputPath(fs.Args())
	name := *filename
	if name == "" && path != "" {
		name = filepath.Base(path)
	}

	p := &publisher{
		client:   noRedirectClient(),
		base:     base,
		token:    token,
		password: os.Getenv("DEMIPLANE_PASSWORD"),
		slug:     *slug,
		private:  *private,
		ttl:      *ttl,
		render:   *render,
		filename: name,
	}

	ctx := context.Background()

	if *watch {
		return runWatch(ctx, p, path, *open)
	}

	// One-shot publish: a file argument (re-openable) or stdin.
	var body io.Reader = os.Stdin
	if path != "" {
		f, ferr := os.Open(path)
		if ferr != nil {
			return ferr
		}
		defer f.Close()
		body = f
	}
	res, err := p.publish(ctx, body)
	if err != nil {
		return err
	}
	fmt.Println(res.URL)
	copyClipboard(res.URL)
	if *open {
		openBrowser(res.URL)
	}
	return nil
}

// inputPath returns the file path to publish, or "" for stdin. A missing
// positional argument or a bare "-" both mean stdin.
func inputPath(args []string) string {
	if len(args) == 0 || args[0] == "-" {
		return ""
	}
	return args[0]
}

// resolveBaseURL normalizes and validates the control-plane base URL, applying
// the local default when neither the flag nor DEMIPLANE_URL is set.
func resolveBaseURL(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		v = defaultControlURL
	}
	v = strings.TrimRight(v, "/")
	u, err := url.Parse(v)
	if err != nil {
		return "", fmt.Errorf("--url %q: %w", v, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("--url %q: must be an http or https URL", v)
	}
	if u.Host == "" {
		return "", fmt.Errorf("--url %q: missing host", v)
	}
	return v, nil
}

// validateRender rejects render modes the server would silently ignore, turning
// a typo into a clear client-side error instead of a raw (unrendered) upload.
func validateRender(v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "md", "markdown":
		return nil
	default:
		return fmt.Errorf("--render %q: only \"md\" is supported", v)
	}
}

// noRedirectClient returns an HTTP client that never follows redirects. The
// publish endpoint answers 201/4xx and never legitimately redirects, so a 3xx is
// treated as the final response. This matters for token discipline: Go strips the
// Authorization header on a cross-host redirect but NOT the custom
// X-Demiplane-Password header, so following a redirect to an attacker-controlled
// host would replay the view password (and, for 307/308, the body). Refusing
// redirects closes that leak.
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// publishResult is the subset of the server's JSON publish response the CLI uses.
type publishResult struct {
	URL  string `json:"url"`
	Slug string `json:"slug"`
}

// publisher is a thin HTTP client of the control-plane POST /publish endpoint.
// It carries the per-publish parameters so a --watch loop can re-issue the same
// request against a re-opened file.
type publisher struct {
	client   *http.Client
	base     string // no trailing slash
	token    string
	password string
	slug     string
	private  bool
	ttl      string
	render   string
	filename string
}

// endpoint builds the POST /publish URL with the current parameters. The
// password is NEVER placed in the query (the server 400s that); it rides the
// X-Demiplane-Password header instead.
func (p *publisher) endpoint() string {
	q := url.Values{}
	if p.slug != "" {
		q.Set("slug", p.slug)
	}
	if p.private {
		q.Set("private", "true")
	}
	if p.ttl != "" {
		q.Set("ttl", p.ttl)
	}
	if p.render != "" {
		q.Set("render", p.render)
	}
	if p.filename != "" {
		q.Set("filename", p.filename)
	}
	u := p.base + "/publish"
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

// publish POSTs body to the control plane and returns the parsed result. body is
// streamed, so large uploads never buffer in memory.
func (p *publisher) publish(ctx context.Context, body io.Reader) (publishResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), body)
	if err != nil {
		return publishResult{}, err
	}
	req.Header.Set("Accept", "application/json")
	// Raw body: octet-stream keeps the server on the raw-upload path (never the
	// multipart branch); the stored content-type is derived from the filename hint.
	req.Header.Set("Content-Type", "application/octet-stream")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	if p.password != "" {
		req.Header.Set(passwordHeader, p.password)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return publishResult{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode != http.StatusCreated {
		return publishResult{}, httpError(resp.StatusCode, data)
	}
	var res publishResult
	if err := json.Unmarshal(data, &res); err != nil || res.URL == "" {
		return publishResult{}, fmt.Errorf("publish: unexpected response from %s (status %d)", p.base, resp.StatusCode)
	}
	return res, nil
}

// publishFile opens path fresh and publishes it. Re-opening per call is what lets
// --watch re-stream a changed file without holding a descriptor open.
func (p *publisher) publishFile(ctx context.Context, path string) (publishResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return publishResult{}, err
	}
	defer f.Close()
	return p.publish(ctx, f)
}

// httpError maps a non-201 publish response to a helpful, token-free error. The
// server's plain-text error body is trimmed to its first line so a stray HTML
// error page cannot flood the terminal.
func httpError(code int, body []byte) error {
	msg := firstLine(string(body))
	switch code {
	case http.StatusUnauthorized:
		return fmt.Errorf("publish: unauthorized (401) — set --token-file or DEMIPLANE_TOKEN")
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("publish: upload too large (413) — the server's --max-upload rejected it")
	case http.StatusBadRequest:
		return fmt.Errorf("publish: bad request (400): %s", msg)
	default:
		if msg == "" {
			return fmt.Errorf("publish: server returned status %d", code)
		}
		return fmt.Errorf("publish: server returned status %d: %s", code, msg)
	}
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// runWatch publishes path once, then re-publishes to the SAME slug on every
// mtime change so a browser tab opened on <content>/<slug>?live keeps updating.
// It requires a real file (stdin cannot be polled) and a non-private target
// (each private publish is a fresh capability URL, which defeats a stable live
// view). It runs until the process is interrupted.
func runWatch(ctx context.Context, p *publisher, path string, open bool) error {
	if path == "" {
		return fmt.Errorf("--watch requires a file argument (stdin cannot be polled)")
	}
	if p.private {
		return fmt.Errorf("--watch cannot be combined with --private: each private publish mints a new capability URL, so a live view would never stabilize; use --slug for a stable public live view")
	}

	res, err := p.publishFile(ctx, path)
	if err != nil {
		return err
	}
	fmt.Println(res.URL)
	copyClipboard(res.URL)
	if open {
		openBrowser(res.URL)
	}
	// Pin subsequent republishes to the slug the server assigned, so the live
	// tab's URL never changes across edits.
	p.slug = res.Slug

	fmt.Fprintf(os.Stderr, "demiplane publish: watching %s (Ctrl-C to stop)\n", path)
	return watchLoop(ctx, path, watchInterval, func() {
		res, err := p.publishFile(ctx, path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "demiplane publish: republish failed: %v\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "demiplane publish: republished %s\n", res.URL)
	})
}

// watchLoop polls path's mtime every interval and calls onChange when it moves.
// The interval is the sole rate limit on republishes. It returns when ctx is
// cancelled. Extracted from runWatch so tests can drive it with a fast interval
// and a cancellable context.
func watchLoop(ctx context.Context, path string, interval time.Duration, onChange func()) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	last := info.ModTime()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			info, err := os.Stat(path)
			if err != nil {
				// A transient stat error (e.g. an editor's atomic rename in
				// flight) is not fatal; try again on the next tick.
				continue
			}
			if m := info.ModTime(); !m.Equal(last) {
				last = m
				onChange()
			}
		}
	}
}

// clipboardCandidates is the ordered set of clipboard tools tried by
// copyClipboard. It is a package var so tests can substitute a fake.
var clipboardCandidates = [][]string{
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
	{"pbcopy"},
	{"clip.exe"},
}

// copyClipboard best-effort copies s to the system clipboard using whichever
// tool is on PATH. It is a silent no-op when none is found or the tool fails —
// clipboard support must never break a publish.
func copyClipboard(s string) {
	for _, c := range clipboardCandidates {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(s)
		_ = cmd.Run()
		return
	}
}

// browserCandidates is the ordered set of URL openers tried by openBrowser.
var browserCandidates = []string{"xdg-open", "open"}

// openBrowser best-effort launches the system browser on u. Silent no-op when no
// opener is available.
func openBrowser(u string) {
	for _, bin := range browserCandidates {
		if path, err := exec.LookPath(bin); err == nil {
			_ = exec.Command(path, u).Start()
			return
		}
	}
}
