// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/store"
)

// TestSiteArchiveCountsEveryEntry: directories and other non-regular entries
// count toward the per-archive entry cap, in zip and in tar, so an archive of
// thousands of directory entries is refused like one of thousands of files.
func TestSiteArchiveCountsEveryEntry(t *testing.T) {
	ts := newTestServer(t, "")

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	for i := 0; i < maxSiteEntries+5; i++ {
		if _, err := zw.Create(fmt.Sprintf("d%d/", i)); err != nil {
			t.Fatal(err)
		}
	}
	w, _ := zw.Create("index.html")
	w.Write([]byte("<p>hi</p>"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if resp, rb := postSite(t, ts.URL, "dirs-zip", "fmt=zip", "application/zip", zbuf.Bytes()); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("zip of %d directory entries: status %d (%s), want 413", maxSiteEntries+5, resp.StatusCode, rb)
	}

	var tbuf bytes.Buffer
	tw := tar.NewWriter(&tbuf)
	for i := 0; i < maxSiteEntries+5; i++ {
		if err := tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("d%d/", i), Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
	}
	tw.WriteHeader(&tar.Header{Name: "index.html", Mode: 0o644, Size: 9, Typeflag: tar.TypeReg})
	tw.Write([]byte("<p>hi</p>"))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if resp, rb := postSite(t, ts.URL, "dirs-tar", "fmt=tar", "application/x-tar", tbuf.Bytes()); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("tar of %d directory entries: status %d (%s), want 413", maxSiteEntries+5, resp.StatusCode, rb)
	}
}

// rawCentralDirectoryZip builds a zip that is only a central directory of n
// directory entries named "a/" and an end record declaring count and cdSize
// (which may understate the real directory, as a hostile archive would).
func rawCentralDirectoryZip(n int, count uint16, cdSize uint32) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		writeU32(&b, 0x02014b50)
		writeU16(&b, 20) // version made by
		writeU16(&b, 20) // version needed
		writeU16(&b, 0)  // flags
		writeU16(&b, 0)  // method
		writeU16(&b, 0)  // time
		writeU16(&b, 0)  // date
		writeU32(&b, 0)  // crc
		writeU32(&b, 0)  // compressed size
		writeU32(&b, 0)  // uncompressed size
		writeU16(&b, 2)  // name length
		writeU16(&b, 0)  // extra length
		writeU16(&b, 0)  // comment length
		writeU16(&b, 0)  // disk
		writeU16(&b, 0)  // internal attributes
		writeU32(&b, 0)  // external attributes
		writeU32(&b, 0)  // local header offset
		b.WriteString("a/")
	}
	writeU32(&b, 0x06054b50)
	writeU16(&b, 0)
	writeU16(&b, 0)
	writeU16(&b, count)
	writeU16(&b, count)
	writeU32(&b, cdSize)
	writeU32(&b, 0) // central directory offset
	writeU16(&b, 0)
	return b.Bytes()
}

// TestZipCentralDirectoryParseIsBounded: a zip whose central directory holds
// a quarter of a million tiny entries is refused as too many entries without
// the parse allocating anything near what describing them all would take,
// whether its end record states the directory's size honestly or understates
// both the size and (modulo 65536) the count.
func TestZipCentralDirectoryParseIsBounded(t *testing.T) {
	const n = 1 << 18
	for _, tc := range []struct {
		name   string
		cdSize uint32
	}{
		{"honest directory size", n * 48},
		{"understated directory size", 48},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := rawCentralDirectoryZip(n, uint16(n%65536), tc.cdSize)
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			sw, err := st.NewSiteWriter("bomb")
			if err != nil {
				t.Fatal(err)
			}
			defer sw.Abort()

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			err = extractZip(sw, bytes.NewReader(archive), int64(len(archive)))
			runtime.ReadMemStats(&after)
			if !errors.Is(err, errTooManyFiles) {
				t.Errorf("extractZip = %v, want errTooManyFiles", err)
			}
			if got := after.TotalAlloc - before.TotalAlloc; got > 24<<20 {
				t.Errorf("parsing the central directory allocated %d MiB, want under 24 MiB", got>>20)
			}
		})
	}
}

