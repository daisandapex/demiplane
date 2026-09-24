// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import "testing"

// setPublishRoot confines the client's `path` publishes to dir.
func setPublishRoot(t *testing.T, c *Client, dir string) {
	t.Helper()
	c.PublishRoot = dir
}
