#!/usr/bin/env bash
# Regression tests for pre-push-leak-audit.
#
# Run: test-pre-push-leak-audit [path-to-hook]
#      (vendored: scripts/test-pre-push-leak-audit.sh [path-to-hook])
#
# Without an argument it tests the hook next to it. Byte-identical wherever it
# is vendored, like the hook.
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
# large metadata stream did not. A linter cannot see that. The metadata case
# can.
#
# The 2026-10 cases reproduce ways the previous hook let a leak through while
# reporting clean: root and merge commits, display-quoted filenames, NUL bytes,
# a file named like the scanner, a deleted restricted file, failed enumeration,
# history known only to another remote, annotated tags and filenames. A planted
# backreference pattern hung it instead.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
default_hook="$here/pre-push-leak-audit"
[[ -e "$default_hook.sh" ]] && default_hook="$default_hook.sh"
HOOK="${1:-$default_hook}"
[[ -x "$HOOK" ]] || { echo "no executable hook at $HOOK" >&2; exit 2; }
HOOK=$(cd "$(dirname "$HOOK")" && pwd)/$(basename "$HOOK")

ZERO=0000000000000000000000000000000000000000
pass=0; fail=0
work=$(mktemp -d "${TMPDIR:-/tmp}/leak-audit-test.XXXXXX") || exit 2
trap 'rm -rf "$work"' EXIT

# A hung hook is a failed hook, and must not hang the suite.
budget=()
if command -v timeout >/dev/null 2>&1; then budget=(timeout 30)
elif command -v gtimeout >/dev/null 2>&1; then budget=(gtimeout 30)
fi

g() { git -C "$repo" "$@"; }
commit() { g add -A; g commit -q -m "$1"; }
# The two-remote cases mean nothing unless the other remote really holds HEAD;
# a failed push or fetch would otherwise let them pass on an empty remote.
private_holds_head() {
  [[ "$(g rev-parse -q --verify refs/remotes/private/main)" == "$(g rev-parse HEAD)" ]]
}

write_patterns() {
  cat > "$1" <<'PAT'
# test patterns
\bsecrethost\b
private\.example\.test
[Ff]orbidden[- ]?[Nn]ame
PAT
}

new_repo() {
  repo="$work/repo"
  rm -rf "$repo" "$work"/dest*.git; mkdir -p "$repo"
  g init -q
  g config user.name tekton
  g config user.email tekton@example.test
  g config commit.gpgsign false
  g config tag.gpgsign false
  write_patterns "$repo/.git/info/leakpatterns"
  ln -sf "$HOOK" "$repo/.git/hooks/pre-push"
}

setup_repo() {
  new_repo
  # A body long enough that the producer is still writing when grep -q exits.
  # Without this the SIGPIPE bug hides.
  local filler; filler=$(head -c 4000 /dev/zero | tr '\0' 'x')
  local i
  for i in $(seq 1 40); do
    echo "$i" > "$repo/f$i.txt"
    g add "f$i.txt"
    g commit -q -m "chore: seed $i" -m "$filler"
  done
}

# Feeds the hook one pre-push line. Defaults: push HEAD to a new branch on a
# destination that has nothing. Echoes the exit status.
run_hook() {
  local lref="${1:-refs/heads/main}" lsha="${2:-$(g rev-parse HEAD)}"
  local rref="${3:-refs/heads/main}" rsha="${4:-$ZERO}"
  echo "$lref $lsha $rref $rsha" \
    | (cd "$repo" && ${budget[@]+"${budget[@]}"} "$HOOK" origin test-url) >"$work/out" 2>&1
  echo $?
}

