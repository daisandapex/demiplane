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
	//   reply_hook_log_output = on     log the exec hook's output on failure
	//                                  (off by default; it often echoes answers)
	registerModuleConfig([]string{"reply_hook_exec", "reply_hook_url", "reply_hook_log_output"},
		func(cfg map[string]string) error {
			logOutput := false
			if v := cfg["reply_hook_log_output"]; v != "" {
				b, err := parseOnOff("reply_hook_log_output", v)
				if err != nil {
					return err
				}
				logOutput = b
			}
			return reply.ConfigureHook(cfg["reply_hook_exec"], cfg["reply_hook_url"], logOutput)
		})
}
