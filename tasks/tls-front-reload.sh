#!/bin/sh
set -eu

config=$1
shift
front_config="${TMPDIR:-/tmp}/front.conf"
front_pid="${TMPDIR:-/tmp}/front.pid"
printf '%b' "$config" > "$front_config"
previous=$(cksum "$@")
nginx -e /dev/stderr -c "$front_config" -g "daemon off; pid $front_pid;" &
server=$!
trap 'kill -TERM "$server" 2>/dev/null || true; wait "$server" 2>/dev/null || true' EXIT
trap 'exit 0' TERM INT
while kill -0 "$server" 2>/dev/null; do
    sleep "${TLS_FRONT_RELOAD_INTERVAL:-5}"
    if current=$(cksum "$@" 2>&1); then
        if [ "$current" != "$previous" ]; then
            if nginx -t -e /dev/stderr -c "$front_config" -g "pid $front_pid;"; then
                kill -HUP "$server"
                previous=$current
                echo 'tls-front: reloaded certificate; established connections drain normally' >&2
            else
                echo 'tls-front: certificate reload failed; retaining last usable pair' >&2
            fi
        fi
    else
        echo "tls-front: certificate reload failed; retaining last usable pair: $current" >&2
    fi
done
wait "$server"