# Optional 4th argument: a regex the hook output must contain, so a block for
# the wrong reason (an unrelated error) does not count as the right block.
check() {
  local name="$1" want="$2" got="$3" why="${4:-}"
  if [[ -n "$why" && "$got" == "$want" ]] && ! command grep -qE -- "$why" "$work/out"; then
    printf 'FAIL %s (exit %s, but output lacks /%s/)\n' "$name" "$got" "$why"; fail=$((fail + 1))
  elif [[ "$got" == "$want" ]]; then
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
echo 'deploy to secrethost' > "$repo/d.md"; commit "docs: d"
check "file content: local pattern" 1 "$(run_hook)"

# awk, not sed -i: BSD sed takes -i's next word as a backup suffix and does not
# expand \r, so the fixture would silently stay LF there.
setup_repo
awk '{ printf "%s\r\n", $0 }' "$repo/.git/info/leakpatterns" > "$work/crlf" \
  && mv "$work/crlf" "$repo/.git/info/leakpatterns"
n_lines=$(wc -l < "$repo/.git/info/leakpatterns")
if [[ $n_lines -gt 0 && $(command grep -c $'\r$' "$repo/.git/info/leakpatterns") -eq $n_lines ]]; then
  check "CRLF fixture has a CR on every line" 0 0
else
  check "CRLF fixture has a CR on every line" 0 1
fi
echo 'deploy to secrethost' > "$repo/d.md"; commit "docs: d"
check "CRLF pattern file still matches" 1 "$(run_hook)" 'LEAK .*d\.md'

setup_repo
rm -f "$repo/.git/info/leakpatterns"
check "no pattern file: says generic only" 0 "$(run_hook)" 'checking the generic set only'

setup_repo
printf '# only comments\n\n' > "$repo/.git/info/leakpatterns"
check "comment-only pattern file: says generic only" 0 "$(run_hook)" 'has no patterns'

setup_repo
echo 'mail me at bob@private.example.test' > "$repo/c.md"; commit "docs: c"
check "file content: local domain pattern" 1 "$(run_hook)"

# The regression case. Metadata never appears in a diff, and the pipeline form
# of this check reported clean on a real match.
setup_repo
echo x > "$repo/m.md"; g add m.md
g -c user.name='Forbidden Name' -c user.email='x@example.test' commit -q -m "docs: m"
check "commit metadata: author name (SIGPIPE regression)" 1 "$(run_hook)"

setup_repo
echo x > "$repo/s.md"; commit "docs: mentions secrethost in the subject"
check "commit metadata: subject line" 1 "$(run_hook)"

# The credential fixtures below are ASSEMBLED AT RUNTIME rather than written
# as literals. This file would otherwise match the very generic patterns it
# tests, and the guard would block any push carrying it — correctly, since it
# cannot tell a fixture from a leak. Exempting this path by name was the other
# option and is worse: it would create a file where a real credential could sit
# unnoticed. The printf output is identical; only the source text differs.
setup_repo
printf -- '-----BEGIN OPENSSH %s-----\n' 'PRIVATE KEY' > "$repo/k.txt"; commit "chore: k"
check "generic pattern: private key header" 1 "$(run_hook)"

setup_repo
write_patterns "$repo/.leakpatterns"; g add -f .leakpatterns; g commit -q -m "chore: p"
check "tracked .leakpatterns is refused" 1 "$(run_hook)"

setup_repo
mkdir -p "$repo/.beads"; echo '{}' > "$repo/.beads/issues.jsonl"
g add -f .beads; g commit -q -m "chore: b"
check "tracked .beads is refused" 1 "$(run_hook)"

# A repo with no pattern file must still work, on generic patterns alone.
setup_repo
rm -f "$repo/.git/info/leakpatterns"
echo 'deploy to secrethost' > "$repo/d.md"; commit "docs: d"
check "no pattern file: local pattern not enforced" 0 "$(run_hook)"

setup_repo
rm -f "$repo/.git/info/leakpatterns"
printf 'AKIA%s\n' 'IOSFODNN7EXAMPLE' > "$repo/a.txt"; commit "chore: a"
check "no pattern file: generic pattern still enforced" 1 "$(run_hook)"

# --- 2026-10: ways the previous hook reported clean on a leak ---------------

# Root commit: diff-tree without --root lists nothing for it.
new_repo
echo 'deploy to secrethost' > "$repo/r.md"; commit "chore: root"
echo clean > "$repo/later.md"; commit "chore: later"
check "root commit content" 1 "$(run_hook)" "LEAK  blob .* r\\.md"

# Merge resolution: diff-tree without -m lists nothing for a merge.
setup_repo
g checkout -q -b side; echo side > "$repo/side.md"; commit "chore: side"
g checkout -q -; echo main > "$repo/main.md"; commit "chore: main"
g merge -q --no-ff --no-commit side >/dev/null 2>&1
echo 'deploy to secrethost' > "$repo/resolved.md"; g add resolved.md
g commit -q -m "chore: merge"
check "content introduced in a merge resolution" 1 "$(run_hook)" "LEAK  blob .* resolved\\.md"

# Filenames git display-quotes: the quoted form is not a valid object path,
# the lookup failed, and the failure read as a clean file.
for name in 'with space.md' $'new\nline.md' $'na\xc3\xafve.md' 'back\slash.md' 'dq"uote.md'; do
  setup_repo
  echo 'deploy to secrethost' > "$repo/$name"; commit "chore: odd name"
  check "content under filename $(printf '%q' "$name")" 1 "$(run_hook)" "LEAK  blob"
done

# A NUL byte made grep -I call the blob binary and report no match.
setup_repo
printf 'deploy to secrethost\n\0\0binary tail\n' > "$repo/blob.bin"; commit "chore: bin"
check "content in a blob with NUL bytes" 1 "$(run_hook)" "LEAK  blob .* blob\\.bin"

# Any file with the scanner's basename was exempt from scanning.
setup_repo
mkdir -p "$repo/scripts"
echo 'deploy to secrethost' > "$repo/scripts/pre-push-leak-audit.sh"; commit "chore: s"
check "content in a file named like the scanner" 1 "$(run_hook)" "LEAK  blob .* scripts/pre-push"

# A branch that tracks .leakpatterns plants patterns in the working tree. The
# previous hook evaluated them before refusing the file, so a backreference
# pattern hung it (the suite's 30 s budget reports 124). The working tree must
# never be read, and the tracked file is refused.
setup_repo
printf '%s\n' '(a*)(a*)(a*)(a*)\4\3\2\1b' > "$repo/.leakpatterns"
printf '%sb\n' "$(head -c 32 /dev/zero | tr '\0' a)" > "$repo/as.txt"
g add -f .leakpatterns as.txt; g commit -q -m "chore: planted patterns"
check "planted backreference pattern in the working tree" 1 "$(run_hook)" "LEAK  \\.leakpatterns is in outgoing"

# The pattern file itself refuses backreferences rather than running them.
setup_repo
printf '%s\n' '(a*)\1b' >> "$repo/.git/info/leakpatterns"
check "backreference in the pattern file is refused" 1 "$(run_hook)" "ERROR pattern above uses a backreference"

# The old in-tree location is not read and not silently ignored either.
setup_repo
mv "$repo/.git/info/leakpatterns" "$repo/.leakpatterns"
check "untracked in-tree .leakpatterns asks to be moved" 1 "$(run_hook)" "MOVE"

# Restricted path added then deleted before the push: the index is clean, the
# pushed history is not.
setup_repo
mkdir -p "$repo/.beads"; echo '{}' > "$repo/.beads/issues.jsonl"
g add -f .beads; g commit -q -m "chore: b"
g rm -q -r .beads; g commit -q -m "chore: unb"
check "restricted .beads deleted before push" 1 "$(run_hook)" "LEAK  \\.beads is in outgoing"

# Destination advertises a commit this clone never fetched: rev-list failed,
# and an empty range read as nothing to audit.
setup_repo
echo 'deploy to secrethost' > "$repo/d.md"; commit "docs: d"
check "destination sha unknown locally" 1 \
  "$(run_hook refs/heads/main "" refs/heads/main 1111111111111111111111111111111111111111)" \
  "LEAK  blob .* d\\.md"

# An outgoing object that cannot be read must block, not read as clean.
setup_repo
echo 'deploy to secrethost' > "$repo/gone.md"; commit "docs: gone"
blob=$(g rev-parse HEAD:gone.md)
rm -f "$repo/.git/objects/${blob:0:2}/${blob:2}"
check "missing object in outgoing history" 1 "$(run_hook)" "ERROR could not enumerate"

# History pushed to a private remote, then to a public one as a new branch:
# the previous hook excluded everything any remote had seen.
setup_repo
git init -q --bare "$work/dest-private.git"; git init -q --bare "$work/dest-public.git"
g remote add private "$work/dest-private.git"; g remote add public "$work/dest-public.git"
echo 'deploy to secrethost' > "$repo/d.md"; commit "docs: d"
g push -q --no-verify private HEAD:refs/heads/main 2>/dev/null
g fetch -q private
private_holds_head
check "precondition: the private remote holds the leak" 0 "$?"
check "new branch whose history is only on a private remote" 1 \
  "$(run_hook refs/heads/main "" refs/heads/feature)" "LEAK  blob .* d\\.md"

# Annotated tags are objects of their own: tagger and message are published.
setup_repo
git init -q --bare "$work/dest.git"; g remote add origin "$work/dest.git"
g push -q --no-verify origin HEAD:refs/heads/main 2>/dev/null; g fetch -q origin
g tag -a v1 -m "release for secrethost"
check "annotated tag message" 1 "$(run_hook refs/tags/v1 "$(g rev-parse v1)" refs/tags/v1)" "LEAK  tag"

setup_repo
git init -q --bare "$work/dest.git"; g remote add origin "$work/dest.git"
g push -q --no-verify origin HEAD:refs/heads/main 2>/dev/null; g fetch -q origin
g -c user.name='Forbidden Name' tag -a v1 -m "release"
check "annotated tag tagger" 1 "$(run_hook refs/tags/v1 "$(g rev-parse v1)" refs/tags/v1)" "LEAK  tag"

# Filenames are published too, as tree entries.
setup_repo
echo clean > "$repo/secrethost-notes.md"; commit "docs: n"
check "filename itself matches" 1 "$(run_hook)" "LEAK  tree"

# --- a failed intermediate step must block, not read as clean ---------------

# Fault injection by PATH shim: fault <tool> <body> writes a <tool> that runs
# <body> with the real binary in $REAL. Each case breaks one step and leaves a
# leak the hook would otherwise catch, so a pass here is a silent false pass.
fault_bin="$work/fault-bin"
fault() {
  local real; real=$(command -v "$1") || return 1
  rm -rf "$fault_bin"; mkdir -p "$fault_bin"
  { printf '#!/usr/bin/env bash\nREAL=%q\n' "$real"; printf '%s\n' "$2"; } > "$fault_bin/$1"
  chmod +x "$fault_bin/$1"
}

# The revision list exists but cannot be written: rev-list then reads an empty
# list, enumerates nothing, and the push looked clean. (Root ignores modes.)
if [[ $(id -u) -ne 0 ]]; then
  setup_repo
  echo 'deploy to secrethost' > "$repo/d.md"; commit "docs: d"
  # shellcheck disable=SC2016  # expands in the shim, not here
  fault mktemp 'd=$("$REAL" "$@") || exit
case $d in */leak-audit.*) : > "$d/revs"; chmod a-w "$d/revs" ;; esac
printf "%s\n" "$d"'
  check "revision list write fails" 1 "$(PATH="$fault_bin:$PATH" run_hook)" 'ERROR cannot write .*revs'
