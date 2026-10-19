// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/manage"
	"github.com/daisandapex/demiplane/v3/internal/server"
	"github.com/daisandapex/demiplane/v3/internal/store"
)

const cliInventory = `{"owner":true,"instance":"https://control.test:443","count":1,"artifacts":[{"kind":"artifact","slug":"page","revision":"rev","size":12,"created_at":"2000-01-01T00:00:00Z","series":"s","content_type":"text/html"}]}`

func cliTransport(t *testing.T, reply string, posts *int, manifest *manage.DeleteRequest) {
	t.Helper()
	useManageTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			if r.URL.Query().Get("all") != "1" {
				t.Fatal("not owner list")
			}
			return manageResponse(r, 200, cliInventory), nil
		}
		*posts++
		if r.Method != "POST" || r.URL.Path != "/manage/delete" {
			t.Fatalf("mutation %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(manifest); err != nil {
			t.Fatal(err)
		}
		return manageResponse(r, 200, reply), nil
	})
}
func TestManageCLIManifest(t *testing.T) {
	pages := 0
	useManageTransport(t, func(r *http.Request) (*http.Response, error) {
		pages++
		if r.URL.Query().Get("all") != "1" {
			t.Fatal("missing owner flag")
		}
		switch pages {
		case 1:
			if r.URL.Query().Get("after") != "" {
				t.Fatal("initial cursor")
			}
			return manageResponse(r, 200, strings.TrimSuffix(cliInventory, "}")+`,"next":"opaque:2"}`), nil
		case 2:
			if r.URL.Query().Get("after") != "opaque:2" {
				t.Fatal("lost cursor")
			}
			return manageResponse(r, 200, strings.ReplaceAll(strings.ReplaceAll(cliInventory, `"page"`, `"second"`), `"rev"`, `"rev2"`)), nil
		default:
			t.Fatal("extra request")
			return nil, errors.New("extra")
		}
	})
	var out bytes.Buffer
	if err := runDelete([]string{"--all", "--url", "https://control.test"}, &out, strings.NewReader(""), false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2 items, 24 bytes", "artifact page 12 bytes revision=rev", "artifact second 12 bytes revision=rev2"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("manifest missing %s: %s", want, &out)
		}
	}
}
func TestManageCLIDryRun(t *testing.T) {
	for _, yes := range []bool{false, true} {
		t.Run(map[bool]string{false: "dry", true: "real"}[yes], func(t *testing.T) {
			posts := 0
			var m manage.DeleteRequest
			cliTransport(t, `{"deleted":[{"kind":"artifact","id":"page","revision":"rev","size":12}],"skipped":[],"missing":[],"failed":[]}`, &posts, &m)
			args := []string{"page", "--url", "https://control.test"}
			if yes {
				args = append(args, "--yes")
			}
			var out bytes.Buffer
			if err := runDelete(args, &out, strings.NewReader(""), false); err != nil {
				t.Fatal(err)
			}
			if posts != map[bool]int{false: 0, true: 1}[yes] {
				t.Fatal("dry-run mutated or real-run skipped")
			}
			if yes && (len(m.Items) != 1 || m.Items[0].ID != "page" || m.Items[0].Revision != "rev" || m.Instance != "https://control.test:443" || m.SelectedAt.IsZero()) {
				t.Fatalf("manifest changed: %+v", m)
			}
		})
	}
}
func TestManageCLIConfirmation(t *testing.T) {
	for _, selector := range [][]string{{"--all"}, {"--match", "*"}, {"--type", "text/"}, {"--older-than", "1h"}, {"--series", "s"}, {"--sites", "--match", "*"}} {
		for _, tc := range []struct {
			name, confirm, input string
			interactive, ok      bool
		}{{"missing", "", "", false, false}, {"wrong", "wrong", "", false, false}, {"correct", "control.test", "", false, true}, {"interactive", "", "control.test\n", true, true}, {"interactive-wrong", "", "wrong\n", true, false}} {
			t.Run(strings.Join(selector, " ")+"/"+tc.name, func(t *testing.T) {
				posts := 0
				var m manage.DeleteRequest
				cliTransport(t, `{"deleted":[],"skipped":[],"missing":[],"failed":[]}`, &posts, &m)
				args := append([]string{"--url", "https://control.test", "--yes"}, selector...)
				if tc.confirm != "" {
					args = append(args, "--confirm-host", tc.confirm)
				}
				var out bytes.Buffer
				err := runDelete(args, &out, strings.NewReader(tc.input), tc.interactive)
				if (err == nil) != tc.ok || posts != map[bool]int{false: 0, true: 1}[tc.ok] {
					t.Fatalf("err=%v posts=%d", err, posts)
				}
			})
		}
	}
}
func TestManageCLIResults(t *testing.T) {
	for _, kind := range []string{"skipped", "missing", "failed"} {
		t.Run(kind, func(t *testing.T) {
			posts := 0
			var m manage.DeleteRequest
			cliTransport(t, `{"`+kind+`":[{"kind":"artifact","id":"page","revision":"rev","reason":"`+kind+`"}]}`, &posts, &m)
			var out bytes.Buffer
			err := runDelete([]string{"page", "--yes"}, &out, strings.NewReader(""), false)
			if err == nil || clientExitCode(err) != 1 || !strings.Contains(out.String(), kind+" artifact page") {
				t.Fatalf("result %v %s", err, &out)
			}
		})
	}
	posts := 0
	var m manage.DeleteRequest
	cliTransport(t, `{}`, &posts, &m)
	var out bytes.Buffer
	if err := runDelete([]string{"missing", "--yes"}, &out, strings.NewReader(""), false); err != nil || out.String() != "nothing to delete\n" || posts != 0 {
		t.Fatalf("empty selection %v %s", err, &out)
	}
	if err := runDelete([]string{"--not-a-flag"}, &out, strings.NewReader(""), false); err == nil || clientExitCode(err) != 2 {
		t.Fatal("invalid flag exit")
	}
}
func TestManageCLIPrivacy(t *testing.T) {
	secret := "capability-secret"
	for _, body := range []string{secret, `{"owner":false,"count":0,"artifacts":[]}`, strings.ReplaceAll(cliInventory, `"revision":"rev",`, "")} {
		t.Run(body, func(t *testing.T) {
			useManageTransport(t, func(r *http.Request) (*http.Response, error) { return manageResponse(r, 200, body), nil })
			var out bytes.Buffer
			err := runDelete([]string{"--all"}, &out, strings.NewReader(""), false)
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
	useManageTransport(t, func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("https://control.test/" + secret)
	})
	var out bytes.Buffer
	if err := runPatch("pin", []string{"page"}, &out); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("transport error leaked")
	}
}
func TestManageCLIPatch(t *testing.T) {
	t.Setenv("DEMIPLANE_URL", "https://env.test")
	t.Setenv("DEMIPLANE_TOKEN", "env-token")
	for _, tc := range []struct {
		cmd  string
		args []string
		want map[string]any
	}{{"ttl", []string{"page", "2h"}, map[string]any{"ttl": "2h"}}, {"ttl", []string{"page", "none"}, map[string]any{"ttl": nil}}, {"pin", []string{"page"}, map[string]any{"pinned": true}}, {"unpin", []string{"page"}, map[string]any{"pinned": false}}} {
		t.Run(tc.cmd+strings.Join(tc.args, ""), func(t *testing.T) {
			tokenFile := manageTokenFile(t)
			calls := 0
			useManageTransport(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host != "explicit.test" || r.Header.Get("Authorization") != "Bearer file-token" || r.Method != "PATCH" || r.URL.Path != "/page" {
					t.Fatalf("request %v", r)
				}
				var b map[string]any
				json.NewDecoder(r.Body).Decode(&b)
				if !reflect.DeepEqual(b, tc.want) {
					t.Fatalf("body %v want %v", b, tc.want)
				}
				return manageResponse(r, 200, `{"slug":"page"}`), nil
			})
			var out bytes.Buffer
			if err := runPatch(tc.cmd, append(tc.args, "--url", "https://explicit.test", "--token-file", tokenFile), &out); err != nil || calls != 1 {
				t.Fatalf("patch %v calls=%d", err, calls)
			}
		})
	}
	for _, args := range [][]string{{"page", "59s"}, {"page", "366d"}, {"page"}, {"../secret", "1h"}} {
		if runPatch("ttl", args, io.Discard) == nil {
			t.Fatal("bad patch accepted")
		}
	}
}
func TestManageDefaultFlag(t *testing.T) {
	if os.Getenv("DEMIPLANE_1B_TEST_HELP") == "1" {
		runServe([]string{"--help"})
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestManageDefaultFlag$")
	cmd.Env = append(os.Environ(), "DEMIPLANE_1B_TEST_HELP=1")
	help, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`-default-ttl string
[^
]*\(default "0"\)`).Match(help) {
		t.Fatalf("default expiry is not off: %s", help)
	}
	for _, value := range []string{"59s", "366d", "-1h", "garbage"} {
		err := runServe([]string{"--default-ttl", value, "--store", t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), "--default-ttl") {
			t.Fatalf("flag %s: %v", value, err)
		}
	}
}
func TestManageReceiveDefaultTTL(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.SetDefaultTTL(time.Hour); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "input")
	os.WriteFile(input, []byte("ssh body"), 0600)
	oldIn, oldOut := os.Stdin, os.Stdout
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	os.Stdout = out
	for _, tc := range []struct {
		slug, ttl string
		expires   bool
	}{{"absent", "", true}, {"zero", "0", false}, {"none", "none", false}} {
		f, err := os.Open(input)
		if err != nil {
			t.Fatal(err)
		}
		os.Stdin = f
		args := []string{"--store", dir, "--slug", tc.slug}
		if tc.ttl != "" {
			args = append(args, "--ttl", tc.ttl)
		}
		err = runReceive(args)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		a, err := st.Patch(tc.slug, nil, nil)
		if err != nil || a.ExpiresAt.IsZero() == tc.expires {
			t.Fatalf("receive expiry %+v %v", a, err)
		}
	}
	p := true
	if _, err = st.Patch("absent", nil, &p); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(input)
	defer f.Close()
	os.Stdin = f
	if err = runReceive([]string{"--store", dir, "--slug", "absent", "--ttl", "0"}); !errors.Is(err, store.ErrPinned) {
		t.Fatalf("pinned SSH %v", err)
	}
}
func TestManageCLIEndToEnd(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.Put(store.PutOptions{Slug: "page"}, strings.NewReader("bytes")); err != nil {
		t.Fatal(err)
	}
	h := server.New(st, server.Config{AuthToken: "owner", BaseURL: "https://control.test"}).ControlHandler()
	t.Setenv("DEMIPLANE_TOKEN", "owner")
	useManageTransport(t, func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Result(), nil
	})
	var out bytes.Buffer
	if err = runPatch("pin", []string{"page", "--url", "https://control.test"}, &out); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEMIPLANE_TOKEN", "owner")
	if err = runDelete([]string{"--all", "--yes", "--confirm-host", "control.test", "--url", "https://control.test"}, &out, strings.NewReader(""), false); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Patch("page", nil, nil); err != nil {
		t.Fatal("delete all removed pin")
	}
	t.Setenv("DEMIPLANE_TOKEN", "owner")
	if err = runDelete([]string{"--all", "--yes", "--include-pinned", "--confirm-host", "control.test", "--url", "https://control.test"}, &out, strings.NewReader(""), false); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Patch("page", nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("include pinned failed")
	}
}
