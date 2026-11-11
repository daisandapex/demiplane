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
regenerates it at startup when it nears expiry or no longer names every
host.
A newly generated certificate's SHA-256 fingerprint is logged so clients can
pin it (`curl --cacert <store>/modules/tls/self-signed-cert.pem …`).

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

## CLI and MCP clients against a self-signed instance

`demiplane publish`, `list`, `mcp` and the other client commands verify the
server certificate with the system trust store, so they refuse a self-signed
instance (`x509: certificate signed by unknown authority`). On Linux, point
them at the generated certificate with `SSL_CERT_FILE`:

```sh
SSL_CERT_FILE=<store>/modules/tls/self-signed-cert.pem \
  demiplane publish --url https://127.0.0.1:8080 page.html
```

Set it in the environment the MCP harness starts `demiplane mcp` with, too.
On Debian, Ubuntu and similar systems the variable adds the certificate to
the system roots rather than replacing them: Go still loads the certificates
in `/etc/ssl/certs`. To trust only this instance's certificate, set
`SSL_CERT_DIR` to an empty or missing directory as well:

```sh
SSL_CERT_FILE=<store>/modules/tls/self-signed-cert.pem SSL_CERT_DIR=/nonexistent \
  demiplane publish --url https://127.0.0.1:8080 page.html
```

The certificate must also name the address the client dials. The derived SANs
are `localhost`, `127.0.0.1`, `::1` and the bind hosts; a wildcard bind
(`0.0.0.0`) adds the machine's hostname, not its interface IPs. Clients that
reach the instance by a mesh IP (or any other name) therefore need
`tls_hosts` set to list it. `tls_hosts` replaces the derived set rather than
adding to it, so list the loopback names too if local clients use them:

```ini
tls_hosts = localhost,127.0.0.1,100.64.0.10
```

The set decides which names a certificate must cover, not which names it
holds. At startup the server keeps the persisted certificate when it names
every host in the set, even if it also names hosts that are no longer in the
set, so removing a host from `tls_hosts` (or from the bind addresses) leaves
it in the certificate. Adding a host that the certificate lacks does produce a
new certificate. To drop a name, stop the server, delete
`<store>/modules/tls/self-signed-cert.pem` and start it again: it generates a
new key and certificate for the current set and logs the new fingerprint.

## See also

- [Deployment](./deployment.md) — reachability tiers, the two-origin topology, the config file
- [HTTP API](../API.md) — why per-artifact passwords are only meaningful over TLS
- [SSH transport](./receive.md) — the ingest path that needs no certificate
- [Modules — developer guide](./MODULES.md) — how build-tagged modules work
- [ADR 0004 — native TLS module](./adr/0004-native-tls-module.md)
