// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package mcp

import (
	"os"
	"syscall"
)

// pseudoFSDirs are mount points whose files expose process and kernel state
// (the process environment, memory maps, device nodes) rather than documents.
var pseudoFSDirs = []string{"/proc", "/sys", "/dev"}

// onPseudoFS reports whether fi lives on the same filesystem as /proc, /sys or
// /dev. A pseudo directory that shares the root filesystem's device (not a
// separate mount, as in a minimal chroot) is ignored, so an unmounted /proc
// cannot turn this into a refusal of every file.
func onPseudoFS(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	var rootSt syscall.Stat_t
	if err := syscall.Stat("/", &rootSt); err != nil {
		return false
	}
	for _, d := range pseudoFSDirs {
		var ds syscall.Stat_t
		if err := syscall.Stat(d, &ds); err != nil {
			continue
		}
		if uint64(ds.Dev) == uint64(rootSt.Dev) {
			continue
		}
		if uint64(ds.Dev) == uint64(st.Dev) {
			return true
		}
	}
	return false
}
