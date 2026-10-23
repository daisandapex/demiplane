#!/usr/bin/env bash
# Pre-push leak audit.
#
# Repos that develop in the open have no promotion step between a commit and
# the public internet, so the last useful moment to catch a private identifier
# is immediately before the push.
#
# Install per repo by symlinking, never by copying, so a fix lands once:
#
#   ln -sf <path-to>/pre-push-leak-audit .git/hooks/pre-push
#
# A repo that vendors this file (as scripts/pre-push-leak-audit.sh) keeps it
# byte-identical to the upstream copy, next to the same test script.
#
# Verify it after installing. A leak guard that silently passes is worse than
# no guard, because it is trusted.
#
# Bypass (deliberately awkward):
#   git push --no-verify
#
# Two pattern sources:
#
#   1. The generic set below. Credential shapes nobody should publish from any
#      machine. Useful to every repo without configuration.
#
#   2. A per-repo pattern file at .git/info/leakpatterns, one extended regex
#      per line, `#` for comments. A published list of what you are hiding
#      describes the thing you are hiding, so it lives under .git/, which is
#      never pushed or checked out. Fleet hostnames, personal domains, real-name
#      identities and deanonymising signing keys go here, because they differ
#      per repo: what is permitted in one repo's identity policy is a leak in
#      another's. The working tree is never read: a checked-out branch could
#      plant patterns there. Backreferences are refused (the one ERE feature
#      with exponential matching cost) and the scan runs under a time budget,
#      LEAK_AUDIT_TIMEOUT seconds (default 300), when timeout(1) exists.
#
# What is audited: every object reachable from the pushed refs that the
# destination does not already have, judged only from the remote SHAs git
# passes on stdin. Presence on some other remote proves nothing about this
# destination. That covers root commits, merge resolutions, every filename
# (tree objects), binary blobs, commit metadata and annotated tags. Any
# enumeration or read error blocks the push: an audit that could not look is
# not a clean audit.
set -uo pipefail
export LC_ALL=C

# Credential shapes only. Deliberately NOT private IP ranges: some of these
# repos are self-hosted LAN and mesh tools whose documentation legitimately
# contains addresses like 192.168.1.50 as examples. A rule that fires on every
# doc page is a rule that gets bypassed, and a bypassed hook protects nothing.
# Repos that need address patterns add their own ranges to the pattern file.
GENERIC_PATTERNS=(
  'BEGIN [A-Z ]*PRIVATE KEY'
  '\bAKIA[0-9A-Z]{16}\b'
  '\bgh[pousr]_[A-Za-z0-9]{20,}\b'
  '\bxox[baprs]-[0-9A-Za-z-]{10,}\b'
  '\bsk-[A-Za-z0-9]{32,}\b'
  'aws_secret_access_key\s*='
)

# Paths that must never be published, at the repo root.
#   .beads         local tracker state; in some setups it carries infra detail.
#   .leakpatterns  the old in-tree pattern file; committing it publishes the
#                  list of identifiers being suppressed.
RESTRICTED_RE='^(\.beads(/|$)|\.leakpatterns$)'

fail=0
note() { printf '  %s\n' "$*" >&2; }

blocked() {
  cat >&2 <<'MSG'

  PUSH BLOCKED — see above.

  A private identifier in a public repository is not retractable, and
  rewriting published history is a separate and disruptive operation.
  Fix the content rather than bypassing.

  False positive? Push with --no-verify and say why in the PR.
MSG
  exit 1
}

# An audit that could not complete is a failed audit.
abort() {
  note "ERROR $*"
  note "the audit could not complete, so nothing was verified"
  blocked
}

echo "== pre-push leak audit ==" >&2

git_dir=$(git rev-parse --git-common-dir 2>/dev/null) || abort "not inside a git repository"
repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || repo_root=

work=$(mktemp -d "${TMPDIR:-/tmp}/leak-audit.XXXXXX") || abort "cannot create a scratch directory"
trap 'rm -rf "$work"' EXIT

# --- patterns ---------------------------------------------------------------

pattern_file="$git_dir/info/leakpatterns"
printf '%s\n' "${GENERIC_PATTERNS[@]}" > "$work/patterns" || abort "cannot write $work/patterns"

