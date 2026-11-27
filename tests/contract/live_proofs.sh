#!/usr/bin/env bash
# The live requirement proofs (#1237), as CI's E2E job runs them after the gate
# and a person runs them with `chore test:contract-live-proofs`:
#
#   tests/contract/live_proofs.sh API_KEY_FIXTURE_JSON
#
# Runners against a running topology, each with a directory of its own
# under .cache/contract/live-proofs (CONTRACT_LIVE_PROOFS to put it elsewhere):
# named anonymous examples, named over-cap examples, and the bounded matrix of
# real principals, exact human-assurance checks for grants and links, and
# reference-client IMAP and POP3 checks.
# scripts/example_proofs.py --live reads the named-example proof files and
# decides what counts; the assurance and reference-client runners have their
# own reports.
#
# This exits nonzero when a runner proved less than it selected, or could not
# run. Every runner still runs, so one failure takes no other runner's proofs
# with it. A fixture is good for one run: live_principals.py enrolls it in MFA.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1

[ $# -eq 1 ] || {
  echo "usage: $0 API_KEY_FIXTURE_JSON" >&2
  exit 2
}
fixture=$1
endpoint=${CONTRACT_ENDPOINT:-http://10.99.0.20:8080}
out=${CONTRACT_LIVE_PROOFS:-.cache/contract/live-proofs}
principals=${CONTRACT_PRINCIPALS:-.cache/contract-principals.json}

# An earlier run's verdicts are never this run's proofs.
rm -rf "$out"
mkdir -p "$out"

if ! vertrag="$(python3 -B scripts/vertrag_pin.py ensure "${CONTRACT_VERTRAG_DIR:-.cache/bin}")"; then
  echo "the pinned Vertrag could not be installed: no live proofs" >&2
  exit 1
fi
PATH="$(dirname "$vertrag"):$PATH"
export PATH
python3 -B -c 'import yaml, idna' 2>/dev/null || pip install --quiet pyyaml idna==3.11

status=0
run_live() {
  local name=$1 code=0 started=$SECONDS
  shift
  mkdir -p "$out/$name"
  "$@" 2>&1 | tee "$out/$name.log"
  code=${PIPESTATUS[0]}
  echo "live runner $name: exit $code, $((SECONDS - started)) s"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    echo "live runner $name: exit $code, $((SECONDS - started)) s" >>"$GITHUB_STEP_SUMMARY"
  fi
  if [ "$code" -ne 0 ]; then status=1; fi
}

run_live anonymous python3 -B tests/contract/live_requirements.py anonymous "$endpoint" "$out/anonymous" \
  --vertrag "$vertrag"
run_live over-cap python3 -B tests/contract/live_requirements.py over-cap "$endpoint" "$out/over-cap" \
  --vertrag "$vertrag" --workers "${CONTRACT_LIVE_WORKERS:-4}"
run_live principals python3 -B tests/contract/live_principals.py "$endpoint" "$out/principals" \
  --vertrag "$vertrag" --principals "$principals" --fixture "$fixture"
run_live assurance python3 -B tests/contract/assurance_live.py "$endpoint" "$out/assurance/report.json"
run_live reference-mail python3 -B tests/contract/reference_mail_live.py "$endpoint" "$out/reference-mail/report.json" \
  --commit "$(git rev-parse HEAD)" --topology "${TOPOLOGY_RUN:-${CONTRACT_FIXTURE_PROJECT:-restmail.test}}" \
  --imap-host "${CONTRACT_IMAP_HOST:-${RESTMAIL_IMAP_IP:-10.99.0.15}}" \
  --imap-port "${CONTRACT_IMAP_PORT:-${RESTMAIL_IMAP_TLS_PORT:-993}}" \
  --pop3-host "${CONTRACT_POP3_HOST:-${RESTMAIL_POP3_IP:-10.99.0.16}}" \
  --pop3-port "${CONTRACT_POP3_PORT:-${RESTMAIL_POP3_TLS_PORT:-995}}" \
  --insecure
exit "$status"
