// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"archive/tar"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// Bounds on parsing an uploaded site archive, applied before archive/zip or
// archive/tar builds anything from it. A body within --max-upload can still
// describe millions of tiny entries, and archive/zip reads a zip's whole
// central directory into memory before any per-file check runs; archive/tar
// accepts PAX and GNU long-name headers of up to 1 MiB each and expands PAX
// records into a map. So a zip's directory is checked first and its read
// limited, archive/tar's header reads are limited per entry, and only a few
// archives are parsed at once.

const (
	// maxSiteEntries caps the entries one site archive may hold, counting
	// every entry: files, directories, links and other special entries.
	maxSiteEntries = 4096
	// maxZipDirBytes caps a zip's central directory: room for maxSiteEntries
	// entries with paths of a few hundred bytes. archive/zip holds about five
	// times the directory's size in memory while it reads it.
	maxZipDirBytes = 2 << 20
	// maxTarHeaderBytes caps the 512-byte blocks archive/tar reads to reach
	// one entry, from its first header block: its header and any PAX, global
	// PAX and GNU long-name/long-link headers in front of it, with their data
	// and padding (archive/tar's own cap is 1 MiB for each).
	maxTarHeaderBytes = 64 << 10
	// siteParseSlots is how many site archives are parsed and extracted at
	// once; more wait up to sitePublishWait and are then refused with 503.
	siteParseSlots = 2
)

// sitePublishWait is how long a site publish waits for a parse slot. A var
// so tests can shorten it.
var sitePublishWait = 5 * time.Second

// errSiteBusy is the parse-slot refusal, answered 503.
var errSiteBusy = errors.New("site publishing is at capacity; retry shortly")

// errTarHeaderTooLarge refuses a tar entry whose headers exceed
// maxTarHeaderBytes.
var errTarHeaderTooLarge = fmt.Errorf("site archive has an entry with more than %d bytes of headers", maxTarHeaderBytes)

