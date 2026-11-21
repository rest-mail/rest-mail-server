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
case "$RESTMAIL_LABEL" in
  ?*=?*) ;;
  *) echo 'cleanup requires a nonempty RESTMAIL_LABEL key=value' >&2; exit 2 ;;
esac
DATA_LABEL=rest-mail.data=true
filter="label=$RESTMAIL_LABEL"
left=0

containers=$(docker ps -aq --no-trunc --filter "$filter") || exit 1
flags=-fv
[ "${CONTAINERS_ONLY:-}" != true ] || flags=-f
for container in $containers; do
  if docker rm "$flags" "$container" >/dev/null; then
    printf 'cleanup: removed container %s (%s)\n' "$container" "$RESTMAIL_LABEL"
  else
    left=1
  fi
done
if [ "${CONTAINERS_ONLY:-}" = true ]; then
  exit "$left"
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