if [[ -e "$pattern_file" ]]; then
  [[ -f "$pattern_file" && -r "$pattern_file" ]] || abort "$pattern_file is not a readable file"
  # A CR left by a CRLF editor would make every line demand a literal \r
  # and silently match nothing, so carriage returns are stripped first.
  # Blank lines would match everything, so they go along with comments.
  tr -d '\r' < "$pattern_file" | command grep -vE '^[[:space:]]*(#|$)' > "$work/repo-patterns"
  rc=("${PIPESTATUS[@]}")
  [[ ${rc[0]} -eq 0 && ${rc[1]} -le 1 ]] || abort "cannot read $pattern_file"
  if [[ -s "$work/repo-patterns" ]]; then
    cat "$work/repo-patterns" >> "$work/patterns" || abort "cannot write $work/patterns"
  else
    note "NOTE  $pattern_file has no patterns; checking the generic set only"
  fi
elif [[ -n "$repo_root" && -e "$repo_root/.leakpatterns" ]]; then
  # The old location. Reading it would trust the working tree; ignoring it
  # would silently drop this repo's patterns. Neither is acceptable.
  note "MOVE  .leakpatterns is no longer read from the working tree"
  note "      run: mv .leakpatterns $pattern_file"
  blocked
else
  note "NOTE  no $pattern_file; checking the generic set only"
fi

# Odd number of backslashes before a digit: a backreference.
if command grep -nE '(^|[^\\])(\\\\)*\\[1-9]' "$work/patterns" >&2; then
  abort "pattern above uses a backreference; rewrite it without one"
fi

command grep -E -f "$work/patterns" </dev/null >/dev/null 2>"$work/regex-err"
if [[ $? -gt 1 ]]; then
  note "$(cat "$work/regex-err")"
  abort "a pattern is not a valid extended regex"
fi

# Time budget, where the platform has one. On macOS without coreutils the
# backreference ban is the only bound.
budget=
if command -v timeout >/dev/null 2>&1; then
  budget=timeout
elif command -v gtimeout >/dev/null 2>&1; then
  budget=gtimeout
fi

# -a: a NUL byte must not turn a blob into "binary, no match".
scan() {
  if [[ -n "$budget" ]]; then
    "$budget" "${LEAK_AUDIT_TIMEOUT:-300}" grep -qaiE -f "$work/patterns"
  else
    command grep -qaiE -f "$work/patterns"
  fi
}

# --- what is outgoing -------------------------------------------------------

# git feeds pre-push one line per ref: <local ref> <local sha> <remote ref>
# <remote sha>. remote_ref is unused but named rather than dropped, so the
# protocol stays legible at the read.
: > "$work/revs"
refs=()
sha_re='^[0-9a-f]{40}([0-9a-f]{24})?$'
# shellcheck disable=SC2034
while read -r local_ref local_sha remote_ref remote_sha; do
  [[ -z "${local_ref:-}" ]] && continue
  [[ "${local_sha:-}" =~ $sha_re && "${remote_sha:-}" =~ $sha_re ]] \
    || abort "unexpected pre-push input for ${local_ref}"

  if [[ ! "$local_sha" =~ ^0+$ ]]; then          # not a deletion
    echo "$local_sha" >> "$work/revs"
    refs+=("$local_ref")
  fi

  # The destination advertised remote_sha, so it holds that history. If it is
  # not here locally it cannot be excluded, and the wider set is audited.
  if [[ ! "$remote_sha" =~ ^0+$ ]]; then
    if git cat-file -e "$remote_sha" 2>/dev/null; then
      echo "^$remote_sha" >> "$work/revs"
    else
      note "destination has ${remote_sha:0:12}, unknown here; auditing without excluding it"
    fi
  fi
done

