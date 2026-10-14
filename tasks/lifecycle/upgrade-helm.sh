#!/usr/bin/env bash
# chore upgrade:helm [--version V]
#
# Takes nothing but the version. Everything else is read back from the
# release — hostname, domain, every value it was installed with — and given to
# Helm again, because forgetting one does not fail: `--set
# mailserver.hostname` left off an upgrade silently reverts it to the chart's
# placeholder, and the server starts answering as mail.example.com.
#
# The values go back as a file rather than through --reuse-values, which would
# also keep the OLD chart's defaults and miss whatever the new one changed.
#
# It does not roll back on its own when verify fails afterwards: a migration
# may have run, and old code under a migrated database can be worse than the
# failure. It says what failed and how to go back, and leaves the choice to you.
set -uo pipefail
here=$(dirname "$0")
# shellcheck source=lib.sh
. "$here/lib.sh"
# shellcheck source=status.sh
. "$here/status.sh"

need helm kubectl jq curl
VERSION=${VERSION:-}
need_release
is_offline && refuse "The server is offline (chore server:offline)." \
  "An upgrade would bring it back up behind your back. Bring it online first: chore server:online"

from=$(release_chart_version)
from_revision=$(release_revision)
if [ -z "$VERSION" ]; then
  VERSION=$(latest_chart_version)
  [ -n "$VERSION" ] || die "Could not read the newest chart from $CHART. Pass --version."
fi
[ "$VERSION" = "$from" ] && { say "Already on chart $from. Nothing to do."; exit 0; }

values_json=$(release_values)
host=$(jq -r '.mailserver.hostname // empty' <<<"$values_json")
domain=$(jq -r '.mailserver.domain // empty' <<<"$values_json")
if [ -z "$host" ] || [ -z "$domain" ]; then
  refuse "The release does not record its hostname and domain, so an upgrade would put the chart's placeholders in." \
    "It was installed some other way; upgrade it with helm directly, naming both."
fi

status_block
say "" "Upgrading chart $from -> $VERSION. Carried forward, unchanged:"
jq -r 'paths(scalars) as $p | ($p | map(tostring) | join(".")) as $k
  | if ($k | test("[Pp]assword|[Ss]ecret|[Kk]ey$")) then "  \($k) = (secret)" else "  \($k) = \(getpath($p))" end' <<<"$values_json"

# The moment "you should have a backup" is actionable is before anything has
# happened. Afterwards it is only a reproach.
say ""
backup_before_changing || refuse "Stopped before upgrading. Nothing changed."

values=$(private_tmp)
printf '%s' "$values_json" >"$values"
h upgrade "$NAME" "$CHART" --version "$VERSION" -f "$values" >/dev/null ||
  die "helm upgrade failed (above). The release is as it was: chore server:status"

say "Waiting for the new version to start..."
for d in $(serving_deployments) $(k get statefulset -l "app.kubernetes.io/instance=$NAME" -o name); do
  k rollout status "$d" --timeout=300s >/dev/null 2>&1 || break
done
say ""

# Identity must have survived: the whole reason this task exists.
after=$(release_values)
if [ "$(jq -r '.mailserver.hostname' <<<"$after")" != "$host" ] || [ "$(jq -r '.mailserver.domain' <<<"$after")" != "$domain" ]; then
  err "WARNING: the hostname or domain changed during the upgrade. Check: helm get values $NAME -n $NAMESPACE"
fi

"$here/verify.sh"
verified=$?
if [ "$verified" = "$EXIT_OUTSIDE" ]; then
  cat >&2 <<EOF

The new version is running and answers in-cluster; only the way in from
outside fails (above), and an upgrade does not change that. Nothing to roll
back for.
EOF
  exit "$EXIT_OUTSIDE"
fi
if [ "$verified" != 0 ]; then
  cat >&2 <<EOF

The upgrade to chart $VERSION is in place and is not working.

Going back is your call, not this task's: if the new version ran a database
migration, the old one may not understand the database any more. To go back:

    helm rollback $NAME $from_revision -n $NAMESPACE

If the old version will not start on the migrated database, restore your
backup into it: chore backup:restore --from FILE
EOF
  exit "$EXIT_FAILED"
fi
