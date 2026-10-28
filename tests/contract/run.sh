#!/usr/bin/env bash
# The contract check (#627), as CI's E2E job runs it and a person runs it with
# `chore contract`: Vertrag against a running E2E topology, every phase writing
# JUnit, HTML and Markdown reports to .cache/contract/, and a summary at the end.
#
#   tests/contract/run.sh [gate|authorization|ownership|fuzz] [selection flags]
#
#   gate           the examples phase, then the coverage phase held to
#                  tests/contract/coverage-baseline.txt (the default)
#   authorization  the authorization matrix, a report and not a gate (#376)
#   ownership      each account tenant sent the other's ids, then each domain
#                  administrator the other's domain, a report and not a gate
#                  (#657)
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
gate | authorization | ownership | fuzz)
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
# fetch error. Required parameter examples are covered by
# TestEveryRequiredParameterHasAnExample (#851). The phases still read the
# independent oracle until #670 completes its generated-description switch and
# live contract proof.
served="$PWD/$out/openapi.json"
export CONTRACT_SPEC="$PWD/openapi.yaml"
names=("$phase")
if [ "$phase" = ownership ]; then names=(ownership ownership-domains ownership-mailboxes); fi
reset_run=()
if [ "$phase" != authorization ] && [ "$phase" != ownership ]; then
  reset_run=(--reset-run)
  names=(fuzz)
  if [ "$phase" = gate ]; then names=(examples coverage authorization ownership ownership-domains ownership-mailboxes browser-sessions); fi
fi
# Clear this invocation's reports before dependencies or tools can fail. An
# authorization or ownership run keeps the gate's saved inputs, but not an old
# verdict.
python3 -B tests/contract/report_files.py "${reset_run[@]}" "$out" "${names[@]}"
# The full command prints once after authorization. Standalone phases summarize
# only reports from this invocation, never a previous gate or fuzz run.
if [ "${CONTRACT_DEFER_SUMMARY:-0}" != 1 ]; then
  summary_phases=("$phase")
  if [ "$phase" = gate ]; then summary_phases=(examples coverage); fi
  trap 'python3 -B tests/contract/summary.py "$out" "${summary_phases[@]}"' EXIT
fi
# Select the same verified binary as CI before contacting the target server.
CONTRACT_VERTRAG="$(python3 -B scripts/vertrag_pin.py ensure .cache/bin)"
export CONTRACT_VERTRAG
export RESTMAIL_VERTRAG_BINARY="$CONTRACT_VERTRAG"
PATH="$(dirname "$CONTRACT_VERTRAG"):$PATH"
export PATH
"$CONTRACT_VERTRAG" version
python3 -B -c 'import yaml' 2>/dev/null || pip install --quiet pyyaml
if [ "$phase" != authorization ] && [ "$phase" != ownership ]; then
  python3 -B tests/contract/description.py vertrag.yml "$served"
fi
test -s "$served"

