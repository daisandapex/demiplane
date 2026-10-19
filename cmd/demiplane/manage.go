// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/store"
)

// parseClientFlags suppresses flag's diagnostics, which echo arbitrary values
// and unknown flag names. Help is printed only from static flag definitions.
func parseClientFlags(fs *flag.FlagSet, args []string) error {
	output := fs.Output()
	usage := fs.Usage
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	err := fs.Parse(args)
	fs.SetOutput(output)
	fs.Usage = usage
	if errors.Is(err, flag.ErrHelp) {
		fs.Usage()
		return flag.ErrHelp
	}
	if err != nil {
		return fmt.Errorf("%s: invalid flags; use --help", fs.Name())
	}
	return nil
}

func resolveURLFlag(fs *flag.FlagSet, value *string) {
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "url" {
			explicit = true
		}
	})
	if !explicit {
		*value = os.Getenv("DEMIPLANE_URL")
	}
}

// Keep transport errors and untrusted response bodies out of errors: they can
// contain owner capability URLs, pagination cursors or credentials.
func managementRequest(client *http.Client, method, endpoint, token string, status int) (*http.Response, error) {
	req, err := http.NewRequest(method, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("management: invalid request URL")
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("management: request failed")
	}
	if resp.StatusCode != status {
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("management: server returned status 403; configure a token on the server and supply it with --token-file or DEMIPLANE_TOKEN; cross-origin requests are also refused")
		}
		return nil, fmt.Errorf("management: server returned status %d", resp.StatusCode)
	}
	return resp, nil
}

func managementClient(rawURL, tokenFile string) (*http.Client, string, string, error) {
	base, err := resolveBaseURL(rawURL)
	if err != nil {
		return nil, "", "", fmt.Errorf("management: invalid --url")
	}
	u, err := url.Parse(base)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, "", "", fmt.Errorf("management: invalid --url")
	}
	token, err := resolveToken(tokenFile)
	if err != nil {
		return nil, "", "", fmt.Errorf("management: cannot read token file")
	}
	client := noRedirectClient()
	client.Timeout = 30 * time.Second
	return client, base, token, nil
}

func runList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	rawURL := fs.String("url", "", "control-plane URL (env DEMIPLANE_URL; default local control listener)")
	tokenFile := fs.String("token-file", "", "bearer token file; overrides DEMIPLANE_TOKEN")
	all := fs.Bool("all", false, "owner inventory including private artifacts and sites; requires configured token")
	asJSON := fs.Bool("json", false, "print JSON instead of a human table")
	if err := parseClientFlags(fs, args); err != nil {
		return err
	}
	resolveURLFlag(fs, rawURL)
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: demiplane list [--url URL] [--token-file FILE] [--all] [--json]")
	}
	client, base, token, err := managementClient(*rawURL, *tokenFile)
	if err != nil {
		return err
	}
	items := make([]json.RawMessage, 0)
	q := url.Values{}
	if *all {
		q.Set("all", "1")
	}
	seen := map[string]bool{}
	for {
		resp, err := managementRequest(client, http.MethodGet, base+"/list?"+q.Encode(), token, http.StatusOK)
		if err != nil {
			return err
		}
		var page struct {
			Artifacts []json.RawMessage `json:"artifacts"`
			Count     int               `json:"count"`
			Next      string            `json:"next"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil || page.Artifacts == nil || page.Count != len(page.Artifacts) {
			return fmt.Errorf("list: unexpected response")
		}
		if *all {
			for _, raw := range page.Artifacts {
				var item struct {
					Kind string `json:"kind"`
				}
				if json.Unmarshal(raw, &item) != nil || item.Kind == "" {
					return fmt.Errorf("list: server does not support --all; upgrade it")
				}
			}
		}
		items = append(items, page.Artifacts...)
		if page.Next == "" {
			break
		}
		if seen[page.Next] {
			return fmt.Errorf("list: repeated pagination cursor")
		}
		seen[page.Next] = true
		q.Set("after", page.Next)
	}
	if *asJSON {
		return json.NewEncoder(out).Encode(struct {
			Artifacts []json.RawMessage `json:"artifacts"`
			Count     int               `json:"count"`
		}{items, len(items)})
	}
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "KIND\tSLUG\tRENDER\tREPLY\tSERIES\tEXPIRES\tURL")
	for _, raw := range items {
		var item struct {
			Kind, Slug, Render, Series, URL string
			Reply                           bool
			ExpiresAt                       string `json:"expires_at"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return fmt.Errorf("list: unexpected entry")
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%t\t%s\t%s\t%s\n", item.Kind, item.Slug, item.Render, item.Reply, item.Series, item.ExpiresAt, item.URL)
	}
	return table.Flush()
}

func deleteRemoteSite(rawURL, tokenFile, name string) error {
	if store.ValidateNamedSlug(name) != nil {
		return fmt.Errorf("delete-site: invalid site name")
	}
	client, base, token, err := managementClient(rawURL, tokenFile)
	if err != nil {
		return err
	}
	resp, err := managementRequest(client, http.MethodDelete, base+"/sites/"+url.PathEscape(name), token, http.StatusNoContent)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
