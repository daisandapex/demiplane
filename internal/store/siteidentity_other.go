// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix

package store

import "os"

// Release platforms are Unix; other platforms retain the publish timestamp.
func siteFileIdentity(fi os.FileInfo) string { return fi.Name() }
