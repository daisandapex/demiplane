// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix

package mcp

import "os"

// openNoBlock is zero outside Unix, where there is no O_NONBLOCK open flag.
const openNoBlock = 0

// onPseudoFS has no pseudo filesystems to refuse outside Unix.
func onPseudoFS(os.FileInfo) bool { return false }
