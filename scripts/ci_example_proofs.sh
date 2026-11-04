#!/usr/bin/env bash
# The in-process example proofs of one commit's CI run (#1237), for the job in
# e2e.yml that judges them together with the live proofs. CI and E2E are separate
# workflows, so this waits for CI's Test report, which runs once every job that writes
# proofs has passed. Waiting holds a runner, and the job's timeout in e2e.yml has to
# outlast the limit here (#1919). A report that did not pass, or never came, is an error:
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
wait_seconds=${CI_PROOFS_WAIT_SECONDS:-3600}
deadline=$((SECONDS + wait_seconds))
started=$SECONDS

refuse() {
  echo "::error::$*" >&2
  exit 1
}

waiting=""
while :; do
  # Read again on every pass: a newer run of this commit cancels the one read before.
  # Not simply the newest run: an edit to the title or description starts a newer one
  # that skips every job.
  run="$(gh run list --workflow ci.yml --commit "$commit" --event "$event" --limit 20 \
    --json databaseId,displayTitle \
    --jq '[.[] | select(.displayTitle != "CI (ignored edit)")][0].databaseId // empty')" || run=""
  # "<run status> <report status> <report conclusion>", with "absent" for a report the
  # run has not scheduled yet.
  state=""
  if [ -n "$run" ]; then
    state="$(gh run view "$run" --json status,jobs --jq '"\(.status) \(first(.jobs[] |
      select(.name == "Test report") | "\(.status) \(.conclusion)") // "absent")"')" || state=""
  fi
  report="${state#* }"
  report="${report% }"
  case "$state" in
  "") report="" ;;
  "completed absent") refuse "CI run $run finished without a Test report: no in-process proofs to judge" ;;
  esac
  case "$report" in
  "completed success") break ;;
  "completed failure") refuse "CI run $run's Test report failed: no in-process proofs to judge" ;;
  "completed cancelled") refuse "CI run $run's Test report was cancelled: no in-process proofs to judge" ;;
  "completed skipped") refuse "CI run $run's Test report was skipped, as a job it needs failed or was cancelled: no in-process proofs to judge" ;;
  completed\ *) refuse "CI run $run's Test report ended '${report#completed }': no in-process proofs to judge" ;;
  esac
  if [ "$SECONDS" -ge "$deadline" ]; then
    waited="after $((SECONDS - started))s, limit ${wait_seconds}s"
    [ -n "$run" ] || refuse "no CI run of $commit ($event) $waited: no in-process proofs to judge"
    [ -n "$state" ] || refuse "could not read CI run $run $waited: no in-process proofs to judge"
    [ "$report" != absent ] || refuse "CI run $run has no Test report yet $waited: no in-process proofs to judge"
    refuse "CI run $run's Test report is still '$report' $waited: no in-process proofs to judge"
  fi
  if [ -z "$waiting" ]; then
    waiting=yes
    echo "::notice::CI's Test report is not finished (run ${run:-none}): this job holds a runner while it waits"
  fi
  sleep "${CI_PROOFS_POLL_SECONDS:-30}"
done

gh run download "$run" --pattern 'example-proofs-*' --dir "$directory"
