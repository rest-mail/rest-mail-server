#!/bin/sh
set -eu

[ "$#" -eq 4 ] || { echo "usage: $0 VOLUME UID MODE TARGET" >&2; exit 2; }
volume=$1
owner=$2
mode=$3
target=$4
: "${RESTMAIL_LABEL:=rest-mail.run=local}"

if ! docker volume inspect "$volume" >/dev/null 2>&1; then
  docker volume create --label "$RESTMAIL_LABEL" "$volume" >/dev/null
  docker run --rm --label "$RESTMAIL_LABEL" --network none \
    -v "$volume:/writable" alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc \
    sh -ec 'chown "$1:$1" /writable; chmod "$2" /writable' _ "$owner" "$mode" >/dev/null
fi
printf '%s\n' "-v $volume:$target"
