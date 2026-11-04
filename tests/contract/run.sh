#!/usr/bin/env bash
# The contract check (#627), as CI's E2E job runs it and a person runs it with
# `chore contract`: Vertrag against a running E2E topology, every phase writing
# JUnit, HTML and Markdown reports to .cache/contract/, and a summary at the end.
#
#   tests/contract/run.sh [gate|authorization|ownership|fuzz|own-data|teardown] [selection flags]
#
#   gate           the examples phase, then the coverage phase held to
#                  tests/contract/coverage-baseline.txt (the default)
#   authorization  the authorization matrix, a report and not a gate (#376)
#   ownership      each account tenant sent the other's ids, then each domain
#                  administrator the other's domain, a report and not a gate
#                  (#657)
#   fuzz           values drawn from each schema, a report and not a gate; a
#                  finding replays with --seed from its log line
#   own-data       the operations moved to a clean start, each exchange after
#                  a reset and seed, and what follows their links
#                  (tests/contract/own_data.py); a gate, sent once nothing else
#                  needs the data
#   teardown       the operations that wipe the run's data, sent last, once
#                  nothing else needs it (tests/contract/teardown.py); a gate
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
gate | authorization | ownership | fuzz | own-data | teardown)
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
# Every phase consumes the exact description served by this server. The saved
# bytes are also the coverage/security inventory, even when Vertrag reads its URL.
served="$PWD/$out/openapi.json"
export CONTRACT_SPEC="$served"
names=("$phase")
if [ "$phase" = ownership ]; then names=(ownership ownership-domains ownership-mailboxes ownership-messages); fi
reset_run=()
# The teardown, like authorization, keeps the gate's saved inputs and reports.
if [ "$phase" != authorization ] && [ "$phase" != ownership ] && [ "$phase" != own-data ] && [ "$phase" != teardown ]; then
  reset_run=(--reset-run)
  names=(fuzz)
  if [ "$phase" = gate ]; then names=(examples coverage authorization ownership ownership-domains ownership-mailboxes ownership-messages browser-sessions); fi
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
  trap 'status=$?; python3 -B tests/contract/summary.py "$out" "${summary_phases[@]}" || true; exit "$status"' EXIT
