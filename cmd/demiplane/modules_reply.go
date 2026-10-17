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
	"strings"
	"time"

	"github.com/daisandapex/demiplane/internal/limit"
	"github.com/daisandapex/demiplane/internal/modules/reply"
	"github.com/daisandapex/demiplane/internal/store"
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

// Reply admission and storage limits (defaults in reply.DefaultLimits):
//
//	reply_max_per_artifact = <n>        replies stored per artifact (1000)
//	reply_max_total        = <n>        replies stored in all (100000)
//	reply_max_bytes        = <bytes>    reply text stored in all (64 MiB)
//	reply_retention        = <ttl>|0    delete replies older than this (90d; 0 keeps them)
//	reply_rate_client      = <n/min>|0  submissions per client a minute (10; 0 = unlimited)
//	reply_rate_target      = <n/min>|0  submissions per artifact a minute (60)
//	reply_rate_global      = <n/min>|0  submissions in all a minute (300)
func init() {
	registerModuleConfig([]string{"reply_max_per_artifact", "reply_max_total", "reply_max_bytes",
		"reply_retention", "reply_rate_client", "reply_rate_target", "reply_rate_global"},
		func(cfg map[string]string) error {
			l, err := replyLimits(cfg)
			if err != nil {
				return err
			}
			return reply.ConfigureLimits(l)
		})
}

// replyLimits reads the reply_* limit keys, defaulting absent ones.
func replyLimits(cfg map[string]string) (reply.Limits, error) {
	l := reply.DefaultLimits()
	atoi := func(key string, dst *int) error {
		if v := cfg[key]; v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return fmt.Errorf("%s: want a positive integer, got %q", key, v)
			}
			*dst = n
		}
		return nil
	}
	if err := atoi("reply_max_per_artifact", &l.PerArtifact); err != nil {
		return l, err
	}
	if err := atoi("reply_max_total", &l.Total); err != nil {
		return l, err
	}
	if v := cfg["reply_max_bytes"]; v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return l, fmt.Errorf("reply_max_bytes: want a positive byte count, got %q", v)
		}
		l.TotalBytes = n
	}
	if v := strings.TrimSpace(cfg["reply_retention"]); v != "" {
		if v == "0" || strings.EqualFold(v, "off") {
			l.Retention = 0
		} else {
			d, err := store.ParseTTL(v)
			if err != nil || d <= 0 {
				return l, fmt.Errorf("reply_retention: want a duration such as 90d or 720h, or 0, got %q", v)
			}
			l.Retention = d
		}
	}
	for _, k := range []struct {
		key string
		dst *limit.Rate
	}{{"reply_rate_client", &l.RateClient}, {"reply_rate_target", &l.RateTarget}, {"reply_rate_global", &l.RateGlobal}} {
		if v := cfg[k.key]; v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return l, fmt.Errorf("%s: want submissions a minute (0 = unlimited), got %q", k.key, v)
			}
			*k.dst = limit.PerMinute(n, n)
		}
	}
	return l, nil
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
