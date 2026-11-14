#!/usr/bin/env bash
# Run every native Go fuzz target in the module, one at a time.
#
#   scripts/fuzz.sh [seconds] [target-regexp]
#
# Each target fuzzes for the given time (default 60s). Targets run one after
# another, never two go commands at once, with one package built at a time
# (GOFLAGS=-p=1), two fuzz workers (-parallel 2) and no -race, so a run fits a
# small machine. It prints one line per target:
#
#   <target> <package> execs=<n> new=<n> <PASS | FAIL path/to/crasher>
#
# A crasher is the minimised input the fuzzer wrote under the package's
# testdata/fuzz/<Target>/; plain `go test` replays it from then on. The exit
# status is non-zero when any target failed.
set -uo pipefail

secs="${1:-60}"
filter="${2:-.}"
case "$secs" in
'' | *[!0-9]*)
	echo "usage: $0 [seconds] [target-regexp]" >&2
	exit 2
	;;
esac

cd "$(dirname "$0")/.." || exit 2
export GOFLAGS=-p=1

# target<TAB>package for every func FuzzXxx(f *testing.F) in a _test.go file.
targets=$(grep -rE --include='*_test.go' '^func Fuzz[A-Za-z0-9_]*\(f \*testing\.F\)' . |
	sed -E 's|^\./(.*)/[^/]+_test\.go:func (Fuzz[A-Za-z0-9_]*)\(.*|\2\t./\1|' |
	sort -k2,2 -k1,1 | grep -E "^[^	]*${filter}" || true)
if [ -z "$targets" ]; then
	echo "no fuzz targets match ${filter}" >&2
	exit 2
fi

log=$(mktemp)
trap 'rm -f "$log"' EXIT
failed=0
while IFS=$'\t' read -r name pkg; do
	go test -run '^$' -fuzz "^${name}\$" -fuzztime "${secs}s" -parallel 2 "$pkg" >"$log" 2>&1
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
			# No input was blamed: a build error, or the engine itself
			# stopping (for example "context deadline exceeded" while it
			# was minimising). Show the tail of the log.
			result="FAIL (no crasher) $(grep -vE '^fuzz: elapsed:' "$log" | tail -n 3 | tr '\n' ' ')"
		fi
	fi
	printf '%-24s %-28s execs=%-10s new=%-5s %s\n' "$name" "${pkg#./}" "${execs:-0}" "${new:-0}" "$result"
done <<<"$targets"
exit "$failed"
