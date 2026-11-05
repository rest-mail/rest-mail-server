#!/usr/bin/env bash
# The registered Vertrag example-pair tests (internal/api *WithVertrag), as
# `chore contract:pairs` runs them (and `chore contract`, locally and in CI):
#
#   tests/contract/example_pairs.sh -run 'WithVertrag$' [shards]
#
# One process took about 25 minutes of exchanges, nearly all of it waiting on
# vertrag. The test binary is built once and the matching tests run in shards side
# by side (four by default); each shard has the 25-minute limit, and the step
# fails when any shard does. RESTMAIL_VERTRAG_BINARY must name vertrag.
# Anything else after the pattern is refused: go test flags such as -count=1
# were once read as the shard count, which ran no shard and still exited 0.
set -euo pipefail

# A temporary directory of its own, removed however the run ends (#1963).
if [ -z "${RESTMAIL_SCRATCH_RUN:-}" ]; then
	exec python3 -B "$(dirname "$0")/../../scripts/scratch_run.py" "$0" "$@"
fi

usage() { echo "usage: $0 -run 'pattern' [shards]" >&2; exit 2; }
[ "${1:-}" = "-run" ] && [ -n "${2:-}" ] && [ $# -le 3 ] || usage
pattern=$2
shards=${3:-4}
[[ $shards =~ ^[1-9][0-9]*$ ]] || usage
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
go test -c -o "$work/api.test" ./internal/api

list=$(cd internal/api && "$work/api.test" -test.list "$pattern")
names=()
while IFS= read -r name; do
	[[ $name == Test* ]] && names+=("$name")
done <<<"$list"
if [ ${#names[@]} -eq 0 ]; then
	echo "no tests match pattern: $pattern" >&2
	exit 1
fi

pids=()
for ((s = 0; s < shards; s++)); do
	pick=()
	for ((i = s; i < ${#names[@]}; i += shards)); do pick+=("${names[i]}"); done
	[ ${#pick[@]} -gt 0 ] || continue
	run="^($(IFS='|'; echo "${pick[*]}"))\$"
	# The tests read the spec and fixtures relative to their package directory.
	(cd internal/api && exec "$work/api.test" -test.v -test.timeout 25m -test.run "$run") >"$work/shard$s.log" 2>&1 &
	pids+=("$s:$!")
done

status=0
for entry in "${pids[@]}"; do
	s=${entry%%:*}
	if wait "${entry#*:}"; then result=ok; else result=FAILED; status=1; fi
	echo "== shard $s: $result"
	# A passing shard names each test's verdict, so a skip shows; a failing one
	# prints everything.
	if [ "$result" = ok ]; then grep -E '^--- ' "$work/shard$s.log" || true; else cat "$work/shard$s.log"; fi
done
exit "$status"
