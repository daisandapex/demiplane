# Connecting your harness to demiplane

demiplane is a publish target for any coding agent, editor, or shell. This
directory holds the **client-side** companions that make that one-command. None
of them change the server: every path lands on demiplane's existing
`POST /publish` API, so an artifact goes to **your** mesh instead of a public host.

The running instance also serves this same guidance, templated with its own base
URL, at **`GET /connect`** — the fastest way to get a copy-paste block for your
tool.

## Pick your connection

| Harness | How it connects | Where to configure | Verified |
|---|---|---|---|
| Claude Code | MCP native tools, **or** the capture hook | `claude mcp add …` / `capture-hook/` | Yes — tested and used daily |
| Any MCP client | MCP | wherever that client keeps its `mcpServers` config | Not yet verified |
| Aider | shell `/run` + curl | run the curl below | Yes — the curl path is tested |
| Any shell / CI | `curl` or `demiplane publish` | environment or flags | Yes — REST and CLI tests |

### What "verified" means

The **Verified** column is deliberately narrow: it marks the paths demiplane's own
test suite exercises or that we run every day.

- **Claude Code** — MCP tools and the capture hook, dogfooded daily.
- **curl / any shell / CI** — covered by the REST handler tests.
- **`demiplane publish`** — covered by the CLI tests.

Everything else is "should work, not yet confirmed." demiplane's MCP server is a
standard stdio JSON-RPC implementation with no client-specific behaviour, so
MCP-capable editors are expected to work; we simply have not run them ourselves.
Get one working and open an issue — we will add it to the table with a test.

## MCP (any MCP-capable client)

`demiplane mcp` is a standard stdio JSON-RPC MCP server and a thin client of the
control plane, so it works against a remote or local instance with no filesystem
coupling. Register it once and `publish`, `list`, `get`, and `delete` become
native tools. The stanza is the same everywhere; only the config file path
differs, and your client's own docs name that path:

```json
{
  "mcpServers": {
    "demiplane": {
      "command": "demiplane",
      "args": ["mcp", "--url", "http://demiplane.your-mesh:8891", "--token-file", "~/.config/demiplane/token"]
    }
  }
}
```

Claude Code one-liner:

```sh
claude mcp add demiplane -- demiplane mcp --url http://demiplane.your-mesh:8891 --token-file ~/.config/demiplane/token
```

## CLI (`demiplane publish`)

The universal fallback for anything that can shell out. Prints the URL, copies it
to the clipboard, and can watch a file for live-reload:

```sh
# one-off
demiplane publish --url http://demiplane.your-mesh:8891 --token-file ~/.config/demiplane/token index.html

# watch + re-publish on save (pairs with the ?live view for auto-reload)
demiplane publish --url http://demiplane.your-mesh:8891 --watch --slug draft notes.md

# or set the environment once
export DEMIPLANE_URL=http://demiplane.your-mesh:8891
export DEMIPLANE_TOKEN_FILE=~/.config/demiplane/token
demiplane publish index.html
```

## Bare curl (works everywhere)

```sh
curl --data-binary @index.html http://demiplane.your-mesh:8891/publish
# add auth only if the instance requires it — the token is READ locally, never inlined:
curl --data-binary @index.html \
  -H "Authorization: Bearer $(cat ~/.config/demiplane/token)" \
  http://demiplane.your-mesh:8891/publish
```

Aider and other shell-command harnesses run the same thing (`/run curl …`) and
paste the returned URL back into the conversation.

## Claude Code capture hook

For a hands-off flow that publishes artifacts as Claude writes them, use the
`PostToolUse` capture hook in [`capture-hook/`](./capture-hook/). It is dormant
until `DEMIPLANE_CAPTURE=1`, so wiring it up never triggers a surprise publish.
See [`capture-hook/README.md`](./capture-hook/README.md).

## Token discipline

Every path above resolves the bearer token from a **local file**
(`--token-file`) or the `DEMIPLANE_TOKEN` / `DEMIPLANE_TOKEN_FILE` environment,
never from a command-line argument, a URL query, or a rendered page. Store yours
once and keep it off argv:

```sh
mkdir -p ~/.config/demiplane
printf %s "$YOUR_TOKEN" > ~/.config/demiplane/token
chmod 600 ~/.config/demiplane/token
```

An open instance (no bearer token configured) needs none of this — the snippets
work as-is, and the same blocks keep working if the operator later enables auth.
