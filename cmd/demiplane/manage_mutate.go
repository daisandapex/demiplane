// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/daisandapex/demiplane/v3/internal/manage"
	"github.com/daisandapex/demiplane/v3/internal/store"
	"github.com/daisandapex/demiplane/v3/internal/ttl"
)

// Allow the documented command [slugs...] [flags...] form as well as flags first.
func parseManageFlags(fs *flag.FlagSet, args []string) error {
	var flags, positionals []string
	for n := 0; n < len(args); n++ {
		a := args[n]
		if a == "--" {
			positionals = append(positionals, args[n+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positionals = append(positionals, a)
			continue
		}
		flags = append(flags, a)
		name, _, equal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		b, ok := f.Value.(interface{ IsBoolFlag() bool })
		if !equal && !(ok && b.IsBoolFlag()) && n+1 < len(args) {
			n++
			flags = append(flags, args[n])
		}
	}
	return parseClientFlags(fs, append(append(flags, "--"), positionals...))
}

func runDelete(args []string, out io.Writer, in io.Reader, interactive bool) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	rawURL := fs.String("url", "", "control-plane URL (or DEMIPLANE_URL)")
	tokenFile := fs.String("token-file", "", "bearer token file (or DEMIPLANE_TOKEN)")
	var sel manage.Selection
	fs.StringVar(&sel.Match, "match", "", "identifier glob")
	fs.StringVar(&sel.OlderThan, "older-than", "", "age since publish (Go duration)")
	fs.StringVar(&sel.Series, "series", "", "exact series name")
	fs.StringVar(&sel.Type, "type", "", "MIME prefix")
	fs.BoolVar(&sel.Sites, "sites", false, "include sites in selection")
	fs.BoolVar(&sel.All, "all", false, "select both artifacts and sites")
	fs.BoolVar(&sel.IncludePinned, "include-pinned", false, "include pinned artifacts")
	yes := fs.Bool("yes", false, "execute the displayed manifest")
	confirm := fs.String("confirm-host", "", "confirm server hostname for broad deletion")
	if err := parseManageFlags(fs, args); err != nil {
		return err
	}
	resolveURLFlag(fs, rawURL)
	sel.Slugs = fs.Args()
	client, base, token, err := managementClient(*rawURL, *tokenFile)
	if err != nil {
		return err
	}
	m, err := manage.Inventory(context.Background(), client, base, token)
	if err != nil {
		return err
	}
	m, err = manage.Select(m, sel)
	if err != nil {
		return err
	}
	if len(m.Items) == 0 {
		fmt.Fprintln(out, "nothing to delete")
		return nil
	}
	var total int64
	for _, i := range m.Items {
		total += i.Size
	}
	fmt.Fprintf(out, "%d items, %d bytes\n", len(m.Items), total)
	fmt.Fprintf(out, "instance %s, selected %s\n", m.Instance, m.SelectedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	for _, i := range m.Items {
		fmt.Fprintf(out, "%s %s %d bytes revision=%s\n", i.Kind, i.ID, i.Size, i.Revision)
	}
	if !*yes {
		return nil
	}
	host := manage.Host(m.Instance)
	if sel.Broad() {
		if *confirm == "" && interactive {
			fmt.Fprintf(out, "Type %s to confirm: ", host)
			line, _ := bufio.NewReader(in).ReadString('\n')
			*confirm = strings.TrimSpace(line)
		}
		if host == "" || *confirm != host {
			return errors.New("delete: host confirmation required; use --confirm-host with the server hostname")
		}
	}
	report, err := manage.Execute(context.Background(), client, base, token, manage.DeleteRequest{Manifest: m, IncludePinned: sel.IncludePinned, ConfirmHost: *confirm})
	for _, group := range []struct {
		name string
		rows []manage.Result
	}{{"deleted", report.Deleted}, {"skipped", report.Skipped}, {"missing", report.Missing}, {"failed", report.Failed}} {
		for _, i := range group.rows {
			fmt.Fprintf(out, "%s %s %s %s\n", group.name, i.Kind, i.ID, i.Reason)
		}
	}
	if err != nil {
		return err
	}
	if report.Partial() {
		return errors.New("delete: some items were not deleted")
	}
	return nil
}

func runPatch(command string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	rawURL := fs.String("url", "", "control-plane URL (or DEMIPLANE_URL)")
	tokenFile := fs.String("token-file", "", "bearer token file (or DEMIPLANE_TOKEN)")
	if err := parseManageFlags(fs, args); err != nil {
		return err
	}
	resolveURLFlag(fs, rawURL)
	want := 1
	if command == "ttl" {
		want = 2
	}
	if fs.NArg() != want || store.ValidateNamedSlug(fs.Arg(0)) != nil {
		return errors.New("management: invalid arguments; use ttl SLUG DURATION|none, or pin|unpin SLUG")
	}
	body := map[string]any{}
	if command == "ttl" {
		d, err := ttl.Management(fs.Arg(1))
		if err != nil {
			return err
		}
		if d == 0 {
			body["ttl"] = nil
		} else {
			body["ttl"] = fs.Arg(1)
		}
	} else {
		body["pinned"] = command == "pin"
	}
	client, base, token, err := managementClient(*rawURL, *tokenFile)
	if err != nil {
		return err
	}
	var item json.RawMessage
	if err = manage.Request(context.Background(), client, base, token, "PATCH", "/"+url.PathEscape(fs.Arg(0)), body, &item); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(item))
	return err
}
