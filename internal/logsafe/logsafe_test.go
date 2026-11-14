// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package logsafe

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
)

const capSlug = "Zq3xW9pLm2Rt8vKs4bNc7yHd"

func TestErrRedactsPathsAndSecrets(t *testing.T) {
	blob := "/srv/demiplane/blobs/" + capSlug + "~0123456789abcdef"
	src := "/srv/demiplane/sources/" + capSlug + "~0123456789abcdef"
	cases := []struct {
		name string
		err  error
	}{
		{"path error", &fs.PathError{Op: "open", Path: blob, Err: syscall.EACCES}},
		{"wrapped", fmt.Errorf("open blob: %w", &fs.PathError{Op: "open", Path: blob, Err: syscall.EACCES})},
		{"link error", fmt.Errorf("commit blob: %w", &os.LinkError{Op: "rename", Old: "/srv/demiplane/blobs/.tmp-1", New: blob, Err: syscall.EXDEV})},
		{"joined", errors.Join(
			fmt.Errorf("remove blob: %w", &fs.PathError{Op: "remove", Path: blob, Err: syscall.EACCES}),
			fmt.Errorf("remove source: %w", &fs.PathError{Op: "remove", Path: src, Err: syscall.EACCES}))},
		{"slug in message", fmt.Errorf("delete expired %q: %w", capSlug, errors.New("disk on fire"))},
	}
	for _, c := range cases {
		got := Err(c.err, capSlug)
		if strings.Contains(got, capSlug) {
			t.Errorf("%s: slug leaked: %s", c.name, got)
		}
		if strings.Contains(got, "/srv/demiplane/blobs/") || strings.Contains(got, "/srv/demiplane/sources/") {
			t.Errorf("%s: path leaked: %s", c.name, got)
		}
	}
	// The cause stays readable; that is the point of logging at all.
	got := Err(fmt.Errorf("open blob: %w", &fs.PathError{Op: "open", Path: blob, Err: syscall.EACCES}))
	if !strings.Contains(got, "permission denied") || !strings.Contains(got, "open blob") {
		t.Errorf("cause lost: %s", got)
	}
	if Err(nil) != "<nil>" {
		t.Errorf("Err(nil) = %q", Err(nil))
	}
}

func TestErrRedactsURLs(t *testing.T) {
	raw := "https://user:hunter2@hooks.example.com/services/T00/B00/s3cr3tpath?token=s3cr3tquery#frag"
	err := &url.Error{Op: "Post", URL: raw, Err: errors.New("connection refused")}
	got := Err(fmt.Errorf("webhook: %w", err))
	for _, leak := range []string{"hunter2", "user:", "s3cr3tpath", "s3cr3tquery", "frag"} {
		if strings.Contains(got, leak) {
			t.Errorf("url secret %q leaked: %s", leak, got)
		}
	}
	if !strings.Contains(got, "https://hooks.example.com") || !strings.Contains(got, "connection refused") {
		t.Errorf("origin or cause lost: %s", got)
	}
	if got := URL(raw); got != "https://hooks.example.com" {
		t.Errorf("URL(...) = %q, want the bare origin", got)
	}
	if got := URL("::not a url"); got != "[url]" {
		t.Errorf("URL(unparseable) = %q, want [url]", got)
	}
}

func TestTextEscapesControls(t *testing.T) {
	cases := map[string]string{
		"plain text":           "plain text",
		"bell\a":               `bell\x07`,
		"\x1b[31mred\x1b[0m":   `\x1b[31mred\x1b[0m`,
		"line1\nFORGED line":   `line1\nFORGED line`,
		"cr\rover":             `cr\rover`,
		"c1\u009bcsi":          `c1\x9bcsi`,
		"bidi\u202eevil":       `bidi\u202eevil`,
		"ls\u2028ps\u2029":     `ls\u2028ps\u2029`,
		"bad\xffbyte":          `bad\xffbyte`,
		"tag\U000e0001x":       `tag\U000e0001x`,
		"bom\ufeffx":           `bom\ufeffx`,
		"unicode ok: café, 日本": "unicode ok: café, 日本",
	}
	for in, want := range cases {
		if got := Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugTagStable(t *testing.T) {
	a, b := Slug(capSlug), Slug(capSlug)
	if a != b || !strings.HasPrefix(a, "slug#") || strings.Contains(a, capSlug) {
		t.Errorf("Slug tag = %q / %q", a, b)
	}
}

// TestErrRedactsQuotedURL: url.Error formats its URL with %q, which escapes
// quotes and backslashes; the redaction must still match.
func TestErrRedactsQuotedURL(t *testing.T) {
	raw := `http://127.0.0.1:1/private-token?x="secret"&y=a\b`
	err := &url.Error{Op: "Post", URL: raw, Err: errors.New("connection refused")}
	got := Err(fmt.Errorf("webhook: %w", errors.Join(err)))
	for _, leak := range []string{"private-token", "secret", `a\b`} {
		if strings.Contains(got, leak) {
			t.Errorf("quoted URL leaked %q: %s", leak, got)
		}
	}
	if !strings.Contains(got, "http://127.0.0.1:1") {
		t.Errorf("origin lost: %s", got)
	}
}

func TestJSONEscapesUnsafeRunes(t *testing.T) {
	in := "{\"a\":\"x\u009b2J\u202e\u2028\x7f\U000e0001\xff\",\n\t\"b\":\"caf\u00e9\"}"
	got := JSON([]byte(in))
	want := `{"a":"x\u009b2J\u202e\u2028\u007f\udb40\udc01\ufffd",` + "\n\t" + `"b":"café"}`
	if string(got) != want {
		t.Fatalf("JSON(%q) = %q, want %q", in, got, want)
	}
	var a, b map[string]string
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(strings.ReplaceAll(in, "\xff", "\ufffd")), &b); err != nil {
		t.Fatal(err)
	}
	if a["a"] != b["a"] || a["b"] != b["b"] {
		t.Errorf("JSON changed the decoded value: %q, want %q", a, b)
	}
	if clean := []byte(`{"a":"caf\u00e9 \u65e5"}`); &JSON(clean)[0] != &clean[0] {
		t.Error("JSON copied a clean document")
	}
}

// TestJSONKeepsCRInsideStringEscaped: in valid JSON a carriage return inside
// a string can only be the escape \r, and JSON leaves it escaped, so the
// decoded value keeps its CR and the output carries no raw CR there. A raw
// CR between tokens is white space and is left alone; callers compact the
// document before printing it.
func TestJSONKeepsCRInsideStringEscaped(t *testing.T) {
	in := "{\"a\":\"x\\ry\\u202e\",\r\n\"b\":1}"
	got := string(JSON([]byte(in)))
	want := "{\"a\":\"x\\ry\\u202e\",\r\n\"b\":1}"
	if got != want {
		t.Fatalf("JSON(%q) = %q, want %q", in, got, want)
	}
	str := got[strings.Index(got, `"x`):strings.Index(got, `",`)]
	if strings.ContainsRune(str, '\r') || !strings.Contains(str, `\r`) {
		t.Errorf("string token %q: want the CR as the escape \\r, not raw", str)
	}
	var v struct{ A string }
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != "x\ry‮" {
		t.Errorf("decoded %q, want %q", v.A, "x\ry‮")
	}
}
