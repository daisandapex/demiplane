// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type readFromRecorder struct {
	*httptest.ResponseRecorder
	readFrom int
}

func (r *readFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	r.readFrom++
	return io.Copy(r.ResponseRecorder, src)
}

// TestCappedWriteDeadlineKeepsReadFrom: the --write-timeout wrapper passes
// io.Copy through to the connection's ReadFrom (the sendfile path).
func TestCappedWriteDeadlineKeepsReadFrom(t *testing.T) {
	rec := &readFromRecorder{ResponseRecorder: httptest.NewRecorder()}
	var w http.ResponseWriter = &cappedWriteDeadline{ResponseWriter: rec, limit: time.Now().Add(time.Minute)}
	// A reader without WriteTo, as a file read by http.ServeContent would be
	// through its range reader, so io.Copy turns to the writer.
	if _, err := io.Copy(w, struct{ io.Reader }{strings.NewReader("bytes")}); err != nil {
		t.Fatal(err)
	}
	if rec.readFrom != 1 || rec.Body.String() != "bytes" {
		t.Errorf("ReadFrom calls %d, body %q; want 1 and the bytes", rec.readFrom, rec.Body.String())
	}
}
