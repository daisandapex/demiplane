// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package logsafe

import (
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
		"bad\xffbyte":          `bad\xffbyte`,
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