// rawTarHeader is a ustar header block for name with the given type and
// size field.
func rawTarHeader(name string, typ byte, size int64) []byte {
	b := make([]byte, 512)
	copy(b, name)
	copy(b[100:], "0000644\x00")
	copy(b[108:], "0000000\x00")
	copy(b[116:], "0000000\x00")
	copy(b[124:], fmt.Sprintf("%011o\x00", size))
	copy(b[136:], "00000000000\x00")
	b[156] = typ
	copy(b[257:], "ustar\x0000")
	copy(b[148:], "        ")
	var sum int64
	for _, c := range b {
		sum += int64(c)
	}
	copy(b[148:], fmt.Sprintf("%06o\x00 ", sum))
	return b
}

func tarPad(b *bytes.Buffer) {
	if r := b.Len() % 512; r != 0 {
		b.Write(make([]byte, 512-r))
	}
}

// paxRecord is one "<len> key=value\n" PAX record.
func paxRecord(key, value string) string {
	rec := " " + key + "=" + value + "\n"
	n := len(rec) + 1
	for len(fmt.Sprint(n))+len(rec) != n {
		n++
	}
	return fmt.Sprint(n) + rec
}

// TestSiteTarSpecialHeaderCap: a PAX header over the cap is refused with 413,
// also when it follows an entry whose PAX "size" record overrides its header's
// size field (the header walk must follow the override to find it).
func TestSiteTarSpecialHeaderCap(t *testing.T) {
	ts := newTestServer(t, "")
	var b bytes.Buffer
	sizeRec := paxRecord("size", "5")
	b.Write(rawTarHeader("PaxHeaders/a.txt", 'x', int64(len(sizeRec))))
	b.WriteString(sizeRec)
	tarPad(&b)
	b.Write(rawTarHeader("a.txt", '0', 0))
	b.WriteString("hello")
	tarPad(&b)
	big := paxRecord("comment", string(bytes.Repeat([]byte("c"), 70000)))
	b.Write(rawTarHeader("PaxHeaders/index.html", 'x', int64(len(big))))
	b.WriteString(big)
	tarPad(&b)
	b.Write(rawTarHeader("index.html", '0', 9))
	b.WriteString("<p>hi</p>")
	tarPad(&b)
	b.Write(make([]byte, 1024))
	resp, rb := postSite(t, ts.URL, "pax-big", "fmt=tar", "application/x-tar", b.Bytes())
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("tar with a %d-byte PAX header: status %d (%s), want 413", len(big), resp.StatusCode, rb)
	}
}

// TestSiteTarWithPAXHeadersStillPublishes: an ordinary tar whose entries carry
// PAX headers (long names, extended attributes) publishes, and its files are
// served, so the header walk keeps in step with real archives.
func TestSiteTarWithPAXHeadersStillPublishes(t *testing.T) {
	ts := newTestServer(t, "")
	long := strings.Repeat("deep-deep-deep-deep-deep/", 5) + "page.html" // over the 100-byte ustar name field
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, f := range []struct{ name, body string }{{"index.html", "<p>home</p>"}, {long, "<p>long</p>"}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg,
			Format: tar.FormatPAX, PAXRecords: map[string]string{"SCHILY.xattr.user.note": "x"}}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(f.body))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	resp, rb := postSite(t, ts.URL, "pax-ok", "fmt=tar", "application/x-tar", b.Bytes())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PAX tar: status %d (%s), want 201", resp.StatusCode, rb)
	}
	got, err := http.Get(ts.URL + "/pax-ok/" + long)
	if err != nil {
		t.Fatal(err)
	}
	got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Errorf("long-named file from a PAX tar: %d, want 200", got.StatusCode)
	}
}

