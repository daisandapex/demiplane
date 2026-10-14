// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix

package mcp

import "os"

// openNoBlock is zero where opening a special file does not block on a peer.
const openNoBlock = 0

// onPseudoFS has no pseudo filesystems to refuse outside Unix.
func onPseudoFS(os.FileInfo) bool { return false }
