// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"fmt"
	"net/http"
	"strings"
)

// handleLLMs serves /llms.txt: a concise, one-fetch, plain-text reference an LLM
// or agent reads to learn the API. Examples use the instance's own base URL.
func (s *Server) handleLLMs(w http.ResponseWriter, r *http.Request) {
	base := s.requestBase(r)
	authLine := "OPEN — no bearer token configured on this instance (publish/list/delete are unauthenticated)."
	if s.authToken != "" {
		authLine = "REQUIRED — send `Authorization: Bearer <token>` on POST /publish, GET /list, DELETE /{slug}."
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, llmsTemplate, base, authLine, base, base, base, base, base, base)
}

const llmsTemplate = `# demiplane

Self-hosted, internal-first static & HTML publishing with a REST API. You POST a
file to your own server and get back a URL; only your network can reach it. The
inverse of public paste/host services: sealed by default, shared by choice.

Base URL: %s

## Auth
Publish auth (who can write): %s
View auth (who can read a URL): network reachability by default; a private
artifact's unguessable capability slug is its secret; an optional per-artifact
password gates reads via HTTP Basic.

## Endpoints
POST   /publish        Store the request body, return the artifact URL.
GET    /{slug}         Fetch an artifact (open; password-gated ones need Basic auth).
GET    /list           JSON list of artifacts (auth).
DELETE /{slug}         Delete an artifact (auth).
GET    /docs           Human docs (HTML).  GET /help  Machine API (JSON).

## POST /publish
Body: raw bytes (any file type) or multipart/form-data (first file part).
The body is streamed to disk — no size limit by default.
Query parameters (all optional):
  ?slug=<name>    Named, stable URL that OVERWRITES in place on re-publish.
                  Omit to get an auto-generated friendly slug (adjective-creature, e.g. shadow-specter).
  ?private=true   Mint a high-entropy, unguessable capability slug. Cannot be
                  combined with ?slug= (a named slug is guessable).
  ?ttl=<dur>      Auto-expire: 30m, 2h, 7d (days), or any Go duration.
  ?render=md      Render a markdown body to a styled HTML page on publish, using
                  the instance's house style: a sticky title header (the doc's
                  first H1), a vanity footer, and — on the default palette —
                  a client-side light/dark toggle (a pinned named theme such as
                  dracula/catppuccin/one-dark fixes its palette instead).
                  A leading YAML frontmatter block (--- key: value --- ) becomes a
                  meta-header (localized date/published timestamp + labeled fields).
                  Operator-tuned via serve --theme/--css/--header/--footer/
                  --meta-header or ${XDG_CONFIG_HOME:-~/.config}/demiplane/config.
  ?reply=question Bake a first-class inline reply box into the rendered page
                  (requires ?render=md; not valid with ?private). Viewers submit a
                  single free-text answer via a JS-free form that posts SAME-ORIGIN
                  to the page's own URL; the content plane records it as a
                  comment-kind reply and returns a server-rendered confirmation
                  (honest by construction — a failed write shows an error, never a
                  false success). Answers are read via GET /replies?slug=<slug>
                  (auth). Needs the -tags reply build. A recorded reply can fire
                  a configurable server-side hook (exec and/or webhook; config
                  keys reply_hook_exec / reply_hook_url) so an agent reacts with
                  zero polling.
  ?next=<slug>    Forward flow (requires ?reply): names the page that follows
                  this one. After a recorded answer the confirmation page waits
                  (JS-free meta-refresh via GET /answer/<slug>/next?to=<next>)
                  until <next> is published, then carries the reader there.
                  <next> need not exist yet; it must differ from ?slug=.
  ?filename=<n>   Content-type hint for raw-body uploads.
Headers:
  X-Demiplane-Password: <pw>   Set a view password (NEVER put it in the URL).
Response: the artifact URL as text/plain, or JSON if you send Accept: application/json.

## Examples
Publish a page (raw body):
  curl --data-binary @index.html %s/publish
Named, overwrite-in-place URL:
  curl --data-binary @report.html "%s/publish?slug=reports"
Private capability URL that expires in a day:
  curl --data-binary @secret.html "%s/publish?private=true&ttl=24h"
Password-protected (password via header, read with Basic auth):
  curl -H "X-Demiplane-Password: hunter2" --data-binary @q.html "%s/publish?slug=q"
  curl -u any:hunter2 %s/q
List your artifacts as JSON:
  curl -H "Accept: application/json" %s/list

## Error codes
200/201  OK / created.
400      Bad request (invalid or reserved slug, private+named, password in URL query, bad ttl).
401      Missing/invalid bearer token (write/list) or wrong view password (read).
404      No such artifact (or it expired).
405      Wrong HTTP method for a known path (e.g. POST /list).
413      Upload exceeds the configured --max-upload limit.

## Reserved slugs
These names collide with built-in routes and are rejected (400) as ?slug=:
publish, list, docs, help, llms.txt.
`

