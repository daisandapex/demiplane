// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/server"
	"github.com/daisandapex/demiplane/v3/internal/store"
)

type manageTransport func(*http.Request) (*http.Response, error)

func (f manageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func useManageTransport(t *testing.T, f manageTransport) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = old })
}
func manageResponse(r *http.Request, code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}
}
func manageTokenFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("file-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestManageListCLI(t *testing.T) {
	t.Setenv("DEMIPLANE_URL", "https://env.test")
	t.Setenv("DEMIPLANE_TOKEN", "env-token")
	tokenFile := manageTokenFile(t)
	for _, tc := range []struct {
		name             string
		args             []string
		host, token      string
		all, json, empty bool
	}{
		{name: "table-env", host: "env.test", token: "env-token"},
		{name: "json-owner", args: []string{"--url", "https://flag.test", "--token-file", tokenFile, "--json", "--all"}, host: "flag.test", token: "file-token", all: true, json: true},
		{name: "empty", args: []string{"--json"}, host: "env.test", token: "env-token", json: true, empty: true},
		{name: "empty-owner", args: []string{"--all", "--json"}, host: "env.test", token: "env-token", all: true, json: true, empty: true},
		{name: "default", host: "127.0.0.1:8080", token: "env-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DEMIPLANE_TOKEN", "env-token")
			if tc.name == "default" {
				t.Setenv("DEMIPLANE_URL", "")
			}
			calls := 0
			useManageTransport(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || r.URL.Host != tc.host || r.URL.Path != "/list" || r.Header.Get("Authorization") != "Bearer "+tc.token || (r.URL.Query().Get("all") == "1") != tc.all {
					t.Fatalf("request: %s %s %v", r.Method, r.URL, r.Header)
				}
				response := func(body string) *http.Response {
					if tc.all {
						body = `{"owner":true,` + body[1:]
					}
					return manageResponse(r, 200, body)
				}
				if tc.empty {
					return response(`{"artifacts":[],"count":0}`), nil
				}
				if calls == 1 {
					return response(`{"artifacts":[{"kind":"artifact","slug":"one","render":"md","reply":true,"series":"lesson","expires_at":"tomorrow","url":"https://content.test/one"}],"count":1,"next":"artifact:private-cursor"}`), nil
				}
				if calls != 2 || r.URL.Query().Get("after") != "artifact:private-cursor" {
					t.Fatal("bad pagination")
				}
				return response(`{"artifacts":[{"kind":"site","slug":"two","render":"raw","reply":false,"url":"https://content.test/two/"}],"count":1}`), nil
			})
			var out bytes.Buffer
			if err := runList(tc.args, &out); err != nil {
				t.Fatal(err)
			}
			if tc.empty {
				if calls != 1 || !strings.Contains(out.String(), `"count":0`) {
					t.Fatal(out.String())
				}
				return
			}
			if calls != 2 {
				t.Fatal("did not page")
			}
			if tc.json {
				var result struct {
					Artifacts []map[string]any
					Count     int
					Next      string
				}
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Count != 2 || len(result.Artifacts) != 2 || result.Next != "" || result.Artifacts[0]["reply"] != true {
					t.Fatal(result)
				}
			} else {
				for _, value := range []string{"KIND", "SLUG", "RENDER", "REPLY", "SERIES", "EXPIRES", "URL", "artifact", "site", "one", "two", "true", "lesson", "tomorrow"} {
					if !strings.Contains(out.String(), value) {
						t.Fatalf("missing %s: %s", value, &out)
					}
				}
			}
		})
	}
	// Exercise actual main dispatch as well as the command function.
	useManageTransport(t, func(r *http.Request) (*http.Response, error) {
		return manageResponse(r, 200, `{"artifacts":[],"count":0}`), nil
	})
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = []string{"demiplane", "list", "--json"}
	got := captureStdout(t, main)
	if !strings.Contains(got, `"count":0`) {
		t.Fatal(got)
	}
}