else
  printf 'skip revision list write fails: running as root\n'
fi

# The restricted-path filter ran in a process substitution, whose status
# nothing read: a failed stage dropped every path and .beads went out.
setup_repo
mkdir -p "$repo/.beads"; echo '{}' > "$repo/.beads/issues.jsonl"
g add -f .beads; g commit -q -m "chore: b"
# shellcheck disable=SC2016  # expands in the shim, not here
fault tr 'if [[ "${1:-}" == "\\0" ]]; then exit 1; fi
exec "$REAL" "$@"'
check "restricted-path filter fails" 1 "$(PATH="$fault_bin:$PATH" run_hook)" 'ERROR could not filter'

# An empty commit count read as zero and skipped every per-commit stage, so
# a restricted file deleted again before the push went out unseen.
setup_repo
mkdir -p "$repo/.beads"; echo '{}' > "$repo/.beads/issues.jsonl"
g add -f .beads; g commit -q -m "chore: b"
g rm -q -r .beads; g commit -q -m "chore: unb"
fault wc 'exit 1'
check "commit count fails" 1 "$(PATH="$fault_bin:$PATH" run_hook)" 'ERROR could not count'

# --- gitleaks ---------------------------------------------------------------

# An AWS temporary-credential key id (ASIA...) is outside the generic set, so
# only gitleaks can block it. Assembled at runtime like the fixtures above, and
# not of the EXAMPLE form gitleaks allowlists.
gl_key() { printf 'aws_key = "ASIA%s"\n' 'QZ3KF7MXR2B4N6TW'; }
gl_secret="ASIA""QZ3KF7MXR2B4N6TW"