if [[ ${#refs[@]} -eq 0 ]]; then
  echo "== leak audit clean ==" >&2
  exit 0
fi

git rev-list --objects --no-object-names --stdin < "$work/revs" > "$work/objects" \
  || abort "could not enumerate outgoing objects (missing or corrupt object?)"
git cat-file --batch-check='%(objectname) %(objecttype)' < "$work/objects" > "$work/typed" \
  || abort "could not read outgoing object types"
if command grep -q ' missing$' "$work/typed"; then
  abort "outgoing object missing: $(command grep -m1 ' missing$' "$work/typed")"
fi
git rev-list --stdin < "$work/revs" > "$work/commits" \
  || abort "could not enumerate outgoing commits"

n_obj=$(wc -l < "$work/objects" | tr -d ' ')
n_commit=$(wc -l < "$work/commits" | tr -d ' ')
note "auditing ${n_commit} commit(s), ${n_obj} object(s) for ${refs[*]}"

# --- content: every object, raw bytes ---------------------------------------

# Blobs, trees (filenames), commits (identities, messages) and annotated tags
# (tagger, message) all go through one stream. cat-file ends every object with
# a newline, so no match can straddle two objects.
#
# A pipeline, so read every status. grep -q exits on the first match and the
# producer then takes SIGPIPE: a match is a leak whatever the producer says,
# but "no match" is only believed when the producer finished cleanly.
git cat-file --batch < "$work/objects" | scan
st=("${PIPESTATUS[@]}")
if [[ ${st[1]} -eq 0 ]]; then
  # Slow path, only on a hit: find which objects matched.
  git rev-list --objects --stdin < "$work/revs" > "$work/names" \
    || abort "could not enumerate outgoing object names"
  hits=0
  while read -r sha type; do
    git cat-file "$type" "$sha" | scan
    s=("${PIPESTATUS[@]}")
    if [[ ${s[1]} -eq 0 ]]; then
      where=$(command grep -m1 "^$sha " "$work/names" | cut -d' ' -f2-)
      where=${where//[[:cntrl:]]/?}
      note "LEAK  $type ${sha:0:12}  ${where:-(commit, tag or root tree)}"
      hits=$((hits + 1))
    elif [[ ${s[1]} -ne 1 || ${s[0]} -ne 0 ]]; then
      abort "could not scan $type $sha"
    fi
  done < "$work/typed"
  [[ $hits -gt 0 ]] || note "LEAK  match in the outgoing object stream, not attributable to one object"
  fail=1
elif [[ ${st[1]} -ne 1 ]]; then
  [[ ${st[1]} -eq 124 ]] && abort "content scan exceeded LEAK_AUDIT_TIMEOUT (${LEAK_AUDIT_TIMEOUT:-300}s)"
  abort "content scan failed (grep exit ${st[1]})"
elif [[ ${st[0]} -ne 0 ]]; then
  abort "could not read outgoing objects (cat-file exit ${st[0]})"
fi

# --- signing identity -------------------------------------------------------

# Signing key and signer are computed by the verifier, not stored as text, so
# the raw scan above cannot see them. Same pipeline status rules as above.
if [[ $n_commit -gt 0 ]]; then
  git log --no-walk=unsorted --stdin --format='%GK %GS' < "$work/commits" 2>/dev/null | scan
  st=("${PIPESTATUS[@]}")
  if [[ ${st[1]} -eq 0 ]]; then
    note "LEAK  commit signing key or signer matches a private pattern"
    fail=1
  elif [[ ${st[1]} -ne 1 || ${st[0]} -ne 0 ]]; then
    abort "could not read commit signatures (git ${st[0]}, grep ${st[1]})"
  fi
fi

# --- restricted paths in outgoing history -----------------------------------

# Judged against the commits being published, not the index: a file added and
# deleted again before the push is still in the history the push carries.
# -m --root: merges against every parent, root commits against the empty tree.
# -z: names arrive verbatim, whatever bytes they contain.
: > "$work/paths"
if [[ $n_commit -gt 0 ]]; then
  git diff-tree --stdin --no-commit-id -r -m --root -z --name-only \
    --diff-filter=ACMRT < "$work/commits" > "$work/paths" \
    || abort "could not list paths changed by outgoing commits"
fi
# The tips themselves, for files inherited from history the destination has.
while read -r rev; do
  [[ "$rev" == ^* ]] && continue
  git rev-parse -q --verify "$rev^{commit}" >/dev/null 2>&1 || continue
  git ls-tree -z --name-only "$rev" -- .beads .leakpatterns >> "$work/paths" \
    || abort "could not list the tree of $rev"
done < "$work/revs"
while read -r p; do
  case "$p" in
    .beads)        note "LEAK  .beads is in outgoing history — it is local state and must stay untracked" ;;
    .leakpatterns) note "LEAK  .leakpatterns is in outgoing history — it names what you are hiding" ;;
  esac
  fail=1
done < <(tr '\0' '\n' < "$work/paths" | command grep -E "$RESTRICTED_RE" | cut -d/ -f1 | sort -u)

[[ "$fail" -ne 0 ]] && blocked

echo "== leak audit clean ==" >&2
exit 0
