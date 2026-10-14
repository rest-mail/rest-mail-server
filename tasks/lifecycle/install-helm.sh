#!/usr/bin/env bash
# chore install:helm HOST [--domain D] [--password P] [--version V] [--values FILE]
#
# The admin password is made here, not by you, unless you bring one:
#   - none given, at a terminal: generated, used, shown once
#   - none given, no terminal (a pipeline): refused, because a generated
#     password printed to a log is a password in a log
#   - none given, but an earlier install left one (its Secret is kept on
#     uninstall): left alone, so re-installing never changes a password
#     someone may be halfway through using
#   - PASSWORD in the environment (chore's --password): used as given
#
# It installs once. A release that is already there is upgrade:helm's to
# change: an install over the top would put back whatever this command's
# defaults are, which is how a hostname silently becomes mail.example.com.
set -uo pipefail
here=$(dirname "$0")
# shellcheck source=lib.sh
. "$here/lib.sh"

need helm kubectl jq openssl curl
: "${HOST:?}" "${DOMAIN:?}"
PASSWORD=${PASSWORD:-}
VERSION=${VERSION:-}
VALUES=${VALUES:-}
# Relative to where chore was run.
case "$VALUES" in "" | /*) ;; *) VALUES="${USER_WORKING_DIR:-$PWD}/$VALUES" ;; esac

if release_exists; then
  refuse "rest-mail is already installed here: release \"$NAME\" in $NAMESPACE, chart $(release_chart_version)." \
    "" \
    "  to move to a newer chart:    chore upgrade:helm" \
    "  to install again from clean: chore uninstall:helm, then this — the mail is kept" \
    "  to see what is there:        chore server:status"
fi

if [ -z "$VERSION" ]; then
  VERSION=$(latest_chart_version)
  [ -n "$VERSION" ] || die "Could not read the newest chart from $CHART. Pass --version."
fi

# What an earlier install left behind, kept on uninstall on purpose.
kept_secret=$(k get secret -l "$(component api)" -o json 2>/dev/null |
  jq -r '[.items[] | select(.data.BOOTSTRAP_ADMIN_PASSWORD)][0].metadata.name // empty')
kept_volumes=$(k get pvc -l "app.kubernetes.io/instance=$NAME" -o name 2>/dev/null | sed 's|^persistentvolumeclaim/||' | paste -sd' ' -)

generated=
if [ -n "$PASSWORD" ]; then
  [ "${#PASSWORD}" -ge 16 ] || refuse "The password must be at least 16 characters."
elif [ -n "$kept_secret" ]; then
  say "Keeping the admin password the earlier install left in Secret $kept_secret."
elif [ -t 1 ]; then
  PASSWORD=$(new_password)
  generated=1
else
  refuse "No --password, and no terminal to show a generated one on." \
    "In a pipeline, make one where your secrets live and pass it: --password \"\$RESTMAIL_ADMIN_PASSWORD\"" \
    "(chore password:new makes one.)"
fi
[ -n "$kept_volumes" ] && say "Found the data of an earlier install ($kept_volumes); this install picks it up."

values=$(private_tmp)
jq -n --arg host "$HOST" --arg domain "$DOMAIN" --arg pw "$PASSWORD" '
  {mailserver: {hostname: $host, domain: $domain}}
  + (if $pw != "" then {api: {bootstrapAdminPassword: $pw}} else {} end)' >"$values"

say "Installing chart $VERSION as \"$NAME\" in namespace $NAMESPACE..."
# Theirs first, ours last: the hostname, domain and password given here win.
h install "$NAME" "$CHART" --version "$VERSION" --create-namespace \
  ${VALUES:+-f "$VALUES"} -f "$values" >/dev/null ||
  die "helm install failed (above). Nothing to undo if the release does not exist: chore server:status"

if [ -n "$generated" ]; then
  cat <<EOF

  Your rest-mail admin password — this is the only time it is shown:

      $PASSWORD

  Sign in to the rest-mail app at https://$HOST as "admin" with it. You will
  choose your own password and enroll an authenticator before anything else
  works. It stops working 72 hours after the server first takes it.

  Lost it before then? Install again with a new one, or: chore password:reset
  It can also be read back from the cluster:
      kubectl get secret -n $NAMESPACE -l app.kubernetes.io/instance=$NAME,app.kubernetes.io/component=api \\
        -o jsonpath='{.items[0].data.BOOTSTRAP_ADMIN_PASSWORD}' | base64 -d

EOF
fi

say "Waiting for it to start (every pod waits for the TLS Secret ${TLS_SECRET:-restmail-tls})..."
for d in $(serving_deployments) $(k get statefulset -l "app.kubernetes.io/instance=$NAME" -o name); do
  k rollout status "$d" --timeout=300s >/dev/null 2>&1 || break
done
say ""
ADMIN_PASSWORD=$PASSWORD exec "$here/verify.sh"
