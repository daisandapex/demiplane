// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only
//
// demiplane — self-hosted, internal-first static/HTML publishing.
// Copyright (C) 2026 Dais & Apex
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version.
//
// This program is distributed in the hope that it will be useful, but WITHOUT
// ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS
// FOR A PARTICULAR PURPOSE. See the GNU Affero General Public License for more
// details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

// Command demiplane is a self-hosted, internal-first static/HTML publishing
// service: POST a file, get a URL, fetch it back over your own network.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/daisandapex/demiplane/internal/module"
	"github.com/daisandapex/demiplane/internal/server"
	"github.com/daisandapex/demiplane/internal/store"
	"github.com/daisandapex/demiplane/internal/theme"
	"github.com/daisandapex/demiplane/internal/transport"
)

// moduleTLS is the native-TLS listener seam, installed by the build-tagged
// wiring file modules_tls.go (`-tags tls`) — the listener-level analogue of
// the liveView/publishSite nil-func hooks in internal/server. It receives the
// server (as the narrow module.Host, for ModuleDataDir) plus the listeners'
// bind hosts (for self-signed SANs) and returns the *tls.Config both planes
// serve with — nil when TLS is not enabled. A nil func (the default build)
// means plain HTTP always; core stays ignorant of certificates entirely.
var moduleTLS func(host module.Host, bindHosts []string) (*tls.Config, error)

const shutdownTimeout = 10 * time.Second

// defaultMaxUpload bounds publish/ingest size out of the box so a fresh install
// is not a trivial disk-fill target. Operators lift the cap with --max-upload=0
// (the explicit "unlimited" opt-out that preserves the streaming design) or set
// any other byte count. Shared by both `serve` and `receive`.
const defaultMaxUpload = 100 << 20 // 100 MiB

