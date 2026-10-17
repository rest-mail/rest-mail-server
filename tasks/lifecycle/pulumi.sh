#!/usr/bin/env bash
# chore install:pulumi / upgrade:pulumi / uninstall:pulumi
#
# Chore owns the stack's config; you own the program. The only contract
# between them is the config keys under NAME (default restmail):
#
#   NAME:hostname  NAME:domain  NAME:namespace  NAME:chartVersion  NAME:adminPasswordHash
#
# The admin password is made here and shown once; the stack keeps only a bcrypt
# hash of it, an ordinary config value, which nobody can sign in with. So no
# step here needs the stack's passphrase.
#
# install   sets the config, prints the code (pulumi-code.sh), waits for you to
#           add it, proves with `pulumi preview` that the code and the config
#           found each other, and offers `pulumi up` on the release alone.
#           Where the stack's state cannot be read from here — a project a
#           pipeline deploys — it prints the config to commit with the code
#           instead, waits for the pipeline, and checks the server over its
#           public API.
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
# shellcheck source=owner-key.sh
. "$here/owner-key.sh"

need pulumi jq helm
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

# A stack that is not selected is a mistake to say so about. One whose state
# cannot be read from here belongs to a pipeline: install goes that way, and
# nothing else can.
stack_err=$(private_tmp)
stack=$(p stack --show-name 2>"$stack_err") || stack=''
if [ -z "$stack" ]; then
  grep -qiE 'no stack|select a stack' "$stack_err" &&
    refuse "No stack to work with in $PROJECT: pass --stack, or run \`pulumi stack select\` there."
  [ "$mode" = install ] || refuse "Stack ${STACK:-(the selected one)} of $PROJECT cannot be read from here:" "" \
    "$(tail -n 3 "$stack_err")" "" "Run this where its state is, or change the program and let your pipeline deploy it."
fi
# A stack read from here is deployed from here, and the cluster is checked from
# here: kubectl. One a pipeline deploys is checked over its public API, and
# needs none.
[ -n "$stack" ] && need kubectl

# The admin password as the stack keeps it: a bcrypt hash, from the restmail CLI.
password_hash() {
  local hash
  hash=$(printf '%s' "$1" | restmail password-hash 2>/dev/null) && [ -n "$hash" ] ||
    die "Could not hash the admin password: this needs the restmail CLI 0.7.0 or newer (restmail password-hash)."
  printf '%s' "$hash"
}