func TestManageClientErrors(t *testing.T) {
	t.Setenv("DEMIPLANE_URL", "https://control.test")
	t.Setenv("DEMIPLANE_TOKEN", "secret-token")
	for _, mode := range []string{"http", "json", "shape", "transport", "redirect", "cycle", "entry"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			useManageTransport(t, func(r *http.Request) (*http.Response, error) {
				calls++
				switch mode {
				case "http":
					return manageResponse(r, 403, "https://content.test/private-cap secret-token"), nil
				case "json":
					return manageResponse(r, 200, "private-cap"), nil
				case "shape":
					return manageResponse(r, 200, `{"count":0}`), nil
				case "entry":
					return manageResponse(r, 200, `{"artifacts":[42],"count":1}`), nil
				case "transport":
					return nil, errors.New("https://content.test/private-cap secret-token")
				case "redirect":
					resp := manageResponse(r, 302, "")
					resp.Header.Set("Location", "https://evil.test/private-cap")
					return resp, nil
				case "cycle":
					return manageResponse(r, 200, `{"artifacts":[],"count":0,"next":"private-cap"}`), nil
				}
				panic("unreachable")
			})
			err := runList(nil, io.Discard)
			if err == nil || strings.Contains(err.Error(), "private-cap") || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "https://") {
				t.Fatalf("unsafe/missing error: %v", err)
			}
			want := 1
			if mode == "cycle" {
				want = 2
			}
			if calls != want {
				t.Fatalf("calls %d want %d", calls, want)
			}
			if mode == "http" || mode == "transport" || mode == "redirect" {
				err = deleteRemoteSite("https://control.test", "", "demo")
				if err == nil || strings.Contains(err.Error(), "private-cap") {
					t.Fatal(err)
				}
			}
		})
	}
	for _, args := range [][]string{{"extra"}, {"--unknown"}, {"--url", "javascript:private-cap"}, {"--url", "https://secret-token@control.test"}, {"--token-file", "/missing/private-cap"}} {
		err := runList(args, io.Discard)
		if err == nil || strings.Contains(err.Error(), "private-cap") || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("args %v: %v", args, err)
		}
	}
}