// version is the release baseline; a build stamps the exact commit via
// -ldflags "-X main.version=1.2-<short-sha>" (see the Dockerfile and the release
// runbook). A bare `go build` reports this baseline.
var version = "1.2.0"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("demiplane: ")

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "receive":
		if err := runReceive(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "mcp":
		if err := runMCP(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "publish":
		if err := runPublish(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "version", "--version", "-v":
		fmt.Println("demiplane", version)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `demiplane — internal-first publishing service

Usage:
  demiplane serve   --bind <addr> --store <dir> [--base-url <url>] [--token-file <path>]
                    [--theme <name>] [--css <file>] [--header] [--footer] [--footer-link <url>] [--meta-header]
  demiplane receive --store <dir> [--slug <name>|--untar] [--base-url <url>] [--ttl <dur>] [--private]
  demiplane mcp     [--url <control-url>] [--token-file <path>]
  demiplane publish <file> [--url <control-url>] [--token-file <path>] [--slug <name>] [--watch] [--open]
  demiplane version

Commands:
  serve     Run the HTTP server.
  receive   Ingest an artifact from stdin into the store (designed to run as an
            SSH forced command for pubkey-auth publish; --untar for directory sync).
  mcp       Run a stdio MCP server (JSON-RPC 2.0 over stdin/stdout) that exposes
            publish/list/delete/get as tools to any MCP-speaking harness (Claude
            Code, Cursor, Cline, Windsurf, Zed, Continue). Thin client of the
            control plane at --url (env DEMIPLANE_URL / DEMIPLANE_TOKEN).
  publish   Publish a file (or stdin) to a running instance's control plane and
            print the URL; --watch re-publishes on change to a stable slug.

Auth:
  HTTP: set --token-file or DEMIPLANE_TOKEN to require a bearer token on
        POST /publish, DELETE /{slug}, and GET /list. GET /{slug} is always open.
  SSH:  pin a forced command in authorized_keys (sshd does pubkey auth; flags
        bake into command=, SSH_ORIGINAL_COMMAND is ignored), e.g.
        restrict,command="demiplane receive --store /var/lib/demiplane --base-url https://host" <key>

Run "demiplane serve -h" / "demiplane receive -h" for flags.
`)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	// Internal-first default: bind to loopback only. The operator opts into
	// exposure by binding a LAN/mesh IP (e.g. a Netbird address) or 0.0.0.0.
	bind := fs.String("bind", "127.0.0.1:8080", "control-plane address (host:port) for /publish, /list, DELETE; defaults to loopback — set a LAN/mesh IP or 0.0.0.0 to expose")
	contentBind := fs.String("content-bind", "", "address for the separate artifact (content) origin that serves GET /{slug}; empty = host of --bind with port+1")
	contentBaseURL := fs.String("content-base-url", "", "advertised origin for artifact bodies (proxy/TLS deployments); empty = derived from --content-bind")
	unsafeSameOrigin := fs.Bool("unsafe-same-origin", false, "serve artifacts and the control plane on ONE origin (legacy; re-enables the stored-XSS footgun ADR 0003 closes)")
	dir := fs.String("store", "", "directory for content + metadata (required)")
	baseURL := fs.String("base-url", "", "advertised control-plane base URL; if empty, derived from request Host")
	tokenFile := fs.String("token-file", "", "file holding the bearer token for publish/delete/list; overrides DEMIPLANE_TOKEN")
	sweep := fs.Duration("sweep-interval", time.Minute, "how often to reap past-TTL artifacts")
	browse := fs.Bool("browse", false, "serve an HTML index of non-private artifacts at GET /")
	maxUpload := fs.Int64("max-upload", defaultMaxUpload, "max publish body size in bytes (default 100 MiB; 0 = unlimited)")
	writeTimeout := fs.Duration("write-timeout", 0, "HTTP write timeout (0 = none; keep 0 if serving large files)")
	idleTimeout := fs.Duration("idle-timeout", 120*time.Second, "HTTP idle (keep-alive) timeout")
	themeName := fs.String("theme", "", "theme for the whole instance (chrome + ?render=md pages): light (default), dark, or a pinned palette catppuccin|dracula|one-dark")
	cssFile := fs.String("css", "", "path to a custom stylesheet for ?render=md pages; replaces the built-in theme")
	footer := fs.Bool("footer", true, "render the 'Generated by demiplane' vanity footer on ?render=md pages")
	footerLink := fs.String("footer-link", repoURL, "vanity-footer link target on ?render=md pages")
	header := fs.Bool("header", true, "render the sticky title header (+ theme toggle) on ?render=md pages")
	metaHeader := fs.Bool("meta-header", true, "render a frontmatter-driven meta-header (localized date + fields) on ?render=md pages")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("--store is required")
	}
	if *themeName != "" && !theme.Valid(*themeName) {
		return fmt.Errorf("--theme %q: unknown theme (choose one of: %s)", *themeName, strings.Join(theme.Names, ", "))
	}

	// Config file (XDG): CLI flag > config file > built-in default. A malformed
	// file is a hard startup error; a missing file means all-defaults.
	setFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })
	cfgPath := configPath()
	fileCfg, err := loadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("config %s: %w", cfgPath, err)
	}
	chrome, err := resolveChrome(setFlags, fileCfg, *themeName, *footerLink, *footer, *header, *metaHeader)
	if err != nil {
		return fmt.Errorf("config %s: %w", cfgPath, err)
	}
	// Module-owned config (e.g. the reply module's reply_hook_* keys), registered
	// by build-tagged wiring files. A bad value is a hard startup error.
	if err := applyModuleConfig(fileCfg); err != nil {
		return fmt.Errorf("config %s: %w", cfgPath, err)
	}

	var renderCSS string
	if *cssFile != "" {
		b, err := os.ReadFile(*cssFile)
		if err != nil {
			return fmt.Errorf("read --css file: %w", err)
		}
		renderCSS = string(b)
	}

	token, err := resolveToken(*tokenFile)
	if err != nil {
		return err
	}
	if token == "" {
		log.Print("WARNING: no auth token configured (--token-file / DEMIPLANE_TOKEN) — " +
			"POST /publish, DELETE /{slug}, and GET /list are UNAUTHENTICATED")
	}
	if isWildcardBind(*bind) {
		log.Printf("WARNING: binding %s exposes demiplane on ALL interfaces — "+
			"ensure this host is only reachable over a trusted network/mesh%s", *bind,
			tokenAdvice(token))
	}

	// Origin isolation (ADR 0003): artifacts are served from a SEPARATE origin
	// from the control plane so hosted JS cannot drive /publish, /list, or DELETE.
	// The default is a second listener on the same host with port+1, which yields
	// a distinct origin on bare loopback with no DNS or certs. --unsafe-same-origin
	// collapses to one origin (legacy footgun).
	var cAddr, cPort string
	if !*unsafeSameOrigin {
		cAddr, cPort, err = contentAddr(*bind, *contentBind)
		if err != nil {
			return err
		}
		if cAddr == *bind {
			return fmt.Errorf("--content-bind %q must differ from --bind %q (or use --unsafe-same-origin)", cAddr, *bind)
		}
	}

	st, err := store.Open(*dir)
	if err != nil {
		return err
	}
	defer st.Close()

	srv := server.New(st, server.Config{
		BaseURL:          *baseURL,
		ContentBaseURL:   *contentBaseURL,
		ContentPort:      cPort,
		AuthToken:        token,
		Browse:           *browse,
		MaxUpload:        *maxUpload,
		RenderTheme:      chrome.theme,
		RenderCSS:        renderCSS,
		RenderHeader:     chrome.header,
		RenderFooter:     chrome.footer,
		RenderFooterLink: chrome.footerLink,
		RenderMetaHeader: chrome.metaHeader,
	})

	// Native TLS (optional module, `-tags tls`; ADR 0004). nil moduleTLS =
	// module not compiled in; nil tlsConf = compiled in but `tls = on` not set.
	// Either way the listeners below stay plain HTTP — TLS is strictly opt-in.
	var tlsConf *tls.Config
	if moduleTLS != nil {
		tlsConf, err = moduleTLS(srv, bindHostsOf(*bind, cAddr))
		if err != nil {
			return err
		}
	}

	// ReadHeaderTimeout caps slowloris-style header stalls. A whole-request
	// ReadTimeout is intentionally NOT set: demiplane streams arbitrarily large
	// uploads, and a global read deadline would kill legitimate slow transfers.
	// WriteTimeout is likewise off by default (large downloads); operators can opt
	// in via --write-timeout. See SECURITY.md.
	mkServer := func(addr string, h http.Handler) *http.Server {
		return &http.Server{
			Addr:              addr,
			Handler:           h,
			TLSConfig:         tlsConf, // nil = plain HTTP (the default)
			ReadHeaderTimeout: 15 * time.Second,
			WriteTimeout:      *writeTimeout,
			IdleTimeout:       *idleTimeout,
		}
	}

	var servers []*http.Server
	if *unsafeSameOrigin {
		log.Print("WARNING: --unsafe-same-origin serves artifacts and the control plane " +
			"on ONE origin — hosted HTML/JS can drive /publish, /list, and DELETE (stored-XSS footgun; see ADR 0003)")
		servers = append(servers, mkServer(*bind, srv.Handler()))
	} else {
		servers = append(servers,
			mkServer(*bind, srv.ControlHandler()),
			mkServer(cAddr, srv.ContentHandler()))
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go runSweeper(ctx, st, *sweep)

	printStartup(*bind, cAddr, *dir, *baseURL, *contentBaseURL, token, st, tlsConf != nil)

	errCh := make(chan error, 1)
	for _, s := range servers {
		s := s
		go func() {
			var err error
			if s.TLSConfig != nil {
				// Certificates come from TLSConfig (module-supplied); the
				// file arguments are unused.
				err = s.ListenAndServeTLS("", "")
			} else {
				err = s.ListenAndServe()
			}
			if err != nil && err != http.ErrServerClosed {
				errCh <- err
			}
		}()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Print("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		var firstErr error
		for _, s := range servers {
			if err := s.Shutdown(shutCtx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
}

// contentAddr resolves the address + port for the artifact (content) origin.
// An explicit --content-bind wins; otherwise the content listener reuses the
// control host with the next port (so bare loopback yields a distinct origin
// with no DNS).
func contentAddr(bind, explicit string) (addr, port string, err error) {
	if explicit != "" {
		_, p, perr := net.SplitHostPort(explicit)
		if perr != nil {
			return "", "", fmt.Errorf("--content-bind %q: %w", explicit, perr)
		}
		return explicit, p, nil
	}
	host, p, perr := net.SplitHostPort(bind)
	if perr != nil {
		return "", "", fmt.Errorf("--bind %q: %w", bind, perr)
	}
	pn, perr := strconv.Atoi(p)
	if perr != nil {
		return "", "", fmt.Errorf("--bind %q: port is not numeric (set --content-bind explicitly): %w", bind, perr)
	}
	cp := strconv.Itoa(pn + 1)
	return net.JoinHostPort(host, cp), cp, nil
}

// printStartup prints a friendly first-run orientation: where it's serving, the
// store + artifact count, auth state, the key URLs, and a copy-pasteable publish
// one-liner — so a brand-new installer is immediately oriented. contentBind is
// "" in --unsafe-same-origin mode (artifacts share the control origin); tlsOn
// flips the printed links to https (module-terminated TLS, ADR 0004).
func printStartup(bind, contentBind, dir, baseURL, contentBaseURL, token string, st *store.Store, tlsOn bool) {
	display := displayURL(baseURL, bind, tlsOn)

	auth := "OPEN (no token — anyone on this network can publish)"
	if token != "" {
		auth = "bearer token required for publish/list/delete"
	}
	count := -1
	if n, err := st.Count(store.DefaultOwner); err == nil {
		count = n
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n  demiplane %s — your private publishing plane.\n\n", version)
	fmt.Fprintf(&b, "    control   %s   (publish / list / delete)\n", display)
	if contentBind != "" {
		fmt.Fprintf(&b, "    content   %s   (artifacts — isolated origin)\n", displayURL(contentBaseURL, contentBind, tlsOn))
	}
	if count >= 0 {
		fmt.Fprintf(&b, "    store     %s  (%d artifact%s)\n", dir, count, plural(count))
	} else {
		fmt.Fprintf(&b, "    store     %s\n", dir)
	}
	fmt.Fprintf(&b, "    auth      %s\n", auth)
	fmt.Fprintf(&b, "    explore   %s/  ·  %s/docs  ·  %s/llms.txt  ·  %s/help\n", display, display, display, display)
	fmt.Fprintf(&b, "\n    publish your first page:\n      curl --data-binary @index.html %s/publish\n\n", display)
	// Write directly to stderr so the block is readable (not log-prefixed).
	fmt.Fprint(os.Stderr, b.String())
}

// displayURL builds a browser-clickable origin: prefer an explicit base URL,
// else swap a wildcard bind for loopback so the printed link resolves locally.
// tlsOn selects the https scheme for derived (non-explicit) URLs.
func displayURL(baseURL, bind string, tlsOn bool) string {
	if d := strings.TrimRight(baseURL, "/"); d != "" {
		return d
	}
	host := bind
	if h, p, err := net.SplitHostPort(bind); err == nil {
		if h == "" || h == "0.0.0.0" || h == "::" || net.ParseIP(h).IsUnspecified() {
			h = "127.0.0.1"
		}
		host = net.JoinHostPort(h, p)
	}
	scheme := "http"
	if tlsOn {
		scheme = "https"
	}
	return scheme + "://" + host
}

// bindHostsOf extracts the host part of each listener bind address (control,
// content) for the TLS module's self-signed SAN derivation. Wildcard and
// malformed hosts pass through as-is / are skipped — the module normalizes.
func bindHostsOf(binds ...string) []string {
	var hosts []string
	for _, b := range binds {
		if b == "" {
			continue
		}
		if h, _, err := net.SplitHostPort(b); err == nil {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// runSweeper periodically reaps past-TTL artifacts until ctx is cancelled. It
// runs an immediate sweep on start so a restart promptly clears anything that
// expired while the process was down.
func runSweeper(ctx context.Context, st *store.Store, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	sweep := func() {
		n, err := st.SweepExpired(time.Now())
		if err != nil {
			log.Printf("ttl sweep error: %v", err)
			return
		}
		if n > 0 {
			log.Printf("ttl sweep: reaped %d expired artifact(s)", n)
		}
	}
	sweep()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

// isWildcardBind reports whether addr binds all interfaces (0.0.0.0, ::, or a
// bare :port), as opposed to a specific loopback/LAN/mesh address.
func isWildcardBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return true
	}
	return net.ParseIP(host).IsUnspecified()
}

// tokenAdvice appends a nudge to set a token when none is configured.
func tokenAdvice(token string) string {
	if token == "" {
		return " and set --token-file / DEMIPLANE_TOKEN"
	}
	return ""
}

// runReceive ingests a single artifact (or a tar directory) from stdin into the
// store and prints the resulting URL(s). It is the SSH/pipe transport: intended
// to run as an OpenSSH forced command where sshd has already authenticated the
// publisher's public key. The view password, if any, comes from the
// DEMIPLANE_PASSWORD env var so it never lands in the process argument list.
// receiveFlags holds the parsed `receive` command flags.
type receiveFlags struct {
	dir       string
	baseURL   string
	slug      string
	filename  string
	private   bool
	ttl       string
	untar     bool
	maxUpload int64
}

// newReceiveFlagSet registers the `receive` flags onto a fresh FlagSet bound to
// the returned struct. Extracted from runReceive so the defaults — notably the
// bounded --max-upload — are unit-testable without opening a store or reading
// stdin.
func newReceiveFlagSet() (*flag.FlagSet, *receiveFlags) {
	fs := flag.NewFlagSet("receive", flag.ExitOnError)
	c := &receiveFlags{}
	fs.StringVar(&c.dir, "store", "", "directory for content + metadata (required)")
	fs.StringVar(&c.baseURL, "base-url", "", "advertised base URL for the printed link")
	fs.StringVar(&c.slug, "slug", "", "named slug (overwrites in place); empty generates one")
	fs.StringVar(&c.filename, "filename", "", "filename hint for content-type")
	fs.BoolVar(&c.private, "private", false, "mark private + mint a capability slug (single-file only)")
	fs.StringVar(&c.ttl, "ttl", "", "auto-expire after a duration, e.g. 30m, 2h, 7d")
	fs.BoolVar(&c.untar, "untar", false, "read a tar stream from stdin and publish each file (directory sync)")
	fs.Int64Var(&c.maxUpload, "max-upload", defaultMaxUpload, "cap total stdin bytes (default 100 MiB; 0 = unlimited)")
	return fs, c
}

func runReceive(args []string) error {
	fs, c := newReceiveFlagSet()
	if err := fs.Parse(args); err != nil {
		return err
	}
	if c.dir == "" {
		return fmt.Errorf("--store is required")
	}
	ttl, err := store.ParseTTL(c.ttl)
	if err != nil {
		return err
	}

	st, err := store.Open(c.dir)
	if err != nil {
		return err
	}
	defer st.Close()

	opts := transport.ReceiveOptions{
		PutOptions: store.PutOptions{
			Slug:     c.slug,
			Filename: c.filename,
			Private:  c.private,
			Password: os.Getenv("DEMIPLANE_PASSWORD"),
			TTL:      ttl,
		},
		BaseURL:   c.baseURL,
		Untar:     c.untar,
		MaxUpload: c.maxUpload,
	}
	return transport.Receive(st, opts, os.Stdin, os.Stdout)
}

// resolveToken returns the bearer token from --token-file (preferred) or the
// DEMIPLANE_TOKEN environment variable, or "" if neither is set. A token file
// is read whole and trimmed of surrounding whitespace/newlines.
func resolveToken(tokenFile string) (string, error) {
	if tokenFile != "" {
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("read token file: %w", err)
		}
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			return "", fmt.Errorf("token file %q is empty", tokenFile)
		}
		return tok, nil
	}
	return strings.TrimSpace(os.Getenv("DEMIPLANE_TOKEN")), nil
}