install() {
  local urn version password generated='' hash='' found owner_key
  owner_key=$(read_owner_key) || exit $?
  if [ -n "$stack" ]; then
    urn=$(stack_release_urn)
    [ -n "$urn" ] && refuse "Mail server \"$NAME\" is already deployed in stack $stack." \
      "To move to a newer chart: chore upgrade:pulumi --project $PROJECT"
    version=${VERSION:-$(config_get chartVersion)}
  else
    version=${VERSION:-}
  fi
  [ -n "$version" ] || version=$(latest_chart_version)
  [ -n "$version" ] || die "Could not read the newest chart from $CHART. Pass --version."

  # The password: given, kept from an earlier run, or made here and shown once.
  password=${PASSWORD:-}
  if [ -n "$password" ]; then
    [ "${#password}" -ge 16 ] || refuse "The password must be at least 16 characters."
  elif [ -n "$stack" ] && config_has adminPasswordHash; then
    say "Keeping the admin password already in stack $stack: it was shown when it was made."
  elif [ -t 1 ]; then
    password=$(new_password)
    generated=1
  else
    refuse "No --password, and no terminal to show a generated one on." \
      "In a pipeline, pass one: --password \"\$RESTMAIL_ADMIN_PASSWORD\" (chore password:new makes one)."
  fi
  [ -n "$password" ] && hash=$(password_hash "$password")

  if [ -n "$stack" ]; then
    # Set what is not set; leave what is. Re-running skips straight to the preview.
    config_set hostname "$HOST"
    config_set domain "$DOMAIN"
    config_set namespace "$NAMESPACE"
    config_set chartVersion "$version"
    [ -n "$hash" ] && config_set adminPasswordHash "$hash"
    [ -n "$owner_key" ] && config_set ownerKeys "$owner_key"
    say "Stack $stack config is set: $NAME:hostname=$HOST, domain=$DOMAIN, namespace=$NAMESPACE, chartVersion=$version" \
      "and $NAME:adminPasswordHash, a bcrypt hash of the admin password." ""
  else
    say "Stack ${STACK:-(the selected one)} of $PROJECT cannot be read from here, so a pipeline deploys it:" \
      "nothing is set in it from here. Add this to Pulumi.${STACK:-<stack>}.yaml, under config:, and commit it" \
      "with the code below:" ""
    printf "  %s:hostname: %s\n  %s:domain: %s\n  %s:namespace: %s\n  %s:chartVersion: \"%s\"\n  %s:adminPasswordHash: '%s'\n" \
      "$NAME" "$HOST" "$NAME" "$DOMAIN" "$NAME" "$NAMESPACE" "$NAME" "$version" "$NAME" "$hash"
    if [ -n "$owner_key" ]; then
      printf '  %s:ownerKeys: %s\n' "$NAME" "$(jq -rn --arg key "$owner_key" '$key | @json')"
    fi
  fi
  if [ -z "$owner_key" ] && [ -n "$stack" ] && config_has ownerKeys; then
    owner_key=$(config_get ownerKeys)
  fi
  owner_key_guidance "$owner_key"

  # Shown the moment it exists, whatever happens next.
  if [ -n "$generated" ]; then
    printf '\n  Your rest-mail admin password — this is the only time it is shown.\n'
    show_sign_in "https://$HOST" "admin@$DOMAIN" "$password"
    cat <<EOF
  Sign in to the rest-mail app with it, once the server is deployed: you will
  choose your own and enroll an authenticator. It stops working 72 hours after
  the server first takes it. Only its hash is kept, so it cannot be read back:
  lost before you have signed in? chore password:reset

EOF
  fi

  if [ -z "$stack" ]; then
    say "The code:" ""
    RUNTIME=$(runtime) NAME=$NAME CHART=$CHART OWNER_KEYS_CONFIGURED=${owner_key:+1} "$here/pulumi-code.sh"
    say ""
    yes_no "Committed, and has your pipeline deployed it?" || {
      say "" "When it has, check it from anywhere: restmail status --server https://$HOST"
      exit 0
    }
    verify_public "$password"
    return
  fi

  say "Checking whether your program already declares it..."
  found=$(preview_release) || preview_failed
  if [ -z "$found" ]; then
    say "" "It does not yet." ""
    RUNTIME=$(runtime) NAME=$NAME CHART=$CHART OWNER_KEYS_CONFIGURED=${owner_key:+1} "$here/pulumi-code.sh"
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

  if ! yes_no "Run pulumi up to install it now (the release only, nothing else in the stack)?"; then
    say "" "Deploy it however you deploy — pulumi up, or your pipeline — then check it:" \
      "  chore server:verify --name $NAME --namespace $NAMESPACE"
    exit 0
  fi
  p up --yes --skip-preview --target "$(jq -r .urn <<<"$found")" || die "pulumi up failed (above)."
  wait_and_verify "$password"
}

# The server as anybody reaches it: its public API answers as rest-mail, and
# the admin password signs in — into setup, owing a password of its own and an
# authenticator. Needs nothing but the address.
verify_public() {
  local password=$1 server code resp
  resp=$(private_tmp)
  say "" "Checking https://$HOST ..."
  server=$(curl -sS --max-time 15 "https://$HOST/api/v1/server" 2>&1) ||
    refuse "https://$HOST did not answer: $server" "Has the pipeline finished, and does $HOST point at the cluster?"
  [ "$(jq -r '.data.product // empty' <<<"$server" 2>/dev/null)" = rest-mail ] ||
    refuse "https://$HOST answered, but not as rest-mail:" "$(head -c 200 <<<"$server")"
  say "ok   rest-mail $(jq -r '.data.version' <<<"$server") answers at https://$HOST"
  [ -n "$password" ] || { say "ok   skipped signing in: the password was not made here"; return; }
  code=$(jq -nc --arg e "admin@$DOMAIN" --arg p "$password" '{email: $e, password: $p, client: "native"}' |
    curl -sS -o "$resp" -w '%{http_code}' --max-time 15 -H 'Content-Type: application/json' \
      --data @- "https://$HOST/api/v1/auth/login")
  case $code in
  200) say "ok   the admin password signs in (still to do: $(jq -r '.data.setup_required | join(", ")' "$resp"))" ;;
  401) refuse "The admin password does not sign in." \
    "Is $NAME:adminPasswordHash in the stack the one printed above, and has the pipeline deployed it?" ;;
  *) die "Signing in answered $code: $(head -c 200 "$resp")" ;;
  esac
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
  if config_has hostname || config_has adminPasswordHash || config_has adminPassword; then
    say ""
    if yes_no "Remove the $NAME:* config from stack $stack too (the admin password included)?"; then
      for key in hostname domain namespace chartVersion adminPasswordHash adminPassword; do
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
