// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
)

// A listing without a token and without ?slug= filters by artifact, which
// takes a lookup in the core store per slug, so one request examines at most
// listScanRows replies and looks up at most listScanSlugs slugs. When it
// stops there, the page holds fewer replies than listPageSize (perhaps none)
// and next resumes after the last reply examined, which may be one the
// caller may not see; such cursors are sealed (sealedCursorPrefix), so
// neither its time nor its id is handed out.
const (
	listScanBatch = 256  // rows read per query while filtering
	listScanRows  = 1024 // most replies one such listing examines
	listScanSlugs = 128  // most slugs one such listing looks up
)

// sealedCursorPrefix starts a sealed cursor; a plain one starts with a digit
// or '-'.
const sealedCursorPrefix = "s"

// listVisible is one page of the replies a caller without a token may see
// when it names no slug: written to an unprotected artifact (protected = 0,
// in the query) that is public now (publicNow, per slug). It stops when ctx
// ends, and at the bounds above with a cursor to resume from.
func (m *Module) listVisible(ctx context.Context, status string, after *listCursor) ([]Reply, *listCursor, error) {
	public := map[string]bool{}
	var out []Reply
	pos := after
	examined := 0
	for {
		batch, more, err := m.rs.listUnprotected(ctx, status, pos, listScanBatch)
		if err != nil {
			return nil, nil, err
		}
		for i, r := range batch {
			if examined == listScanRows {
				return out, pos, nil
			}
			vis, known := public[r.Slug]
			if !known {
				if len(public) == listScanSlugs {
					return out, pos, nil
				}
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
				vis = m.publicNow(r.Slug)
				public[r.Slug] = vis
			}
			examined++
			pos = &listCursor{created: r.CreatedAt.Unix(), id: r.ID}
			if !vis {
				continue
			}
			out = append(out, r)
			if len(out) == listPageSize {
				if i < len(batch)-1 || more != nil {
					return out, pos, nil
				}
				return out, nil, nil
			}
		}
		if more == nil {
			return out, nil, nil
		}
	}
}

// cursorAEAD returns the per-process key sealed cursors are sealed with. A
// sealed cursor does not outlive the process: after a restart it is refused
// as malformed, and the listing starts again from the top.
func (m *Module) cursorAEAD() (cipher.AEAD, error) {
	m.cursorOnce.Do(func() {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			m.cursorErr = err
			return
		}
		b, err := aes.NewCipher(key)
		if err != nil {
			m.cursorErr = err
			return
		}
		m.cursorKey, m.cursorErr = cipher.NewGCM(b)
	})
	return m.cursorKey, m.cursorErr
}

// sealCursor returns c as a sealed cursor.
func (m *Module) sealCursor(c listCursor) (string, error) {
	aead, err := m.cursorAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(c.String())+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return sealedCursorPrefix + base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(c.String()), nil)), nil
}

// parseCursor parses an ?after= value, plain or sealed.
func (m *Module) parseCursor(v string) (listCursor, error) {
	sealed, ok := strings.CutPrefix(v, sealedCursorPrefix)
	if !ok {
		return parseListCursor(v)
	}
	bad := errors.New("invalid cursor")
	if len(sealed) > 128 {
		return listCursor{}, bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return listCursor{}, bad
	}
	aead, err := m.cursorAEAD()
	if err != nil || len(raw) < aead.NonceSize() {
		return listCursor{}, bad
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		return listCursor{}, bad
	}
	return parseListCursor(string(plain))
}
