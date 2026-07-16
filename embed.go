// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package demiplane embeds the project's user-facing markdown docs so a deployed
// binary can serve them at /docs without the repository checked out. The embed
// directive must live at the module root because go:embed cannot reference
// parent directories — this keeps the repo's own .md files the single source of
// truth (edit a doc, rebuild, /docs updates).
package demiplane

import "embed"

// DocsFS holds the curated, user-facing docs rendered at /docs. Internal build
// briefs under docs/ (KICKOFF-*, HARDEN-*, PREFLIP-*, V1.1-*) are deliberately
// NOT embedded.
//
//go:embed README.md API.md SECURITY.md CONTRIBUTING.md CHANGELOG.md
var DocsFS embed.FS
