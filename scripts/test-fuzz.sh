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
# delete-me/, timing switch) and the delete-me/ size cap with a stub in
# place of the go command.
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
check "only the filtered target ran" '[ "$(grep -c "^args=" "$record")" -eq 1 ] && grep -q "^args=test -run ^\$ -fuzz ^FuzzNextRedirect\$ -fuzztime 1s -fuzzminimizetime 1000x -parallel 2 ./internal/reply$" "$record"'
check "GOMEMLIMIT is overridable" 'grep -qx "GOMEMLIMIT=123MiB" "$record"'
check "ulimit -v is overridable" 'grep -qx "ulimit=2097152" "$record"'
check "the timing switch is set" 'grep -qx "timing=1" "$record"'
tdir=$(sed -n 's/^TMPDIR=//p' "$record")
check "TMPDIR is a per-target directory under FUZZ_TMPDIR" '[[ "$tdir" == "$tmpbase"/fuzz-FuzzNextRedirect.* ]]'
check "GOTMPDIR follows TMPDIR" 'grep -qx "GOTMPDIR=$tdir" "$record"'
check "the target's TMPDIR was moved into delete-me/" '[ ! -e "$tdir" ] && [ -e "$tmpbase/delete-me/$(basename "$tdir")/left-behind" ]'
check "nothing else is left beside delete-me/" '[ "$(ls "$tmpbase")" = "delete-me" ]'

# delete-me/ is capped, never emptied by the script: over the cap a run
# refuses to start and names the directory; the kept files stay.
full="$scratch/full"
mkdir -p "$full/delete-me/fuzz-old.1"
head -c 262144 /dev/urandom >"$full/delete-me/fuzz-old.1/blob"
: >"$record"
out=$(FUZZ_ROOT="$fixture" FUZZ_GO="$stub" FUZZ_TMPDIR="$full" FUZZ_DELETE_ME_MAX_KB=64 "$fuzz" 1 Redirect 2>&1)
status=$?
check "a run over the delete-me/ cap exits 2" '[ "$status" -eq 2 ]'
check "the refusal names the directory to clear and the variable" 'grep -qF "$full/delete-me" <<<"$out" && grep -q FUZZ_DELETE_ME_MAX_KB <<<"$out"'
check "a refused run starts no target" '[ ! -s "$record" ] && [ "$(ls "$full")" = "delete-me" ]'
check "a refused run deletes nothing" '[ "$(ls "$full/delete-me")" = "fuzz-old.1" ] && [ "$(wc -c <"$full/delete-me/fuzz-old.1/blob")" -eq 262144 ]'
out=$(FUZZ_ROOT="$fixture" FUZZ_GO="$stub" FUZZ_TMPDIR="$full" FUZZ_DELETE_ME_MAX_KB=1048576 "$fuzz" 1 Redirect 2>&1)
status=$?
check "under a raised cap the same directory runs" '[ "$status" -eq 0 ] && grep -q "^args=" "$record"'
check "and keeps the earlier run beside the new one" '[ -e "$full/delete-me/fuzz-old.1/blob" ] && [ "$(ls "$full/delete-me" | wc -l)" -eq 2 ]'
out=$(FUZZ_ROOT="$fixture" FUZZ_GO="$stub" FUZZ_TMPDIR="$scratch/fresh" FUZZ_DELETE_ME_MAX_KB=0 "$fuzz" 1 Redirect 2>&1)
status=$?
check "a run that leaves delete-me/ over the cap still passes" '[ "$status" -eq 0 ]'
check "and warns that the next run will need it cleared" 'grep -qF "$scratch/fresh/delete-me now holds" <<<"$out"'
out=$(FUZZ_ROOT="$fixture" FUZZ_GO="$stub" FUZZ_TMPDIR="$full" FUZZ_DELETE_ME_MAX_KB=2GiB "$fuzz" 1 Redirect 2>&1)
status=$?
check "a cap that is not a number of KiB is refused" '[ "$status" -eq 2 ] && grep -q "FUZZ_DELETE_ME_MAX_KB must be" <<<"$out"'

# Minimisation is bounded by a count, 1000 calls unless overridden.
mins="$scratch/mins"
for case in "300::1000x" "1:500x:500x" "1:20s:20s"; do
	secs=${case%%:*}
	want=${case##*:}
	over=${case#*:}
	over=${over%:*}
	: >"$record"
	FUZZ_ROOT="$fixture" FUZZ_GO="$stub" FUZZ_TMPDIR="$mins" FUZZ_MINIMIZE_TIME="$over" "$fuzz" "$secs" Redirect >/dev/null 2>&1
	check "a ${secs}s run${over:+ with FUZZ_MINIMIZE_TIME=$over} minimises for $want" 'grep -q "^args=.* -fuzzminimizetime $want -parallel 2 " "$record"'
done
out=$(FUZZ_ROOT="$fixture" FUZZ_GO="$stub" FUZZ_TMPDIR="$mins" FUZZ_MINIMIZE_TIME='1s;x' "$fuzz" 1 Redirect 2>&1)
status=$?
check "a minimise time with other characters is refused" '[ "$status" -eq 2 ] && grep -q "FUZZ_MINIMIZE_TIME must be" <<<"$out"'

# The scratch tree is moved aside, not deleted, like the script's own.
mkdir -p "${TMPDIR:-/tmp}/delete-me"
mv "$scratch" "${TMPDIR:-/tmp}/delete-me/"

if [ "$fails" -ne 0 ]; then
	echo "$fails case(s) failed"
	exit 1
fi
echo "all cases passed"
