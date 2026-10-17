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
	"fmt"
	"strconv"
	"time"

	"github.com/daisandapex/demiplane/internal/modules/reply"
)

func init() {
	// Reply-event hook (fires on every durably recorded reply, async):
	//   reply_hook_exec    = <command>     run via /bin/sh -c; reply JSON on stdin,
	//                                     DEMIPLANE_REPLY_{ID,SLUG,KIND,BODY} in env
	//   reply_hook_url     = <http(s) URL> reply JSON POSTed to it
	//   reply_hook_timeout = <duration>    bound on one exec run (default 5m)
	//   reply_hook_workers = <n>           events run at once (default 2)
	//   reply_hook_queue   = <n>           admitted events waiting (default 32)
	registerModuleConfig([]string{"reply_hook_exec", "reply_hook_url",
		"reply_hook_timeout", "reply_hook_workers", "reply_hook_queue"},
		func(cfg map[string]string) error {
			opts, err := replyHookOptions(cfg)
			if err != nil {
				return err
			}
			return reply.ConfigureHook(cfg["reply_hook_exec"], cfg["reply_hook_url"], opts)
		})
}

// replyHookOptions reads the reply_hook_* pool keys, defaulting absent ones.
func replyHookOptions(cfg map[string]string) (reply.HookOptions, error) {
	opts := reply.DefaultHookOptions()
	if v := cfg["reply_hook_timeout"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return opts, fmt.Errorf("reply_hook_timeout: %w", err)
		}
		opts.Timeout = d
	}
	for _, k := range []struct {
		key string
		dst *int
	}{{"reply_hook_workers", &opts.Workers}, {"reply_hook_queue", &opts.Queue}} {
		if v := cfg[k.key]; v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, fmt.Errorf("%s: want an integer, got %q", k.key, v)
			}
			*k.dst = n
		}
	}
	return opts, nil
}
