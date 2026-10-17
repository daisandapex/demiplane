// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import "context"

// Markdown renders with the default limits for tests whose documents are far
// inside them; a limit error fails loudly rather than yielding an empty page.
func Markdown(src []byte, opts Options) []byte {
	out, err := Render(context.Background(), src, opts)
	if err != nil {
		panic(err)
	}
	return out
}
