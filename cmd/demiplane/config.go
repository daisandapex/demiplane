// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daisandapex/demiplane/internal/theme"
)

// repoURL is the default vanity-footer link target.
const repoURL = "https://github.com/daisandapex/demiplane"

// configKeys is the set of recognized config-file keys. An unknown key is a hard
// error so typos surface loudly instead of silently doing nothing.
var configKeys = map[string]bool{
	"footer":      true,
	"footer_link": true,
	"theme":       true,
	"header":      true,
	"meta_header": true,
}

// moduleConfigKeys and moduleConfigAppliers extend the config surface for
// build-tag-gated modules. A module's cmd wiring file (e.g. modules_reply.go)
// registers its keys and an applier from init(), so a key is recognized exactly
// when its module is compiled into the binary — a config file referencing a
// module this build lacks fails loudly at startup, same as any typo.
var (
	moduleConfigKeys     = map[string]bool{}
	moduleConfigAppliers []func(cfg map[string]string) error
)

// registerModuleConfig declares module-owned config keys and the applier that
// consumes them. Call from a build-tagged wiring file's init(). The applier
// receives the whole parsed config map (absent keys read as "") and returns a
// hard startup error for an invalid value.
func registerModuleConfig(keys []string, apply func(cfg map[string]string) error) {
	for _, k := range keys {
		moduleConfigKeys[k] = true
	}
	if apply != nil {
		moduleConfigAppliers = append(moduleConfigAppliers, apply)
	}
}

// applyModuleConfig runs every registered module-config applier against the
// parsed config. Any error is a hard startup error (fail-loud contract).
func applyModuleConfig(cfg map[string]string) error {
	for _, apply := range moduleConfigAppliers {
		if err := apply(cfg); err != nil {
			return err
		}
	}
	return nil
}

// validConfigKeys lists every recognized key (core + compiled-in modules),
// sorted, for the unknown-key error message.
func validConfigKeys() string {
	keys := make([]string, 0, len(configKeys)+len(moduleConfigKeys))
	for k := range configKeys {
		keys = append(keys, k)
	}
	for k := range moduleConfigKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// configPath returns the XDG config-file path:
// ${XDG_CONFIG_HOME:-~/.config}/demiplane/config.
func configPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			// No home and no XDG override: nothing to read. Return a path that
			// won't exist so loadConfig falls through to defaults.
			return filepath.Join("demiplane", "config")
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "demiplane", "config")
}

// loadConfig reads the config file at path into a key→value map. A missing file
// is not an error (returns an empty map). A malformed line or unknown key is a
// hard error so misconfiguration never fails silently.
//
// The format is a minimal stdlib-only `key = value` parser: one pair per line,
// '#' line comments, blank lines ignored. No new dependency.
func loadConfig(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for i, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected `key = value`, got %q", i+1, raw)
		}
		key := strings.TrimSpace(k)
		if !configKeys[key] && !moduleConfigKeys[key] {
			return nil, fmt.Errorf("line %d: unknown key %q (valid: %s)", i+1, key, validConfigKeys())
		}
		out[key] = strings.TrimSpace(v)
	}
	return out, nil
}

// parseOnOff interprets a config on/off (or true/false) value as a bool.
func parseOnOff(key, v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("%s: want on|off, got %q", key, v)
}

// chromeSettings is the resolved render-chrome configuration (flag > config >
// default), threaded into server.Config.
type chromeSettings struct {
	theme      string // "" | "light" | "dark"  ("" = no server default → client prefers-color-scheme)
	footer     bool
	footerLink string
	header     bool
	metaHeader bool
}

// resolveChrome applies precedence — CLI flag (when explicitly set) > config
// file > built-in default — to produce the render-chrome settings. setFlags is
// the set of flag names the user actually passed (from flag.FlagSet.Visit).
func resolveChrome(setFlags map[string]bool, file map[string]string,
	flagTheme, flagFooterLink string, flagFooter, flagHeader, flagMetaHeader bool) (chromeSettings, error) {

	cs := chromeSettings{footer: true, footerLink: repoURL, header: true, metaHeader: true}

	// theme: empty default (lets the client toggle fall back to prefers-color-scheme).
	if setFlags["theme"] {
		cs.theme = flagTheme
	} else if v, ok := file["theme"]; ok {
		cs.theme = v
	}
	if cs.theme != "" && !theme.Valid(cs.theme) {
		return cs, fmt.Errorf("theme %q: unknown (choose: %s)", cs.theme, strings.Join(theme.Names, ", "))
	}

	// footer (bool).
	if setFlags["footer"] {
		cs.footer = flagFooter
	} else if v, ok := file["footer"]; ok {
		b, err := parseOnOff("footer", v)
		if err != nil {
			return cs, err
		}
		cs.footer = b
	}

	// footer_link.
	if setFlags["footer-link"] {
		cs.footerLink = flagFooterLink
	} else if v, ok := file["footer_link"]; ok {
		cs.footerLink = v
	}

	// header (bool).
	if setFlags["header"] {
		cs.header = flagHeader
	} else if v, ok := file["header"]; ok {
		b, err := parseOnOff("header", v)
		if err != nil {
			return cs, err
		}
		cs.header = b
	}

	// meta_header (bool).
	if setFlags["meta-header"] {
		cs.metaHeader = flagMetaHeader
	} else if v, ok := file["meta_header"]; ok {
		b, err := parseOnOff("meta_header", v)
		if err != nil {
			return cs, err
		}
		cs.metaHeader = b
	}

	return cs, nil
}
