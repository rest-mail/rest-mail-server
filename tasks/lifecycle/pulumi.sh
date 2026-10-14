#!/usr/bin/env bash
# chore install:pulumi / upgrade:pulumi / uninstall:pulumi
#
# Chore owns the stack's config; you own the program. The only contract
# between them is the config keys under NAME (default restmail):
#
#   NAME:hostname  NAME:domain  NAME:namespace  NAME:chartVersion  NAME:adminPassword
#
# install   sets the config, prints the code (pulumi-code.sh), waits for you to
#           add it, proves with `pulumi preview` that the code and the config
#           found each other, and offers `pulumi up` on the release alone.
# upgrade   is a config change: chartVersion, preview, up — never your source.
# uninstall cannot uninstall anything. Any removal your program does not
#           reflect is undone by the next `pulumi up`, so it tells you what to
#           remove, waits, and checks that it went and what was kept.
#
#   pulumi.sh install|upgrade|uninstall     (PROJECT, STACK, NAME, HOST, DOMAIN,
#                                            PASSWORD, VERSION from the task)
set -uo pipefail
here=$(dirname "$0")
# shellcheck source=lib.sh
. "$here/lib.sh"

need pulumi jq kubectl helm
mode=${1:-}
: "${PROJECT:?}"
STACK=${STACK:-}
VERSION=${VERSION:-}

p() { pulumi -C "$PROJECT" "$@" ${STACK:+--stack "$STACK"}; }
stack_name() { p stack --show-name 2>/dev/null; }

# The release in the stack's state — deployed, not merely declared.
stack_release_urn() {
  p stack export 2>/dev/null | jq -r --arg n "$NAME" '[.deployment.resources[]?
    | select(.type == "kubernetes:helm.sh/v3:Release")
    | select(.outputs.name == $n or (.urn | endswith("::" + $n)))][0].urn // empty'
}
config_has() { p config --json 2>/dev/null | jq -e --arg k "$NAME:$1" 'has($k)' >/dev/null; }
config_get() { p config get "$NAME:$1" 2>/dev/null; }
config_set() { p config set "$NAME:$1" "$2" >/dev/null; }

runtime() {
  local f="$PROJECT/Pulumi.yaml"
  [ -f "$f" ] || f="$PROJECT/Pulumi.yml"
  awk '/^runtime:/ { if ($2 != "") { print $2; exit } inrt = 1; next }
       inrt && /^[[:space:]]+name:/ { print $2; exit }
       inrt && /^[^[:space:]]/ { exit }' "$f" 2>/dev/null | tr -d "\"'"
}

# What `pulumi preview` says it would do with our release, as JSON:
# {op, urn, name, hostname}, or empty if the program does not declare it.
# Preview is the only thing that proves the config and the code found each
# other: wrong stack, wrong config namespace, wrong project all show up here.
preview_release() {
  local out
  out=$(p preview --json 2>"$preview_err") || return 1
  jq -c --arg n "$NAME" '[.steps[]?
    | select(.newState.type == "kubernetes:helm.sh/v3:Release")
    | select(.newState.inputs.name == $n)
    | {op, urn, name: .newState.inputs.name,
       hostname: .newState.inputs.values.mailserver.hostname,
       version: .newState.inputs.version}][0] // empty' <<<"$out"
}
preview_err=$(private_tmp)
preview_failed() {
  err "pulumi preview failed, so nothing can be checked yet:" "" "$(tail -n 20 "$preview_err")" ""
  if grep -qiE 'kubernetes|cluster|kubeconfig|connection refused' "$preview_err"; then
    err "That is the provider failing to reach a cluster: check the kubeconfig this stack uses."
  else
    err "That is most likely the program not building: fix it, then run this again — the config is already set."
  fi
  exit "$EXIT_FAILED"
}

stack=$(stack_name) || refuse "No stack to work with in $PROJECT: pass --stack, or run \`pulumi stack select\` there."