// TestZip64DeclaredEntriesAreChecked: a zip64 end record declaring more than
// maxSiteEntries entries is refused before archive/zip reads it.
func TestZip64DeclaredEntriesAreChecked(t *testing.T) {
	var b bytes.Buffer
	rec64 := b.Len()
	writeU32(&b, 0x06064b50)
	b.Write(make([]byte, 8+2+2+4+4)) // record size, versions, disk numbers
	binary.Write(&b, binary.LittleEndian, uint64(100000))
	binary.Write(&b, binary.LittleEndian, uint64(100000))
	binary.Write(&b, binary.LittleEndian, uint64(0)) // directory size
	binary.Write(&b, binary.LittleEndian, uint64(0)) // directory offset
	writeU32(&b, 0x07064b50)
	writeU32(&b, 0)
	binary.Write(&b, binary.LittleEndian, uint64(rec64))
	writeU32(&b, 1)
	writeU32(&b, 0x06054b50)
	writeU16(&b, 0)
	writeU16(&b, 0)
	writeU16(&b, 0xffff)
	writeU16(&b, 0xffff)
	writeU32(&b, 0xffffffff)
	writeU32(&b, 0xffffffff)
	writeU16(&b, 0)
	if err := checkZipDirectory(bytes.NewReader(b.Bytes()), int64(b.Len())); !errors.Is(err, errTooManyFiles) {
		t.Errorf("zip64 end record declaring 100000 entries: %v, want errTooManyFiles", err)
	}
}

// TestSitePublishBusyIs503: with every parse slot taken, a site publish waits
// briefly and is then refused with 503 and Retry-After.
func TestSitePublishBusyIs503(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := New(st, Config{})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	old := sitePublishWait
	sitePublishWait = 50 * time.Millisecond
	t.Cleanup(func() { sitePublishWait = old })
	for i := 0; i < cap(srv.siteSem); i++ {
		srv.siteSem <- struct{}{}
	}
	resp, rb := postSite(t, ts.URL, "busy", "fmt=tar", "application/x-tar", makeTar(t, threeFileSite()))
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Errorf("publish with no free slot: %d %q (%s), want 503 with Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"), rb)
	}
	<-srv.siteSem
	if resp, rb := postSite(t, ts.URL, "busy", "fmt=tar", "application/x-tar", makeTar(t, threeFileSite())); resp.StatusCode != http.StatusCreated {
		t.Errorf("publish with a free slot: %d (%s), want 201", resp.StatusCode, rb)
	}
}

// TestSiteTarHeaderOnlyEntryCannotHideHeaders: a directory (or other
// header-only) entry declaring a nonzero size has no data to archive/tar,
// which reads the next header straight after it. An oversized PAX header
// placed there is still refused.
func TestSiteTarHeaderOnlyEntryCannotHideHeaders(t *testing.T) {
	ts := newTestServer(t, "")
	for _, typ := range []byte{'5', '2', '1', '3', '4', '6'} {
		var inner bytes.Buffer
		big := paxRecord("comment", string(bytes.Repeat([]byte("c"), 70000)))
		inner.Write(rawTarHeader("PaxHeaders/index.html", 'x', int64(len(big))))
		inner.WriteString(big)
		tarPad(&inner)

		var b bytes.Buffer
		b.Write(rawTarHeader("d/", typ, int64(inner.Len())))
		b.Write(inner.Bytes())
		b.Write(rawTarHeader("index.html", '0', 9))
		b.WriteString("<p>hi</p>")
		tarPad(&b)
		b.Write(make([]byte, 1024))
		resp, rb := postSite(t, ts.URL, "hidden-"+string(rune('a'+typ-'1')), "fmt=tar", "application/x-tar", b.Bytes())
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("type %q entry with a nonzero size hiding a %d-byte PAX header: status %d (%s), want 413",
				typ, len(big), resp.StatusCode, rb)
		}
	}
}

// tarStep is one header archive/tar returned, with the archive offset it
// had reached.
type tarStep struct {
	name string
	typ  byte
	size int64
	off  int64
	err  string
}

