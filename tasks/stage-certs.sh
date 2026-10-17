#!/bin/sh
# Gives one service the certificates it needs, and nothing else (#193).
#
# Certificates are issued into volumes that also hold the keys of the CAs that
# signed them: the testbed's CA key in testbed_certs, the internal mTLS CA key in
# <project>-internal-mtls. A service that mounted one could sign certificates its
# peers trust, so no service does. Instead, right before a service starts, this
# copies the files it names out of those volumes, read-only, into a volume of the
# service's own, owned by the user it runs as: keys 0400, certificates 0444. The
# one-off container here is the only thing besides the issuers that mounts an
# issuing volume.
#
#   stage-certs.sh <service volume> <uid:gid> <issuing volume>:<file>,<file>,... ...
#
# A file whose name ends in ? is copied if it is there; any other file has to be,
# or nothing starts.
set -eu

if [ $# -lt 3 ]; then
  echo "usage: $0 <service volume> <uid:gid> <issuing volume>:<file>,<file>,... ..." >&2
  exit 2
fi
service_volume=$1 owner=$2
shift 2

mounts="" plan="" i=0
for source in "$@"; do
  volume=${source%%:*}
  files=${source#*:}
  if [ -z "$volume" ] || [ "$volume" = "$source" ]; then
    echo "stage-certs: '$source' is not <volume>:<file>,..." >&2
    exit 2
  fi
  mounts="$mounts -v $volume:/from/$i:ro"
  plan="$plan $i:$volume:$files"
  i=$((i + 1))
done

# shellcheck disable=SC2086 # $mounts and $plan are lists of words
docker run --rm --network none $mounts -v "$service_volume:/to" alpine:3.20 sh -euc '
  set -f
  owner=$1
  shift
  find /to -mindepth 1 -delete
  missing=0
  for entry in "$@"; do
    i=${entry%%:*}; rest=${entry#*:}; volume=${rest%%:*}; files=${rest#*:}
    for f in $(echo "$files" | tr , " "); do
      optional=
      case $f in *\?) optional=1; f=${f%\?} ;; esac
      if [ -f "/from/$i/$f" ]; then
        cp "/from/$i/$f" "/to/$f"
        case $f in *.key) chmod 0400 "/to/$f" ;; *) chmod 0444 "/to/$f" ;; esac
      elif [ -z "$optional" ]; then
        echo "  no $f in volume $volume" >&2
        missing=1
      fi
    done
  done
  chown -R "$owner" /to
  chmod 0500 /to
  exit "$missing"
' _ "$owner" $plan || {
  echo "  issue them first: chore instance:certs:issue, and chore instance:mtls:issue for internal mTLS" >&2
  exit 1
}
