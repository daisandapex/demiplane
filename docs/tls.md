# Native TLS (optional module)

demiplane can terminate TLS itself instead of sitting behind a reverse proxy.
This page covers the build tag, the config-file keys, the zero-config
self-signed mode, ACME/Let's Encrypt issuance, and the bring-your-own-certificate
path.

It's an **opt-in module** (build tag `tls`), and even a tls-tagged binary serves plain
HTTP until you flip it on in the config file, so the default posture is
unchanged:

```sh
go build -tags "reply tls" -o demiplane ./cmd/demiplane
```

```ini
# ${XDG_CONFIG_HOME:-~/.config}/demiplane/config  (-tags tls builds only)
tls = on
```

That alone is the **zero-config self-signed** mode: on first TLS start the
module generates an ECDSA P-256 certificate (SANs derived from your
`--bind`/`--content-bind` addresses, or set `tls_hosts = a,b,…` explicitly),
persists it under `<store>/modules/tls/`, reuses it across restarts, and
regenerates it automatically near expiry or when the bind hosts change. The
SHA-256 fingerprint is logged so clients can pin it
(`curl --cacert <store>/modules/tls/self-signed-cert.pem …`).

Two more certificate sources, picked by which keys you set (mixing them is a
hard startup error):

```ini
# ACME / Let's Encrypt — automatic issuance + renewal (TLS-ALPN-01).
# The domain must resolve to this host and the CONTROL listener must be
# reachable on :443. Setting domains constitutes ToS acceptance.
# Optional: tls_acme_email (account contact) and tls_acme_ca (directory
# override, e.g. the Let's Encrypt staging URL). Comments must be on their
# own line — the config format has no inline comments.
tls_acme_domains = demi.example.com
tls_acme_email   = ops@example.com

# — or — bring your own certificate (fleet CA, external DNS-01 flow, …):
tls_cert = /etc/demiplane/demi.crt
tls_key  = /etc/demiplane/demi.key
```

Both planes (control + content) terminate with the same configuration; minted
URLs switch to `https://` automatically. The SSH ingest path needs no
certificate (sshd already encrypts it). Full rationale — and why DNS-01 is
deliberately out — in [ADR 0004](./adr/0004-native-tls-module.md).

> **Migrating a live plain-HTTP instance?** Published absolute `http://` URLs,
> hook scripts, and cron consumers don't rewrite themselves — schedule the
> scheme flip as a coordinated change, not a config toggle.

## See also

- [Deployment](./deployment.md) — reachability tiers, the two-origin topology, the config file
- [HTTP API](./api.md) — why per-artifact passwords are only meaningful over TLS
- [SSH transport](./receive.md) — the ingest path that needs no certificate
- [Modules — developer guide](./MODULES.md) — how build-tagged modules work
- [ADR 0004 — native TLS module](./adr/0004-native-tls-module.md)
