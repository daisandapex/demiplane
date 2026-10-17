// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"os"
	"sync"
	"syscall"
)

// readers.go keeps the quota honest about files that are gone from the tree
// but still open.
//
// Unlinking a file frees its disk blocks only when the last open handle
// closes, and a slow download holds its handle for as long as it runs. So a
// removed file that a reader still holds keeps its charge: its size moves from
// the live count to a retired count (quotaState.retired), which counts toward
// the quota like any other byte and is released when the last reader closes
// it. A recount walks only the tree, so it leaves the retired count alone.
//
// Readers are the handles the store gives out (Get, OpenSiteFile), counted by
// file identity rather than path: a replaced site tree is renamed before its
// files are removed. Opening a file and counting it happen under a read lock,
// and removing a file and checking its readers under the write lock, so a
// removal never misses a reader that already has the file open. Source reads
// are not counted: the handle never leaves the call that reads it.

// fileKey identifies a file by device and inode.
type fileKey struct{ dev, ino uint64 }

func keyOf(fi os.FileInfo) (fileKey, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileKey{}, false
	}
	return fileKey{uint64(st.Dev), uint64(st.Ino)}, true
}

// openFiles counts the readers of each open store file and the bytes of the
// open ones that have been removed.
type openFiles struct {
	gate    sync.RWMutex // read: open and count; write: remove and check
	mu      sync.Mutex
	refs    map[fileKey]int
	retired map[fileKey]int64
}

// File is an open store file. Close it when done: until then a removal of
// the file keeps its bytes charged.
type File struct {
	*os.File
	s       *Store
	key     fileKey
	tracked bool
	once    sync.Once
}

// Close closes the file and, for the last reader of a removed file, releases
// its bytes.
func (f *File) Close() error {
	err := f.File.Close()
	if f.tracked {
		f.once.Do(func() { f.s.readerClosed(f.key) })
	}
	return err
}

// openTracked runs open and counts the file it opens as read, both under the
// read lock.
func (s *Store) openTracked(open func() (*os.File, os.FileInfo, error)) (*File, os.FileInfo, error) {
	o := &s.open
	o.gate.RLock()
	defer o.gate.RUnlock()
	f, fi, err := open()
	if err != nil {
		return nil, nil, err
	}
	tf := &File{File: f, s: s}
	if k, ok := keyOf(fi); ok {
		o.mu.Lock()
		if o.refs == nil {
			o.refs = make(map[fileKey]int)
		}
		o.refs[k]++
		o.mu.Unlock()
		tf.key, tf.tracked = k, true
	}
	return tf, fi, nil
}

// readerClosed drops a reader of k and, for the last reader of a retired
// file, releases its bytes. The release happens under o.mu, like the retiring
// in unlinkFile, so the two can never cross (lock order: o.mu, then q.mu).
func (s *Store) readerClosed(k fileKey) {
	o := &s.open
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.refs[k]--; o.refs[k] > 0 {
		return
	}
	delete(o.refs, k)
	if n := o.retired[k]; n > 0 {
		delete(o.retired, k)
		s.releaseRetired(n)
	}
}

// unlinkFile removes path. For a regular file it returns its size, and
// whether a reader still holds it open. A held file is retired here: under
// o.mu, recorded for its last reader and its bytes moved from the live count
// to the retired one in one step, so the caller must not release them. A
// file already gone is not an error and reports removed false.
//
// counted is how much of the file this process's usage included (ledger.go),
// which is what the caller releases (or, for a retired file, what retire
// moved).
func (s *Store) unlinkFile(path string) (size, counted int64, removed, retired bool, err error) {
	o := &s.open
	o.gate.Lock()
	defer o.gate.Unlock()
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, false, false, nil
		}
		return 0, 0, false, false, err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return 0, 0, false, false, nil
		}
		return 0, 0, false, false, err
	}
	if !fi.Mode().IsRegular() {
		return 0, 0, true, false, nil
	}
	if k, ok := keyOf(fi); ok {
		o.mu.Lock()
		if o.refs[k] > 0 {
			if o.retired == nil {
				o.retired = make(map[fileKey]int64)
			}
			o.retired[k] += fi.Size()
			counted = s.uncountFile(path, fi.Size())
			s.retire(counted, fi.Size())
			retired = true
		}
		o.mu.Unlock()
	}
	if retired && afterRetire != nil {
		afterRetire()
	}
	if !retired {
		counted = s.uncountFile(path, fi.Size())
	}
	return fi.Size(), counted, true, retired, nil
}

// afterRetire is a test hook run after unlinkFile retires a file, before its
// caller ends the transition.
var afterRetire func()
