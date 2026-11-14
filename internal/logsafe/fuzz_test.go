// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package logsafe

import (
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

// FuzzLogsafe checks the log scrubbers: Text leaves no control, format or
// line-separator character and never changes clean text; URL keeps only an
// origin; Err hides the path and the named secret; JSON escapes without
// changing what the document means.
func FuzzLogsafe(f *testing.F) {
	for _, s := range []string{
		"plain", "a\nb", "a\x1b[31mred", "  ", "‮", "\xff\xfe", "\x00",
		"https://user:pw@host:8080/p?q#f", "http://h", "//h/x", "not a url", "http://[::1]:1/x",
		`{"a":"b ","c":[1,"\u0000"]}`, "{\"a\":\"\x01\xff\"}",
	} {
		f.Add(s, "capslug-ABCDEFGHIJ")
	}
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
			var a, b any
			if json.Unmarshal([]byte(s), &a) != nil || json.Unmarshal(out, &b) != nil {
				t.Fatalf("JSON(%q) = %q is not valid JSON", s, out)
			}
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("JSON changed the document: %q -> %q", s, out)
			}
			checkLogText(t, "JSON", string(out))
		}
	})
}
