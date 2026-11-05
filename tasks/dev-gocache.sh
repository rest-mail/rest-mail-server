#!/usr/bin/env bash
# Prints the docker flag that gives a dev container a Go build cache that outlasts it,
# for a run that sets RESTMAIL_DEV_GOCACHE_VOLUME (e2e-suite.yml). Without it, the cache
# stays on the container's tmpfs and every start compiles everything again.
#
# One volume per user: the API's dev container runs as root and the gateways' as 10001,
# and Go cannot write into a cache directory another user created. A named volume starts
# out owned by root, so the first use hands it over.
#
#   tasks/dev-gocache.sh UID
set -euo pipefail

[ $# -eq 1 ] || {
  echo "usage: $0 UID" >&2
  exit 2
}
owner=$1
volume="${RESTMAIL_DEV_GOCACHE_VOLUME:?}-$owner"
: "${RESTMAIL_LABEL:=rest-mail.run=local}"

if ! docker volume inspect "$volume" >/dev/null 2>&1; then
  docker volume create --label "$RESTMAIL_LABEL" "$volume" >/dev/null
  docker run --rm --label "$RESTMAIL_LABEL" -v "$volume:/cache" alpine:3.20 chown "$owner:$owner" /cache >/dev/null
fi
echo "-v $volume:/tmp/go-build-cache"
