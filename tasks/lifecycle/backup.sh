#!/usr/bin/env bash
# chore backup:create | backup:database | backup:keys | backup:attachments
# chore backup:restore --from FILE
#
# A backup is one tar file holding a manifest and the parts it was asked for:
#
#   database.dump    pg_dump of the embedded Postgres: mail, accounts, filters
#   keys.json        MASTER_KEY — without it the restored DKIM keys, ACME keys
#                    and every admin's authenticator secret are unreadable
#   facts.json       hostname, domain, namespace, chart version: what you need
#                    to deploy again before you can restore into it
#   attachments.tar  the attachments volume — only from backup:attachments
#
# backup:create takes everything but the attachments. Not for their size
# alone: they are a volume, not a dump, and copying one means streaming all of
# it through the cluster. The default is fixed rather than size-dependent, and
# it says every time what it left out, how big it was, and the command for it.
#
# The JWT secret is left out on purpose: losing it only signs everyone out,
# and keeping it would have a restored server honour sessions minted before
# whatever went wrong.
#
# Carrying MASTER_KEY makes the file a secret, so there is no default place
# for it: --to, or a `backupTarget` value on the release. And never into a git
# working tree that is not ignoring it.
#
#   backup.sh create|database|keys|attachments    (TO from --to)
#   backup.sh restore                              (FROM from --from)
set -uo pipefail
here=$(dirname "$0")
# shellcheck source=lib.sh
. "$here/lib.sh"

need kubectl helm jq tar
mode=${1:-}
need_release

readonly LAST_BACKUP_ANNOTATION=rest-mail.com/last-backup

postgres_pod() { k get pod -l "$(component postgres)" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null; }
api_pod() { k get pod -l "$(component api)" --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}' 2>/dev/null; }

# The Secret the API reads MASTER_KEY from: the chart's, or the operator's own.
master_key_secret() {
  k get "$(api_deployment)" -o json | jq -r '.spec.template.spec.containers[0].env[]
    | select(.name == "MASTER_KEY") | .valueFrom.secretKeyRef.name'
}

attachments_size() { # human-readable, or empty when it cannot be measured
  local pod
  pod=$(api_pod)
  [ -n "$pod" ] || return 0
  k exec "$pod" -c api -- du -sh /attachments 2>/dev/null | awk '{ print $1 }'
}

