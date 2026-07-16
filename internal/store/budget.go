// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"io"
)

// budget.go holds the archive decompression-bomb defense shared by every ingest
// path that expands an archive into the store: the HTTP multi-file site publish
// (internal/server/site.go, tar + zip) and the SSH directory-sync untar
// (internal/transport/ssh.go). Both cap TOTAL decompressed bytes across all
// entries — independent of the compressed/stored upload size — because a small
// archive can inflate enormously (a zip bomb) and a PAX/GNU sparse tar declares
// a tiny stored payload yet expands to attacker-chosen zero-fill through
// tar.Reader. The per-request upload cap (--max-upload) bounds the stored bytes,
// not the decompressed output, so it is not a substitute for this budget.

// DefaultDecompressBudget bounds the TOTAL decompressed bytes one archive may
// expand to across all its entries. Sized well above any realistic static site
// or directory sync; ingest paths keep their own var initialized from this so
// tests can lower it cheaply.
const DefaultDecompressBudget int64 = 512 << 20 // 512 MiB

// ErrDecompressBudget is returned once an archive's cumulative decompressed
// output exceeds its budget. Ingest paths map it to the appropriate rejection
// (HTTP 413; a hard error on the SSH path).
var ErrDecompressBudget = errors.New("archive expands beyond the size limit")

// BudgetReader fails with ErrDecompressBudget once the shared remaining byte
// budget is exhausted. Remaining points at a counter shared across every entry
// of an archive, so the cap is on TOTAL decompressed output, not per-file: wrap
// each entry's reader in a BudgetReader that points at the same counter.
type BudgetReader struct {
	R         io.Reader
	Remaining *int64
}

func (b *BudgetReader) Read(p []byte) (int, error) {
	if *b.Remaining <= 0 {
		return 0, ErrDecompressBudget
	}
	n, err := b.R.Read(p)
	*b.Remaining -= int64(n)
	if *b.Remaining < 0 {
		return n, ErrDecompressBudget
	}
	return n, err
}
