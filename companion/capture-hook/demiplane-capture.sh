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
#   DEMIPLANE_CAPTURE_MAX_BYTES  Largest file a restricted capture will snapshot,
#                          in bytes. Default 104857600 (the server's 100 MiB
#                          --max-upload default); 0 = unlimited.
#
# Dependencies: bash, curl 7.55+, jq; python3 when DEMIPLANE_CAPTURE_DIR is set.
#
# Exit status is always 0 in hook mode (publishing is best-effort and must never
# break the editing workflow); failures are reported on stderr.

set -uo pipefail

# Bytes, not characters: log() below matches control bytes, and nothing else
# here needs more than ASCII. C is always available, so this never warns.
# curl gets the caller's value back, since it can use the locale to convert a
# non-ASCII host name; an empty LC_ALL means the same as none.
caller_lc_all=${LC_ALL-}
LC_ALL=C

# The token reaches curl as a header line on stdin. Never in argv, which any
# local account can read with ps, and never in a child's environment. A caller
# that exports its own `token` would keep it exported through the assignment,
# so the export attribute is dropped as well (export -n is bash 3.2 safe).
token=${DEMIPLANE_TOKEN:-}
export -n token
unset DEMIPLANE_TOKEN

# log writes one stderr line per message. File names and server replies reach
# it, so each control character becomes '?', as the leak audit does: C0 bytes,
# DEL, and the UTF-8 encoding of C1 (c2 80 to c2 9f). Under LC_ALL=C the
# patterns match bytes, so the result does not depend on the caller's locale.
log() {
	local msg="$*"
	msg=${msg//$'\xc2'[$'\x80'-$'\x9f']/?}
	msg=${msg//[$'\x01'-$'\x1f'$'\x7f']/?}
	printf 'demiplane-capture: %s\n' "$msg" >&2
}

# Mirrors the server's --max-upload default (100 MiB) so a restricted capture
# never snapshots a file the default server would refuse anyway.
default_capture_max_bytes=104857600

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
	local f=$1 source=${2:-$1}
	[[ -f $source ]] || return 1
	case "${f,,}" in
	*.html | *.htm)
		# Self-contained heuristic: a real document declares <html> or a doctype.
		# A fragment edited into a partial file is skipped on purpose.
		# grep's own errors would name the file unreplaced; an unreadable file
		# is simply not an artifact.
		grep -qiE '<!doctype html|<html' -- "$source" 2>/dev/null
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
	local file=$1 source=${2:-$1}

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

	# -H @- reads header lines from stdin (curl 7.55+), fed by the here-string.
	local -a auth=()
	if [[ -n $token ]]; then
		if [[ $token == *[$'\r\n']* ]]; then
			log "DEMIPLANE_TOKEN contains a line break — not sending it"
			return 1
		fi
		auth=(-H @-)
	fi

	# Accept: application/json so we can pull the canonical URL out of the reply.
	local resp http
	resp=$(LC_ALL=$caller_lc_all curl -fsS --max-time 30 \
		"${auth[@]}" \
		-H 'Accept: application/json' \
		--data-binary @"$source" \
		"${base}/publish${query}" \
		-w $'\n%{http_code}' 2>/dev/null <<<"Authorization: Bearer ${token}")
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

# Optional path restriction. Copy from one confined open into a private
# snapshot: inspection and curl must not reopen the attacker-controlled path.
source=$file
if [[ -n ${DEMIPLANE_CAPTURE_DIR:-} ]]; then
	# The snapshot holds a copy of the user's file: keep it owner-only.
	umask 077
	if ! capture_tmp=$(mktemp -d); then
		log "skipped $file: cannot create snapshot directory"
		exit 0
	fi
	trap 'rm -rf -- "$capture_tmp"' EXIT
	source=$capture_tmp/artifact
	if ! python3 - "$DEMIPLANE_CAPTURE_DIR" "$file" \
		"${DEMIPLANE_CAPTURE_MAX_BYTES:-$default_capture_max_bytes}" >"$source" <<'PY'
import os
import re
import stat
import sys

path = sys.argv[2] if len(sys.argv) > 2 else "?"
try:
    root, path, limit = sys.argv[1:]
    try:
        limit = int(limit)
    except ValueError:
        limit = -1
    if limit < 0:
        raise ValueError("DEMIPLANE_CAPTURE_MAX_BYTES must be a non-negative integer")
    if not os.path.isabs(root) or not os.path.isabs(path):
        raise ValueError("absolute paths required")
    prefix = root.rstrip("/") + "/"
    if not path.startswith(prefix):
        raise ValueError("outside capture directory")
    parts = path[len(prefix):].split("/")
    if ".." in parts or not parts[-1]:
        raise ValueError("parent traversal or directory")
    parts = [part for part in parts if part not in ("", ".")]
    if not parts:
        raise ValueError("no file")
    # The configured root is trusted; every component beneath it must be
    # opened relative to a held directory descriptor, without following links.
    directory = os.open(root, os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in parts[:-1]:
            child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                            dir_fd=directory)
            os.close(directory)
            directory = child
        fd = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK,
                     dir_fd=directory)
    finally:
        os.close(directory)
    with os.fdopen(fd, "rb") as opened:
        info = os.fstat(opened.fileno())
        if not stat.S_ISREG(info.st_mode):
            raise ValueError("not a regular file")
        # A hardlink is a second name for an inode that may live outside the
        # capture directory; O_NOFOLLOW cannot see it, so refuse shared inodes.
        if info.st_nlink > 1:
            raise ValueError("hardlinked file")
        if limit and info.st_size > limit:
            raise ValueError(f"{info.st_size} bytes exceeds the {limit}-byte cap")
        # The file can grow after fstat; bound the copy itself as well.
        copied = 0
        while True:
            chunk = opened.read(1 << 16)
            if not chunk:
                break
            copied += len(chunk)
            if limit and copied > limit:
                raise ValueError(f"grew past the {limit}-byte cap while copying")
            sys.stdout.buffer.write(chunk)
        sys.stdout.buffer.flush()
except (OSError, ValueError) as exc:
    reason = exc.strerror if isinstance(exc, OSError) and exc.strerror else str(exc)
    # Same replacement as log(), on the path's own bytes.
    line = os.fsencode(f"capture dir rejected {path}: {reason}")
    line = re.sub(rb"\xc2[\x80-\x9f]|[\x00-\x1f\x7f]", b"?", line)
    sys.stderr.buffer.write(b"demiplane-capture: " + line + b"\n")
    sys.stderr.buffer.flush()
    sys.exit(1)
PY
	then
		log "skipped $file: not captured (reason logged above)"
		exit 0
	fi
fi

if ! looks_like_artifact "$file" "$source"; then
	exit 0
fi

if url=$(publish "$file" "$source"); then
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
