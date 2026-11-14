#!/usr/bin/env bash
# Run every native Go fuzz target in the module, one at a time.
#
#   scripts/fuzz.sh [seconds] [target-regexp]
#   scripts/fuzz.sh --list [target-regexp]
#
# Each target fuzzes for the given time (default 60s). The regexp (ERE, as
# awk reads it) is matched against the target's name only, never its
# package path. --list prints the matching targets as <name><TAB><package>
# and runs nothing.
#
# Targets run one after another, never two go commands at once, with one
# package built at a time (GOFLAGS=-p=1), two fuzz workers (-parallel 2) and
# no -race, so a run fits a small machine. Each go process gets a soft heap
# limit and a hard address-space ceiling:
#
#   FUZZ_GOMEMLIMIT   GOMEMLIMIT for every process (default 1GiB)
#   FUZZ_VMEM_KB      ulimit -v in KiB, per process (default 4194304, 4 GiB)
#
# Each target gets its own TMPDIR (and GOTMPDIR, which Go uses for
# t.TempDir), a fresh directory under FUZZ_TMPDIR (default $TMPDIR, else
# /tmp). Afterwards the script moves it into FUZZ_TMPDIR/delete-me/ rather
# than deleting it; empty that directory when convenient. The script never
# deletes anything, so delete-me/ is capped instead:
#
#   FUZZ_DELETE_ME_MAX_KB   most KiB delete-me/ may hold (default 2097152,
#                           2 GiB)
#
# A run refuses to start, naming the directory to clear, while delete-me/
# holds more than that, and a run that leaves it over the cap says so. A
# tmpfs there keeps the store-backed targets fast. DEMIPLANE_FUZZ_TIMING=1 is set, which
# turns on the targets' wall-clock bounds (plain `go test` leaves them off).
#
# It prints one line per target:
#
#   <target> <package> execs=<n> new=<n> <PASS | FAIL path/to/crasher>
#
# A crasher is the minimised input the fuzzer wrote under the package's
# testdata/fuzz/<Target>/; plain `go test` replays it from then on. The exit
# status is non-zero when any target failed.
#
# FUZZ_ROOT (default: the repository) and FUZZ_GO (default: go) exist for
# scripts/test-fuzz.sh.
set -uo pipefail

list_only=0
if [ "${1:-}" = "--list" ]; then
	list_only=1
	shift
	secs=0
else
	secs="${1:-60}"
	shift || true
fi
filter="${1:-.}"
case "$secs" in
'' | *[!0-9]*)
	echo "usage: $0 [seconds] [target-regexp] | $0 --list [target-regexp]" >&2
	exit 2
	;;
esac

here=$(cd "$(dirname "$0")" && pwd)
root="${FUZZ_ROOT:-$here/..}"
cd "$root" || exit 2
gocmd="${FUZZ_GO:-go}"
export GOFLAGS=-p=1
export GOMEMLIMIT="${FUZZ_GOMEMLIMIT:-1GiB}"
export DEMIPLANE_FUZZ_TIMING=1
vmem_kb="${FUZZ_VMEM_KB:-4194304}"
case "$vmem_kb" in
'' | *[!0-9]*)
	echo "FUZZ_VMEM_KB must be a number of KiB" >&2
	exit 2
	;;
esac
tmpbase="${FUZZ_TMPDIR:-${TMPDIR:-/tmp}}"
keep_max_kb="${FUZZ_DELETE_ME_MAX_KB:-2097152}"
case "$keep_max_kb" in
'' | *[!0-9]*)
	echo "FUZZ_DELETE_ME_MAX_KB must be a number of KiB" >&2
	exit 2
	;;
esac
keep="$tmpbase/delete-me"

# over_cap prints the KiB delete-me/ holds and succeeds when that is more
# than the cap.
over_cap() {
	local kb
	kb=$(du -sk "$keep" 2>/dev/null | cut -f1)
	echo "${kb:-0}"
	[ "${kb:-0}" -gt "$keep_max_kb" ]
}

# name<TAB>package for every func FuzzXxx(f *testing.F) in a _test.go file,
# keeping the names that match the filter.
targets=$(grep -rE --include='*_test.go' '^func Fuzz[A-Za-z0-9_]*\(f \*testing\.F\)' . |
	sed -E 's|^\./(.*)/[^/]+_test\.go:func (Fuzz[A-Za-z0-9_]*)\(.*|\2\t./\1|' |
	sort -t "$(printf '\t')" -k2,2 -k1,1 |
	awk -F '\t' -v re="$filter" '$1 ~ re')
if [ -z "$targets" ]; then
	echo "no fuzz targets match ${filter}" >&2
	exit 2
fi
if [ "$list_only" -eq 1 ]; then
	printf '%s\n' "$targets"
	exit 0
fi

if kb=$(over_cap); then
	echo "fuzz.sh: $keep holds ${kb} KiB, over the ${keep_max_kb} KiB cap (FUZZ_DELETE_ME_MAX_KB)." >&2
	echo "fuzz.sh: it keeps earlier runs' temporary directories; empty $keep, or raise the cap, and run again." >&2
	exit 2
fi
mkdir -p "$keep" || exit 2
failed=0
while IFS=$'\t' read -r name pkg; do
	work=$(mktemp -d "$tmpbase/fuzz-${name}.XXXXXX") || exit 2
	log="$work/fuzz.log"
	(
		ulimit -v "$vmem_kb" || exit 2
		export TMPDIR="$work" GOTMPDIR="$work"
		exec "$gocmd" test -run '^$' -fuzz "^${name}\$" -fuzztime "${secs}s" -parallel 2 "$pkg"
	) >"$log" 2>&1
	status=$?
	last=$(grep -E '^fuzz: elapsed:' "$log" | tail -n 1)
	execs=$(sed -nE 's/.*execs: ([0-9]+).*/\1/p' <<<"$last")
	new=$(sed -nE 's/.*new interesting: ([0-9]+).*/\1/p' <<<"$last")
	if [ "$status" -eq 0 ]; then
		result=PASS
	else
		failed=1
		crasher=$(sed -nE 's/^[[:space:]]*Failing input written to (.*)$/\1/p' "$log" | tail -n 1)
		if [ -n "$crasher" ]; then
			result="FAIL ${pkg#./}/${crasher}"
		else
			# No input was blamed: a build error, the memory ceiling, or the
			# engine itself stopping (for example "context deadline
			# exceeded" while it was minimising). Show the tail of the log.
			result="FAIL (no crasher) $(grep -vE '^fuzz: elapsed:' "$log" | tail -n 3 | tr '\n' ' ')"
		fi
	fi
	printf '%-24s %-28s execs=%-10s new=%-5s %s\n' "$name" "${pkg#./}" "${execs:-0}" "${new:-0}" "$result"
	mv "$work" "$keep/" || failed=1
done <<<"$targets"
if kb=$(over_cap); then
	echo "fuzz.sh: $keep now holds ${kb} KiB, over the ${keep_max_kb} KiB cap; empty it before the next run." >&2
fi
exit "$failed"
