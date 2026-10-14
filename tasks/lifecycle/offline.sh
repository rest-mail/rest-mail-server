#!/usr/bin/env bash
# chore server:offline / server:online
#
# Offline stops everything that answers — the API and the three gateways —
# and leaves the database running, because what you do next needs it:
# sessions:revoke, password:reset, backup:create. Every stolen token stops
# working the moment nothing is there to accept it.
#
# It contains rather than cures. Coming back online without rotating whatever
# leaked hands the access straight back, which online says before it starts.
#
# Each Deployment remembers its replica count in an annotation, so online
# puts back exactly what was there. An upgrade would reset them to the chart's
# values and bring the server back behind your back, which is why upgrade:*
# refuses while it is offline.
#
#   offline.sh offline | online
set -uo pipefail
here=$(dirname "$0")
# shellcheck source=lib.sh
. "$here/lib.sh"
# shellcheck source=status.sh
. "$here/status.sh"

need kubectl helm jq
need_release

case "${1:-}" in
offline)
  is_offline && refuse "It is already offline." "$(status_block)"
  for d in $(serving_deployments); do
    replicas=$(k get "$d" -o jsonpath='{.spec.replicas}')
    k annotate "$d" --overwrite "$OFFLINE_ANNOTATION=$replicas" >/dev/null
    k scale "$d" --replicas=0 >/dev/null
    say "  stopped ${d#deployment.apps/} (was $replicas)"
  done
  k wait pod --for=delete -l "app.kubernetes.io/instance=$NAME,app.kubernetes.io/component!=postgres" --timeout=120s >/dev/null 2>&1
  say "" "Offline. Nothing answers; the database is still running." "" \
    "Before coming back, end whatever leaked:" \
    "  chore sessions:revoke     sign everybody out" \
    "  chore password:reset      if an admin's password is suspect" \
    "Then: chore server:online"
  ;;
online)
  is_offline || refuse "It is not offline." "$(status_block)"
  for d in $(serving_deployments); do
    replicas=$(k get "$d" -o jsonpath="{.metadata.annotations.rest-mail\.com/replicas-before-offline}")
    [ -n "$replicas" ] || continue
    k scale "$d" --replicas="$replicas" >/dev/null
    k annotate "$d" "$OFFLINE_ANNOTATION-" >/dev/null
    say "  started ${d#deployment.apps/} ($replicas)"
  done
  for d in $(serving_deployments); do
    k rollout status "$d" --timeout=300s >/dev/null 2>&1 || err "  ${d#deployment.apps/} is not ready yet: chore server:status"
  done
  say "" "Online."
  ;;
*)
  die "usage: offline.sh offline|online"
  ;;
esac
