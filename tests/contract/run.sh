#!/usr/bin/env bash
# The contract check (#627), as CI's E2E job runs it and a person runs it with
# `chore contract`: Vertrag against a running E2E topology, every phase writing
# JUnit, HTML and Markdown reports to .cache/contract/, and a summary at the end.
#
#   tests/contract/run.sh [gate|authorization|fuzz] [selection flags]
#
#   gate           the examples phase, then the coverage phase held to
#                  tests/contract/coverage-baseline.txt (the default)
#   authorization  the authorization matrix, a report and not a gate (#376)
#   fuzz           values drawn from each schema, a report and not a gate; a
#                  finding replays with --seed from its log line
#
# Selection flags are Vertrag's (--tag Domains, --operation-id getDomain,
# --only-matching 'regex'), and narrow every phase to them. A narrowed run
# reports without the surface gate and the coverage baseline, which are about
# the whole API. CONTRACT_ENDPOINT points Vertrag at a server other than
# vertrag.yml's; CONTRACT_LOGS keeps the logs somewhere else too (CI's runner
# temp directory).
set -euo pipefail
cd "$(dirname "$0")/../.."

phase=gate
case "${1:-}" in
gate | authorization | fuzz)
  phase=$1
  shift
  ;;
esac
select=("$@")
narrowed=()
if [ ${#select[@]} -gt 0 ]; then
  narrowed=(--narrowed)
fi

out=.cache/contract
logs=${CONTRACT_LOGS:-$out}
mkdir -p "$out" "$logs"
# Save the document this server actually serves, with the reports, and fail on a
# fetch error. The phases still read openapi.yaml: the generated document gives no
# example to most path and required query parameters, and Vertrag builds no request
# without one (2,876 refusals in E2E run 40111450987, 2026-10-24). #670 moves the
# phases onto the served document once every parameter carries its example.
served="$PWD/$out/openapi.json"
export CONTRACT_SPEC="$PWD/openapi.yaml"
if [ "$phase" != authorization ]; then
  # Do not report an earlier run's transactions after this run fails early.
  names=(fuzz)
  if [ "$phase" = gate ]; then names=(examples coverage authorization browser-sessions); fi
  for name in "${names[@]}"; do
    rm -f "$out/$name.xml" "$out/$name.html" "$out/$name.md" "$out/$name.har" "$out/$name.json"
  done
  rm -f "$served"
fi
trap 'python3 tests/contract/summary.py "$out"' EXIT
python3 -c 'import yaml' 2>/dev/null || pip install --quiet pyyaml
vertrag version
if [ "$phase" != authorization ]; then
  python3 tests/contract/description.py vertrag.yml "$served"
fi
test -s "$served"

# vertrag.yml as this run uses it: absolute paths, the endpoint, and the surface
# gate only for a whole-API run.
python3 tests/contract/reports.py vertrag.yml "$out/vertrag-run.yml" "$out/run" "${narrowed[@]}"

case "$phase" in
gate)
  # Real browser cookies, both CSRF variants, rotation and revocation are a
  # required gate even while the broader authorization matrix is report-only.
  go run ./tests/contract/browser_sessions \
    --endpoint "${CONTRACT_ENDPOINT:-http://10.99.0.20:8080}" \
    --spec "$served" --principals .cache/contract-principals.json \
    --output "$out/browser-sessions.json"
  # 1 is a failed transaction (or a run that could not be carried out) and 3 an
  # operation not exercised and passing (the surface gate): either fails here,
  # at once. 2 is a failed boundary probe, which the baseline judges below.
  python3 tests/contract/reports.py "$out/vertrag-run.yml" "$out/vertrag-examples.yml" "$out/examples" "${narrowed[@]}"
  vertrag run --config "$out/vertrag-examples.yml" --sequence --phases examples "${select[@]}" 2>&1 | tee "$logs/contract-examples.log"

  # The second process repeats create examples after the first one consumed
  # its queue entry and left its folder and contact behind.
  python3 tests/contract/repeat_fixtures.py "$out/vertrag-run.yml"
  chore test:contract-fixtures
  python3 tests/contract/probes.py "$out/vertrag-run.yml" "$out/vertrag-probes.yml" "$out/coverage.xml"
  python3 tests/contract/reports.py "$out/vertrag-probes.yml" "$out/vertrag-probes.yml" "$out/coverage" "${narrowed[@]}"
  code=0
  vertrag run --config "$out/vertrag-probes.yml" --sequence --phases coverage "${select[@]}" \
    2>&1 | tee "$logs/contract.log" || code=$?
  echo "vertrag exited $code"
  if [ "$code" -ne 0 ] && [ "$code" -ne 2 ]; then exit "$code"; fi
  if [ ${#select[@]} -eq 0 ]; then
    # The coverage baseline only shrinks.
    python3 tests/contract/gate.py "$CONTRACT_SPEC" vertrag.yml "$out/coverage.xml" tests/contract/coverage-baseline.txt
  else
    echo "a narrowed run: the coverage baseline is not applied"
  fi
  ;;
authorization)
  # Examples always run first, then the matrix repeats destructive requests as
  # the admin, so this runs after the gate.
  python3 tests/contract/authorization.py "$CONTRACT_SPEC" "$out/vertrag-run.yml" \
    .cache/contract-principals.json "$out/vertrag-authorization.yml" "$out/authorization.xml"
  python3 tests/contract/reports.py "$out/vertrag-authorization.yml" "$out/vertrag-authorization.yml" "$out/authorization" "${narrowed[@]}"
  code=0
  vertrag run --config "$out/vertrag-authorization.yml" --sequence --phases authorization "${select[@]}" \
    >"$logs/contract-authorization.log" 2>&1 || code=$?
  cat "$logs/contract-authorization.log"
  echo "vertrag authorization exited $code (report only)"
  exit "$code"
  ;;
fuzz)
  python3 tests/contract/probes.py "$out/vertrag-run.yml" "$out/vertrag-probes.yml" "$out/fuzz.xml"
  python3 tests/contract/reports.py "$out/vertrag-probes.yml" "$out/vertrag-probes.yml" "$out/fuzz" "${narrowed[@]}"
  vertrag run --config "$out/vertrag-probes.yml" --sequence --phases fuzz "${select[@]}" \
    >"$logs/contract-fuzz.log" 2>&1 || echo "vertrag exited $?: findings are in the report"
  cat "$logs/contract-fuzz.log"
  ;;
esac
