#!/usr/bin/env bash
# chore data:destroy — delete what an uninstall kept: the mail, the
# attachments, the master key and the database password. There is no undo.
#
# It is the only task with a typed-code gate, and the gate is here rather than
# on uninstall because this is the irreversible step: a gate on a reversible
# one teaches people to grind through gates. --yes does not answer it, a pipe
# cannot feed it, and with no terminal it refuses. --i-mean-it (AUTOMATE=1)
# is the one way past, for whoever genuinely means to script it.
#
# It refuses while the release is installed: deleting the volumes from under a
# running server is not a way to uninstall it.
set -uo pipefail
here=$(dirname "$0")
# shellcheck source=lib.sh
. "$here/lib.sh"

need kubectl helm jq

release_exists && refuse "Release \"$NAME\" is still installed in $NAMESPACE." \
  "Uninstall it first — chore uninstall:helm, or remove it from your Pulumi program — then run this."

targets=$(k get pvc,secret -l "app.kubernetes.io/instance=$NAME" -o json 2>/dev/null |
  jq -r '.items[] | "\(.kind | ascii_downcase)/\(.metadata.name)"')
[ -n "$targets" ] || { say "Nothing of \"$NAME\" is left in $NAMESPACE. Nothing to do."; exit 0; }

say "In namespace $NAMESPACE, on $(kubectl config current-context):" ""
while IFS= read -r t; do say "  $t"; done <<<"$targets"
say "" "That is every mailbox, every attachment, and the key that decrypts the DKIM keys," \
  "certificates and authenticator secrets. Have a backup if any of it matters: once this" \
  "runs, chore backup:create has nothing left to take."

typed_code_gate "This deletes them permanently."

for t in $targets; do
  k delete "$t" --wait=false >/dev/null && say "  deleted $t"
done
say "" "Done. The namespace $NAMESPACE itself is left alone; it may hold other things."
