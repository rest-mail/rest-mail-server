#!/usr/bin/env bash
# Prints the --tmpfs option that hands a tmpfs to the user a container runs as, for
# the engine behind `docker`. Docker takes uid= and gid=. Podman (the antimatter-vps
# runners' engine) refuses both and takes U instead, which chowns the tmpfs to the
# container's user. Keep the tmpfs's mode (0700) next to it either way.
#
#   tasks/tmpfs-owner.sh UID [GID]     GID defaults to UID
set -euo pipefail

[ $# -ge 1 ] && [ $# -le 2 ] || {
  echo "usage: $0 UID [GID]" >&2
  exit 2
}
uid=$1
gid=${2:-$1}

if docker version --format '{{range .Server.Components}}{{.Name}} {{end}}' 2>/dev/null | grep -q 'Podman Engine'; then
  echo U
else
  echo "uid=$uid,gid=$gid"
fi
