#!/bin/sh
# What a run does to Docker on its way out (#1963). Neither step can fail a run: a
# run that passed has passed, whatever its cleanup then ran into.
#
#   docker-hygiene.sh cap-build-cache    trim the build cache to RESTMAIL_BUILD_CACHE_CAP
#   docker-hygiene.sh drop-guest-cache   release the files the Docker Desktop VM holds open
set -eu

: "${RESTMAIL_LABEL:=rest-mail.run=local}"

case "${1:-}" in
  cap-build-cache)
    # A CI runner is thrown away after its job, and a later step in the job rebuilds
    # from this cache. Trimming it there costs time and saves nothing.
    if [ -n "${CI:-}" ]; then exit 0; fi
    cap=${RESTMAIL_BUILD_CACHE_CAP:-5GB}
    # Only by size: the oldest cache goes first, and another project may share it.
    # buildx replaced --keep-storage, which older clients still need.
    flag=--keep-storage
    if docker builder prune --help 2>&1 | grep -q -- --max-used-space; then
      flag=--max-used-space
    fi
    docker builder prune --force --all "$flag" "$cap" >/dev/null 2>&1 ||
      echo "  note: could not trim the build cache to $cap" >&2
    ;;
  drop-guest-cache)
    # Only Docker Desktop has a VM whose guest cache holds files open on the host. On
    # a Linux runner this command would evict the runner's own cache, for no gain.
    os=$(docker info --format '{{.OperatingSystem}}' 2>/dev/null) || exit 0
    if [ "$os" != "Docker Desktop" ]; then exit 0; fi
    # Evicts dentries and inodes for the whole VM, and nothing else. Containers keep
    # running; their next file reads are slower. Measured 217,092 open files to 69.
    #
    # Those 69 are the VM's own: besides the guest's cache it keeps the last 40 to 60
    # files it read open on the Mac, which no number of drops releases and only later
    # reads displace. Measured: 58 of 300 deleted files stayed held through four drops.
    # So the container reads a set of files that are never deleted first, and what the
    # VM keeps is those. Measured: 0 of 300 held, three times out of three.
    decoys=${RESTMAIL_GUEST_DECOYS:-${XDG_CACHE_HOME:-$HOME/.cache}/rest-mail/guest-cache-decoys}
    i=0
    while [ "$i" -lt 256 ] && mkdir -p "$decoys" 2>/dev/null; do
      i=$((i + 1))
      [ -e "$decoys/$i" ] || printf x >"$decoys/$i" 2>/dev/null || break
    done
    if docker run --rm --label "$RESTMAIL_LABEL" --privileged --pid=host -v "$decoys:/decoys:ro" alpine:3.20 \
      sh -c 'sync; cat /decoys/* >/dev/null; echo 2 > /proc/sys/vm/drop_caches' >/dev/null 2>&1; then
      exit 0
    fi
    # The decoys may not be on the Mac: this may be a Linux runner with Docker's socket.
    docker run --rm --label "$RESTMAIL_LABEL" --privileged --pid=host alpine:3.20 \
      sh -c 'sync; echo 2 > /proc/sys/vm/drop_caches' >/dev/null 2>&1 ||
      echo "  note: could not drop the Docker VM's guest cache" >&2
    ;;
  *)
    echo "usage: $0 cap-build-cache|drop-guest-cache" >&2
    exit 2
    ;;
esac
