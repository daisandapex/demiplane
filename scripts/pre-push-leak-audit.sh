#!/usr/bin/env bash
# Pre-push leak audit.
#
# Repos that develop in the open have no promotion step between a commit and
# the public internet, so the last useful moment to catch a private identifier
# is immediately before the push.
#
# Install:
#
#   ln -sf ../../scripts/pre-push-leak-audit.sh .git/hooks/pre-push
#
# Verify it after installing. A leak guard that silently passes is worse than
# no guard, because it is trusted — see scripts/test-pre-push-leak-audit.sh,
# which CI runs.
#
# Bypass (deliberately awkward):
#   git push --no-verify
#
# Two pattern sources:
#
#   1. The generic set below. Credential shapes nobody should publish from any
#      machine. Useful to every repo without configuration.
#
#   2. A per-repo `.leakpatterns` file in the repo root, one extended-regex per
#      line, `#` for comments. It must stay untracked — a published list of what
#      you are hiding describes the thing you are hiding. Fleet hostnames,
#      personal domains, real-name identities and deanonymising signing keys go
#      here, because they differ per repo: what is permitted in one repo's
#      identity policy is a leak in another's.
set -uo pipefail

# Credential shapes only. Deliberately NOT private IP ranges: some of these
# repos are self-hosted LAN and mesh tools whose documentation legitimately
# contains addresses like 192.168.1.50 as examples. A rule that fires on every
# doc page is a rule that gets bypassed, and a bypassed hook protects nothing.
# Repos that need address patterns add their own ranges to .leakpatterns.
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

# Any in-repo copy of this script necessarily contains pattern-shaped text.
SELF_EXEMPT='(^|/)pre-push-leak-audit(\.sh)?$'

fail=0
note() { printf '  %s\n' "$*" >&2; }

echo "== pre-push leak audit ==" >&2

# git feeds pre-push one line per ref: <local ref> <local sha> <remote ref>
# <remote sha>. remote_ref is unused but named rather than dropped, so the
# protocol stays legible at the read.
# shellcheck disable=SC2034
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
      # Process substitution, not a pipe: `grep -q` exits on first match, the
      # producer takes SIGPIPE (141), and `set -o pipefail` would then report
      # the pipeline as FAILED — turning a detected leak into a passing check.
      if grep -qIiE "$patterns" < <(git show "$c:$f" 2>/dev/null); then
        note "LEAK  $c  $f"
        fail=1
      fi
    done < <(git diff-tree --no-commit-id --name-only -r "$c" 2>/dev/null)
  done

  # Commit metadata. Identity leaks live here and never appear in a diff --
  # author, committer and signing key are set independently of file content.
  # Same SIGPIPE/pipefail hazard as above, and this is where it actually bit:
  # the metadata stream is large enough that `git log` is still writing when a
  # match makes `grep -q` exit, so as a pipeline this check silently passed on
  # every leak it found.
  # $commits is deliberately unquoted: it is a newline-separated SHA list and
  # word splitting is how it becomes one argument per commit.
  # shellcheck disable=SC2086
  if grep -qIiE "$patterns" < <(git log --format='%an %ae %cn %ce %GK %GS %s %b' $commits 2>/dev/null); then
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

# The pattern file is the inverse of a secret: committing it publishes the list
# of identifiers being suppressed, which is enough to go looking for them.
if git ls-files --error-unmatch .leakpatterns >/dev/null 2>&1; then
  note "LEAK  .leakpatterns is tracked — it names what you are hiding"
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
