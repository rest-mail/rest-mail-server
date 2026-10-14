#!/usr/bin/env bash
# chore server:status — what have I got. A passive read that works when the
# server is broken, which is when it is needed: versions, revision, when it
# last changed, and every component's replicas. server:verify prints this
# first, so nobody has to run both.
set -uo pipefail
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"

need kubectl helm jq

status_block() {
  local list chart app revision updated workloads state
  list=$(h list --filter "^${NAME}\$" -o json 2>/dev/null) || list='[]'
  chart=$(jq -r '.[0].chart // "" | sub("^restmail-"; "")' <<<"$list")
  app=$(jq -r '.[0].app_version // ""' <<<"$list")
  revision=$(jq -r '.[0].revision // ""' <<<"$list")
  updated=$(jq -r '.[0].updated // "" | sub("\\.[0-9]+"; "")' <<<"$list")
  state=online
  is_offline && state="OFFLINE — chore server:online brings it back"

  say "rest-mail \"$NAME\" in namespace $NAMESPACE, on $(kubectl config current-context 2>/dev/null || echo '(no context)')"
  printf '  %-12s %s\n' chart "$chart (rest-mail $app)" revision "$revision, deployed $updated" state "$state"

  workloads=$(k get deploy,statefulset -l "app.kubernetes.io/instance=$NAME" -o json 2>/dev/null) || workloads='{"items":[]}'
  jq -r --arg offline "$OFFLINE_ANNOTATION" '.items[]
    | [ .metadata.labels["app.kubernetes.io/component"] // .metadata.name,
        "\(.status.readyReplicas // 0)/\(.spec.replicas // 0) ready"
        + (if .metadata.annotations[$offline] then " (offline; was \(.metadata.annotations[$offline]))" else "" end) ]
    | "  \(.[0] | . + " " * (12 - length)) \(.[1])"' <<<"$workloads"
}

# Run as a script: the status block, then the pods behind it.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  need_release
  status_block
  say ""
  k get pods -l "app.kubernetes.io/instance=$NAME" -o wide 2>/dev/null
fi