fi
# A narrowed run can select no operation with a pinned field, and Vertrag refuses
# such a pin, before it sends anything. Vertrag is what knows what a selection
# holds, so ask it, and write the probe configuration again without the pins it
# names. A full run always has the fields, so there the refusal stays an error.
drop_unmatched_pin() {
  [ ${#select[@]} -ne 0 ] || return 1
  local names
  names="$(sed -n 's/.*fuzz\.pin names .* nothing in this description declares: \([a-z_, ]*\).*/\1/p' "$1" | head -n 1 | tr -d ' ')"
  [ -n "$names" ] || return 1
  echo "a narrowed run: nothing selected has $names, so it is not pinned"
  python3 -B tests/contract/probes.py "$out/vertrag-run.yml" "$out/vertrag-probes.yml" "$2.xml" "--no-pin=$names"
  CONTRACT_RUN_SPEC= CONTRACT_SPEC= python3 -B tests/contract/reports.py "$out/vertrag-probes.yml" "$out/vertrag-probes.yml" "$2" "${narrowed[@]}"
}

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
# Compile the live URL and the exact saved JSON with the pinned binary before
# making API calls. Fail on changed bytes, transactions, schemas or annotations.
export CONTRACT_RUN_SPEC
CONTRACT_RUN_SPEC="$(python3 -B tests/contract/description_equivalence.py \
  vertrag.yml "$served" "$out/description-equivalence.json" "$CONTRACT_VERTRAG" \
  --mode "${CONTRACT_DESCRIPTION_MODE:-saved}")"

# vertrag.yml as this run uses it: absolute paths, the endpoint, and the surface
# gate only for a whole-API run.
python3 -B tests/contract/reports.py vertrag.yml "$out/vertrag-run.yml" "$out/run" "${narrowed[@]}"
# The documented statuses not proven yet, by operation: taken out of the
# whole-API surface, and held to only shrink after the examples phase.
if [ ${#narrowed[@]} -eq 0 ]; then
  python3 -B tests/contract/status_baseline.py expand "$out/vertrag-run.yml" tests/contract/status-baseline.txt
fi

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
  # at once.
  python3 -B tests/contract/reports.py "$out/vertrag-run.yml" "$out/vertrag-examples.yml" "$out/examples" "${narrowed[@]}"
  "$CONTRACT_VERTRAG" run --config "$out/vertrag-examples.yml" --sequence --phases examples "${select[@]}" 2>&1 | tee "$logs/contract-examples.log"
  if [ ${#select[@]} -eq 0 ]; then
    python3 -B tests/contract/status_baseline.py check "$served" "$out/examples.xml" tests/contract/status-baseline.txt
  fi

  # The second process repeats create examples after the first one consumed
  # its queue entry and left its folder and contact behind.
  python3 -B tests/contract/repeat_fixtures.py "$out/vertrag-run.yml"
  chore test:contract-fixtures
  python3 -B tests/contract/probes.py "$out/vertrag-run.yml" "$out/vertrag-probes.yml" "$out/coverage.xml"
  python3 -B tests/contract/reports.py "$out/vertrag-probes.yml" "$out/vertrag-probes.yml" "$out/coverage" "${narrowed[@]}"
  code=0
  # Probes a hook ends are in no report: the hook worker counts them (lost_probes.py).
  # --fail-on-skipped-probes: valid probes not sent because an example failed are
  # a failure, not a count. Vertrag's exit 2 now means that or a failed probe.
  export CONTRACT_LOST_PROBES="$PWD/$out/lost-probes.txt"
  : >"$CONTRACT_LOST_PROBES"
  CONTRACT_COVERAGE=1 "$CONTRACT_VERTRAG" run --config "$out/vertrag-probes.yml" --sequence --phases coverage --fail-on-skipped-probes "${select[@]}" \
    2>&1 | tee "$logs/contract.log" || code=$?
  if drop_unmatched_pin "$logs/contract.log" "$out/coverage"; then
    code=0
    : >"$CONTRACT_LOST_PROBES"
    CONTRACT_COVERAGE=1 "$CONTRACT_VERTRAG" run --config "$out/vertrag-probes.yml" --sequence --phases coverage --fail-on-skipped-probes "${select[@]}" \
      2>&1 | tee "$logs/contract.log" || code=$?
  fi
  echo "vertrag exited $code"
  if [ "$code" -ne 0 ] && [ "$code" -ne 2 ]; then exit "$code"; fi
  if [ ${#select[@]} -eq 0 ]; then
    # What Vertrag cannot see: the configuration, and the probes a hook ended.
    python3 -B tests/contract/gate.py "$CONTRACT_SPEC" vertrag.yml "$out/coverage.xml" tests/contract/coverage-baseline.txt \
      --lost-probes tests/contract/lost-probes-baseline.txt "$CONTRACT_LOST_PROBES"
  else
    echo "a narrowed run: the coverage baseline is not applied"
  fi
  # Vertrag's verdict is final: a failed probe or a skipped valid probe fails here.
  exit "$code"
  ;;
authorization)
  # Examples always run first, then the matrix repeats destructive requests as
  # the admin, so this runs after the gate.
  chore test:contract-report-targets -- authorization ||
    echo "the held mail could not be topped up; the found examples of deleteQuarantine and clearSuppression will say so"
  python3 -B tests/contract/authorization.py "$CONTRACT_SPEC" "$out/vertrag-run.yml" \
    .cache/contract-principals.json "$out/vertrag-authorization.yml" "$out/authorization.xml"
  python3 -B tests/contract/reports.py "$out/vertrag-authorization.yml" "$out/vertrag-authorization.yml" "$out/authorization" "${narrowed[@]}"
  code=0
  "$CONTRACT_VERTRAG" run --config "$out/vertrag-authorization.yml" --sequence --phases authorization "${select[@]}" \
    >"$logs/contract-authorization.log" 2>&1 || code=$?
  cat "$logs/contract-authorization.log"
  # Vertrag's refuse list covers every cell; this holds a 404 to the routes the
  # owner's decision covers. A run that wrote no report has already failed.
  if [ -f "$out/authorization.json" ]; then
    python3 -B tests/contract/authorization_refusals.py "$CONTRACT_SPEC" "$out/authorization.json" \
      2>&1 | tee -a "$logs/contract-authorization.log" || code=$?
  fi
  echo "vertrag authorization exited $code (report only)"
  exit "$code"
  ;;
ownership)
  # Like authorization, after the gate: each tenant's resources are made again
  # as their owner before the other tenant is sent their ids. The creates meet
  # what the earlier runs made, so they may clear it (a_leftover_to_remove).
  export CONTRACT_REMOVE_LEFTOVERS=1
  # The message run's hooks send from the sender's own address (ownership_hooks.py).
  export CONTRACT_OWNERSHIP=1
  chore test:contract-report-targets -- ownership ||
    echo "the held mail could not be topped up; the found examples of deleteQuarantine and clearSuppression will say so"
  python3 -B tests/contract/ownership.py "$CONTRACT_SPEC" "$out/vertrag-run.yml" \
    .cache/contract-principals.json "$out/vertrag-ownership.yml" "$out/ownership.xml"
  python3 -B tests/contract/reports.py "$out/vertrag-ownership.yml" "$out/vertrag-ownership.yml" "$out/ownership" "${narrowed[@]}"
  code=0
  "$CONTRACT_VERTRAG" run --config "$out/vertrag-ownership.yml" --sequence --phases ownership "${select[@]}" \
    >"$logs/contract-ownership.log" 2>&1 || code=$?
  cat "$logs/contract-ownership.log"
  echo "vertrag ownership exited $code (report only)"
  # The accounts again, sent each other's messages, drafts, scheduled sends and
  # attachments: their ids are not an account's, so in a run of their own.
  python3 -B tests/contract/ownership.py --messages "$CONTRACT_SPEC" "$out/vertrag-run.yml" \
    .cache/contract-principals.json "$out/vertrag-ownership-messages.yml" "$out/ownership-messages.xml"
  python3 -B tests/contract/reports.py "$out/vertrag-ownership-messages.yml" "$out/vertrag-ownership-messages.yml" "$out/ownership-messages" "${narrowed[@]}"
  messages_code=0
  "$CONTRACT_VERTRAG" run --config "$out/vertrag-ownership-messages.yml" --sequence --phases ownership "${select[@]}" \
    >"$logs/contract-ownership-messages.log" 2>&1 || messages_code=$?
  cat "$logs/contract-ownership-messages.log"
  echo "vertrag ownership of messages exited $messages_code (report only)"
  if [ "$code" -eq 0 ]; then code=$messages_code; fi
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
own-data)
  python3 -B tests/contract/own_data.py "$out/vertrag-run.yml" "$out/vertrag-own-data.yml"
  python3 -B tests/contract/reports.py "$out/vertrag-own-data.yml" "$out/vertrag-own-data.yml" "$out/own-data" --narrowed
  code=0
  CONTRACT_RESET_SEED=1 "$CONTRACT_VERTRAG" run --config "$out/vertrag-own-data.yml" --sequence --phases examples "${select[@]}" \
    2>&1 | tee "$logs/contract-own-data.log" || code=$?
  echo "vertrag own-data exited $code"
  if [ "$code" -eq 0 ]; then
    python3 -B tests/contract/own_data.py --check "$out/own-data.xml" || code=$?
  fi
  exit "$code"
  ;;
teardown)
  python3 -B tests/contract/teardown.py "$out/vertrag-run.yml" "$out/vertrag-teardown.yml"
  python3 -B tests/contract/reports.py "$out/vertrag-teardown.yml" "$out/vertrag-teardown.yml" "$out/teardown" --narrowed
  code=0
  "$CONTRACT_VERTRAG" run --config "$out/vertrag-teardown.yml" --sequence --phases examples "${select[@]}" \
    2>&1 | tee "$logs/contract-teardown.log" || code=$?
  echo "vertrag teardown exited $code"
  exit "$code"
  ;;
fuzz)
  # The mailbox probes.py pins deliverMessage's generated bodies to.
  chore test:contract-delivery-recipient || echo "the delivery recipient could not be prepared; deliverMessage's fuzz probes answer 404"
  python3 -B tests/contract/probes.py "$out/vertrag-run.yml" "$out/vertrag-probes.yml" "$out/fuzz.xml"
  CONTRACT_RUN_SPEC= CONTRACT_SPEC= python3 -B tests/contract/reports.py "$out/vertrag-probes.yml" "$out/vertrag-probes.yml" "$out/fuzz" "${narrowed[@]}"
  "$CONTRACT_VERTRAG" run --config "$out/vertrag-probes.yml" --sequence --phases fuzz "${select[@]}" \
    >"$logs/contract-fuzz.log" 2>&1 || echo "vertrag exited $?: findings are in the report"
  if drop_unmatched_pin "$logs/contract-fuzz.log" "$out/fuzz"; then
    "$CONTRACT_VERTRAG" run --config "$out/vertrag-probes.yml" --sequence --phases fuzz "${select[@]}" \
      >"$logs/contract-fuzz.log" 2>&1 || echo "vertrag exited $?: findings are in the report"
  fi
  cat "$logs/contract-fuzz.log"
  ;;
esac