// walkTar lists the headers archive/tar returns for archive, reading through
// the header budget when budgeted.
func walkTar(archive []byte, budgeted bool) []tarStep {
	f := bytes.NewReader(archive)
	var steps []tarStep
	var tr *tar.Reader
	var hr *tarHeaderReader
	if budgeted {
		hr = &tarHeaderReader{f: f}
		tr = tar.NewReader(hr)
	} else {
		tr = tar.NewReader(f)
	}
	for len(steps) < 1000 {
		var hdr *tar.Header
		var err error
		if budgeted {
			hdr, err = hr.next(tr)
		} else {
			hdr, err = tr.Next()
		}
		off, _ := f.Seek(0, io.SeekCurrent)
		if err != nil {
			steps = append(steps, tarStep{err: err.Error(), off: off})
			return steps
		}
		steps = append(steps, tarStep{name: hdr.Name, typ: hdr.Typeflag, size: hdr.Size, off: off})
	}
	return steps
}

// randomTar builds an archive of assorted entries, including header-only
// types with nonzero sizes, legacy type-0 entries with and without a trailing
// slash, PAX size and path overrides, GNU long names and global headers.
func randomTar(rng *rand.Rand) []byte {
	var b bytes.Buffer
	n := 1 + rng.Intn(8)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("e%d", i)
		size := int64(rng.Intn(3) * rng.Intn(1500))
		data := bytes.Repeat([]byte{byte('a' + i)}, int(size))
		switch rng.Intn(6) {
		case 0: // PAX header, maybe overriding size and path
			var recs string
			if rng.Intn(2) == 0 {
				size = int64(rng.Intn(1200))
				data = bytes.Repeat([]byte{'p'}, int(size))
				recs += paxRecord("size", fmt.Sprint(size))
			}
			if rng.Intn(2) == 0 {
				name += "/"
				recs += paxRecord("path", name)
			}
			recs += paxRecord("comment", strings.Repeat("c", rng.Intn(3000)))
			b.Write(rawTarHeader("PaxHeaders/"+name, 'x', int64(len(recs))))
			b.WriteString(recs)
			tarPad(&b)
		case 1: // GNU long name
			long := strings.Repeat("l", 100+rng.Intn(300))
			if rng.Intn(2) == 0 {
				long += "/"
			}
			b.Write(rawTarHeader("././@LongLink", 'L', int64(len(long)+1)))
			b.WriteString(long + "\x00")
			tarPad(&b)
		case 2: // global header
			g := paxRecord("comment", "global")
			b.Write(rawTarHeader("pax_global_header", 'g', int64(len(g))))
			b.WriteString(g)
			tarPad(&b)
		}
		types := []byte{'0', '5', '2', '1', '3', '4', '6', 0, 0}
		typ := types[rng.Intn(len(types))]
		if typ == '0' && rng.Intn(3) == 0 {
			// Data well over the header budget, which archive/tar skips
			// rather than reads, so it must not count.
			size = 100000 + int64(rng.Intn(50000))
			data = bytes.Repeat([]byte{'d'}, int(size))
		}
		if typ == 0 && rng.Intn(2) == 0 {
			name += "/"
		}
		b.Write(rawTarHeader(name, typ, size))
		// Header-only types get their declared bytes anyway, as a hostile
		// archive would; archive/tar reads them as the next header.
		b.Write(data)
		tarPad(&b)
	}
	b.Write(make([]byte, 1024))
	return b.Bytes()
}

// TestTarHeaderBudgetMatchesArchiveTar: for a few hundred generated archives
// under the budget, reading through tarHeaderReader returns exactly the
// headers, offsets and errors archive/tar returns on its own, so the budget
// never changes how an archive is read.
func TestTarHeaderBudgetMatchesArchiveTar(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 400; i++ {
		archive := randomTar(rng)
		plain, budgeted := walkTar(archive, false), walkTar(archive, true)
		if fmt.Sprint(plain) != fmt.Sprint(budgeted) {
			t.Fatalf("archive %d: archive/tar read\n%v\nbut through the budget\n%v", i, plain, budgeted)
		}
	}
}