install() {
  local urn version password generated='' found
  urn=$(stack_release_urn)
  [ -n "$urn" ] && refuse "Mail server \"$NAME\" is already deployed in stack $stack." \
    "To move to a newer chart: chore upgrade:pulumi --project $PROJECT"

  version=${VERSION:-$(config_get chartVersion)}
  [ -n "$version" ] || version=$(latest_chart_version)
  [ -n "$version" ] || die "Could not read the newest chart from $CHART. Pass --version."

  # Set what is not set; leave what is. Re-running skips straight to the preview.
  config_set hostname "$HOST"
  config_set domain "$DOMAIN"
  config_set namespace "$NAMESPACE"
  config_set chartVersion "$version"
  password=${PASSWORD:-}
  if [ -n "$password" ]; then
    [ "${#password}" -ge 16 ] || refuse "The password must be at least 16 characters."
    printf '%s' "$password" | p config set --secret "$NAME:adminPassword" >/dev/null
  elif config_has adminPassword; then
    say "Keeping the admin password already in stack $stack."
    password=$(config_get adminPassword)
  elif [ -t 1 ]; then
    password=$(new_password)
    generated=1
    printf '%s' "$password" | p config set --secret "$NAME:adminPassword" >/dev/null
  else
    refuse "No --password, and no terminal to show a generated one on." \
      "In a pipeline, pass one: --password \"\$RESTMAIL_ADMIN_PASSWORD\" (chore password:new makes one)."
  fi
  say "Stack $stack config is set: $NAME:hostname=$HOST, domain=$DOMAIN, namespace=$NAMESPACE, chartVersion=$version" \
    "and $NAME:adminPassword, encrypted." ""

  say "Checking whether your program already declares it..."
  found=$(preview_release) || preview_failed
  if [ -z "$found" ]; then
    say "" "It does not yet. Add this to the program in $PROJECT:" ""
    RUNTIME=$(runtime) NAME=$NAME CHART=$CHART "$here/pulumi-code.sh"
    say ""
    yes_no "Added it to your program?" || {
      say "" "When you have, run this again: the config is set, so it goes straight to the preview."
      exit 0
    }
    found=$(preview_release) || preview_failed
    [ -n "$found" ] || refuse "pulumi preview still shows no Helm Release named \"$NAME\" in stack $stack." \
      "Is the new file wired in (the import, or the call in main), and is it this project and stack?"
  fi
  [ "$(jq -r .hostname <<<"$found")" = "$HOST" ] || refuse \
    "The program declares \"$NAME\", but its hostname comes out as \"$(jq -r .hostname <<<"$found")\", not $HOST." \
    "It should read $NAME:hostname from config, as the printed code does."
  say "pulumi preview: would $(jq -r .op <<<"$found") Helm release \"$NAME\" as $HOST, chart $(jq -r .version <<<"$found")."

  if [ -n "$generated" ]; then
    cat <<EOF

  Your rest-mail admin password — this is the only time it is shown:

      $password

  Sign in to the rest-mail app at https://$HOST as "admin" with it; you will
  choose your own and enroll an authenticator. It stops working 72 hours after
  the server first takes it. Read it back with:
      pulumi config get $NAME:adminPassword -C $PROJECT${STACK:+ --stack $STACK}

EOF
  fi

  if ! yes_no "Run pulumi up to install it now (the release only, nothing else in the stack)?"; then
    say "" "Deploy it however you deploy — pulumi up, or your pipeline — then check it:" \
      "  chore server:verify --name $NAME --namespace $NAMESPACE"
    exit 0
  fi
  p up --yes --skip-preview --target "$(jq -r .urn <<<"$found")" || die "pulumi up failed (above)."
  wait_and_verify "$password"
}