# vertrag.yml as this run uses it: absolute paths, the endpoint, and the surface
# gate only for a whole-API run.
python3 -B tests/contract/reports.py vertrag.yml "$out/vertrag-run.yml" "$out/run" "${narrowed[@]}"

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
  python3 -B tests/contract/reports.py "$out/vertrag-run.yml" "$out/vertrag-examples.yml" "$out/examples" "${narrowed[@]}"
  "$CONTRACT_VERTRAG" run --config "$out/vertrag-examples.yml" --sequence --phases examples "${select[@]}" 2>&1 | tee "$logs/contract-examples.log"

  # The second process repeats create examples after the first one consumed
  # its queue entry and left its folder and contact behind.
  python3 -B tests/contract/repeat_fixtures.py "$out/vertrag-run.yml"
  chore test:contract-fixtures
  python3 -B tests/contract/probes.py "$out/vertrag-run.yml" "$out/vertrag-probes.yml" "$out/coverage.xml"
  python3 -B tests/contract/reports.py "$out/vertrag-probes.yml" "$out/vertrag-probes.yml" "$out/coverage" "${narrowed[@]}"
  code=0
  "$CONTRACT_VERTRAG" run --config "$out/vertrag-probes.yml" --sequence --phases coverage "${select[@]}" \
    2>&1 | tee "$logs/contract.log" || code=$?
  echo "vertrag exited $code"
  if [ "$code" -ne 0 ] && [ "$code" -ne 2 ]; then exit "$code"; fi
  if [ ${#select[@]} -eq 0 ]; then
    # The coverage baseline only shrinks.
    python3 -B tests/contract/gate.py "$CONTRACT_SPEC" vertrag.yml "$out/coverage.xml" tests/contract/coverage-baseline.txt
  else
    echo "a narrowed run: the coverage baseline is not applied"
    # Without the whole-API ratchet, Vertrag's selected probe verdict is final.
    exit "$code"
  fi
  ;;
authorization)
  # Examples always run first, then the matrix repeats destructive requests as
  # the admin, so this runs after the gate.
  python3 -B tests/contract/authorization.py "$CONTRACT_SPEC" "$out/vertrag-run.yml" \
    .cache/contract-principals.json "$out/vertrag-authorization.yml" "$out/authorization.xml"
  python3 -B tests/contract/reports.py "$out/vertrag-authorization.yml" "$out/vertrag-authorization.yml" "$out/authorization" "${narrowed[@]}"
  code=0
  "$CONTRACT_VERTRAG" run --config "$out/vertrag-authorization.yml" --sequence --phases authorization "${select[@]}" \
    >"$logs/contract-authorization.log" 2>&1 || code=$?
  cat "$logs/contract-authorization.log"
  echo "vertrag authorization exited $code (report only)"
  exit "$code"
  ;;
ownership)
  # Like authorization, after the gate: each tenant's resources are made again
  # as their owner before the other tenant is sent their ids.
  python3 -B tests/contract/ownership.py "$CONTRACT_SPEC" "$out/vertrag-run.yml" \
    .cache/contract-principals.json "$out/vertrag-ownership.yml" "$out/ownership.xml"
  python3 -B tests/contract/reports.py "$out/vertrag-ownership.yml" "$out/vertrag-ownership.yml" "$out/ownership" "${narrowed[@]}"
  code=0
  "$CONTRACT_VERTRAG" run --config "$out/vertrag-ownership.yml" --sequence --phases ownership "${select[@]}" \
    >"$logs/contract-ownership.log" 2>&1 || code=$?
  cat "$logs/contract-ownership.log"
  echo "vertrag ownership exited $code (report only)"
  # The domain administrators, in runs of their own: tenant ids are keyed by
  # parameter name, so each kind of id they declare (their domain's, their own
  # mailbox's) needs a run, as the account run already declares path.id.
  tenants=.cache/contract/domain-tenants.json
  for kind in domains mailboxes; do
    if [ ! -s "$tenants" ]; then
      echo "no domain tenants in $tenants (chore test:contract-domain-tenants): the $kind run is not sent"
      continue
    fi
    python3 -B tests/contract/ownership.py "--$kind" "$CONTRACT_SPEC" "$out/vertrag-run.yml" \
      "$tenants" "$out/vertrag-ownership-$kind.yml" "$out/ownership-$kind.xml"
    python3 -B tests/contract/reports.py "$out/vertrag-ownership-$kind.yml" "$out/vertrag-ownership-$kind.yml" "$out/ownership-$kind" "${narrowed[@]}"
    kind_code=0
    "$CONTRACT_VERTRAG" run --config "$out/vertrag-ownership-$kind.yml" --sequence --phases ownership "${select[@]}" \
      >"$logs/contract-ownership-$kind.log" 2>&1 || kind_code=$?
    cat "$logs/contract-ownership-$kind.log"
    echo "vertrag ownership of $kind exited $kind_code (report only)"
    if [ "$code" -eq 0 ]; then code=$kind_code; fi
  done
  exit "$code"
  ;;
fuzz)
  python3 -B tests/contract/probes.py "$out/vertrag-run.yml" "$out/vertrag-probes.yml" "$out/fuzz.xml"
  python3 -B tests/contract/reports.py "$out/vertrag-probes.yml" "$out/vertrag-probes.yml" "$out/fuzz" "${narrowed[@]}"
  "$CONTRACT_VERTRAG" run --config "$out/vertrag-probes.yml" --sequence --phases fuzz "${select[@]}" \
    >"$logs/contract-fuzz.log" 2>&1 || echo "vertrag exited $?: findings are in the report"
  cat "$logs/contract-fuzz.log"
  ;;
esac
