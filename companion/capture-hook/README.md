# Artifact-capture hook (companion)

A **client-side** companion for demiplane — a Claude Code hook (and a standalone
publish CLI) that takes a self-contained HTML artifact and POSTs it to **your**
demiplane instance. The page lands on your mesh, not on a public host.

This is a *companion piece*, not a server module (see
[ADR 0001](../../docs/adr/0001-module-extension-pattern.md) for the distinction):
the capture happens in the editing client, the publish hits demiplane's existing
`POST /publish` API. Nothing server-side changes.

> **The linchpin (epic `rox.1`):** when Claude writes an artifact — a complete,
> self-contained HTML page — this hook publishes it to demiplane and hands the
> agent the **internal URL** to share, instead of a link to a public service.

## Two ways to use it

### 1. As a Claude Code hook

Fires after every `Write`/`Edit`. If the written file is a self-contained HTML
document, it publishes it and feeds the resulting URL back to the agent.

1. Make sure `curl` and `jq` are installed.
2. Merge the `hooks` block from [`settings.example.json`](./settings.example.json)
   into your Claude Code settings (`~/.claude/settings.json` for all projects, or
   `.claude/settings.json` for one). The command path uses `${CLAUDE_PROJECT_DIR}`;
   point it at wherever this script lives (or copy the script into
   `.claude/hooks/`).
3. Export the config (in your shell profile, or a `SessionStart` hook):

   ```sh
   export DEMIPLANE_URL=http://demiplane.your-mesh:8080
   export DEMIPLANE_TOKEN=…          # only if the instance requires publish auth
   export DEMIPLANE_CAPTURE=1        # turns the hook ON (it no-ops until set)
   ```

The hook is **dormant until `DEMIPLANE_CAPTURE=1`**, so wiring it up never
surprises you with an unexpected publish.

### 2. As a CLI

Publish any file directly — no Claude Code involved:

```sh
export DEMIPLANE_URL=http://demiplane.your-mesh:8080
companion/capture-hook/demiplane-capture.sh ./report.html
# → http://demiplane.your-mesh:8080/report
```

An explicit file argument is consent, so CLI mode ignores the `DEMIPLANE_CAPTURE`
gate and the self-contained heuristic (it publishes exactly what you point it at).

## What gets captured (hook mode)

| File | Captured? |
|---|---|
| `.html` / `.htm` containing `<!doctype html>` or `<html` | ✅ yes — a self-contained page |
| `.html` fragment (no doctype/`<html>`) | ❌ skipped — likely a partial edit, not an artifact |
| `.md` / `.markdown` | only when `DEMIPLANE_RENDER=1` (published with `?render=md`) |
| anything else | ❌ skipped |

## Configuration

| Env var | Effect |
|---|---|
| `DEMIPLANE_URL` | **Required.** Base URL of your instance. |
| `DEMIPLANE_TOKEN` | Bearer token, if publish auth is enabled. |
| `DEMIPLANE_CAPTURE` | Hook-mode on/off gate (`1`/`true`/`yes`). CLI mode ignores it. |
| `DEMIPLANE_SLUG` | Force a named slug. Default: derived from the filename (e.g. `My_Report.html` → `my_report`). Set empty to let the server generate one. |
| `DEMIPLANE_PRIVATE` | `1` → publish `?private=true` (unguessable capability URL). Drops the named slug, since the two are mutually exclusive. |
| `DEMIPLANE_RENDER` | `1` → also capture markdown, published with `?render=md`. |
| `DEMIPLANE_CAPTURE_DIR` | Restrict hook captures to files under this absolute path. |

## Behaviour & safety

- **Best-effort, never blocking.** In hook mode the script always exits `0`; a
  failed publish is logged to stderr and the edit proceeds untouched.
- **Slug derivation** lowercases the basename and maps unsafe characters to `-`,
  matching demiplane's slug rules. A named slug **overwrites in place**, so
  re-writing `report.html` keeps updating the same `…/report` URL — bookmark once.
- **Filenames**, not contents, drive the slug; the file's bytes are streamed to
  `POST /publish` exactly as written.

## Requirements

`bash`, `curl`, `jq`. The demiplane instance must be reachable from the machine
running Claude Code (i.e. you're on the mesh/LAN).
