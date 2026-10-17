// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package logsafe formats values for log lines that may be read by a wider
// audience than the store itself. A private slug is a capability secret, and
// it travels inside filesystem paths (blobs/<slug>~<gen>), so neither may
// reach a log; webhook URLs carry credentials in their userinfo, path or
// query; and anything an outside party can influence must not carry terminal
// control bytes into a log viewer.
//
// Every log call that prints an error from the store, the filesystem or an
// HTTP client goes through Err, and every untrusted string through Text.
package logsafe

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Slug returns a short, non-reversible tag for a slug so a log line can
// correlate events for one artifact without carrying the slug itself.
func Slug(slug string) string {
	sum := sha256.Sum256([]byte(slug))
	return "slug#" + hex.EncodeToString(sum[:])[:12]
}

// redactedPath replaces every filesystem path found in an error.
const redactedPath = "[path]"

// Err renders err for a log line. Every path carried by an *fs.PathError or
// *os.LinkError anywhere in the error tree is replaced by "[path]", every URL
// carried by a *url.Error is reduced to its origin (see URL), each of the
// given secrets (typically the slug the operation was about) is replaced by
// its Slug tag, and control characters are escaped. A nil error renders as
// "<nil>".
func Err(err error, secrets ...string) string {
	if err == nil {
		return "<nil>"
	}
	type repl struct{ old, new string }
	var rs []repl
	walk(err, func(e error) {
		var pe *fs.PathError
		if errors.As(e, &pe) && pe.Path != "" {
			rs = append(rs, repl{pe.Path, redactedPath})
		}
		var le *os.LinkError
		if errors.As(e, &le) {
			if le.Old != "" {
				rs = append(rs, repl{le.Old, redactedPath})
			}
			if le.New != "" {
				rs = append(rs, repl{le.New, redactedPath})
			}
		}
		var ue *url.Error
		if errors.As(e, &ue) && ue.URL != "" {
			// url.Error prints its URL with %q, so a URL containing a quote
			// or backslash appears escaped: replace that form, too.
			rs = append(rs, repl{strconv.Quote(ue.URL), strconv.Quote(URL(ue.URL))})
			rs = append(rs, repl{ue.URL, URL(ue.URL)})
		}
	})
	for _, s := range secrets {
		if s != "" {
			rs = append(rs, repl{s, Slug(s)})
		}
	}
	// Longest first, so a path is replaced whole before a slug inside it.
	sort.SliceStable(rs, func(i, j int) bool { return len(rs[i].old) > len(rs[j].old) })
	msg := err.Error()
	for _, r := range rs {
		msg = strings.ReplaceAll(msg, r.old, r.new)
	}
	return Text(msg)
}

// walk calls fn for err and every error it wraps, following both single and
// multi-error (errors.Join, fmt.Errorf with several %w) unwrapping.
func walk(err error, fn func(error)) {
	if err == nil {
		return
	}
	fn(err)
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		walk(u.Unwrap(), fn)
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			walk(e, fn)
		}
	}
}

// URL reduces a URL to scheme://host for a log line. Userinfo, path, query
// and fragment are all dropped: webhook services routinely put their secret
// in the path (chat-service hook URLs) or the query (?token=), not only in the
// userinfo. An unparseable value is replaced entirely.
func URL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[url]"
	}
	return Text(u.Scheme + "://" + u.Host)
}

// Text escapes every control character (C0, DEL, C1), every Unicode format
// character (bidi overrides and the like) and every invalid UTF-8 byte in s,
// so a value an outside party influenced cannot move the cursor, recolor a
// terminal, forge a new log line or reorder the text a reader sees.
func Text(s string) string {
	clean := true
	for _, r := range s {
		if r == utf8.RuneError || unsafeRune(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteString(`\x`)
			b.WriteString(hex.EncodeToString([]byte{s[i]}))
		case unsafeRune(r):
			b.WriteString(escapeRune(r))
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

func escapeRune(r rune) string {
	switch r {
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	}
	const digits = "0123456789abcdef"
	if r < 0x100 {
		return `\x` + string([]byte{digits[r>>4], digits[r&0xf]})
	}
	var buf [4]byte
	for i := 3; i >= 0; i-- {
		buf[i] = digits[r&0xf]
		r >>= 4
	}
	return `\u` + string(buf[:])
}
