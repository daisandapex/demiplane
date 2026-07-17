// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

// Package demiplane embeds the project's user-facing markdown docs so a deployed
// binary can serve them at /docs without the repository checked out. The embed
// directive must live at the module root because go:embed cannot reference
// parent directories — this keeps the repo's own .md files the single source of
// truth (edit a doc, rebuild, /docs updates).
package demiplane

import (
	"embed"
	"encoding/base64"
)

// DocsFS holds the curated, user-facing docs rendered at /docs. Internal build
// briefs under docs/ (KICKOFF-*, HARDEN-*, PREFLIP-*, V1.1-*) are deliberately
// NOT embedded.
//
//go:embed README.md API.md SECURITY.md CONTRIBUTING.md CHANGELOG.md
var DocsFS embed.FS

// BrandFS holds the icon assets served on the control plane (and the combined
// same-origin handler): /favicon.svg, /favicon.ico, /apple-touch-icon.png —
// the three a browser fetches by convention. The canonical brand sources all
// live in assets/brand/; embedding at the module root keeps the served bytes
// byte-identical to the committed files (regenerate the raster, rebuild, routes
// update), the same single-source pattern DocsFS uses.
//
//go:embed assets/brand/favicon.svg assets/brand/favicon.ico assets/brand/apple-touch-icon.png
var BrandFS embed.FS

// faviconSVG is the theme-adaptive line-art favicon (transparent; its ink
// follows prefers-color-scheme via an internal <style>). It is embedded a second
// time as raw bytes so FaviconDataURI can inline it — a data: URI keeps a
// published artifact's tab icon working even when the page is saved and opened
// offline, which is the whole point of demiplane's self-contained HTML.
//
//go:embed assets/brand/favicon.svg
var faviconSVG []byte

// FaviconDataURI is the favicon as a self-contained data: URI, computed once from
// the embedded SVG so it never drifts from the committed file. Rendered pages
// reference it in a <link rel="icon">; because it carries its own bytes it needs
// no served /favicon route and works on any origin (including the isolated
// content origin) and offline.
var FaviconDataURI = "data:image/svg+xml;base64," +
	base64.StdEncoding.EncodeToString(faviconSVG)
