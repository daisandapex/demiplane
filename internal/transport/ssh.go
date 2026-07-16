// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package transport implements demiplane's SSH ingest path. Rather than
// embedding an SSH server (which would add a crypto dependency and a second host
// key + auth surface to manage), demiplane reuses the operating system's sshd:
// the operator pins a forced command in authorized_keys, e.g.
//
//	command="demiplane receive --store /var/lib/demiplane --base-url https://host",no-pty <key>
//
// sshd performs the public-key authentication; the forced command runs
// `demiplane receive`, which reads the artifact bytes from the SSH channel
// (stdin) and writes them into the SAME store the HTTP server uses. This makes
// demiplane a true superset of pgs (HTTP API + SSH/pipe + directory sync) with
// zero new dependencies and no regression to the HTTP path.
package transport

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/daisandapex/demiplane/internal/store"
)

// ErrTooLarge is returned when the stdin stream exceeds the configured cap.
var ErrTooLarge = errors.New("input exceeds the configured size limit")

// ReceiveOptions configures a single `receive` invocation.
type ReceiveOptions struct {
	store.PutOptions        // Slug/Filename/Private/Password/TTL for the single-file path
	BaseURL          string // advertised base URL for the printed link ("" → relative)
	Untar            bool   // read a tar stream and publish each regular file (directory sync)
	MaxUpload        int64  // cap total stdin bytes; 0 = unlimited
}

// capReader returns ErrTooLarge once more than `remaining` bytes are read. A
// stream of exactly the limit succeeds. Unlike io.LimitReader (which silently
// truncates) this fails loudly, so the SSH path never stores a partial artifact
// as if it were complete.
type capReader struct {
	r         io.Reader
	remaining int64
}

func (c *capReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	if c.remaining < 0 {
		return 0, ErrTooLarge
	}
	return n, err
}

func limit(r io.Reader, max int64) io.Reader {
	if max <= 0 {
		return r
	}
	return &capReader{r: r, remaining: max}
}

// Receive ingests from in and writes the resulting artifact URL(s) to out, one
// per line. With Untar it consumes a tar stream (directory sync); otherwise it
// stores the raw stdin bytes as a single artifact.
func Receive(st *store.Store, opts ReceiveOptions, in io.Reader, out io.Writer) error {
	in = limit(in, opts.MaxUpload)
	if opts.Untar {
		return receiveTar(st, opts, in, out)
	}
	art, err := st.Put(opts.PutOptions, in)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, artifactURL(opts.BaseURL, art.Slug))
	return err
}

// untarDecompressBudget bounds the TOTAL decompressed bytes one SSH untar may
// expand to across all entries, mirroring the HTTP site path's siteDecompressBudget.
// A var initialized from the shared default only so tests can lower it cheaply.
var untarDecompressBudget = store.DefaultDecompressBudget

// receiveTar publishes every regular file in a tar stream as a named artifact
// keyed by its (sanitized) path, so re-syncing a directory overwrites in place.
// Private is incompatible with directory sync (named slugs can't be private).
//
// MaxUpload (in Receive) caps the raw stdin bytes, but a PAX/GNU sparse tar
// declares a tiny stored payload yet expands to attacker-chosen zero-fill
// through tar.Reader — so it is bounded here by the same total-decompressed-bytes
// budget the HTTP site path enforces (store.BudgetReader), or a ~10KB sparse tar
// would write GBs into the store.
func receiveTar(st *store.Store, opts ReceiveOptions, in io.Reader, out io.Writer) error {
	if opts.Private {
		return fmt.Errorf("directory sync cannot be private (named slugs are not capability secrets)")
	}
	tr := tar.NewReader(in)
	budget := untarDecompressBudget
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // skip dirs, symlinks, etc.
		}
		slug, err := pathToSlug(hdr.Name)
		if err != nil {
			return err
		}
		art, err := st.Put(store.PutOptions{
			Slug:     slug,
			Filename: path.Base(hdr.Name),
			Password: opts.Password,
			TTL:      opts.TTL,
		}, &store.BudgetReader{R: tr, Remaining: &budget})
		if err != nil {
			return fmt.Errorf("publish %q: %w", hdr.Name, err)
		}
		if _, err := fmt.Fprintln(out, artifactURL(opts.BaseURL, art.Slug)); err != nil {
			return err
		}
		count++
	}
	if count == 0 {
		return fmt.Errorf("tar stream contained no regular files")
	}
	return nil
}

// pathToSlug maps a tar entry path to a flat, validated slug: directory
// separators become hyphens so nested files get a stable, URL-safe name.
func pathToSlug(name string) (string, error) {
	clean := path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." {
		return "", fmt.Errorf("invalid tar entry path %q", name)
	}
	slug := strings.ReplaceAll(clean, "/", "-")
	if err := store.ValidateNamedSlug(slug); err != nil {
		return "", fmt.Errorf("tar entry %q yields invalid slug: %w", name, err)
	}
	return slug, nil
}

// artifactURL builds the printed link. With no base URL (operator didn't set
// --base-url) it falls back to a root-relative path.
func artifactURL(baseURL, slug string) string {
	if baseURL == "" {
		return "/" + slug
	}
	return strings.TrimRight(baseURL, "/") + "/" + slug
}