func TestManageDeleteSiteCLI(t *testing.T) {
	t.Setenv("DEMIPLANE_URL", "https://env.test")
	t.Setenv("DEMIPLANE_TOKEN", "env-token")
	file := manageTokenFile(t)
	calls := 0
	useManageTransport(t, func(r *http.Request) (*http.Response, error) {
		calls++
		token := "env-token"
		host := "env.test"
		if calls == 1 {
			token = "file-token"
			host = "flag.test"
		}
		if r.Method != "DELETE" || r.URL.Host != host || r.URL.Path != "/sites/demo" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("%s %s %v", r.Method, r.URL, r.Header)
		}
		return manageResponse(r, 204, ""), nil
	})
	if err := runDeleteSite([]string{"--url", "https://flag.test", "--token-file", file, "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := runDeleteSite([]string{"demo"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--store", t.TempDir(), "--url", "https://flag.test", "demo"}, {"--store", t.TempDir(), "--token-file", file, "demo"}} {
		if err := runDeleteSite(args); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("conflicting flags: %v", err)
		}
	}
	for _, args := range [][]string{{"../bad"}, {"demo", "extra"}, {"--store", "", "demo"}, {}} {
		if err := runDeleteSite(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if calls != 2 {
		t.Fatalf("invalid args caused request: %d", calls)
	}
	// Environment URL must not change explicit offline mode.
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sw, err := st.NewSiteWriter("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err = sw.AddFile("index.html", strings.NewReader("site")); err != nil {
		t.Fatal(err)
	}
	if _, err = sw.Commit(); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err = runDeleteSite([]string{"--store", dir, "demo"}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("offline command used HTTP")
	}
}

func captureManageStderr(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	old := os.Stderr
	os.Stderr = f
	defer func() { os.Stderr = old }()
	fn()
	if _, err = f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestManageCLIHelp(t *testing.T) {
	if command := os.Getenv("DEMIPLANE_TEST_MANAGE_HELP"); command != "" {
		os.Args = []string{"demiplane", command, "--help"}
		main()
		return
	}
	for _, command := range []string{"list", "delete-site"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestManageCLIHelp$")
		cmd.Env = append(os.Environ(), "DEMIPLANE_TEST_MANAGE_HELP="+command)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s --help failed: %v: %s", command, err, output)
		}
	}
	help := captureManageStderr(t, usage)
	for _, text := range []string{"demiplane list", "demiplane delete-site [--url", "demiplane delete-site --store", "offline", "--all", "--json"} {
		if !strings.Contains(help, text) {
			t.Fatalf("missing %q", text)
		}
	}
	// Individual FlagSets write to the current stderr at construction.
	for _, fn := range []func() error{func() error { return runList([]string{"--help"}, io.Discard) }, func() error { return runDeleteSite([]string{"--help"}) }} {
		got := captureManageStderr(t, func() { _ = fn() })
		if !strings.Contains(got, "-url") || !strings.Contains(got, "-token-file") {
			t.Fatal(got)
		}
	}
}
func TestManageStartupInventory(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, o := range []store.PutOptions{{Slug: "public"}, {Private: true}, {Slug: "expired", TTL: time.Nanosecond}} {
		if _, err = st.Put(o, strings.NewReader("bytes")); err != nil {
			t.Fatal(err)
		}
	}
	sw, err := st.NewSiteWriter("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err = sw.AddFile("index.html", strings.NewReader("site")); err != nil {
		t.Fatal(err)
	}
	if _, err = sw.Commit(); err != nil {
		t.Fatal(err)
	}
	h := server.New(st, server.Config{AuthToken: "owner"}).ControlHandler()
	req := httptest.NewRequest("GET", "http://control.test/list?all=1", nil)
	req.Header.Set("Authorization", "Bearer owner")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var page struct {
		Count     int
		Artifacts []struct {
			Slug    string
			Private bool
		}
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || page.Count != 3 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	banner := captureManageStderr(t, func() { printStartup(":8080", ":8081", st.Root(), "", "", "owner", st, false) })
	for _, text := range []string{"2 unexpired artifacts including private", "1 site", fmt.Sprintf("%d owner entries", page.Count)} {
		if !strings.Contains(banner, text) {
			t.Fatalf("missing %q: %s", text, banner)
		}
	}
	for _, a := range page.Artifacts {
		if a.Private && strings.Contains(banner, a.Slug) {
			t.Fatal("banner leaked capability")
		}
	}
}

// Exercise dispatch in a subprocess to check both diagnostics and exit status.
func TestClientFlagSecrets(t *testing.T) {
	if command := os.Getenv("DEMIPLANE_TEST_FLAG_COMMAND"); command != "" {
		args := strings.Split(os.Getenv("DEMIPLANE_TEST_FLAG_ARGS"), "\n")
		os.Args = append([]string{"demiplane", command}, args...)
		main()
		return
	}
	secret := "https://secret-token@control.test/private-capability"
	for _, command := range []string{"list", "delete-site", "publish"} {
		boolean := "all"
		if command == "publish" {
			boolean = "private"
		}
		for _, args := range []string{"--bogus", "--" + secret, "--" + boolean + "=" + secret, "--help", "--url\n" + secret + "\n--bogus"} {
			t.Run(command+"/"+strings.Split(args, "\n")[0], func(t *testing.T) {
				c := exec.Command(os.Args[0], "-test.run=^TestClientFlagSecrets$")
				c.Env = append(os.Environ(), "DEMIPLANE_TEST_FLAG_COMMAND="+command, "DEMIPLANE_TEST_FLAG_ARGS="+args, "DEMIPLANE_URL="+secret)
				output, err := c.CombinedOutput()
				if args == "--help" {
					if err != nil {
						t.Errorf("help failed: %v", err)
					}
				} else {
					var exitErr *exec.ExitError
					if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
						t.Errorf("exit error = %v, want status 2", err)
					}
					if !strings.Contains(string(output), command+": invalid flags; use --help") {
						t.Errorf("missing sanitized diagnostic: %s", output)
					}
				}
				if len(output) == 0 {
					t.Fatalf("missing help/error: %v", err)
				}
				for _, value := range []string{secret, "secret-token", "private-capability", "http://", "https://"} {
					if strings.Contains(string(output), value) {
						t.Errorf("diagnostic leaked %q: %s", value, output)
					}
				}
				if args == "--help" && !strings.Contains(string(output), "-url") {
					t.Fatalf("help missing: %s", output)
				}
			})
		}
	}
}

func TestManageAllRequiresOwnerFields(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		for name, body := range map[string]string{
			"old-public":   `{"artifacts":[{"slug":"public","url":"https://content.test/public"}],"count":1}`,
			"old-empty":    `{"artifacts":[],"count":0}`,
			"false-marker": `{"owner":false,"artifacts":[],"count":0}`,
			"missing-kind": `{"owner":true,"artifacts":[{"slug":"public"}],"count":1}`,
		} {
			t.Run(fmt.Sprint(jsonOutput)+"/"+name, func(t *testing.T) {
				useManageTransport(t, func(r *http.Request) (*http.Response, error) {
					return manageResponse(r, 200, body), nil
				})
				var out bytes.Buffer
				args := []string{"--all"}
				if jsonOutput {
					args = append(args, "--json")
				}
				err := runList(args, &out)
				if err == nil || !strings.Contains(err.Error(), "server does not support --all; upgrade it") || out.Len() != 0 {
					t.Fatalf("error=%v output=%s", err, &out)
				}
			})
		}
	}
}

