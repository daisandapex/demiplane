// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/daisandapex/demiplane/internal/logsafe"
)

// tempfile.go accounts for the temp files in blobs/ and sources/: upload
// temps (writeBlob, writeSource) and scratch files (Scratch).
//
// A temp file this process owns is registered while it exists and charged in
// flight as it is written. Committing it renames it into place; discarding it
// removes it and releases its charge. If it cannot be removed, its charge
// moves to the live count instead, the failure is logged, and the file is
// left as a leftover. Any ".tmp-" file there that no writer in this process
// owns, a leftover or one a crash left behind, is measured and kept charged
// like a live file, and Reconcile removes it once it is older than
// orphanGrace, releasing what it removed. A temp file another process (the
// SSH receive path) is writing counts the same way until that write ends.

// removeTempFile removes a temp file; a test hook.
var removeTempFile = os.Remove

// createTemp creates a temp file in dir (os.CreateTemp) and registers it as
// owned, in one transition, so a recount never counts it as a leftover while
// it is also charged in flight.
func (s *Store) createTemp(dir, pattern string) (*os.File, error) {
	s.beginTransition()
	defer s.endTransition(0, 0)
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	s.registerTemp(f.Name())
	return f, nil
}

// discardTemp removes an owned temp file that was not committed and releases
// its in-flight charge. If the file cannot be removed it is unregistered, its
// charge moves to the live count, and it is left for Reconcile; the error is
// logged and returned.
func (s *Store) discardTemp(path string, charged int64) error {
	s.beginTransition()
	err := removeTempFile(path)
	if err != nil && os.IsNotExist(err) {
		err = nil
	}
	s.unregisterTemp(path)
	if err == nil {
		s.release(charged)
		s.endTransition(0, 0)
		return nil
	}
	s.countFile(path, charged)
	s.endTransition(charged, 0)
	log.Printf("store: could not remove a temp file, left for reconcile: %s", logsafe.Err(err))
	return fmt.Errorf("remove temp file: %w", err)
}

func (s *Store) registerTemp(path string) {
	s.quota.mu.Lock()
	if s.quota.temps == nil {
		s.quota.temps = make(map[string]struct{})
	}
	s.quota.temps[path] = struct{}{}
	s.quota.mu.Unlock()
}

func (s *Store) unregisterTemp(path string) {
	s.quota.mu.Lock()
	delete(s.quota.temps, path)
	s.quota.mu.Unlock()
}

func (s *Store) tempLive(path string) bool {
	s.quota.mu.Lock()
	defer s.quota.mu.Unlock()
	_, ok := s.quota.temps[path]
	return ok
}

// contentStats sums the regular files under dir (blobs/ or sources/),
// leftover temp files included and this process's own temp files excluded
// (those are charged in flight).
// Each regular file at the top of dir is recorded in files with its size.
func (s *Store) contentStats(dir string, files map[string]int64) (int64, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var total int64
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if strings.HasPrefix(e.Name(), tempPrefix) && s.tempLive(p) {
			continue
		}
		if e.IsDir() {
			n, _, err := treeStats(p)
			if err != nil {
				return 0, err
			}
			total += n
			continue
		}
		if !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		total += fi.Size()
		files[p] = fi.Size()
	}
	return total, nil
}
