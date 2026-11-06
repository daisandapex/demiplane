// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"html"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

// errorpage.go gives a browser the house error page instead of Go's plaintext
// on every listener: the mux's own 404 and 405, and every http.Error or
// http.NotFound a handler writes. Only requests that ask for HTML get it, so
// curl, agents and the MCP client keep the plaintext reason they parse today.

// maxErrorReason caps how much of a plaintext error body is kept before its
// first line becomes the page's reason; http.Error bodies are one short line.
const maxErrorReason = 512

// themedErrors serves the themed error page to browsers in place of a
// plaintext error response.
func (s *Server) themedErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ew := &errorPageWriter{ResponseWriter: w, html: acceptsHTML(r.Header.Get("Accept"))}
		next.ServeHTTP(ew, r)
		if ew.code != 0 {
			h := w.Header()
			h.Set("Content-Type", "text/html; charset=utf-8")
			// Headers that describe the body the handler meant to send do
			// not describe this page: an attachment's disposition would
			// download the error page as the file.
			for _, k := range []string{"Content-Length", "Content-Disposition", "Content-Encoding", "ETag", "Last-Modified"} {
				h.Del(k)
			}
			w.WriteHeader(ew.code)
			reason, _, _ := strings.Cut(ew.reason.String(), "\n")
			_, _ = w.Write([]byte(s.errorPageHTML(ew.code, strings.ToValidUTF8(strings.TrimSpace(reason), ""))))
		}
	})
}

// acceptsHTML reports whether an Accept header lists text/html with a
// non-zero quality.
func acceptsHTML(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil || mt != "text/html" {
			continue
		}
		if q, ok := params["q"]; ok {
			if v, err := strconv.ParseFloat(q, 64); err != nil || v <= 0 {
				continue
			}
		}
		return true
	}
	return false
}

// errorPageWriter holds back a plaintext error response (status 400 or above,
// text/plain, as http.Error writes it) when the client accepts HTML, and keeps
// its body for the reason line. Every plaintext error says Vary: Accept, since
// its body depends on it. Every other response passes through untouched.
type errorPageWriter struct {
	http.ResponseWriter
	html   bool // the client accepts text/html
	wrote  bool
	code   int // set when the response is held back
	reason strings.Builder
}

func (e *errorPageWriter) WriteHeader(code int) {
	if e.wrote {
		return
	}
	if code < http.StatusOK { // informational: the final status is still to come
		e.ResponseWriter.WriteHeader(code)
		return
	}
	e.wrote = true
	if code >= http.StatusBadRequest && strings.HasPrefix(e.Header().Get("Content-Type"), "text/plain") {
		e.Header().Add("Vary", "Accept")
		if e.html {
			e.code = code
			return
		}
	}
	e.ResponseWriter.WriteHeader(code)
}

func (e *errorPageWriter) Write(b []byte) (int, error) {
	if !e.wrote {
		e.WriteHeader(http.StatusOK)
	}
	if e.code == 0 {
		return e.ResponseWriter.Write(b)
	}
	if room := maxErrorReason - e.reason.Len(); room > 0 {
		e.reason.Write(b[:min(len(b), room)])
	}
	return len(b), nil
}

// Flush commits an implicit 200 like net/http does, so a later error status
// cannot be held back after the headers went out.
func (e *errorPageWriter) Flush() {
	if !e.wrote {
		e.WriteHeader(http.StatusOK)
	}
	if e.code == 0 {
		_ = http.NewResponseController(e.ResponseWriter).Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (e *errorPageWriter) Unwrap() http.ResponseWriter { return e.ResponseWriter }

// errorCopy is the heading and one-line explanation for a status.
func errorCopy(code int) (heading, text string) {
	switch {
	case code == http.StatusBadRequest:
		return "That request couldn't be used", "Part of it was missing or malformed."
	case code == http.StatusUnauthorized:
		return "This page needs credentials", "Sign in with the page's password or the owner's token, then reload."
	case code == http.StatusForbidden:
		return "That isn't allowed here", "The server refused this request."
	case code == http.StatusNotFound:
		return "Nothing is published at this address", "The link may be mistyped, or what it pointed to expired and was removed."
	case code == http.StatusMethodNotAllowed:
		return "This address doesn't take that request", "The page exists, but not for this kind of request."
	case code == http.StatusRequestEntityTooLarge:
		return "That upload is too large", "It is over this instance's size limit."
	case code == http.StatusTooManyRequests:
		return "Too many requests", "Wait a moment, then try again."
	case code >= http.StatusInternalServerError:
		return "Something went wrong on the server", "Try again in a moment. If it keeps happening, the server log has the detail."
	}
	return http.StatusText(code), ""
}

// errorPageHTML builds the themed error document for code. reason is the
// handler's plaintext message, shown escaped unless it only repeats the status.
func (s *Server) errorPageHTML(code int, reason string) string {
	heading, text := errorCopy(code)
	status := strconv.Itoa(code) + " · " + http.StatusText(code)
	var b strings.Builder
	b.WriteString(`<p class="nf-code">` + html.EscapeString(status) + "</p>\n")
	b.WriteString(`<h1 class="nf-h">` + html.EscapeString(heading) + "</h1>\n")
	if text != "" {
		b.WriteString(`<p class="nf-p">` + html.EscapeString(text) + "</p>\n")
	}
	if reason != "" && !strings.EqualFold(reason, http.StatusText(code)) &&
		!strings.EqualFold(reason, status) && reason != "404 page not found" {
		b.WriteString(`<p class="nf-p">The server said: <code>` + html.EscapeString(reason) + "</code></p>\n")
	}
	return s.statusPage(status, b.String())
}
