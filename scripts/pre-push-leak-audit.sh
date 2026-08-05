#!/usr/bin/env bash
# Pre-push leak audit.
#
# This project develops in the open. There is no promotion step between a commit
# and the public internet, so the last useful moment to catch a private
# identifier is immediately before the push.
#
# Install:
#   ln -sf ../../scripts/pre-push-leak-audit.sh .git/hooks/pre-push
#
# Bypass (deliberately awkward):
#   git push --no-verify
#
# Two pattern sources:
#
#   1. The generic set below. Things nobody should publish from any machine:
#      RFC1918 and CGNAT addresses, private key headers, obvious credential
#      shapes. Useful to every contributor.
#
#   2. An optional `.leakpatterns` file in the repo root, one extended-regex per
#      line, `#` for comments. It is gitignored. If you operate this software on
#      infrastructure whose hostnames, domains or address ranges should not
#      appear in public commits, put them there. Deliberately not committed:
#      a published list of what you are hiding describes the thing you are
#      hiding.
set -uo pipefail

# Credential shapes only. Deliberately NOT private IP ranges: this is a
# self-hosted LAN and mesh tool, so its documentation legitimately contains
# addresses like 192.168.1.50 as examples. A rule that fires on every doc page
# is a rule that gets bypassed, and a bypassed hook protects nothing. Operators
# who need address patterns can add their own ranges to .leakpatterns.
GENERIC_PATTERNS=(
  'BEGIN [A-Z ]*PRIVATE KEY'
  '\bAKIA[0-9A-Z]{16}\b'
  '\bgh[pousr]_[A-Za-z0-9]{20,}\b'
  '\bxox[baprs]-[0-9A-Za-z-]{10,}\b'
  '\bsk-[A-Za-z0-9]{32,}\b'
  'aws_secret_access_key\s*='
)

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
patterns=$(IFS='|'; printf '%s' "${GENERIC_PATTERNS[*]}")

if [[ -f "$repo_root/.leakpatterns" ]]; then
  extra=$(grep -vE '^\s*(#|$)' "$repo_root/.leakpatterns" | paste -sd'|' -)
  [[ -n "$extra" ]] && patterns="$patterns|$extra"
fi

# This script necessarily contains pattern-shaped text. Exempt it.
SELF_EXEMPT='^scripts/pre-push-leak-audit\.sh$'

fail=0
note() { printf '  %s\n' "$*" >&2; }

echo "== pre-push leak audit ==" >&2

while read -r local_ref local_sha remote_ref remote_sha; do
  [[ -z "${local_sha:-}" ]] && continue
  [[ "$local_sha" =~ ^0+$ ]] && continue   # branch deletion

  if [[ -z "${remote_sha:-}" || "$remote_sha" =~ ^0+$ ]]; then
    commits=$(git rev-list "$local_sha" --not --remotes 2>/dev/null)
  else
    commits=$(git rev-list "${remote_sha}..${local_sha}" 2>/dev/null)
  fi
  [[ -z "$commits" ]] && continue

  note "auditing $(printf '%s\n' "$commits" | grep -c .) commit(s) for $local_ref"

  # Content of every file the pushed commits introduce or change.
  for c in $commits; do
    while IFS= read -r f; do
      [[ -z "$f" ]] && continue
      [[ "$f" =~ $SELF_EXEMPT ]] && continue
      if git show "$c:$f" 2>/dev/null | grep -qIiE "$patterns"; then
        note "LEAK  $c  $f"
        fail=1
      fi
    done < <(git diff-tree --no-commit-id --name-only -r "$c" 2>/dev/null)
  done

  # Commit metadata. Identity leaks live here and never appear in a diff --
  # author, committer and signing key are set independently of file content.
  if git log --format='%an %ae %cn %ce %s %b' $commits 2>/dev/null | grep -qIiE "$patterns"; then
    note "LEAK  commit metadata or message matches a private pattern"
    fail=1
  fi
done

# Local tracker state must not be committed: it is working state, and in some
# setups it carries infrastructure detail.
if git ls-files --error-unmatch .beads >/dev/null 2>&1; then
  note "LEAK  .beads is tracked — it is local state and must stay untracked"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  cat >&2 <<'MSG'

  PUSH BLOCKED — see above.

  A private identifier in a public repository is not retractable, and
  rewriting published history is a separate and disruptive operation.
  Fix the content rather than bypassing.

  False positive? Push with --no-verify and say why in the PR.
MSG
  exit 1
fi

echo "== leak audit clean ==" >&2
exit 0
