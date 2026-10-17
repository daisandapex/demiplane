// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// canonicalPaths resolves path ambiguity before ServeMux sees the request.
//
// ServeMux cleans a non-canonical path ("//", "/./", "/../") by redirecting,
// but it builds the Location from the ESCAPED path placed in url.URL.Path, so
// every percent is escaped a second time ("/site//%61" redirects to
// "/site/%2561", a different resource). This wrapper does the redirect itself
// from the escaped path, preserving its escaping and the query, so the mux
// only ever sees canonical paths.
//
// Some paths have no canonical meaning for a slug or a site path and are
// refused with 400 instead: a segment that decodes to "." or ".." (the mux
// would hand "../b" to a handler as a wildcard value), a segment that decodes
// to contain "/" or "\" (a slug or site segment never does, and "\" is a
// separator to browsers), and any control character.
func canonicalPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.EscapedPath()
		if !strings.HasPrefix(p, "/") || !unambiguousSegments(p) {
			http.Error(w, "bad request path", http.StatusBadRequest)
			return
		}
		c := cleanEscapedPath(p)
		if c == p {
			next.ServeHTTP(w, r)
			return
		}
		// path.Clean output is rooted and has no empty segments, so it can
		// never be a network-path reference ("//host"); checked anyway, since
		// this value becomes a Location header.
		if !strings.HasPrefix(c, "/") || strings.HasPrefix(c, "//") || strings.ContainsRune(c, '\\') {
			http.Error(w, "bad request path", http.StatusBadRequest)
			return
		}
		if r.URL.RawQuery != "" {
			c += "?" + r.URL.RawQuery
		}
		w.Header().Set("Location", c)
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
}

// cleanEscapedPath canonicalizes an escaped path the way ServeMux does
// (path.Clean, keeping a trailing slash), without unescaping it.
func cleanEscapedPath(p string) string {
	c := path.Clean(p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c
}

// unambiguousSegments reports whether every segment of the escaped path p
// decodes to something with exactly one meaning: no encoded dot segment, no
// separator inside a segment, no control character.
func unambiguousSegments(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		dec, err := url.PathUnescape(seg)
		if err != nil {
			return false
		}
		if dec != seg && (dec == "." || dec == "..") {
			return false
		}
		if strings.ContainsAny(dec, `/\`) {
			return false
		}
		for _, r := range dec {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				return false
			}
		}
	}
	return true
}
