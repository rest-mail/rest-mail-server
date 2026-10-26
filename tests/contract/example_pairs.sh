#!/usr/bin/env bash
# The registered Vertrag example-pair tests (internal/api *WithVertrag), as the
# E2E job's "Registered example-pair boundary checks" step runs them:
#
#   tests/contract/example_pairs.sh -run '^(TestA|TestB|...)$' [shards]
#
# One process took about 25 minutes of exchanges, nearly all of it waiting on
# vertrag. The test binary is built once and the named tests run in shards side
# by side (three by default); each shard has the 25-minute limit, and the step
# fails when any shard does. RESTMAIL_VERTRAG_BINARY must name vertrag.
set -euo pipefail

[ "${1:-}" = "-run" ] && [ -n "${2:-}" ] || { echo "usage: $0 -run '^(TestA|TestB)\$' [shards]" >&2; exit 2; }
pattern=$2
shards=${3:-3}
list=${pattern#^(}
list=${list%)\$}
IFS='|' read -r -a names <<<"$list"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
go test -c -o "$work/api.test" ./internal/api

pids=()
for ((s = 0; s < shards; s++)); do
	pick=()
	for ((i = s; i < ${#names[@]}; i += shards)); do pick+=("${names[i]}"); done
	[ ${#pick[@]} -gt 0 ] || continue
	run="^($(IFS='|'; echo "${pick[*]}"))\$"
	# The tests read the spec and fixtures relative to their package directory.
	(cd internal/api && exec "$work/api.test" -test.timeout 25m -test.run "$run") >"$work/shard$s.log" 2>&1 &
	pids+=("$s:$!")
done

status=0
for entry in "${pids[@]}"; do
	s=${entry%%:*}
	if wait "${entry#*:}"; then result=ok; else result=FAILED; status=1; fi
	echo "== shard $s: $result"
	cat "$work/shard$s.log"
done
exit "$status"