# A PATH with every command on the current one except gitleaks.
nogl="$work/nogl-bin"; mkdir -p "$nogl"
IFS=: read -ra path_dirs <<< "$PATH"
for d in "${path_dirs[@]}"; do
  for f in "$d"/*; do
    b=${f##*/}
    [[ "$b" == gitleaks || -e "$nogl/$b" || ! -x "$f" ]] && continue
    ln -s "$f" "$nogl/$b" 2>/dev/null
  done
done

setup_repo
gl_key > "$repo/cfg.txt"; commit "chore: cfg"
check "gitleaks missing: says so and carries on" 0 "$(PATH="$nogl" run_hook)" 'gitleaks not installed'

if command -v gitleaks >/dev/null 2>&1; then
  setup_repo
  gl_key > "$repo/cfg.txt"; commit "chore: cfg"
  check "gitleaks: AWS key outside the generic set blocks" 1 "$(run_hook)" \
    'LEAK  gitleaks aws-access-token [0-9a-f]{12}  cfg\.txt:1'
  if command grep -qF "$gl_secret" "$work/out"; then
    check "gitleaks: finding is redacted" 0 1
  else
    check "gitleaks: finding is redacted" 0 0
  fi

  setup_repo
  mkdir -p "$repo/fixtures"; gl_key > "$repo/fixtures/cfg.txt"
  printf '[extend]\nuseDefault = true\n\n[[allowlists]]\npaths = [%s]\n' "'''^fixtures/'''" \
    > "$repo/.gitleaks.toml"
  commit "test: allowlisted fixture"
  check "gitleaks: fixture allowlisted in .gitleaks.toml passes" 0 "$(run_hook)" \
    'outgoing commits change \.gitleaks\.toml'

  # git log shows no patch for a merge without -m.
  setup_repo
  g checkout -q -b side; echo side > "$repo/side.md"; commit "chore: side"
  g checkout -q -; echo main > "$repo/main.md"; commit "chore: main"
  g merge -q --no-ff --no-commit side >/dev/null 2>&1
  gl_key > "$repo/resolved.txt"; g add resolved.txt; g commit -q -m "chore: merge"
  check "gitleaks: key added in a merge resolution blocks" 1 "$(run_hook)" \
    'LEAK  gitleaks aws-access-token .* resolved\.txt'

  # A config gitleaks cannot load must block, not read as "no leaks".
  setup_repo
  echo 'not [ toml' > "$repo/.gitleaks.toml"; commit "chore: broken config"
  check "gitleaks: error fails closed" 1 "$(run_hook)" 'ERROR gitleaks reported an error'
