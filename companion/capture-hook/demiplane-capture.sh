#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# demiplane-capture — publish a self-contained HTML (or markdown) artifact to a
# demiplane instance, so the page lands on YOUR mesh instead of a public host.
#
# Two modes, one script:
#   • Claude Code hook  — wired as a PostToolUse hook (Write/Edit). Reads the
#     hook JSON on stdin, and if the just-written file is a self-contained HTML
#     document, POSTs it to demiplane and tells the agent the resulting URL.
#   • CLI publish        — `demiplane-capture <file.html>` publishes a file
#     directly. Handy outside Claude Code, or for testing the hook config.
#
# It is the rox.1 linchpin: artifacts land on the mesh, not claude.ai.
#
# Configuration (environment variables):
#   DEMIPLANE_URL        Base URL of your instance, e.g. http://demiplane.mesh:8080
#                        (required). Trailing slash optional.
#   DEMIPLANE_TOKEN      Bearer token, if the instance requires publish auth.
#   DEMIPLANE_CAPTURE    Hook-mode gate: the hook NO-OPs unless this is 1/true/yes.
#                        (CLI mode ignores it — an explicit file arg is consent.)
#                        Prevents surprise publishing the moment the hook is wired.
#   DEMIPLANE_SLUG       Force a named slug. Default: derived from the filename.
#                        Set to the empty string to always let the server pick one.
#   DEMIPLANE_PRIVATE    1/true → publish ?private=true (unguessable capability URL).
#                        Mutually exclusive with a named slug, so slug is dropped.
#   DEMIPLANE_RENDER     1/true → also capture markdown (.md/.markdown) files,
#                        publishing them with ?render=md (HTML on the server side).
#   DEMIPLANE_CAPTURE_DIR  Restrict hook captures to files under this directory
#                          (absolute path). Default: no restriction.
#
# Dependencies: bash, curl, jq.
#
# Exit status is always 0 in hook mode (publishing is best-effort and must never
# break the editing workflow); failures are reported on stderr.

set -uo pipefail

log() { printf 'demiplane-capture: %s\n' "$*" >&2; }

# is_true returns 0 for 1/true/yes/on (case-insensitive), 1 otherwise.
is_true() {
	case "$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')" in
	1 | true | yes | on) return 0 ;;
	*) return 1 ;;
	esac
}

# derive_slug turns a path into a URL-safe named slug matching demiplane's
# namedSlugRe (^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$). Empty output → let the
# server generate a friendly slug.
derive_slug() {
	local base
	base=$(basename -- "$1")
	base=${base%.*}                                  # strip extension
	base=$(printf '%s' "$base" | tr '[:upper:]' '[:lower:]')
	base=$(printf '%s' "$base" | tr -c 'a-z0-9._-' '-')  # non-safe → '-'
	base=$(printf '%s' "$base" | sed -E 's/-+/-/g; s/^[-._]+//; s/[-._]+$//')
	printf '%s' "${base:0:128}"
}

# looks_like_artifact returns 0 if $1 is a file we should publish: a .html/.htm
# whose content is a self-contained document, or (when DEMIPLANE_RENDER is on) a
# markdown file.
looks_like_artifact() {
	local f=$1
	[[ -f $f ]] || return 1
	case "${f,,}" in
	*.html | *.htm)
		# Self-contained heuristic: a real document declares <html> or a doctype.
		# A fragment edited into a partial file is skipped on purpose.
		grep -qiE '<!doctype html|<html' -- "$f"
		return $?
		;;
	*.md | *.markdown)
		is_true "${DEMIPLANE_RENDER:-}"
		return $?
		;;
	esac
	return 1
}

# publish POSTs $1 to demiplane and prints the artifact URL on stdout. Returns
# nonzero (and logs) on failure.
publish() {
	local file=$1

	if [[ -z ${DEMIPLANE_URL:-} ]]; then
		log "DEMIPLANE_URL is not set — cannot publish $file"
		return 1
	fi
	local base=${DEMIPLANE_URL%/}

	# Assemble query params. private and slug are mutually exclusive (the server
	# rejects the combination), so private wins when both are requested.
	local -a params=()
	if is_true "${DEMIPLANE_PRIVATE:-}"; then
		params+=("private=true")
	else
		local slug=${DEMIPLANE_SLUG-__derive__}
		if [[ $slug == "__derive__" ]]; then
			slug=$(derive_slug "$file")
		fi
		[[ -n $slug ]] && params+=("slug=$slug")
	fi
	case "${file,,}" in
	*.md | *.markdown) params+=("render=md") ;;
	esac

	local query=""
	if ((${#params[@]})); then
		local IFS='&'
		query="?${params[*]}"
	fi

	local -a auth=()
	[[ -n ${DEMIPLANE_TOKEN:-} ]] && auth=(-H "Authorization: Bearer ${DEMIPLANE_TOKEN}")

	# Accept: application/json so we can pull the canonical URL out of the reply.
	local resp http
	resp=$(curl -fsS --max-time 30 \
		"${auth[@]}" \
		-H 'Accept: application/json' \
		--data-binary @"$file" \
		"${base}/publish${query}" \
		-w $'\n%{http_code}' 2>/dev/null)
	local rc=$?
	if ((rc != 0)); then
		log "publish failed (curl exit $rc) for $file → ${base}/publish${query}"
		return 1
	fi
	http=${resp##*$'\n'}
	resp=${resp%$'\n'*}
	if [[ $http != 2* ]]; then
		log "publish rejected (HTTP $http) for $file"
		return 1
	fi

	local url
	url=$(printf '%s' "$resp" | jq -r '.url // empty' 2>/dev/null)
	[[ -z $url ]] && url="$resp"   # non-JSON fallback (shouldn't happen)
	printf '%s' "$url"
}

# --- CLI mode: explicit file argument(s) ---
if [[ ${1:-} != "" && ${1:-} != "-" ]]; then
	status=0
	for f in "$@"; do
		if url=$(publish "$f"); then
			log "published $f → $url"
			printf '%s\n' "$url"
		else
			status=1
		fi
	done
	exit "$status"
fi

# --- Hook mode: read PostToolUse JSON from stdin ---
input=$(cat)

if ! is_true "${DEMIPLANE_CAPTURE:-}"; then
	# Gate off → silently do nothing (the hook is wired but disabled).
	exit 0
fi

file=$(printf '%s' "$input" | jq -r '.tool_input.file_path // empty' 2>/dev/null)
if [[ -z $file ]]; then
	exit 0
fi

# Optional path restriction.
if [[ -n ${DEMIPLANE_CAPTURE_DIR:-} ]]; then
	case "$file" in
	"${DEMIPLANE_CAPTURE_DIR%/}"/*) : ;;
	*) exit 0 ;;
	esac
fi

if ! looks_like_artifact "$file"; then
	exit 0
fi

if url=$(publish "$file"); then
	log "published $(basename -- "$file") → $url"
	# Feed the mesh URL back to the agent so it shares THAT, not a public link.
	jq -n --arg url "$url" --arg file "$(basename -- "$file")" '{
		hookSpecificOutput: {
			hookEventName: "PostToolUse",
			additionalContext: "Published \($file) to demiplane: \($url) — share this internal URL, not a public host."
		}
	}'
fi

# Always succeed: publishing must never block the editing workflow.
exit 0
