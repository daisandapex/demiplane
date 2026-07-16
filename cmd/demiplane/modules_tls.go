// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build tls

package main

// The native-TLS module is opt-in: it is compiled into the binary only when
// built with `-tags tls` (go build -tags tls ./cmd/demiplane). The default
// build omits it entirely, and even a tls-tagged binary serves plain HTTP
// until the operator sets `tls = on` in the config file. See ADR 0001 (the
// inclusion mechanism) and ADR 0004 (the module).
//
// This file wires the module's config keys into the config surface (so tls_*
// is recognized exactly when the module is in the binary) and installs the
// moduleTLS listener seam declared in main.go.
import (
	"crypto/tls"

	"github.com/daisandapex/demiplane/internal/module"
	tlsmod "github.com/daisandapex/demiplane/internal/modules/tls"
)

func init() {
	// Native TLS (both planes; OFF until `tls = on`):
	//   tls              = on|off        master switch (default off)
	//   tls_cert/tls_key = <pem paths>   BYO certificate (set together)
	//   tls_hosts        = a,b,…         self-signed SAN override
	//   tls_acme_domains = a,b,…         ACME/Let's Encrypt hostnames
	//   tls_acme_email   = <addr>        ACME account contact (optional)
	//   tls_acme_ca      = <url>         ACME directory override (optional)
	registerModuleConfig(tlsmod.ConfigKeys, tlsmod.Configure)

	moduleTLS = func(host module.Host, bindHosts []string) (*tls.Config, error) {
		if !tlsmod.Enabled() {
			return nil, nil
		}
		dir, err := host.ModuleDataDir("tls")
		if err != nil {
			return nil, err
		}
		return tlsmod.ServerConfig(dir, bindHosts)
	}
}