else
  printf 'skip gitleaks cases: gitleaks not installed\n'
fi

# --- end to end through git push --------------------------------------------

setup_repo
git init -q --bare "$work/dest.git"; g remote add origin "$work/dest.git"
g push -q origin HEAD:refs/heads/main >/dev/null 2>&1
check "real push: clean history passes" 0 "$?"
echo 'deploy to secrethost' > "$repo/d.md"; commit "docs: d"
g push -q origin HEAD:refs/heads/main >/dev/null 2>&1
check "real push: leak blocked" 1 "$?"
g push -q --no-verify origin HEAD:refs/heads/main >/dev/null 2>&1
check "real push: --no-verify bypasses" 0 "$?"
echo clean > "$repo/e.md"; commit "docs: e"
g push -q origin HEAD:refs/heads/main >/dev/null 2>&1
check "real push: clean push on top of published leak passes" 0 "$?"
g push -q origin HEAD:refs/heads/topic >/dev/null 2>&1
check "real push: new branch over history the destination holds passes" 0 "$?"
echo 'deploy to secrethost again' > "$repo/f.md"; commit "docs: f"
g push -q origin HEAD:refs/heads/topic2 >/dev/null 2>&1
check "real push: new leak on a new branch blocked" 1 "$?"

# Destination isolation through git push itself: another remote already holds
# the leak, this destination does not, so the push must still be blocked.
setup_repo
git init -q --bare "$work/dest.git"; g remote add origin "$work/dest.git"
git init -q --bare "$work/dest-private.git"; g remote add private "$work/dest-private.git"
g push -q origin HEAD:refs/heads/main >/dev/null 2>&1
echo 'deploy to secrethost' > "$repo/d.md"; commit "docs: d"
g push -q --no-verify private HEAD:refs/heads/main >/dev/null 2>&1; g fetch -q private
private_holds_head
check "precondition: real push, the private remote holds the leak" 0 "$?"
g push -q origin HEAD:refs/heads/feature >"$work/out" 2>&1
check "real push: history only on another remote is blocked" 1 "$?" "LEAK  blob .* d\\.md"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
