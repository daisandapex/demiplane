# demiplane — product context

## Product purpose

Self-hosted, internal-first HTML and file publishing with a REST API. You `POST`
a file to your own server and get a link only your network (LAN or mesh) can
reach. It is the honest inverse of public paste and host services: the
convenience of a single-command publish and an instant URL, with the data never
leaving infrastructure you own. Born from a real friction: sending a CEO an HTML
report he had to download and open, when he just wanted a link.

## Register

**brand** for the human-facing published surfaces (rendered `?render=md`
documents, the landing page, `/docs`, `/notes`, `/dispatch`) — these are
long-form reading experiences where the design *is* the product. **product** for
the operator UI (`/gallery`, `/connect`). This file's design guidance targets the
reading surfaces unless a task names the UI.

## Users

- **Developers and AI-agent-harness users** who generate HTML and markdown
  artifacts (reports, dashboards, research, lesson pages) and want to share them
  as a link on their own infrastructure instead of a public host. They know
  catppuccin, dracula, one-dark by name; they run their own boxes.
- **The solo operator** publishing recurring documents: a Chief-of-Staff brief,
  a purple-team class, research reports.
- **Small trusted groups** (a handful of colleagues on a shared host) — the
  first non-solo audience.

## Tone and brand

Honest, calm, trustworthy, technical without being cold. Self-hosted ethos:
"your data stays on your box." Developer-native, anti-corporate, no marketing
gloss. The reading experience should feel like a well-set private document read
late on a wide monitor, not a SaaS dashboard.

## Anti-references (the things to never look like)

- Public paste and host services (the predator model demiplane inverts).
- SaaS dashboards: hero-metric templates, identical icon-card grids, purple
  gradients, glassmorphism.
- **The AI skinsuit**: bullet-soup where prose belongs, no type scale, generic
  palettes with no point of view, emoji-laden cards, nothing that breathes.
- Anything that reads as machine-generated. If a viewer could say "AI made that,"
  it failed.

## Strategic principles

- **One stylesheet source of truth** (`internal/theme/theme.go`). Every human
  surface composes its tokens. Hand-built pages that invent their own palette are
  drift to be corrected, not a feature.
- **No web fonts, no CDN, no external anything.** Strict CSP, fully
  self-contained pages; system font stacks only.
- **Editorial over utilitarian** for reading surfaces; prose over bullet lists.
- **Themes are palette swaps over one system**, never separate designs.
