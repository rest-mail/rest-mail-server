#!/bin/sh
# Stops the processes running exactly this executable path, and no others: a
# development build or another checkout's copy of the app runs from a different path.
#   stop-installed.sh <path to .app/Contents/MacOS/executable>
set -u
executable=${1:?usage: stop-installed.sh <executable path>}
pids=$(ps -ax -o pid= -o comm= | awk -v path="$executable" '$2 == path { print $1 }')
[ -n "$pids" ] || exit 0
for pid in $pids; do kill "$pid" 2>/dev/null || true; done
for _ in 1 2 3 4 5 6 7 8 9 10; do
  alive=false
  for pid in $pids; do kill -0 "$pid" 2>/dev/null && alive=true; done
  $alive || exit 0
  sleep 0.5
done
echo "warning: $executable did not stop within 5 seconds" >&2