// --- /help : self-describing JSON ---

type apiParam struct {
	Name     string `json:"name"`
	In       string `json:"in"` // query | header | body
	Required bool   `json:"required"`
	Desc     string `json:"description"`
}

type apiEndpoint struct {
	Method string     `json:"method"`
	Path   string     `json:"path"`
	Auth   bool       `json:"auth"`
	Desc   string     `json:"description"`
	Params []apiParam `json:"params,omitempty"`
}

type apiError struct {
	Code int    `json:"code"`
	Name string `json:"name"`
	Mean string `json:"meaning"`
}

type apiHelp struct {
	Service     string        `json:"service"`
	Description string        `json:"description"`
	BaseURL     string        `json:"base_url"`
	AuthScheme  string        `json:"auth"`
	AuthEnabled bool          `json:"auth_enabled"`
	Endpoints   []apiEndpoint `json:"endpoints"`
	Errors      []apiError    `json:"errors"`
}

// handleHelp serves /help: a machine-readable description of the API so an agent
// can discover endpoints, parameters, and error codes at runtime.
func (s *Server) handleHelp(w http.ResponseWriter, r *http.Request) {
	help := apiHelp{
		Service:     "demiplane",
		Description: "Self-hosted, internal-first static & HTML publishing with a REST API.",
		BaseURL:     s.requestBase(r),
		AuthScheme:  "Bearer token on write/list (Authorization: Bearer <token>); per-artifact view password via HTTP Basic.",
		AuthEnabled: s.authToken != "",
		Endpoints: []apiEndpoint{
			{
				Method: "POST", Path: "/publish", Auth: true,
				Desc: "Store the request body (raw or multipart) and return the artifact URL.",
				Params: []apiParam{
					{"slug", "query", false, "Named, stable slug; overwrites in place. Omit for an auto friendly slug."},
					{"private", "query", false, "true → mint a high-entropy capability slug. Incompatible with slug."},
					{"ttl", "query", false, "Auto-expire duration: 30m, 2h, 7d, or any Go duration."},
					{"render", "query", false, "md → render a markdown body to HTML on publish."},
					{"filename", "query", false, "Content-type hint for raw-body uploads."},
					{"X-Demiplane-Password", "header", false, "Set a view password (never via URL)."},
					{"Accept", "header", false, "application/json → JSON response instead of text URL."},
				},
			},
			{Method: "GET", Path: "/{slug}", Auth: false,
				Desc: "Fetch an artifact as-is. Open; password-gated artifacts require HTTP Basic credentials."},
			{Method: "GET", Path: "/list", Auth: true, Desc: "JSON list of the owner's artifacts."},
			{Method: "DELETE", Path: "/{slug}", Auth: true, Desc: "Delete an artifact."},
			{Method: "GET", Path: "/docs", Auth: false, Desc: "Human-readable documentation (HTML)."},
			{Method: "GET", Path: "/llms.txt", Auth: false, Desc: "Concise plain-text API reference for LLMs/agents."},
			{Method: "GET", Path: "/help", Auth: false, Desc: "This self-describing API document (JSON)."},
		},
		Errors: []apiError{
			{400, "Bad Request", "Invalid slug, reserved slug, private+named combination, password in URL query, or bad ttl."},
			{401, "Unauthorized", "Missing/invalid bearer token (write/list) or wrong view password (read)."},
			{404, "Not Found", "No such artifact, or it has expired."},
			{405, "Method Not Allowed", "Wrong HTTP method for a known path (e.g. POST /list)."},
			{413, "Payload Too Large", "Upload exceeds the configured --max-upload limit."},
		},
	}
	writeJSON(w, http.StatusOK, help)
}

// publishHint returns a copy-pasteable one-liner to publish a first page.
func publishHint(base string) string {
	return fmt.Sprintf("curl --data-binary @index.html %s/publish", strings.TrimRight(base, "/"))
}
