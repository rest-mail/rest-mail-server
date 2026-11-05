#!/bin/sh
# `chore clean`: removes what carries RESTMAIL_LABEL, and only that (#1963). It works
# from labels rather than from a run's state, so it works after a crash too.
#
# Volumes labelled rest-mail.data=true hold what `e2e:down` keeps: databases,
# attachments, the internal CA. They stay unless VOLUMES=true (chore clean --volumes).
#
# Exits non-zero if anything it should have removed is still there, such as a labelled
# network that an unlabelled container is attached to.
set -u

: "${RESTMAIL_LABEL:=rest-mail.run=local}"
DATA_LABEL=rest-mail.data=true
filter="label=$RESTMAIL_LABEL"
left=0

containers=$(docker ps -aq --filter "$filter")
if [ -n "$containers" ]; then
  # shellcheck disable=SC2086 # a list of ids
  docker rm -fv $containers >/dev/null || left=1
fi

for network in $(docker network ls -q --filter "$filter"); do
  docker network rm "$network" >/dev/null || left=1
done

kept=""
if [ "${VOLUMES:-}" != true ]; then
  kept=$(docker volume ls -q --filter "$filter" --filter "label=$DATA_LABEL")
fi
for volume in $(docker volume ls -q --filter "$filter"); do
  case " $(echo "$kept" | tr '\n' ' ') " in
    *" $volume "*) continue ;;
  esac
  docker volume rm "$volume" >/dev/null || left=1
done

echo "  removed what carries $RESTMAIL_LABEL"
if [ -n "$kept" ]; then
  echo "  kept data volumes: $(echo "$kept" | tr '\n' ' ')(chore clean --volumes removes them)"
fi

# Run last, so the files the removed containers read are released too.
sh "$(dirname "$0")/docker-hygiene.sh" drop-guest-cache

if [ "$left" -ne 0 ]; then
  echo "  some labelled resources could not be removed; see above" >&2
fi
exit "$left"