func TestClientPublishFailureExit(t *testing.T) {
	if os.Getenv("DEMIPLANE_TEST_PUBLISH_FAILURE") == "1" {
		useManageTransport(t, func(r *http.Request) (*http.Response, error) {
			return manageResponse(r, http.StatusServiceUnavailable, ""), nil
		})
		os.Args = []string{"demiplane", "publish", "--url", "https://control.test", "-"}
		main()
		return
	}
	c := exec.Command(os.Args[0], "-test.run=^TestClientPublishFailureExit$")
	c.Env = append(os.Environ(), "DEMIPLANE_TEST_PUBLISH_FAILURE=1", "DEMIPLANE_TOKEN=")
	c.Stdin = strings.NewReader("page")
	output, err := c.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("exit error = %v, want status 1: %s", err, output)
	}
	if !strings.Contains(string(output), "publish: server returned status 503") {
		t.Fatalf("missing publish failure: %s", output)
	}
}

func TestManageForbiddenAdvice(t *testing.T) {
	useManageTransport(t, func(r *http.Request) (*http.Response, error) {
		return manageResponse(r, 403, "configure a token; secret-untrusted-body"), nil
	})
	for _, err := range []error{runList([]string{"--all"}, io.Discard), deleteRemoteSite("https://control.test", "", "demo")} {
		if err == nil || !strings.Contains(err.Error(), "configure a token") || strings.Contains(err.Error(), "secret-untrusted-body") {
			t.Fatalf("advice: %v", err)
		}
	}
}

func TestDeleteSiteRequiresDestination(t *testing.T) {
	t.Setenv("DEMIPLANE_URL", "")
	calls := 0
	useManageTransport(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return manageResponse(r, 204, ""), nil
	})
	for _, args := range [][]string{{"demo"}, {"--url", "", "demo"}, {"--token-file", "unused", "demo"}} {
		err := runDeleteSite(args)
		if err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("args=%v: %v", args, err)
		}
	}
	if calls != 0 {
		t.Fatalf("unsolicited remote deletes: %d", calls)
	}
}

func TestDeleteSiteHelpURLDefault(t *testing.T) {
	output := captureManageStderr(t, usage)
	if !strings.Contains(output, "delete-site [--url <control-url>]") || !strings.Contains(output, "DEMIPLANE_URL") {
		t.Fatalf("help does not explain optional URL: %s", output)
	}
}

func TestClientURLValidationSecrets(t *testing.T) {
	for _, value := range []string{"https://%secret-token/private-capability", "secret-token://control.test/private-capability", "https:///secret-token/private-capability"} {
		_, err := resolveBaseURL(value)
		if err == nil || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "private-capability") {
			t.Errorf("unsafe/missing URL error: %v", err)
		}
	}
}
