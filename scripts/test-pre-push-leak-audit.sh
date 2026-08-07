#!/usr/bin/env bash
# Regression tests for pre-push-leak-audit.
#
# Run: scripts/test-pre-push-leak-audit.sh
#
# The failure mode this exists for is a SILENT FALSE PASS. On 2026-08-06 the
# metadata check was found to have never worked: written as
#
#   git log --format=... | grep -qIiE "$patterns"
#
# under `set -o pipefail`, `grep -q` exits on the first match, `git log` takes
# SIGPIPE (141), and pipefail reports the pipeline as failed — so a detected
# leak read as "clean". It only manifests when the producer is still writing as
# grep quits, which is why small single-file content checks passed and the
# large metadata stream did not. A linter cannot see that. Case 4 can.
set -uo pipefail

HOOK="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pre-push-leak-audit.sh}"
[[ -x "$HOOK" ]] || { echo "no executable hook at $HOOK" >&2; exit 2; }

ZERO=0000000000000000000000000000000000000000
pass=0; fail=0
work=$(mktemp -d) || exit 2
trap 'rm -rf "$work"' EXIT

setup_repo() {
  repo="$work/repo"
  rm -rf "$repo"; mkdir -p "$repo"
  git -C "$repo" init -q
  git -C "$repo" config user.name tekton
  git -C "$repo" config user.email tekton@example.test
  git -C "$repo" config commit.gpgsign false
  # A body long enough that the producer is still writing when grep -q exits.
  # Without this the SIGPIPE bug hides.
  local filler; filler=$(head -c 4000 /dev/zero | tr '\0' 'x')
  local i
  for i in $(seq 1 40); do
    echo "$i" > "$repo/f$i.txt"
    git -C "$repo" add "f$i.txt"
    git -C "$repo" commit -q -m "chore: seed $i" -m "$filler"
  done
  cat > "$repo/.leakpatterns" <<'PAT'
# test patterns
\bsecrethost\b
private\.example\.test
[Ff]orbidden[- ]?[Nn]ame
PAT
  ln -sf "$HOOK" "$repo/.git/hooks/pre-push"
}

# Runs the hook over every commit (no remotes exist, so --not --remotes is a
# no-op and the whole history is in range). Echoes the exit status.
run_hook() {
  echo "refs/heads/main $(git -C "$repo" rev-parse HEAD) refs/heads/main $ZERO" \
    | (cd "$repo" && "$HOOK" origin test-url) >/dev/null 2>&1
  echo $?
}

check() {
  local name="$1" want="$2" got="$3"
  if [[ "$got" == "$want" ]]; then
    printf 'ok   %s\n' "$name"; pass=$((pass + 1))
  else
    printf 'FAIL %s (want exit %s, got %s)\n' "$name" "$want" "$got"; fail=$((fail + 1))
  fi
}

# Static analysis first, when available. Intentional splitting and the unused
# pre-push protocol field carry inline disables, so a finding here is real.
if command -v shellcheck >/dev/null 2>&1; then
  shellcheck "$HOOK" >/dev/null 2>&1
  check "shellcheck is clean" 0 "$?"
else
  printf 'skip shellcheck not installed\n'
fi

setup_repo
check "clean history passes" 0 "$(run_hook)"

setup_repo
echo 'deploy to secrethost' > "$repo/d.md"
git -C "$repo" add d.md; git -C "$repo" commit -q -m "docs: d"
check "file content: local pattern" 1 "$(run_hook)"

setup_repo
echo 'mail me at bob@private.example.test' > "$repo/c.md"
git -C "$repo" add c.md; git -C "$repo" commit -q -m "docs: c"
check "file content: local domain pattern" 1 "$(run_hook)"

# The regression case. Metadata never appears in a diff, and the pipeline form
# of this check reported clean on a real match.
setup_repo
echo x > "$repo/m.md"; git -C "$repo" add m.md
git -C "$repo" -c user.name='Forbidden Name' -c user.email='x@example.test' \
  commit -q -m "docs: m"
check "commit metadata: author name (SIGPIPE regression)" 1 "$(run_hook)"

setup_repo
echo x > "$repo/s.md"; git -C "$repo" add s.md
git -C "$repo" commit -q -m "docs: mentions secrethost in the subject"
check "commit metadata: subject line" 1 "$(run_hook)"

# The credential fixtures below are ASSEMBLED AT RUNTIME rather than written
# as literals. This file would otherwise match the very generic patterns it
# tests, and the guard would block any push carrying it — correctly, since it
# cannot tell a fixture from a leak. Exempting this path by name was the other
# option and is worse: it would create a file where a real credential could sit
# unnoticed. The printf output is identical; only the source text differs.
setup_repo
printf -- '-----BEGIN OPENSSH %s-----\n' 'PRIVATE KEY' > "$repo/k.txt"
git -C "$repo" add k.txt; git -C "$repo" commit -q -m "chore: k"
check "generic pattern: private key header" 1 "$(run_hook)"

setup_repo
git -C "$repo" add -f .leakpatterns; git -C "$repo" commit -q -m "chore: p"
check "tracked .leakpatterns is refused" 1 "$(run_hook)"

setup_repo
mkdir -p "$repo/.beads"; echo '{}' > "$repo/.beads/issues.jsonl"
git -C "$repo" add -f .beads; git -C "$repo" commit -q -m "chore: b"
check "tracked .beads is refused" 1 "$(run_hook)"

# A repo with no .leakpatterns must still work, on generic patterns alone.
setup_repo
rm -f "$repo/.leakpatterns"
echo 'deploy to secrethost' > "$repo/d.md"
git -C "$repo" add d.md; git -C "$repo" commit -q -m "docs: d"
check "no .leakpatterns: local pattern not enforced" 0 "$(run_hook)"

setup_repo
rm -f "$repo/.leakpatterns"
printf 'AKIA%s\n' 'IOSFODNN7EXAMPLE' > "$repo/a.txt"
git -C "$repo" add a.txt; git -C "$repo" commit -q -m "chore: a"
check "no .leakpatterns: generic pattern still enforced" 1 "$(run_hook)"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
