// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package logsafe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func checkLogText(t *testing.T, what, s string) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("%s is not valid UTF-8: %q", what, s)
	}
	for _, r := range s {
		if unsafeRune(r) {
			t.Fatalf("%s kept %U: %q", what, r, s)
		}
	}
}

// checkJSONText checks JSON's output for a valid document: it is valid
// UTF-8, and no control, format or line-separator character is left raw,
// except tab, CR and LF between tokens, where JSON allows them as white
// space (both callers compact the document before printing it). Inside a
// string token none is allowed, not even those three.
func checkJSONText(t *testing.T, out []byte) {
	t.Helper()
	if !utf8.Valid(out) {
		t.Fatalf("JSON output is not valid UTF-8: %q", out)
	}
	inString := false
	for i := 0; i < len(out); {
		r, size := utf8.DecodeRune(out[i:])
		switch {
		case inString && r == '\\':
			// An escape: skip the escaped byte, so `\"` stays in the string.
			size++
		case r == '"':
			inString = !inString
		case inString && unsafeRune(r):
			t.Fatalf("JSON kept %U inside a string at byte %d: %q", r, i, out)
		case !inString && unsafeRune(r) && r != '\t' && r != '\r' && r != '\n':
			t.Fatalf("JSON kept %U between tokens at byte %d: %q", r, i, out)
		}
		i += size
	}
	if inString {
		t.Fatalf("JSON output ends inside a string: %q", out)
	}
}

// decodeJSON decodes one JSON value, numbers as json.Number.
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

// FuzzLogsafe checks the log scrubbers: Text leaves no control, format or
// line-separator character and never changes clean text; URL keeps only an
// origin; Err hides the path and the named secret; for valid JSON, JSON
// keeps the document valid and its value unchanged, and leaves no such
// character raw but the white space JSON allows between tokens
// (checkJSONText).
func FuzzLogsafe(f *testing.F) {
	for _, s := range []string{
		"plain", "a\nb", "a\x1b[31mred", "  ", "‮", "\xff\xfe", "\x00",
		"https://user:pw@host:8080/p?q#f", "http://h", "//h/x", "not a url", "http://[::1]:1/x",
		`{"a":"b ","c":[1,"\u0000"]}`, "{\"a\":\"\x01\xff\"}",
	} {
		f.Add(s, "capslug-ABCDEFGHIJ")
	}
	// JSON keeps a carriage return between tokens: once a crasher, when the
	// target wanted no control character anywhere in JSON's output.
	f.Add("\r0", "0")
	f.Add("[1,\r\n\t\"a\\r\\u2028\"]", "0")
	f.Fuzz(func(t *testing.T, s, secret string) {
		txt := Text(s)
		checkLogText(t, "Text", txt)
		clean := utf8.ValidString(s)
		for _, r := range s {
			if unsafeRune(r) {
				clean = false
			}
		}
		if clean && txt != s {
			t.Fatalf("Text changed clean text %q to %q", s, txt)
		}

		u := URL(s)
		checkLogText(t, "URL", u)
		if u != "[url]" {
			scheme, rest, ok := strings.Cut(u, "://")
			if !ok || strings.ContainsAny(rest, "/?#@") || strings.Contains(scheme, "/") {
				t.Fatalf("URL(%q) = %q keeps more than an origin", s, u)
			}
		}

		if len(secret) >= 8 && !strings.Contains("[path]slug#0123456789abcdef: open", secret) {
			err := fmt.Errorf("serve: %w", &fs.PathError{Op: "open", Path: "/store/" + secret + "/" + s, Err: errors.New("denied")})
			got := Err(err, secret)
			checkLogText(t, "Err", got)
			if strings.Contains(got, secret) {
				t.Fatalf("Err leaked the secret: %q", got)
			}
		}

		if json.Valid([]byte(s)) {
			out := JSON([]byte(s))
			if !json.Valid(out) {
				t.Fatalf("JSON(%q) = %q is not valid JSON", s, out)
			}
			// Numbers decode as json.Number: a valid number may be too
			// large for a float64, and JSON leaves its digits as they are.
			a, aerr := decodeJSON([]byte(s))
			b, berr := decodeJSON(out)
			if aerr != nil || berr != nil {
				t.Fatalf("decoding %q: %v; decoding JSON's %q: %v", s, aerr, out, berr)
			}
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("JSON changed the document: %q -> %q", s, out)
			}
			checkJSONText(t, out)
		}
	})
}
