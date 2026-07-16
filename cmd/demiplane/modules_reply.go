// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build reply

package main

// The inline-reply module is opt-in: it is compiled into the binary only when
// built with `-tags reply` (go build -tags reply ./cmd/demiplane). The default
// build omits its code and routes entirely, keeping core tiny. See ADR 0001
// (inclusion mechanism) and ADR 0002 (the module).
//
// Importing the package registers the module (its init calls module.Register);
// this file also wires the module's config-file keys into the config surface,
// so `reply_hook_*` is recognized exactly when the module is in the binary.
import (
	"github.com/daisandapex/demiplane/internal/modules/reply"
)

func init() {
	// Reply-event hook (fires on every durably recorded reply, async):
	//   reply_hook_exec = <command>    run via /bin/sh -c; reply JSON on stdin,
	//                                  DEMIPLANE_REPLY_{ID,SLUG,KIND,BODY} in env
	//   reply_hook_url  = <http(s) URL> reply JSON POSTed to it
	registerModuleConfig([]string{"reply_hook_exec", "reply_hook_url"},
		func(cfg map[string]string) error {
			return reply.ConfigureHook(cfg["reply_hook_exec"], cfg["reply_hook_url"])
		})
}
