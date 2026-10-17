// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// generation.go makes each write of an artifact's bytes a new file and the
// metadata row the single commit point.
//
// Every Put and every body replacement writes its bytes (and a Put its retained
// source) to a file named <slug>~<generation>, where the generation is fresh
// random hex, and then records that generation in the row in the same statement
// or transaction that records the rest of the metadata. A reader resolves the
// row first and opens the file the row names, so the bytes it serves are always
// the bytes that row describes: new bytes can never be served under the previous
// publish's password, privacy or expiry. The previous generation's files are
// removed only once the row no longer names them; a reader that already opened
// one keeps reading it.
//
// Rows written before generations existed carry an empty generation, which maps
// to the original <slug> file name, so an existing store needs no migration.
//
// Writers to one slug (Put, ReplaceBody, Rebake, Delete, expiry reaping) are also
// serialized in-process by a per-slug lock. The row transaction keeps the
// row/file pairing correct across processes (the SSH receive path opens the same
// store); the lock additionally makes a rebake's read-render-write atomic
// against a publish in this process.

// genSep separates slug and generation in a file name. It is outside both the
// named-slug alphabet and the capability-slug alphabet, so a generation file can
// never collide with another slug's legacy file.
const genSep = "~"

// noSourceGen is the source generation of an artifact with no retained source.
const noSourceGen = "-"

// errGenConflict reports that the row no longer names the generation a
// conditional body replacement expected.
var errGenConflict = errors.New("artifact changed during the write")

func newGen() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate blob name: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// blobFile is the path of slug's bytes at generation gen ("" = legacy name).
func (s *Store) blobFile(slug, gen string) string {
	if gen == "" {
		return filepath.Join(s.blobDir, slug)
	}
	return filepath.Join(s.blobDir, slug+genSep+gen)
}

// sourceFile is the path of slug's retained source at generation gen.
func (s *Store) sourceFile(slug, gen string) string {
	if gen == "" {
		return filepath.Join(s.sourceDir, slug)
	}
	return filepath.Join(s.sourceDir, slug+genSep+gen)
}

// removeGenFiles removes the blob at blobGen and the source at sourceGen,
// releasing their bytes from the store quota. A file that is already gone is
// not an error.
func (s *Store) removeGenFiles(slug, blobGen, sourceGen string) error {
	var errs []error
	if err := s.removeLive(s.blobFile(slug, blobGen)); err != nil {
		errs = append(errs, fmt.Errorf("remove blob: %w", err))
	}
	if sourceGen != noSourceGen {
		if err := s.removeLive(s.sourceFile(slug, sourceGen)); err != nil {
			errs = append(errs, fmt.Errorf("remove source: %w", err))
		}
	}
	return errors.Join(errs...)
}

// dropReplacedGen removes a generation the row has stopped naming. The write
// that replaced it has already committed, so a failure here only leaves an
// unreferenced file; it is logged without the path, which carries the slug.
func (s *Store) dropReplacedGen(slug, blobGen, sourceGen string) {
	if err := s.removeGenFiles(slug, blobGen, sourceGen); err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		log.Printf("store: could not remove a replaced artifact file: %v", err)
	}
}

// slugLocks is a set of per-slug mutexes, created on demand and dropped when
// no writer holds or waits for them.
type slugLocks struct {
	mu sync.Mutex
	m  map[string]*slugLock
}

type slugLock struct {
	mu   sync.Mutex
	refs int
}

// lock blocks until the caller is the only writer of slug and returns the
// function that releases it.
func (l *slugLocks) lock(slug string) (unlock func()) {
	l.mu.Lock()
	if l.m == nil {
		l.m = make(map[string]*slugLock)
	}
	sl := l.m[slug]
	if sl == nil {
		sl = &slugLock{}
		l.m[slug] = sl
	}
	sl.refs++
	l.mu.Unlock()

	sl.mu.Lock()
	return func() {
		sl.mu.Unlock()
		l.mu.Lock()
		sl.refs--
		if sl.refs == 0 {
			delete(l.m, slug)
		}
		l.mu.Unlock()
	}
}