// acquireSiteParse takes a parse slot, waiting up to sitePublishWait.
func (s *Server) acquireSiteParse(ctx context.Context) (release func(), err error) {
	select {
	case s.siteSem <- struct{}{}:
		return func() { <-s.siteSem }, nil
	default:
	}
	t := time.NewTimer(sitePublishWait)
	defer t.Stop()
	select {
	case s.siteSem <- struct{}{}:
		return func() { <-s.siteSem }, nil
	case <-t.C:
		return nil, errSiteBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// --- zip -------------------------------------------------------------------

// checkZipDirectory reads a zip's end-of-central-directory record (and its
// zip64 form) the way archive/zip locates them, and refuses an archive that
// declares more than maxSiteEntries entries or a central directory over
// maxZipDirBytes. An archive whose end record cannot be found is left for
// zip.NewReader to reject. The declared values can understate the real
// directory, which archive/zip reads until a header fails to parse, so
// openZip also limits what that read may consume.
func checkZipDirectory(ra io.ReaderAt, size int64) error {
	end, endOff := findZipEnd(ra, size)
	if end == nil {
		return nil
	}
	records := uint64(binary.LittleEndian.Uint16(end[10:]))
	dirSize := uint64(binary.LittleEndian.Uint32(end[12:]))
	dirOff := uint64(binary.LittleEndian.Uint32(end[16:]))
	if records == 0xffff || dirSize == 0xffff || dirOff == 0xffffffff {
		if r64, s64, ok := readZip64End(ra, endOff); ok {
			records, dirSize = r64, s64
		}
	}
	if records > maxSiteEntries || dirSize > maxZipDirBytes {
		return errTooManyFiles
	}
	return nil
}

// findZipEnd returns the 22-byte end-of-central-directory record and its
// offset, found as archive/zip finds it: the last signature in the final
// 1 KiB, else in the final 65 KiB, whose comment fits in the block.
func findZipEnd(ra io.ReaderAt, size int64) ([]byte, int64) {
	for i, n := range []int64{1024, 65 * 1024} {
		if n > size {
			n = size
		}
		buf := make([]byte, n)
		if _, err := ra.ReadAt(buf, size-n); err != nil && err != io.EOF {
			return nil, 0
		}
		for p := len(buf) - 22; p >= 0; p-- {
			if buf[p] == 'P' && buf[p+1] == 'K' && buf[p+2] == 0x05 && buf[p+3] == 0x06 {
				if int(binary.LittleEndian.Uint16(buf[p+20:]))+22+p > len(buf) {
					return nil, 0
				}
				return buf[p : p+22], size - n + int64(p)
			}
		}
		if i == 1 || n == size {
			break
		}
	}
	return nil, 0
}

// readZip64End reads the zip64 end record's entry count and directory size
// through its locator just before the end record at endOff, accepting the
// same records archive/zip does.
func readZip64End(ra io.ReaderAt, endOff int64) (records, dirSize uint64, ok bool) {
	if endOff < 20 {
		return 0, 0, false
	}
	loc := make([]byte, 20)
	if _, err := ra.ReadAt(loc, endOff-20); err != nil {
		return 0, 0, false
	}
	if binary.LittleEndian.Uint32(loc) != 0x07064b50 || binary.LittleEndian.Uint32(loc[4:]) != 0 ||
		binary.LittleEndian.Uint32(loc[16:]) != 1 {
		return 0, 0, false
	}
	rec := make([]byte, 56)
	if _, err := ra.ReadAt(rec, int64(binary.LittleEndian.Uint64(loc[8:]))); err != nil ||
		binary.LittleEndian.Uint32(rec) != 0x06064b50 {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint64(rec[32:]), binary.LittleEndian.Uint64(rec[40:]), true
}

// readBudget is an io.ReaderAt that fails once more than limit bytes have
// been read through it while armed. It bounds what zip.NewReader may read:
// the end-record search, the zip64 records and the central directory.
type readBudget struct {
	r     io.ReaderAt
	armed bool
	left  int64
}

func (b *readBudget) ReadAt(p []byte, off int64) (int, error) {
	if b.armed {
		if int64(len(p)) > b.left {
			return 0, errTooManyFiles
		}
		b.left -= int64(len(p))
	}
	return b.r.ReadAt(p, off)
}

// zipOpenReadLimit is what zip.NewReader may read: the central directory cap
// plus the two end-record search blocks, the zip64 records, the directory
// offset probe and a read-ahead buffer.
const zipOpenReadLimit = maxZipDirBytes + 66*1024 + 1024 + 128 + 2*4096

// --- tar -------------------------------------------------------------------

// tarHeaderReader is the scratch file as archive/tar reads it during
// extraction. archive/tar alone decides what is a header and what is data,
// so nothing here re-implements its format rules (header-only types, legacy
// directories, PAX overrides, sparse maps). Instead, while armed, around
// each tar.Reader.Next, it allows at most maxTarHeaderBytes to be read from
// the first header block on: any PAX, global PAX and GNU long-name/long-link
// headers with their data and padding, a sparse map, and the entry's own
// header. Headers start on a 512-byte boundary, so what Next reads before the
// first boundary at or after its first read, the previous entry's padding and
// archive/tar's check of the last byte of skipped data, is not charged. The
// previous entry's unread data is not read but skipped, because archive/tar
// seeks past it when its reader can seek, so it does not count either.
// Disarmed, the entry data AddFile reads passes through unlimited (the
// decompressed budget bounds it). f must be at offset 0 when reading starts.
type tarHeaderReader struct {
	f       io.ReadSeeker
	pos     int64 // offset of the next byte f returns
	armed   bool
	started bool  // a read has happened since arming
	free    int64 // offset up to which reads are not charged
	left    int64
	tripped bool
}

func (t *tarHeaderReader) Read(p []byte) (int, error) {
	if !t.armed {
		n, err := t.f.Read(p)
		t.pos += int64(n)
		return n, err
	}
	if !t.started {
		t.started = true
		t.free = (t.pos + 511) &^ 511
	}
	free := t.free - t.pos
	if free < 0 {
		free = 0
	}
	if allowed := free + t.left; allowed <= 0 {
		t.tripped = true
		return 0, errTarHeaderTooLarge
	} else if int64(len(p)) > allowed {
		p = p[:allowed]
	}
	n, err := t.f.Read(p)
	t.pos += int64(n)
	if charged := int64(n) - free; charged > 0 {
		t.left -= charged
	}
	return n, err
}

func (t *tarHeaderReader) Seek(off int64, whence int) (int64, error) {
	pos, err := t.f.Seek(off, whence)
	if err == nil {
		t.pos = pos
	}
	return pos, err
}

// next is tr.Next with the header budget applied. A budget overrun is
// errTarHeaderTooLarge whatever archive/tar made of the failed read.
func (t *tarHeaderReader) next(tr *tar.Reader) (*tar.Header, error) {
	t.armed, t.started, t.left = true, false, maxTarHeaderBytes
	hdr, err := tr.Next()
	t.armed = false
	if t.tripped {
		return nil, errTarHeaderTooLarge
	}
	return hdr, err
}