destination() { # destination WHAT -> the file to write
  local to dir
  to=${TO:-$(release_values | jq -r '.backupTarget // empty')}
  [ -n "$to" ] || refuse "Where to? A backup holds the master key, so there is no default place for it." \
    "Pass --to DIR, or record a place on the release once:" \
    "  helm upgrade $NAME $CHART -n $NAMESPACE --reuse-values --set backupTarget=DIR"
  # Relative to where chore was run, not to where the task file is.
  case "$to" in /*) ;; *) to="${USER_WORKING_DIR:-$PWD}/$to" ;; esac
  case "$to" in
  *.tar) dir=$(dirname "$to") ;;
  *) dir=$to to="$to/restmail-$NAME-$(date -u +%Y%m%dT%H%M%SZ)-$1.tar" ;;
  esac
  # A master key committed by accident is the likeliest way this goes wrong.
  # Asked of the nearest directory that exists, before creating anything.
  local existing=$dir
  while [ ! -d "$existing" ]; do existing=$(dirname "$existing"); done
  if git -C "$existing" rev-parse --is-inside-work-tree >/dev/null 2>&1 &&
    ! git -C "$existing" check-ignore -q --no-index "$to"; then
    refuse "$to is inside a git working tree that does not ignore it, and the backup holds the master key." \
      "Put it somewhere else, or add it to .gitignore first."
  fi
  mkdir -p "$dir" 2>/dev/null || refuse "Cannot create $dir."
  printf '%s' "$to"
}

create() { # create PART... -> writes the backup
  local parts=("$@") out stage part missing='[]' contains='[]' pod size
  # A refusal inside $(...) ends only the subshell, so its status is passed on.
  out=$(destination "${mode}") || exit $?
  stage=$(umask 077 && mktemp -d)
  on_exit "rm -rf '$stage'"

  for part in "${parts[@]}"; do
    case $part in
    database)
      pod=$(postgres_pod)
      if [ -z "$pod" ]; then
        missing=$(jq -c '. + [{part: "database", why: "the database is not the chart'"'"'s embedded Postgres: back it up where it runs"}]' <<<"$missing")
        continue
      fi
      say "  database      pg_dump from $pod..."
      # shellcheck disable=SC2016 # expanded by the shell in the Postgres pod
      k exec "$pod" -c postgres -- sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' >"$stage/database.dump" ||
        die "pg_dump failed; no backup written."
      ;;
    keys)
      say "  keys          MASTER_KEY from Secret $(master_key_secret)"
      k get secret "$(master_key_secret)" -o json |
        jq '{MASTER_KEY: (.data.MASTER_KEY | @base64d)}' >"$stage/keys.json" || die "Could not read MASTER_KEY; no backup written."
      ;;
    facts)
      release_values | jq --arg name "$NAME" --arg ns "$NAMESPACE" --arg chart "$(release_chart_version)" '
        {name: $name, namespace: $ns, chartVersion: $chart,
         values: (. | del(.api.bootstrapAdminPassword, .api.jwtSecret, .api.masterKey, .postgres.auth.password))}' >"$stage/facts.json"
      ;;
    attachments)
      pod=$(api_pod)
      [ -n "$pod" ] || refuse "The API is not running (offline?), and the attachments are only reachable through it." \
        "Bring it online, then run this again."
      say "  attachments   streaming /attachments from $pod ($(attachments_size))..."
      k exec "$pod" -c api -- tar -C /attachments -cf - . >"$stage/attachments.tar" || die "Copying the attachments failed; no backup written."
      ;;
    esac
    contains=$(jq -c --arg p "$part" '. + [$p]' <<<"$contains")
  done

  if [ "$mode" = create ]; then
    size=$(attachments_size)
    missing=$(jq -c --arg size "${size:-unknown — the API is not running}" '. + [{part: "attachments",
      why: "not taken by default (\($size)): chore backup:attachments takes them"}]' <<<"$missing")
  fi

  jq -n --arg name "$NAME" --arg ns "$NAMESPACE" --arg chart "$(release_chart_version)" --arg app "$(release_app_version)" \
    --argjson contains "$contains" --argjson missing "$missing" \
    '{format: 1, release: $name, namespace: $ns, chartVersion: $chart, appVersion: $app,
      created: (now | todate), contains: $contains, missing: $missing}' >"$stage/manifest.json"

  (umask 077 && tar -C "$stage" -cf "$out" .) || die "Could not write $out."
  chmod 600 "$out"
  k annotate "$(api_deployment)" --overwrite "$LAST_BACKUP_ANNOTATION=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >/dev/null 2>&1

  say "" "Written: $out ($(du -h "$out" | awk '{ print $1 }'))" \
    "It holds the master key: keep it as you would a password."
  jq -r '.missing[] | "Not in it: \(.part) — \(.why)"' "$stage/manifest.json"
}

restore() {
  local from="${FROM:-}" stage manifest pod helper secret
  [ -n "$from" ] || refuse "Restore from what? Pass --from FILE."
  [ -f "$from" ] || refuse "$from does not exist."
  stage=$(umask 077 && mktemp -d)
  on_exit "rm -rf '$stage'"
  tar -C "$stage" -xf "$from" || refuse "$from is not a rest-mail backup (not a tar file)."
  [ -f "$stage/manifest.json" ] || refuse "$from is not a rest-mail backup (no manifest)."
  manifest=$(cat "$stage/manifest.json")
  has() { jq -e --arg p "$1" '.contains | index($p)' <<<"$manifest" >/dev/null; }

  say "Backup of \"$(jq -r .release <<<"$manifest")\", taken $(jq -r .created <<<"$manifest") from chart $(jq -r .chartVersion <<<"$manifest")."
  say "  contains: $(jq -r '.contains | join(", ")' <<<"$manifest")"
  jq -r '.missing[] | "  NOT in it: \(.part) — \(.why)"' <<<"$manifest"
  if has database && ! has keys; then
    say "" "This has the database but not the key that decrypts it: DKIM keys, ACME keys and" \
      "authenticator secrets will be unreadable unless this server's key is the original."
  fi
  if has database && jq -e '.missing[] | select(.part == "attachments")' <<<"$manifest" >/dev/null; then
    say "" "Restoring the database without the attachments gives working mail whose attachments are gone." \
      "Restore an attachments backup afterwards if you have one."
  fi
  say "" "Into: release \"$NAME\" in $NAMESPACE, on $(kubectl config current-context)."

  typed_code_gate "This replaces what that server holds now with what the backup holds. It cannot be undone."

  local was_online=1
  is_offline && was_online=
  [ -n "$was_online" ] && "$here/offline.sh" offline >/dev/null

  if has keys; then
    secret=$(master_key_secret)
    say "  keys          MASTER_KEY into Secret $secret"
    k patch secret "$secret" --type merge \
      -p "$(jq -c '{stringData: {MASTER_KEY: .MASTER_KEY}}' "$stage/keys.json")" >/dev/null || die "Could not set MASTER_KEY."
  fi
  if has database; then
    pod=$(postgres_pod)
    [ -n "$pod" ] || die "No embedded Postgres to restore into."
    say "  database      pg_restore into $pod..."
    # shellcheck disable=SC2016 # expanded by the shell in the Postgres pod
    k exec -i "$pod" -c postgres -- sh -c 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists --no-owner' \
      <"$stage/database.dump" || err "  pg_restore reported problems (above); check the server before trusting it."
  fi
  if has attachments; then
    # The volume is mounted by the API alone, and the API is stopped, so a pod
    # of our own borrows it.
    helper="$NAME-restore-$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c 6)"
    k get "$(api_deployment)" -o json | jq --arg pod "$helper" '.spec.template as $t | {
        apiVersion: "v1", kind: "Pod", metadata: {name: $pod},
        spec: ($t.spec + {restartPolicy: "Never",
          volumes: [$t.spec.volumes[] | select(.name == "attachments")],
          containers: [$t.spec.containers[0] | del(.livenessProbe, .readinessProbe, .env, .envFrom, .ports)
            | .command = ["sleep", "3600"] | .args = []
            | .volumeMounts = [.volumeMounts[] | select(.name == "attachments")]]})}' | k create -f - >/dev/null ||
      die "Could not start a pod to restore the attachments with."
    on_exit "k delete pod '$helper' --ignore-not-found --wait=false >/dev/null 2>&1"
    k wait "pod/$helper" --for=condition=Ready --timeout=180s >/dev/null || die "The restore pod did not start."
    say "  attachments   into the attachments volume..."
    k exec -i "$helper" -- tar -C /attachments -xf - <"$stage/attachments.tar" || die "Copying the attachments back failed."
    k delete pod "$helper" --wait=true >/dev/null 2>&1
  fi

  if [ -n "$was_online" ]; then
    "$here/offline.sh" online >/dev/null
    say "" && "$here/verify.sh"
  else
    say "" "Restored. The server was offline before, so it is left offline: chore server:online"
  fi
}

case "$mode" in
create) create database keys facts ;;
database) create database facts ;;
keys) create keys facts ;;
attachments) create attachments facts ;;
restore) restore ;;
*) die "usage: backup.sh create|database|keys|attachments|restore" ;;
esac