wait_and_verify() {
  say "" "Waiting for it to start..."
  for d in $(serving_deployments) $(k get statefulset -l "app.kubernetes.io/instance=$NAME" -o name); do
    k rollout status "$d" --timeout=300s >/dev/null 2>&1 || break
  done
  say ""
  ADMIN_PASSWORD=${1:-} "$here/verify.sh"
}

upgrade() {
  local urn from found
  urn=$(stack_release_urn)
  [ -n "$urn" ] || refuse "Stack $stack has no deployed Helm release \"$NAME\". Install it: chore install:pulumi"
  is_offline && refuse "The server is offline (chore server:offline). An upgrade would bring it back behind your back." \
    "Bring it online first: chore server:online"
  from=$(config_get chartVersion)
  [ -n "$from" ] || refuse "Stack $stack has no $NAME:chartVersion, so this program was not set up by install:pulumi." \
    "Upgrade it by changing the version in your program."
  VERSION=${VERSION:-$(latest_chart_version)}
  [ -n "$VERSION" ] || die "Could not read the newest chart from $CHART. Pass --version."
  [ "$VERSION" = "$from" ] && { say "Already on chart $from. Nothing to do."; exit 0; }

  say "Upgrading \"$NAME\" in stack $stack: chart $from -> $VERSION. Nothing else in the config changes." ""
  backup_before_changing || refuse "Stopped before upgrading. Nothing changed."

  config_set chartVersion "$VERSION"
  found=$(preview_release) || { config_set chartVersion "$from"; preview_failed; }
  if [ "$(jq -r .version <<<"$found")" != "$VERSION" ]; then
    config_set chartVersion "$from"
    refuse "pulumi preview does not pick up the new version: the program does not read $NAME:chartVersion." \
      "The config is back to $from. Make the program read it, as install:pulumi's code does."
  fi
  p up --yes --skip-preview --target "$urn" || die "pulumi up failed (above). To put the config back: pulumi config set $NAME:chartVersion $from"
  wait_and_verify
  local verified=$?
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

    pulumi config set $NAME:chartVersion $from -C $PROJECT${STACK:+ --stack $STACK}
    pulumi up --target '$urn' -C $PROJECT${STACK:+ --stack $STACK}

If the old version will not start on the migrated database, restore your
backup into it: chore backup:restore --from FILE
EOF
    exit "$EXIT_FAILED"
  fi
}

uninstall() {
  local urn module=${NAME//-/_}
  urn=$(stack_release_urn)
  if [ -n "$urn" ]; then
    cat <<EOF
Remove rest-mail from the program in $PROJECT, then deploy the change:

  - delete the file $module.(ts|py|go), or the "$module:" entry under resources
    in Pulumi.yaml
  - delete the line that wires it in (the import, or the call in main)
  - leave the namespace alone if your program declares it as a resource:
    deleting a namespace deletes everything in it, the kept mail included
  - run pulumi up

Not \`pulumi destroy --target\`: the program would still declare the release,
and the next \`pulumi up\` anyone runs would put the mail server back.

The mail is kept: the volumes and Secrets carry resource-policy: keep, which a
Release honours because it uninstalls through Helm.

EOF
    yes_no "Removed it and run pulumi up?" || {
      say "" "When you have, run this again to check."
      exit 0
    }
    urn=$(stack_release_urn)
    [ -z "$urn" ] || refuse "Stack $stack still has the release ($urn)." \
      "Was the change deployed, to this stack? Run this again once it has."
  fi
  "$here/uninstall.sh" report || exit $?
  if config_has hostname || config_has adminPassword; then
    say ""
    if yes_no "Remove the $NAME:* config from stack $stack too (the admin password included)?"; then
      for key in hostname domain namespace chartVersion adminPassword; do
        p config rm "$NAME:$key" >/dev/null 2>&1
      done
      say "Removed."
    fi
  fi
}

case "$mode" in
install) install ;;
upgrade) upgrade ;;
uninstall) uninstall ;;
*) die "usage: pulumi.sh install|upgrade|uninstall" ;;
esac
