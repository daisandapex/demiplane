#!/usr/bin/env bash
# check() evals its condition string, so the variables it names are read
# there and the single quotes are meant.
# shellcheck disable=SC2016,SC2034
#
# Regression tests for scripts/fuzz.sh, run without Go.
#
#   scripts/test-fuzz.sh
#
# The name filter once read `grep -E "^[^<TAB>]*$filter"` with a literal
# `\t` inside the bracket, which excludes the letter t rather than a tab:
# filtering on "Redirect" missed FuzzNextRedirect, and the filter could
# match a package path. These cases pin the filter to the target name and
# check the per-target environment (memory ceiling, TMPDIR moved into
# delete-me/, timing switch) with a stub in place of the go command.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
fuzz="$here/fuzz.sh"
scratch=$(mktemp -d)
fails=0

fixture="$scratch/repo"
mkdir -p "$fixture/internal/reply" "$fixture/internal/tab" "$fixture/cmd/x"
cat >"$fixture/internal/reply/a_test.go" <<'GO'
func FuzzNextRedirect(f *testing.F) {
func FuzzAnswerPage(f *testing.F) {
GO
cat >"$fixture/internal/tab/b_test.go" <<'GO'
func FuzzTab(f *testing.F) {
func helperFuzz(f *testing.F) {
GO
cat >"$fixture/cmd/x/c_test.go" <<'GO'
func FuzzLoadConfig(f *testing.F) {
GO

# expect_list <filter> <expected names, space separated, sorted by package then name>
expect_list() {
	local filter=$1 want=$2 got
	got=$(FUZZ_ROOT="$fixture" "$fuzz" --list "$filter" 2>/dev/null | cut -f1 | tr '\n' ' ' | sed 's/ $//')
	if [ "$got" != "$want" ]; then
		echo "FAIL: filter '$filter': got '$got', want '$want'"
		fails=$((fails + 1))
	else
		echo "ok:   filter '$filter' -> '$got'"
	fi
}

expect_list Redirect "FuzzNextRedirect"
expect_list t "FuzzNextRedirect"
expect_list T "FuzzTab"
expect_list '^FuzzTab$' "FuzzTab"
expect_list . "FuzzLoadConfig FuzzAnswerPage FuzzNextRedirect FuzzTab"
expect_list internal ""
expect_list reply ""
expect_list 'Page|Config' "FuzzLoadConfig FuzzAnswerPage"

if FUZZ_ROOT="$fixture" "$fuzz" --list internal >/dev/null 2>&1; then
	echo "FAIL: a filter matching no target name exited 0"
	fails=$((fails + 1))
else
	echo "ok:   a filter matching no target name exits non-zero"
fi

# A stub go records what each target's process saw.
stub="$scratch/go"
record="$scratch/record"
cat >"$stub" <<GO
#!/usr/bin/env bash
{
	echo "args=\$*"
	echo "TMPDIR=\$TMPDIR"
	echo "GOTMPDIR=\$GOTMPDIR"
	echo "GOMEMLIMIT=\$GOMEMLIMIT"
	echo "ulimit=\$(ulimit -v)"
	echo "timing=\${DEMIPLANE_FUZZ_TIMING:-}"
} >>"$record"
touch "\$TMPDIR/left-behind"
echo "fuzz: elapsed: 1s, execs: 42 (42/sec), new interesting: 3 (total: 9)"
GO
chmod +x "$stub"
tmpbase="$scratch/tmp"
mkdir -p "$tmpbase"
out=$(FUZZ_ROOT="$fixture" FUZZ_GO="$stub" FUZZ_TMPDIR="$tmpbase" FUZZ_GOMEMLIMIT=123MiB FUZZ_VMEM_KB=2097152 \
	"$fuzz" 1 Redirect)
status=$?
check() {
	if eval "$2"; then
		echo "ok:   $1"
	else
		echo "FAIL: $1"
		fails=$((fails + 1))
	fi
}
check "a stub run exits 0" '[ "$status" -eq 0 ]'
check "the run reports the stub's counts" 'grep -q "FuzzNextRedirect.*execs=42 .*new=3 .*PASS" <<<"$out"'
check "only the filtered target ran" '[ "$(grep -c "^args=" "$record")" -eq 1 ] && grep -q "^args=test -run ^\$ -fuzz ^FuzzNextRedirect\$ -fuzztime 1s -parallel 2 ./internal/reply$" "$record"'
check "GOMEMLIMIT is overridable" 'grep -qx "GOMEMLIMIT=123MiB" "$record"'
check "ulimit -v is overridable" 'grep -qx "ulimit=2097152" "$record"'
check "the timing switch is set" 'grep -qx "timing=1" "$record"'
tdir=$(sed -n 's/^TMPDIR=//p' "$record")
check "TMPDIR is a per-target directory under FUZZ_TMPDIR" '[[ "$tdir" == "$tmpbase"/fuzz-FuzzNextRedirect.* ]]'
check "GOTMPDIR follows TMPDIR" 'grep -qx "GOTMPDIR=$tdir" "$record"'
check "the target's TMPDIR was moved into delete-me/" '[ ! -e "$tdir" ] && [ -e "$tmpbase/delete-me/$(basename "$tdir")/left-behind" ]'
check "nothing else is left beside delete-me/" '[ "$(ls "$tmpbase")" = "delete-me" ]'

# The scratch tree is moved aside, not deleted, like the script's own.
mkdir -p "${TMPDIR:-/tmp}/delete-me"
mv "$scratch" "${TMPDIR:-/tmp}/delete-me/"

if [ "$fails" -ne 0 ]; then
	echo "$fails case(s) failed"
	exit 1
fi
echo "all cases passed"
