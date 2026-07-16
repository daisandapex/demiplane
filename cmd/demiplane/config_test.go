// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigMissingIsDefaults(t *testing.T) {
	m, err := loadConfig(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("missing file should yield no keys, got %v", m)
	}
}

func TestLoadConfigParses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	writeFile(t, p, "# a comment\n\nfooter = off\ntheme=dark\nfooter_link = https://x.io/r\nheader= on\n")
	m, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"footer": "off", "theme": "dark", "footer_link": "https://x.io/r", "header": "on"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("key %q = %q, want %q", k, m[k], v)
		}
	}
}

func TestLoadConfigMalformed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	writeFile(t, p, "garbage with no equals sign\n")
	if _, err := loadConfig(p); err == nil {
		t.Error("a line without `=` should be a hard error")
	}
	writeFile(t, p, "bogus_key = x\n")
	if _, err := loadConfig(p); err == nil {
		t.Error("an unknown key should be a hard error")
	}
}

// TestModuleConfigSeam covers the build-tag-gated module config surface: a
// registered module key parses, an unregistered one stays a hard error, and
// applyModuleConfig runs the registered applier against the parsed map.
func TestModuleConfigSeam(t *testing.T) {
	var got map[string]string
	registerModuleConfig([]string{"testmod_knob"}, func(cfg map[string]string) error {
		got = cfg
		return nil
	})

	p := filepath.Join(t.TempDir(), "config")
	writeFile(t, p, "testmod_knob = 7\ntheme = dark\n")
	m, err := loadConfig(p)
	if err != nil {
		t.Fatalf("registered module key should parse: %v", err)
	}
	if m["testmod_knob"] != "7" {
		t.Errorf("testmod_knob = %q, want 7", m["testmod_knob"])
	}
	if err := applyModuleConfig(m); err != nil {
		t.Fatalf("applyModuleConfig: %v", err)
	}
	if got == nil || got["testmod_knob"] != "7" {
		t.Errorf("applier did not receive the parsed config: %v", got)
	}

	// A key no module registered is still a loud failure.
	writeFile(t, p, "othermod_knob = x\n")
	if _, err := loadConfig(p); err == nil {
		t.Error("an unregistered module key should be a hard error")
	}
}

func TestResolveChromeDefaults(t *testing.T) {
	cs, err := resolveChrome(map[string]bool{}, map[string]string{}, "", repoURL, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if cs.theme != "" || !cs.footer || cs.footerLink != repoURL || !cs.header || !cs.metaHeader {
		t.Errorf("zero-config defaults wrong: %+v", cs)
	}
}

func TestResolveChromeMetaHeader(t *testing.T) {
	// config off, no flag → off.
	cs, err := resolveChrome(map[string]bool{}, map[string]string{"meta_header": "off"}, "", repoURL, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if cs.metaHeader {
		t.Errorf("config meta_header=off should disable: %+v", cs)
	}
	// flag wins over config.
	set := map[string]bool{"meta-header": true}
	cs, err = resolveChrome(set, map[string]string{"meta_header": "off"}, "", repoURL, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.metaHeader {
		t.Errorf("--meta-header flag should win over config off: %+v", cs)
	}
	// bad value errors.
	if _, err := resolveChrome(map[string]bool{}, map[string]string{"meta_header": "sometimes"}, "", repoURL, true, true, true); err == nil {
		t.Error("a non-on/off meta_header value should error")
	}
}

func TestResolveChromeConfigOverridesDefault(t *testing.T) {
	file := map[string]string{"theme": "dark", "footer": "off", "footer_link": "https://cfg.example", "header": "off"}
	cs, err := resolveChrome(map[string]bool{}, file, "", repoURL, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if cs.theme != "dark" || cs.footer || cs.footerLink != "https://cfg.example" || cs.header {
		t.Errorf("config did not override defaults: %+v", cs)
	}
}

func TestResolveChromeFlagOverridesConfig(t *testing.T) {
	file := map[string]string{"theme": "dark", "footer": "off", "footer_link": "https://cfg.example", "header": "off"}
	set := map[string]bool{"theme": true, "footer": true, "footer-link": true, "header": true}
	cs, err := resolveChrome(set, file, "light", "https://flag.example", true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if cs.theme != "light" || !cs.footer || cs.footerLink != "https://flag.example" || !cs.header {
		t.Errorf("flag should win over config: %+v", cs)
	}
}

func TestResolveChromeInvalidTheme(t *testing.T) {
	if _, err := resolveChrome(map[string]bool{}, map[string]string{"theme": "neon"}, "", repoURL, true, true, true); err == nil {
		t.Error("an invalid config theme should error at startup")
	}
}

func TestResolveChromeBadBool(t *testing.T) {
	if _, err := resolveChrome(map[string]bool{}, map[string]string{"footer": "maybe"}, "", repoURL, true, true, true); err == nil {
		t.Error("a non-on/off footer value should error")
	}
}

func TestParseOnOff(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{{"on", true}, {"off", false}, {"true", true}, {"false", false}, {"YES", true}, {"0", false}} {
		got, err := parseOnOff("k", tc.in)
		if err != nil || got != tc.want {
			t.Errorf("parseOnOff(%q) = %v,%v want %v", tc.in, got, err, tc.want)
		}
	}
	if _, err := parseOnOff("k", "banana"); err == nil {
		t.Error("invalid on/off should error")
	}
}
