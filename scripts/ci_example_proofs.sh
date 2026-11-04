#!/usr/bin/env bash
# The in-process example proofs of one commit's CI run (#1237), for the job in
# e2e.yml that judges them together with the live proofs. CI and E2E are separate
# workflows, so this waits for CI's Test report, which runs once every job that
# writes proofs has passed. A report that did not pass, or never came, is an error:
# without the in-process proofs there is nothing to judge.
#
#   scripts/ci_example_proofs.sh COMMIT EVENT DIRECTORY
set -euo pipefail

[ $# -eq 3 ] || {
  echo "usage: $0 COMMIT EVENT DIRECTORY" >&2
  exit 2
}
commit=$1
event=$2
directory=$3
deadline=$((SECONDS + ${CI_PROOFS_WAIT_SECONDS:-1800}))

while :; do
  run="$(gh run list --workflow ci.yml --commit "$commit" --event "$event" --limit 1 \
    --json databaseId --jq '.[0].databaseId // empty')" || run=""
  state=""
  if [ -n "$run" ]; then
    state="$(gh run view "$run" --json jobs \
      --jq '.jobs[] | select(.name == "Test report") | "\(.status) \(.conclusion)"')" || state=""
  fi
  case "$state" in
  "completed success") break ;;
  completed\ *)
    echo "::error::CI run $run's Test report ended '$state': no in-process proofs to judge" >&2
    exit 1
    ;;
  esac
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "::error::no finished Test report in a CI run of $commit ($event), run '${run:-none}'" >&2
    exit 1
  fi
  sleep "${CI_PROOFS_POLL_SECONDS:-30}"
done

gh run download "$run" --pattern 'example-proofs-*' --dir "$directory"
