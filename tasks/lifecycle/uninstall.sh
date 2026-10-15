#!/usr/bin/env bash
# chore uninstall:helm — and the check that ends uninstall:pulumi.
#
# Uninstalling removes the software, never the mail. The volumes and the two
# Secrets carry helm.sh/resource-policy: keep, so they stay; installing again
# under the same name and namespace picks them up. Deleting them is
# chore data:destroy, a separate act with its own gate.
#
#   uninstall.sh helm       helm uninstall, then report
#   uninstall.sh report     report only: did it go, and what survived
set -uo pipefail
here=$(dirname "$0")
# shellcheck source=lib.sh
. "$here/lib.sh"

need kubectl helm jq

report() {
  local survivors
  if release_exists; then
    err "Release \"$NAME\" is still installed in $NAMESPACE."
    return "$EXIT_FAILED"
  fi
  say "Release \"$NAME\" is gone from $NAMESPACE."
  survivors=$(k get pvc,secret -l "app.kubernetes.io/instance=$NAME" -o json 2>/dev/null |
    jq -r '.items[] | "  \(.kind | ascii_downcase)/\(.metadata.name)"')
  if [ -z "$survivors" ]; then
    say "" "Nothing of it survived: no volumes, no Secrets." \
      "If the mail was meant to be kept, it was not — check how the program removed it:" \
      "a namespace deleted as a resource, or a helm.v3.Chart (which ignores 'keep') instead of a Release."
    return 0
  fi
  say "" "Kept, so installing again under the same name picks them up:" "$survivors" "" \
    "To delete them for good — the mail, the keys, everything — run:" \
    "  chore data:destroy --name $NAME --namespace $NAMESPACE"
}

case "${1:-}" in
helm)
  need_release
  h uninstall "$NAME" >/dev/null || die "helm uninstall failed (above)."
  report
  ;;
report) report ;;
*) die "usage: uninstall.sh helm|report" ;;
esac
