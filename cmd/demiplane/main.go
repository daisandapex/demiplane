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
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/limit"
	"github.com/daisandapex/demiplane/v3/internal/logsafe"
	"github.com/daisandapex/demiplane/v3/internal/module"
	"github.com/daisandapex/demiplane/v3/internal/render"
	"github.com/daisandapex/demiplane/v3/internal/server"
	"github.com/daisandapex/demiplane/v3/internal/store"
	"github.com/daisandapex/demiplane/v3/internal/theme"
	"github.com/daisandapex/demiplane/v3/internal/transport"
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
var version = "2.0.0"

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
			log.Fatal(exitMessage(err))
		}
	case "receive":
		if err := runReceive(os.Args[2:]); err != nil {
			log.Fatal(exitMessage(err))
		}
	case "delete-site":
		if err := runDeleteSite(os.Args[2:]); err != nil && !errors.Is(err, flag.ErrHelp) {
			log.Fatal(exitMessage(err))
		}
	case "mcp":
		if err := runMCP(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "list":
		if err := runList(os.Args[2:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
			log.Fatal(err)
		}
	case "publish":
		if err := runPublish(os.Args[2:]); err != nil && !errors.Is(err, flag.ErrHelp) {
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

// exitMessage formats a fatal error from serve or receive, the commands that
// open the store. It goes through logsafe like every other log line: a failed
// private publish over SSH receive, for one, carries the generated capability
// slug inside the wrapped filesystem path. The client commands (publish, mcp)
// report their errors verbatim; their paths are the user's own files.
func exitMessage(err error) string { return logsafe.Err(err) }

func usage() {
	fmt.Fprint(os.Stderr, `demiplane — internal-first publishing service

Usage:
  demiplane serve   --bind <addr> --store <dir> [--base-url <url>] [--token-file <path>]
                    [--theme <name>] [--css <file>] [--header] [--footer] [--footer-link <url>] [--meta-header]
  demiplane receive --store <dir> [--slug <name>|--untar] [--base-url <url>] [--ttl <dur>] [--private]
  demiplane list [--url <control-url>] [--token-file <path>] [--all] [--json]
  demiplane delete-site [--url <control-url>] [--token-file <path>] <name>
  demiplane delete-site --store <dir> <name>
  demiplane mcp     [--url <control-url>] [--token-file <path>]
  demiplane publish <file> [--url <control-url>] [--token-file <path>] [--slug <name>] [--watch] [--open]
  demiplane version

Commands:
  serve     Run the HTTP server.
  receive   Ingest an artifact from stdin into the store (designed to run as an
            SSH forced command for pubkey-auth publish; --untar for directory sync).
  list      List public artifacts; --all includes private artifacts and sites.
  delete-site
            Delete remotely with --url or DEMIPLANE_URL, or offline with --store and the server stopped: its files,
            and any copy an interrupted replace left for recovery.
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
	unsafeNoSandbox := fs.Bool("unsafe-no-sandbox", false, "serve HTML/SVG/XML artifacts without the per-document sandbox, so any published page can read every other artifact a viewer can open (trusted content only)")
	dir := fs.String("store", "", "directory for content + metadata (required)")
	baseURL := fs.String("base-url", "", "advertised control-plane base URL; if empty, derived from request Host")
	allowedHosts := fs.String("allowed-hosts", "", "comma-separated Host values to serve (host or host:port); others get 421. The --base-url and --content-base-url hosts are always included. Empty = any Host")
	trustedProxy := fs.String("trusted-proxy", "", "comma-separated IPs or CIDRs of reverse proxies whose X-Forwarded-For names the client for per-client limits; empty = the TCP peer is the client")
	tokenFile := fs.String("token-file", "", "file holding the bearer token for publish/delete/list; overrides DEMIPLANE_TOKEN")
	sweep := fs.Duration("sweep-interval", time.Minute, "how often to reap past-TTL artifacts")
	browse := fs.Bool("browse", false, "serve an HTML index of non-private artifacts at GET /")
	maxUpload := fs.Int64("max-upload", defaultMaxUpload, "max publish body size in bytes (default 100 MiB; 0 = unlimited)")
	storeQuota := fs.Int64("store-quota", 0, storeQuotaUsage)
	writeTimeout := fs.Duration("write-timeout", 0, "whole-response HTTP write timeout (0 = none); it also ends live-reload streams, so prefer --response-timeout")
	bodyTimeout := fs.Duration("body-timeout", 30*time.Second, "time a request may take to send its body; for uploads, the grace before --upload-min-rate applies and the longest allowed stall (0 = no limit)")
	uploadMinRate := fs.Int64("upload-min-rate", 16<<10, "minimum average upload rate in bytes/s after --body-timeout (0 = no rate floor)")
	responseTimeout := fs.Duration("response-timeout", 60*time.Second, "write deadline for each response, plus size / --download-min-rate for artifact bodies; live-reload streams are exempt (0 = none)")
	downloadMinRate := fs.Int64("download-min-rate", 16<<10, "bytes/s an artifact download must average after --response-timeout; each write must also go out within --response-timeout (0 = whole body due within --response-timeout)")
	maxConns := fs.Int("max-conns", 1024, "open connections per listener; further connections wait (0 = unlimited)")
	maxConnsPerClient := fs.Int("max-conns-per-client", 64, "open connections per client address per listener; further ones are closed (0 = unlimited)")
	maxRequests := fs.Int("max-requests", 256, "requests in progress per listener; the excess gets 503 (0 = unlimited)")
	maxRequestsPerClient := fs.Int("max-requests-per-client", 32, "requests in progress per client address per listener; the excess gets 503 (0 = unlimited)")
	idleTimeout := fs.Duration("idle-timeout", 120*time.Second, "HTTP idle (keep-alive) timeout")
	themeName := fs.String("theme", "", "theme for the whole instance (chrome + ?render=md pages): light (default), dark, or a pinned palette catppuccin|dracula|one-dark")
	cssFile := fs.String("css", "", "path to a custom stylesheet for ?render=md pages; replaces the built-in theme")
	footer := fs.Bool("footer", true, "render the 'Generated by demiplane' vanity footer on ?render=md pages")
	footerLink := fs.String("footer-link", repoURL, "vanity-footer link target on ?render=md pages")
	header := fs.Bool("header", true, "render the sticky title header (+ theme toggle) on ?render=md pages")
	metaHeader := fs.Bool("meta-header", true, "render a frontmatter-driven meta-header (localized date + fields) on ?render=md pages")
	renderMaxOutput := fs.Int("render-max-output", render.DefaultLimits.MaxOutputBytes, "max bytes of one rendered ?render=md page; a larger render is refused with 413")
	passwordConcurrency := fs.Int("password-concurrency", 0, "view-password KDF runs at once; more wait up to 5s, then 503 (0 = half the cores, 1 to 4)")
	passwordRateClientTarget := fs.String("password-rate-client-target", "10:10", "failed view-password attempts a minute by one client on one artifact, N or N:BURST; past it 429 without hashing (0 = off)")
	passwordRateClient := fs.String("password-rate-client", "20:10", "failed view-password attempts a minute by one client on any artifact, N or N:BURST (0 = off)")
	passwordRateTarget := fs.String("password-rate-target", "10:10", "failed view-password attempts a minute by all clients on one artifact; once spent, clients that failed recently there get 429 (0 = off)")
	passwordRateGlobal := fs.String("password-rate-global", "120:30", "failed view-password attempts a minute in all; once spent, clients that failed recently get 429 (0 = off)")
	liveMaxPerSlug := fs.Int("live-max-per-slug", 16, "live-reload streams watching one artifact; when full, a new client replaces the oldest stream of a client holding at least two more, else gets 503")
	liveMaxPerClient := fs.Int("live-max-per-client", 16, "live-reload streams from one client on all artifacts; past it 503")
	liveMaxPerClientSlug := fs.Int("live-max-per-client-slug", 2, "live-reload streams from one client on one artifact; past it 503")
	liveMaxTotal := fs.Int("live-max-total", 256, "live-reload streams in all; when full, a new client replaces the oldest stream of a client holding at least two more, else gets 503")
	liveMaxPending := fs.Int("live-max-pending", 32, "live-reload requests waiting, in all, for an ended stream's slot (up to 11s each); past it 503")
	liveMaxPendingPerClient := fs.Int("live-max-pending-per-client", 1, "live-reload requests waiting from one client for an ended stream's slot; past it 503")
	renderConcurrency := fs.Int("render-concurrency", 0, "max markdown renders at once (0 = GOMAXPROCS, capped at 4), each holding its document and page in memory until stored; a render that waits 5s for a slot gets 503")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("--store is required")
	}
	hosts, err := server.ParseHostList(*allowedHosts)
	if err != nil {
		return err
	}
	proxies, err := limit.ParseProxies(*trustedProxy)
	if err != nil {
		return err
	}
	if *liveMaxPerSlug < 1 || *liveMaxPerClient < 1 || *liveMaxPerClientSlug < 1 || *liveMaxTotal < 1 ||
		*liveMaxPending < 1 || *liveMaxPendingPerClient < 1 {
		return fmt.Errorf("--live-max-per-slug, --live-max-per-client, --live-max-per-client-slug, --live-max-total, --live-max-pending and --live-max-pending-per-client must be at least 1")
	}
	if *passwordConcurrency < 0 {
		return fmt.Errorf("--password-concurrency must not be negative")
	}
	pwLimits := server.PasswordLimits{Concurrency: *passwordConcurrency}
	for _, f := range []struct {
		name string
		v    string
		dst  *limit.Rate
	}{
		{"password-rate-client-target", *passwordRateClientTarget, &pwLimits.PerClientTarget},
		{"password-rate-client", *passwordRateClient, &pwLimits.PerClient},
		{"password-rate-target", *passwordRateTarget, &pwLimits.PerTarget},
		{"password-rate-global", *passwordRateGlobal, &pwLimits.Global},
	} {
		if *f.dst, err = parseRateFlag(f.name, f.v); err != nil {
			return err
		}
	}
	if *renderMaxOutput <= 0 {
		return fmt.Errorf("--render-max-output must be positive")
	}
	if *renderConcurrency < 0 {
		return fmt.Errorf("--render-concurrency must not be negative")
	}
	if *storeQuota < 0 {
		return fmt.Errorf("--store-quota must be >= 0 (0 = unlimited)")
	}
	if *bodyTimeout < 0 || *uploadMinRate < 0 || *responseTimeout < 0 || *downloadMinRate < 0 ||
		*maxConns < 0 || *maxConnsPerClient < 0 || *maxRequests < 0 || *maxRequestsPerClient < 0 {
		return fmt.Errorf("--body-timeout, --upload-min-rate, --response-timeout, --download-min-rate, " +
			"--max-conns, --max-conns-per-client, --max-requests and --max-requests-per-client must not be negative")
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

	// One server per store: it alone recovers, scavenges and writes sites.
	unlockServe, err := store.LockServe(*dir)
	if err != nil {
		return err
	}
	defer unlockServe()
	st, err := store.Open(*dir)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := applyStoreQuota(st, *storeQuota); err != nil {
		return err
	}

	srv := server.New(st, server.Config{
		BaseURL:           *baseURL,
		ContentBaseURL:    *contentBaseURL,
		ContentPort:       cPort,
		AllowedHosts:      hosts,
		TrustedProxies:    proxies,
		AuthToken:         token,
		Browse:            *browse,
		MaxUpload:         *maxUpload,
		RenderTheme:       chrome.theme,
		RenderCSS:         renderCSS,
		RenderHeader:      chrome.header,
		RenderFooter:      chrome.footer,
		RenderFooterLink:  chrome.footerLink,
		RenderMetaHeader:  chrome.metaHeader,
		UnsafeNoSandbox:   *unsafeNoSandbox,
		RenderMaxOutput:   *renderMaxOutput,
		RenderConcurrency: *renderConcurrency,
		Password:          pwLimits,
		Live: server.LiveLimits{
			PerSlug:       *liveMaxPerSlug,
			PerClient:     *liveMaxPerClient,
			PerClientSlug: *liveMaxPerClientSlug,
			Total:         *liveMaxTotal,

			Pending:          *liveMaxPending,
			PendingPerClient: *liveMaxPendingPerClient,
		},
		Requests: server.RequestLimits{
			BodyTimeout:          offIfZero(*bodyTimeout),
			UploadMinRate:        offIfZero(*uploadMinRate),
			ResponseTimeout:      offIfZero(*responseTimeout),
			DownloadMinRate:      offIfZero(*downloadMinRate),
			MaxRequests:          offIfZero(*maxRequests),
			MaxRequestsPerClient: offIfZero(*maxRequestsPerClient),
		},
	})
	if *unsafeNoSandbox {
		log.Print("WARNING: --unsafe-no-sandbox serves published pages in one shared origin: " +
			"any page can read every other artifact a viewer can open, using the viewer's credentials")
	}

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
	// Bodies and responses get per-route deadlines instead (--body-timeout,
	// --upload-min-rate, --response-timeout, --download-min-rate; see
	// internal/server/limits.go), which leave the live-reload stream alone.
	// WriteTimeout stays off by default; --write-timeout opts in. See SECURITY.md.
	mkServer := func(addr string, h http.Handler) *http.Server {
		return newHTTPServer(addr, h, tlsConf, *writeTimeout, *idleTimeout)
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
			ln, err := net.Listen("tcp", s.Addr)
			if err != nil {
				errCh <- err
				return
			}
			ln = limit.Listener(ln, *maxConns, *maxConnsPerClient)
			if s.TLSConfig != nil {
				// Certificates come from TLSConfig (module-supplied); the
				// file arguments are unused.
				err = s.ServeTLS(ln, "", "")
			} else {
				err = s.Serve(ln)
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

// offIfZero maps a flag's 0 ("no limit") to the negative value server
// limits read as disabled; their own zero means "default".
func offIfZero[T int | int64 | time.Duration](v T) T {
	if v == 0 {
		return -1
	}
	return v
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
	sites, siteErr := st.ListSites()
	if n, err := st.Count(store.DefaultOwner); err == nil && siteErr == nil {
		count = n
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n  demiplane %s — your private publishing plane.\n\n", version)
	fmt.Fprintf(&b, "    control   %s   (publish / list / delete)\n", display)
	if contentBind != "" {
		fmt.Fprintf(&b, "    content   %s   (artifacts — isolated origin)\n", displayURL(contentBaseURL, contentBind, tlsOn))
	}
	if count >= 0 {
		fmt.Fprintf(&b, "    store     %s  (%d unexpired artifact%s including private, %d site%s; %d owner entries)\n", dir, count, plural(count), len(sites), plural(len(sites)), count+len(sites))
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

// reconcileInterval is how often the sweeper also scans the store for
// orphaned files (store.Reconcile). Pending deletions are retried on every
// sweep; the directory scan is the slower, rarer backstop.
const reconcileInterval = time.Hour

// runSweeper periodically reaps past-TTL artifacts until ctx is cancelled. It
// runs an immediate sweep and reconcile on start so a restart promptly clears
// anything that expired, or failed to unlink, while the process was down.
func runSweeper(ctx context.Context, st *store.Store, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	sweep := func() {
		n, err := st.SweepExpired(time.Now())
		if err != nil {
			log.Printf("ttl sweep error: %s", logsafe.Err(err))
		} else if n > 0 {
			log.Printf("ttl sweep: reaped %d expired artifact(s)", n)
		}
		// Pick up bytes another process (SSH receive) committed to the same
		// store. A no-op without --store-quota.
		if err := st.RecountUsage(); err != nil {
			log.Printf("store quota recount error: %s", logsafe.Err(err))
		}
	}
	reconcile := func() {
		n, err := st.Reconcile(time.Now())
		if err != nil {
			log.Printf("store reconcile error: %s", logsafe.Err(err))
			return
		}
		if n > 0 {
			log.Printf("store reconcile: removed %d orphaned file(s)", n)
		}
	}
	sweep()
	reconcile()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastReconcile := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
			if time.Since(lastReconcile) >= reconcileInterval {
				reconcile()
				lastReconcile = time.Now()
			}
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

// storeQuotaUsage documents serve's --store-quota.
const storeQuotaUsage = "aggregate store size cap in bytes across artifacts, retained sources and sites, including in-flight uploads, enforced on this server's writes (0 = unlimited, the default)"

// applyStoreQuota sets the aggregate store quota and logs the measured usage.
func applyStoreQuota(st *store.Store, limit int64) error {
	if limit <= 0 {
		return nil
	}
	if err := st.SetQuota(limit); err != nil {
		return err
	}
	used, _ := st.Usage()
	log.Printf("store quota: %d of %d bytes in use", used, limit)
	return nil
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

	// Shared with the running server: it removes what this replaces (it
	// knows which old generations its downloads still hold), and sites/ is
	// its alone, so this process never recovers or scavenges site trees.
	st, err := store.OpenShared(c.dir)
	if err != nil {
		return err
	}
	defer st.Close()
	// No --store-quota here: a separate process cannot see what the server
	// holds (open downloads of removed files) or what other receives are
	// writing, so it could not keep the quota. The server enforces it on its
	// own writes and counts these at its next recount (SECURITY.md).

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
	tok := strings.TrimSpace(os.Getenv("DEMIPLANE_TOKEN"))
	// Out of the environment once read, so nothing the server starts (a
	// reply hook, its children) inherits it.
	os.Unsetenv("DEMIPLANE_TOKEN")
	return tok, nil
}

// runDeleteSite deletes remotely with a supplied URL, or offline with --store. Offline
// deletion takes the serve lock, so it refuses while a server holds the store, and
// deletes through the store, so an interrupted replace's copy goes too
// (removing the directory by hand would leave that copy for recovery to
// restore at the next start).
func runDeleteSite(args []string) error {
	fs := flag.NewFlagSet("delete-site", flag.ContinueOnError)
	dir := fs.String("store", "", "offline store directory (server must be stopped)")
	remoteURL := fs.String("url", "", "remote control-plane URL; incompatible with --store")
	tokenFile := fs.String("token-file", "", "remote bearer token file; overrides DEMIPLANE_TOKEN")
	if err := parseClientFlags(fs, args); err != nil {
		return err
	}
	resolveURLFlag(fs, remoteURL)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: demiplane delete-site [--url URL | --store DIR] [--token-file FILE] <name>")
	}
	offline, remoteFlags := false, false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "store" {
			offline = true
		}
		if f.Name == "url" || f.Name == "token-file" {
			remoteFlags = true
		}
	})
	conflict := offline && remoteFlags
	if conflict {
		return fmt.Errorf("--store cannot be combined with --url or --token-file")
	}
	if !offline {
		if strings.TrimSpace(*remoteURL) == "" {
			return fmt.Errorf("usage: demiplane delete-site requires --store or --url (or DEMIPLANE_URL)")
		}
		return deleteRemoteSite(*remoteURL, *tokenFile, fs.Arg(0))
	}
	if *dir == "" {
		return fmt.Errorf("--store requires a nonempty directory")
	}

	name := fs.Arg(0)
	unlock, err := store.LockServe(*dir)
	if errors.Is(err, store.ErrStoreServed) {
		return fmt.Errorf("delete-site: stop the server first: %w", err)
	}
	if err != nil {
		return err
	}
	defer unlock()
	st, err := store.Open(*dir)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.DeleteSite(name); err != nil {
		return fmt.Errorf("delete-site: %w", err)
	}
	fmt.Printf("deleted site %s\n", name)
	return nil
}

// newHTTPServer builds a listener's server. It speaks HTTP/1.1 only, TLS or
// not: request admission is released when a handler returns, and HTTP/2 can
// keep a finished response's stream (and its buffers) waiting on flow
// control past that, many streams per connection, so the admission and
// connection caps would not bound what a client holds. A reverse proxy can
// offer HTTP/2 to clients.
func newHTTPServer(addr string, h http.Handler, tlsConf *tls.Config, writeTimeout, idleTimeout time.Duration) *http.Server {
	if tlsConf != nil {
		// A config that offers h2 itself (autocert's does) must not
		// negotiate it either.
		tlsConf = tlsConf.Clone()
		tlsConf.NextProtos = slices.DeleteFunc(slices.Clone(tlsConf.NextProtos), func(p string) bool { return p == "h2" })
	}
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		TLSConfig:         tlsConf, // nil = plain HTTP (the default)
		ReadHeaderTimeout: 15 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		// Non-nil and empty: ServeTLS does not add HTTP/2.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
}
