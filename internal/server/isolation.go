// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"mime"
	"net/http"
	"strings"
)

// isolation.go keeps one published artifact from reaching another through the
// viewer's browser. Every artifact shares the content origin, so without this
// a hostile page could fetch any other artifact the viewer can open (with the
// viewer's cached HTTP Basic credentials), reach into a framed sibling, read
// localStorage written by another page, or register a root-scoped service
// worker that intercepts every later visit.
//
// The fix is a per-document CSP sandbox without allow-same-origin: each
// HTML/SVG/XML document runs in its own opaque origin, so to the browser every
// other URL on the instance, including its own neighbours, is cross-origin.
// Same-origin reads, storage and service workers all depend on a real origin,
// so they stop working. Classic subresources (scripts, styles, images, media)
// are no-cors loads and keep working. Requests a browser makes in CORS mode for
// a page's own code (module scripts, crossorigin stylesheets, fonts) are let
// through by allowSandboxedSubresource, for public artifacts only. DESIGN.md
// ("Content isolation") records the choice against per-artifact origins.

// sandboxTokens are the capabilities a sandboxed document keeps. allow-same-origin
// is deliberately absent; it would hand the document back the shared origin.
//   - allow-scripts: executable HTML is an intended feature.
//   - allow-forms: rendered reply boxes and ordinary forms in uploaded pages.
//   - allow-popups, allow-popups-to-escape-sandbox: target=_blank links open
//     normally, and the page they open is not itself sandboxed (an artifact it
//     opens carries its own sandbox anyway).
//   - allow-modals: alert/confirm/print.
//   - allow-downloads: download links.
const sandboxTokens = "allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads"

// workerSrcNone refuses dedicated, shared and service workers. Under the sandbox
// a service worker cannot be registered anyway (an opaque origin has none); the
// directive keeps that true when an operator turns the sandbox off.
const workerSrcNone = "worker-src 'none'"

// htmlPolicy is the CSP for an uploaded or rendered HTML document.
func (s *Server) htmlPolicy() string {
	if !s.sandbox {
		return workerSrcNone + "; " + frameAncestorsSelf
	}
	return "sandbox " + sandboxTokens + "; " + workerSrcNone + "; " + frameAncestorsSelf
}

// inertDocumentPolicy is the CSP for SVG/XML/XHTML: rendered as documents, but
// expected to be inert, so no scripts and the strictest sandbox.
func (s *Server) inertDocumentPolicy() string {
	p := "script-src 'none'; " + workerSrcNone + "; " + frameAncestorsSelf
	if s.sandbox {
		p = "sandbox; " + p
	}
	return p
}

// setContentPolicy sets the Content-Security-Policy for an artifact or site file
// of contentType. Types that never render as a document (images, scripts,
// stylesheets, PDF) keep the baseline frame-ancestors policy: a sandbox there
// would gain nothing, and it disables the browser's PDF viewer.
func (s *Server) setContentPolicy(w http.ResponseWriter, contentType string) {
	switch mt := mediaType(contentType); {
	case mt == "text/html":
		w.Header().Set("Content-Security-Policy", s.htmlPolicy())
	case isInertDocument(mt):
		w.Header().Set("Content-Security-Policy", s.inertDocumentPolicy())
	}
}

// isInertDocument mirrors store.IsScriptableNonHTML on a bare media type.
func isInertDocument(mt string) bool {
	switch mt {
	case "image/svg+xml", "text/xml", "application/xml", "application/xhtml+xml":
		return true
	}
	return false
}

// setProtectedHeaders marks a response for a private (capability URL) or
// password-protected artifact, including its 401: no Referer leaves a page
// whose URL is a secret, and no copy of the bytes is kept by the browser or a
// shared cache after the artifact expires or is deleted.
//
// Cross-Origin-Resource-Policy: same-origin stops the browser handing the
// bytes to another origin through a no-cors load. Without it a page could run
// a protected script with <script src> (or apply a protected stylesheet, or
// draw a protected image), carrying the viewer's cached credentials, and read
// what it defines; no CORS grant is involved, so withholding one does not
// help. Every sandboxed page is another origin, so protected artifacts cannot
// be used as subresources at all; opening one, or framing it in the ?live
// wrapper, is a navigation and is unaffected.
func setProtectedHeaders(w http.ResponseWriter) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
}

// passwordChallenge is the WWW-Authenticate value for a password-protected
// artifact. The realm is per artifact (a short hash, so the slug itself is not
// echoed), so a browser does not answer one artifact's challenge with
// credentials it cached for another. Browsers may still send cached Basic
// credentials pre-emptively to other paths on the origin; that reaches only the
// server, which checks them against each artifact's own hash, and under the
// sandbox no page can read a cross-origin response it attached them to.
func passwordChallenge(slug string) string {
	sum := sha256.Sum256([]byte(slug))
	return `Basic realm="demiplane ` + hex.EncodeToString(sum[:])[:12] + `", charset="UTF-8"`
}

// allowSandboxedSubresource lets a sandboxed page load its own module scripts,
// crossorigin stylesheets and fonts. The browser fetches those in CORS mode,
// and a sandboxed page's requests carry "Origin: null", so without an
// Access-Control-Allow-Origin grant they fail. The grant is narrow:
//   - only when the sandbox is on and the artifact is public (never private or
//     password-protected);
//   - only for a request the browser itself labels script, style or font
//     (Sec-Fetch-Dest cannot be set by page script), and only when the stored
//     type matches that destination.
//
// So a page can never read HTML, JSON or text through it, and what it can
// read (public JavaScript, CSS and fonts) is what any page can already execute
// or apply from a cross-origin <script>, <link> or @font-face.
func (s *Server) allowSandboxedSubresource(w http.ResponseWriter, r *http.Request, contentType string, protected bool) {
	dest := subresourceDest(mediaType(contentType))
	if dest == "" {
		return
	}
	// The response varies with these request headers; caches must key on them.
	w.Header().Add("Vary", "Origin, Sec-Fetch-Dest")
	if !s.sandbox || protected {
		return
	}
	if r.Header.Get("Origin") != "null" || r.Header.Get("Sec-Fetch-Dest") != dest {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "null")
}

// subresourceDest maps a media type to the Sec-Fetch-Dest it may be granted
// for, or "" when it gets no grant.
func subresourceDest(mt string) string {
	switch {
	case mt == "text/javascript", mt == "application/javascript", mt == "application/x-javascript",
		mt == "application/ecmascript", mt == "text/ecmascript":
		return "script"
	case mt == "text/css":
		return "style"
	case strings.HasPrefix(mt, "font/"), mt == "application/font-woff", mt == "application/font-sfnt",
		mt == "application/vnd.ms-fontobject", strings.HasPrefix(mt, "application/x-font-"):
		return "font"
	}
	return ""
}

// mediaType returns the lower-cased media type without parameters.
func mediaType(contentType string) string {
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		return mt
	}
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

// refuseServiceWorkers rejects every service-worker script fetch. Browsers mark
// those with "Service-Worker: script", and no demiplane URL is meant to be one.
// It is the network-side half of the service-worker defence: it holds even if a
// page somehow ran unsandboxed on the content origin, and it also fails the
// update check of a worker registered before this defence existed.
func refuseServiceWorkers(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Service-Worker") == "" {
		return false
	}
	http.Error(w, "service workers are not permitted", http.StatusForbidden)
	return true
}
