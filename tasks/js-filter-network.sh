#!/bin/sh
# Instance-owned, no-egress API/filter network. Docker chooses its subnet and
# addresses; the API reads the filter's current IP without changing testbed DNS.
set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: $0 up|down|address <instance project>" >&2
  exit 2
fi
action=$1 project=$2
case "$project" in
  ''|*[!a-zA-Z0-9_.-]*|-*) echo "invalid instance project: $project" >&2; exit 2 ;;
esac
network="$project-js-filter-net"

check_network() {
  properties=$(docker network inspect --format \
    '{{.Internal}} {{.Driver}} {{index .Labels "rest-mail.js-filter.project"}}' "$network")
  if [ "$properties" != "true bridge $project" ]; then
    echo "refusing unsafe or unowned js-filter network: $network" >&2
    exit 1
  fi
  peers=$(docker network inspect --format '{{range .Containers}}{{println .Name}}{{end}}' "$network")
  for peer in $peers; do
    case "$peer" in
      "$project-api"|"$project-js-filter") ;;
      *) echo "unexpected peer $peer on $network" >&2; exit 1 ;;
    esac
  done
}

case "$action" in
  up)
    if ! docker network inspect "$network" >/dev/null 2>&1; then
      docker network create --driver bridge --internal \
        --label "rest-mail.js-filter.project=$project" "$network" >/dev/null
    fi
    check_network
    ;;
  down)
    if docker network inspect "$network" >/dev/null 2>&1; then
      check_network
      # No force/disconnect: Docker refuses removal while any endpoint remains.
      docker network rm "$network" >/dev/null
    fi
    ;;
  address)
    check_network
    endpoint=$(docker inspect --format \
      "{{len .NetworkSettings.Networks}} {{with index .NetworkSettings.Networks \"$network\"}}{{.IPAddress}}{{end}}" \
      "$project-js-filter")
    # The filter must have exactly this one network, with an assigned IPv4 IP.
    set -- $endpoint
    if [ "$#" -ne 2 ] || [ "$1" != 1 ]; then
      echo "js-filter must be attached only to $network and have an address" >&2
      exit 1
    fi
    printf '%s\n' "$2"
    ;;
  *) echo "unknown js-filter network action: $action" >&2; exit 2 ;;
esac
