# SSH transport (`demiplane receive`)

Publishing over SSH instead of HTTP: how demiplane reuses the host's `sshd` via
an `authorized_keys` forced command, why `SSH_ORIGINAL_COMMAND` is ignored, and
the per-key publishing modes (drop / reports / sync).

demiplane does **not** run its own SSH server. Instead it reuses the host's `sshd`: you
pin a forced command in `~/.ssh/authorized_keys`, and `sshd` does the public-key auth.
The forced command runs `demiplane receive`, which streams the artifact from the SSH
channel (stdin) into the **same store** the HTTP server uses — no second host key, no extra
auth surface, no new dependency.

demiplane **ignores `SSH_ORIGINAL_COMMAND`** — the publisher cannot pass flags or commands
over the wire, which removes any SSH argument-injection surface. Instead, **each
`authorized_keys` entry bakes its own `receive` flags into the `command=`**, and the
`restrict` option (modern OpenSSH; implies `no-pty`, `no-port-forwarding`,
`no-agent-forwarding`, `no-X11-forwarding`, `no-user-rc`) locks the key to publishing only.
Use one key per publishing mode:

```sh
# A "drop" key — random friendly slug per publish:
restrict,command="demiplane receive --store /var/lib/demiplane --base-url https://demi.example" ssh-ed25519 AAAA... drop

# A "reports" key — always overwrites the same named slug:
restrict,command="demiplane receive --store /var/lib/demiplane --base-url https://demi.example --slug reports" ssh-ed25519 AAAA... reports

# A "sync" key — tar-stream directory sync:
restrict,command="demiplane receive --store /var/lib/demiplane --base-url https://demi.example --untar" ssh-ed25519 AAAA... sync

# then, with a matching Host alias per key in the publisher's ~/.ssh/config:
echo '<h1>hi</h1>' | ssh demi-drop                 # → prints a fresh artifact URL
ssh demi-reports < report.html                     # overwrites /reports in place
tar -C ./site -cf - . | ssh demi-sync              # directory sync (each file → an artifact)
```

`receive` flags to bake into `command=`: `--slug`, `--filename`, `--ttl`, `--private`
(single-file only), `--untar`, `--max-upload <bytes>` (cap stdin size, default 100 MiB, `0` = unlimited). A
view password comes from the `DEMIPLANE_PASSWORD` environment variable (`environment=` in
the entry, or a wrapper), never an argument. Nested tar paths flatten to hyphenated slugs
(`css/style.css` → `css-style.css`), and re-syncing overwrites in place.

`receive` enforces the `--store-quota` the server runs with (recorded in the
store), or its own `--store-quota` if given. Receives on one store take turns:
a second waits until the first has finished.

The binary in `command=` writes to the same store as `serve`, so upgrade it with
`serve`: once a newer `serve` has opened the store, an older `receive` cannot
open it ([Upgrading](./deployment.md#upgrading)).

## See also

- [HTTP API](../API.md) — the other transport, and the auth model the store shares
- [Deployment](./deployment.md) — the store directory and `serve` flags
- [Native TLS](./tls.md) — not needed for SSH ingest; sshd already encrypts it
- [Architecture](./architecture.md) — `internal/transport/` and the shared store
