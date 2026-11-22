#!/usr/bin/env bash
# Prints disk-backed Go state mounts, including modules and toolchain downloads.
# RESTMAIL_DEV_GOCACHE_VOLUME overrides the per-project default.
#
# One volume per user: the API's dev container runs as root and the gateways' as 10001,
# and Go cannot write into a cache directory another user created. A named volume starts
# out owned by root, so the first use hands it over.
#
#   tasks/dev-gocache.sh UID [DEFAULT_VOLUME]
set -euo pipefail

[ $# -ge 1 ] && [ $# -le 2 ] || {
  echo "usage: $0 UID [DEFAULT_VOLUME]" >&2
  exit 2
}
owner=$1
: "${RESTMAIL_LABEL:=rest-mail.run=local}"
volume="${RESTMAIL_DEV_GOCACHE_VOLUME:-${2:-restmail-${RESTMAIL_LABEL#*=}-devcache}}-$owner"
sh "$(dirname "$0")/writable-volume.sh" "$volume" "$owner" 0700 /cache
echo '-e GOPATH=/cache/go -e GOMODCACHE=/cache/go/pkg/mod -e GOCACHE=/cache/build'
