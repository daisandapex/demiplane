# Works with your harness

How to publish from the coding agent, editor, or shell you already use — MCP,
the `demiplane publish` CLI, bare curl, the `/connect` onboarding page — plus the
Claude Code artifact capture hook.

demiplane publishes from whatever coding agent, editor, or shell you already use.
The running instance serves a live, copy-paste config page at **`GET /connect`** —
open it and grab the block for your tool. The snippets below are the same ones,
templated with the instance's own base URL and a token *file path* (never the
token itself).

| Harness | How it connects | Verified |
|---|---|---|
| Claude Code | MCP (native `publish`/`list`/`get`/`delete` tools) or the capture hook | Yes — tested and used daily |
| Any MCP client | MCP (standard stdio JSON-RPC server) | Not yet verified |
| Aider | shell `/run` + curl | Yes — the curl path is tested |
| Any shell / CI | `curl` or `demiplane publish` | Yes — REST and CLI tests |

demiplane speaks plain MCP and plain HTTP, so the set of clients it works with is
larger than the set we verify; the table says which is which. If you get it running
in another MCP client, open an issue and we will add it.

**MCP.** `demiplane mcp` is a standard stdio JSON-RPC MCP server — one stanza, and
only the config file location differs per client (its own docs name that path):

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

Claude Code can add it for you:

```sh
claude mcp add demiplane -- demiplane mcp --url http://demiplane.your-mesh:8891 --token-file ~/.config/demiplane/token
```

**Bare curl** — the universal fallback, works in any harness that shells out
(publish goes to the **control plane**):

```sh
curl --data-binary @index.html http://demiplane.your-mesh:8891/publish
# add -H "Authorization: Bearer $(cat ~/.config/demiplane/token)" if the instance requires auth
```

**CLI** — `demiplane publish` wraps the same call, copies the URL to your
clipboard, and can `--watch` a file for live-reload:

```sh
demiplane publish --url http://demiplane.your-mesh:8891 --token-file ~/.config/demiplane/token index.html
```

The token is read from `--token-file`/`$DEMIPLANE_TOKEN` **locally** and is never
placed on a command line, in a URL, or on the `/connect` page. See
[`companion/README.md`](../companion/README.md) for the full harness matrix and the
Claude Code capture hook.

## Capturing agent artifacts (Claude Code)

When an AI agent generates a self-contained HTML page, that page should land on
**your** mesh — not a public host. The
[`companion/capture-hook/`](../companion/capture-hook/) companion is a Claude Code
`PostToolUse` hook (and a standalone publish CLI) that POSTs such artifacts to
`POST /publish` automatically and hands the agent the internal URL to share.

```sh
export DEMIPLANE_URL=http://demiplane.your-mesh:8080
export DEMIPLANE_CAPTURE=1        # the hook is dormant until this is set
# wire companion/capture-hook/settings.example.json into your Claude Code settings
```

It's a *companion*, not a server module — the capture is client-side; the server
API is unchanged. The hook is one of several ways to connect; see
[`companion/README.md`](../companion/README.md) for the full harness matrix (MCP,
CLI, curl, capture hook) or the running instance's `GET /connect` page.

## See also

- [HTTP API](./api.md) — what these clients call, and the bearer-token auth model
- [Deployment](./deployment.md) — the control-plane / content-origin ports these URLs point at
- [Rendering](./rendering.md) — `?render=md` for markdown artifacts
- [Architecture](./architecture.md) — where `companion/` sits in the repo
