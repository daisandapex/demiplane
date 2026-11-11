// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// assertTerminalSafe fails when s holds a byte or rune that a terminal acts
// on or that hides or reorders text: C0 (other than the allowed newline and
// tab), DEL, C1, format characters, line and paragraph separators, invalid
// UTF-8.
func assertTerminalSafe(t *testing.T, label, s string, allowNewline bool) {
	t.Helper()
	for i, r := range s {
		if allowNewline && r == '\n' {
			continue
		}
		bad := r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == utf8.RuneError ||
			unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
		if bad {
			t.Errorf("%s: unsafe rune %U at byte %d reached the terminal: %q", label, r, i, s)
			return
		}
	}
}

// hostileText is what a hostile control plane puts in every string it can:
// a title set, a screen clear, a forged line, C1 CSI, a bidi override and a
// line separator. In the JSON below the C1 and Unicode characters travel as
// raw UTF-8, which a JSON decoder keeps verbatim in a json.RawMessage.
const hostileJSON = `"x\u001b]0;T\u0007\u001b[2J\r\nforged` + "\u009b2J‮ " + `"`

func hostileServer(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		h, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func jsonReply(code int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
}

func TestListEscapesServerText(t *testing.T) {
	t.Setenv("DEMIPLANE_TOKEN", "tok")
	item := `{"kind":` + hostileJSON + `,"slug":` + hostileJSON + `,"render":` + hostileJSON +
		`,"series":` + hostileJSON + `,"expires_at":` + hostileJSON + `,"url":` + hostileJSON + `}`
	base := hostileServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /list": jsonReply(200, `{"artifacts":[`+item+`],"count":1}`),
	})
	var table bytes.Buffer
	if err := runList([]string{"--url", base}, &table); err != nil {
		t.Fatalf("list: %v", err)
	}
	assertTerminalSafe(t, "list table", table.String(), true)
	if strings.Count(table.String(), "\n") != 2 {
		t.Errorf("list table has a forged line: %q", table.String())
	}

	var js bytes.Buffer
	if err := runList([]string{"--url", base, "--json"}, &js); err != nil {
		t.Fatalf("list --json: %v", err)
	}
	assertTerminalSafe(t, "list --json", js.String(), true)
	var got struct{ Artifacts []struct{ Slug string } }
	if err := json.Unmarshal(js.Bytes(), &got); err != nil || len(got.Artifacts) != 1 {
		t.Fatalf("list --json output is not the same JSON: %v %q", err, js.String())
	}
	var want string
	_ = json.Unmarshal([]byte(hostileJSON), &want)
	if got.Artifacts[0].Slug != want {
		t.Errorf("list --json changed the value: %q, want %q", got.Artifacts[0].Slug, want)
	}
}

func TestPublishEscapesServerErrorBody(t *testing.T) {
	src := t.TempDir() + "/p.html"
	writeFile(t, src, "<p>x</p>")
	for _, code := range []int{400, 500} {
		base := hostileServer(t, map[string]func(http.ResponseWriter, *http.Request){
			"POST /publish": func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
				_, _ = io.WriteString(w, "bad \x1b]0;T\x07\x1b[2J thing \u009b2J‮\x7f")
			},
		})
		err := runPublish([]string{"--url", base, src})
		if err == nil || !strings.Contains(err.Error(), "bad ") {
			t.Fatalf("%d: runPublish = %v, want the server's error", code, err)
		}
		assertTerminalSafe(t, "publish error", err.Error(), false)
	}
}

func TestDeleteEscapesServerText(t *testing.T) {
	t.Setenv("DEMIPLANE_TOKEN", "tok")
	inv := `{"server_time":"2026-01-01T00:00:00Z","owner":true,"instance":` + hostileJSON +
		`,"artifacts":[{"kind":"artifact","slug":` + hostileJSON + `,"revision":` + hostileJSON + `,"size":1}],"count":1}`
	report := `{"deleted":[],"skipped":[],"missing":[],"failed":[{"kind":"artifact","id":` + hostileJSON + `,"reason":` + hostileJSON + `}]}`
	base := hostileServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /list":           jsonReply(200, inv),
		"POST /manage/delete": jsonReply(200, report),
	})
	var out bytes.Buffer
	_ = runDelete([]string{"--url", base, "--match", "*", "--yes", "--confirm-host", "nope"}, &out, strings.NewReader(""), false)
	assertTerminalSafe(t, "delete manifest", out.String(), true)
	if !strings.Contains(out.String(), "revision=") {
		t.Fatalf("no manifest printed: %q", out.String())
	}

	var slug string
	_ = json.Unmarshal([]byte(hostileJSON), &slug)
	out.Reset()
	_ = runDelete([]string{"--url", base, "--yes", slug}, &out, strings.NewReader(""), false)
	assertTerminalSafe(t, "delete report", out.String(), true)
	if !strings.Contains(out.String(), "failed ") {
		t.Fatalf("no report printed: %q", out.String())
	}
}

func TestPatchEscapesServerJSON(t *testing.T) {
	t.Setenv("DEMIPLANE_TOKEN", "tok")
	base := hostileServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"PATCH /x": jsonReply(200, `{"slug":"x","note":`+hostileJSON+`}`),
	})
	var out bytes.Buffer
	if err := runPatch("pin", []string{"--url", base, "x"}, &out); err != nil {
		t.Fatalf("pin: %v", err)
	}
	assertTerminalSafe(t, "pin", out.String(), true)
	var got, want struct{ Note string }
	_ = json.Unmarshal([]byte(`{"note":`+hostileJSON+`}`), &want)
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Note != want.Note {
		t.Errorf("pin output is not the same JSON: %v %q", err, out.String())
	}
}

// TestPatchCompactsServerJSON: JSON allows tab, CR and LF between tokens, so
// a server that pads its reply with them would move the cursor or forge a
// line through ttl, pin and unpin. The output must be one line of the same
// JSON.
func TestPatchCompactsServerJSON(t *testing.T) {
	t.Setenv("DEMIPLANE_TOKEN", "tok")
	padded := "\r\n\t{\"slug\":\"x\",\r\t\"pinned\":false,\r\"note\":\r\n\"demiplane: forged\"\t\n,\n\t\"tags\":[\r1,\t2\n]}\r\n"
	base := hostileServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"PATCH /x": jsonReply(200, padded),
	})
	for _, args := range [][]string{{"ttl", "x", "1h"}, {"pin", "x"}, {"unpin", "x"}} {
		var out bytes.Buffer
		if err := runPatch(args[0], append([]string{"--url", base}, args[1:]...), &out); err != nil {
			t.Fatalf("%s: %v", args[0], err)
		}
		body := strings.TrimSuffix(out.String(), "\n")
		for i := 0; i < len(body); i++ {
			if c := body[i]; c < 0x20 || c == 0x7f {
				t.Errorf("%s: raw byte %#x at %d reached the terminal: %q", args[0], c, i, out.String())
				break
			}
		}
		var got, want any
		_ = json.Unmarshal([]byte(padded), &want)
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s output is not the same JSON: %v %q", args[0], err, out.String())
		}
	}
}
