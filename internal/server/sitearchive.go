// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

// Bounds on parsing an uploaded site archive, applied before archive/zip or
// archive/tar builds anything from it. A body within --max-upload can still
// describe millions of tiny entries, and archive/zip reads a zip's whole
// central directory into memory before any per-file check runs; archive/tar
// accepts PAX and GNU long-name headers of up to 1 MiB each and expands PAX
// records into a map. So the archive's structure is checked first, with
// parsing whose cost is bounded by the caps below, and only a few archives
// are parsed at once.

const (
	// maxSiteEntries caps the entries one site archive may hold, counting
	// every entry: files, directories, links and other special entries.
	maxSiteEntries = 4096
	// maxZipDirBytes caps a zip's central directory: room for maxSiteEntries
	// entries with paths of a few hundred bytes. archive/zip holds about five
	// times the directory's size in memory while it reads it.
	maxZipDirBytes = 2 << 20
	// maxTarHeaders caps a tar's header records, special ones (PAX, GNU long
	// names) included: a PAX header and a long name for every entry fit.
	maxTarHeaders = 3 * maxSiteEntries
	// maxTarSpecialBytes caps the data of one PAX, global PAX or GNU
	// long-name/long-link header (archive/tar's own cap is 1 MiB).
	maxTarSpecialBytes = 64 << 10
	// siteParseSlots is how many site archives are parsed and extracted at
	// once; more wait up to sitePublishWait and are then refused with 503.
	siteParseSlots = 2
)

// sitePublishWait is how long a site publish waits for a parse slot. A var
// so tests can shorten it.
var sitePublishWait = 5 * time.Second

// errSiteBusy is the parse-slot refusal, answered 503.
var errSiteBusy = errors.New("site publishing is at capacity; retry shortly")

// errTarHeaderTooLarge refuses a tar special header over maxTarSpecialBytes.
var errTarHeaderTooLarge = fmt.Errorf("site archive has a header larger than %d bytes", maxTarSpecialBytes)

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

// checkTarHeaders walks a tar's header records without decoding entries: it
// counts every header, special ones included, against maxTarHeaders and
// refuses a PAX, global PAX or GNU long-name/long-link header whose data is
// over maxTarSpecialBytes. It follows the records archive/tar consumes (a PAX
// "size" record overriding the next entry's size, old GNU sparse extension
// blocks) and stops at the first zero block or anything it cannot read,
// which archive/tar then rejects.
func checkTarHeaders(ra io.ReaderAt, size int64) error {
	var (
		blk     [512]byte
		off     int64
		headers int
		paxSize int64 = -1
		zeroBlk [512]byte
	)
	for off+512 <= size {
		if _, err := ra.ReadAt(blk[:], off); err != nil {
			return nil
		}
		if blk == zeroBlk {
			return nil
		}
		headers++
		if headers > maxTarHeaders {
			return errTooManyFiles
		}
		n, ok := tarNumeric(blk[124:136])
		if !ok || n < 0 {
			return nil
		}
		off += 512
		switch typ := blk[156]; typ {
		case 'x', 'g', 'L', 'K':
			if n > maxTarSpecialBytes {
				return errTarHeaderTooLarge
			}
			if typ == 'x' {
				data := make([]byte, n)
				if _, err := ra.ReadAt(data, off); err != nil {
					return nil
				}
				paxSize = paxRecordSize(data)
			}
			off += (n + 511) &^ 511
			continue
		case 'S':
			// Old GNU sparse: extension blocks follow while the flag is set.
			for ext := blk[482] != 0; ext; {
				if _, err := ra.ReadAt(blk[:], off); err != nil {
					return nil
				}
				off += 512
				headers++
				if headers > maxTarHeaders {
					return errTooManyFiles
				}
				ext = blk[504] != 0
			}
		}
		if paxSize >= 0 {
			n = paxSize
		}
		paxSize = -1
		off += (n + 511) &^ 511
	}
	return nil
}

// tarNumeric parses a tar numeric field as archive/tar does: base-256 when
// the high bit is set, else octal with surrounding spaces and NULs ignored.
func tarNumeric(b []byte) (int64, bool) {
	if len(b) > 0 && b[0]&0x80 != 0 {
		if b[0]&0x40 != 0 {
			return 0, false // negative
		}
		var v uint64
		for i, c := range b {
			if i == 0 {
				c &= 0x7f
			}
			if v>>56 != 0 {
				return 0, false
			}
			v = v<<8 | uint64(c)
		}
		if v > 1<<62 {
			return 0, false
		}
		return int64(v), true
	}
	b = bytes.Trim(b, " \x00")
	if len(b) == 0 {
		return 0, true
	}
	v, err := strconv.ParseUint(string(b), 8, 62)
	if err != nil {
		return 0, false
	}
	return int64(v), true
}

// paxRecordSize returns the "size" record of a PAX header's data, or -1.
func paxRecordSize(data []byte) int64 {
	size := int64(-1)
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		if sp <= 0 {
			break
		}
		n, err := strconv.Atoi(string(data[:sp]))
		if err != nil || n <= sp+1 || n > len(data) {
			break
		}
		kv := data[sp+1 : n-1]
		if k, v, ok := bytes.Cut(kv, []byte("=")); ok && string(k) == "size" {
			if s, err := strconv.ParseInt(string(v), 10, 64); err == nil && s >= 0 {
				size = s
			}
		}
		data = data[n:]
	}
	return size
}
