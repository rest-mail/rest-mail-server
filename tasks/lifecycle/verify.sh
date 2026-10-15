#!/usr/bin/env bash
# chore server:verify — is it really working. The gate at the end of every
# install and upgrade, and the thing to run on a Tuesday.
#
# Layer by layer, stopping at the first failure, saying whose failure it is.
# In-cluster comes before public on purpose: passing in-cluster and failing in
# public means "the server is fine; DNS, ingress or the certificate is not".
#
# ADMIN_PASSWORD, when the calling task just set it, is used to sign in.
# Otherwise the install's own password is read from the API's Secret. Signing
# in spends nothing: it is followed by a sign-out, and only choosing a new
# password retires the bootstrap one.
set -uo pipefail
here=$(dirname "$0")
# shellcheck source=lib.sh
. "$here/lib.sh"
# shellcheck source=status.sh
. "$here/status.sh"

need kubectl helm jq curl

step() { printf '  %-22s' "$1"; }
ok() { say "ok   $*"; }
fail() { # fail SUMMARY [what to do...]
  say "NO   $1"
  shift
  [ $# -gt 0 ] && { say ""; printf '%s\n' "$@" >&2; }
  exit "$EXIT_FAILED"
}

need_release
status_block
say ""

step "resources created"
info=$(h status "$NAME" -o json)
helm_status=$(jq -r '.info.status' <<<"$info")
[ "$helm_status" = deployed ] || fail "Helm reports \"$helm_status\"" \
  "This is the install itself. See why: helm status $NAME -n $NAMESPACE"
kinds=$(h get manifest "$NAME" | awk '$1 == "kind:" { print $2 }' | sort -u | paste -sd, - | sed 's/,/, /g')
ok "$kinds"

step "pods ready"
is_offline && fail "the server is offline" "Bring it back with: chore server:online"
workloads=$(k get deploy,statefulset -l "app.kubernetes.io/instance=$NAME" -o json)
not_ready=$(jq -r '.items[] | select((.status.readyReplicas // 0) < .spec.replicas)
  | "\(.metadata.name): \(.status.readyReplicas // 0)/\(.spec.replicas)"' <<<"$workloads")
if [ -n "$not_ready" ]; then
  fail "not all ready" "$not_ready" "" \
    "Every pod waits for the TLS Secret, so check that first; then the pods themselves:" \
    "  kubectl get pods -n $NAMESPACE -l app.kubernetes.io/instance=$NAME" \
    "  chore server:logs api"
fi
ok "$(jq -r '[.items[] | .status.readyReplicas // 0] | add' <<<"$workloads") pods across $(jq '.items | length' <<<"$workloads") workloads"

step "answers in-cluster"
svc=$(k get svc -l "$(component api)" -o jsonpath='{.items[0].metadata.name}')
port=$(k get svc "$svc" -o jsonpath='{.spec.ports[0].port}')
pf_log=$(private_tmp)
k port-forward "svc/$svc" ":$port" >"$pf_log" 2>&1 &
pf=$!
on_exit "kill $pf 2>/dev/null"
local_port=
for _ in $(seq 1 50); do
  local_port=$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9]*\).*/\1/p' "$pf_log" | head -n1)
  [ -n "$local_port" ] && break
  sleep 0.2
done
[ -n "$local_port" ] || fail "could not reach the API's Service" "$(cat "$pf_log")"
api="http://127.0.0.1:$local_port"
server=$(curl -sS --max-time 10 "$api/api/v1/server" 2>&1) || fail "the API did not answer: $server" "  chore server:logs api"
[ "$(jq -r '.data.product // empty' <<<"$server" 2>/dev/null)" = rest-mail ] ||
  fail "something answered, but not as rest-mail" "$(head -c 200 <<<"$server")"
ok "rest-mail $(jq -r '.data.version' <<<"$server"), via port-forward"

step "admin password works"
password=${ADMIN_PASSWORD:-}
if [ -z "$password" ]; then
  secret=$(k get secret -l "$(component api)" -o json 2>/dev/null |
    jq -r '[.items[] | select(.data.BOOTSTRAP_ADMIN_PASSWORD)][0].data.BOOTSTRAP_ADMIN_PASSWORD // empty')
  [ -n "$secret" ] && password=$(base64 -d <<<"$secret")
fi
if [ -z "$password" ]; then
  ok "skipped: the install carries no admin password to try"
else
  headers=$(private_tmp)
  body=$(jq -nc --arg p "$password" '{username: "admin", password: $p}' |
    curl -sS --max-time 10 -D "$headers" -H 'Content-Type: application/json' --data @- "$api/api/v1/auth/login")
  code=$(awk 'NR == 1 { print $2 }' "$headers")
  case "$code:$(jq -r '.error.code // empty' <<<"$body" 2>/dev/null)" in
  200:*)
    # Sign out again, so verifying leaves no session behind. The refresh
    # cookie is Secure, which curl will not send over plain HTTP, so it goes
    # back as a header.
    refresh=$(sed -n 's/^[Ss]et-[Cc]ookie: \(restmail_refresh=[^;]*\).*/\1/p' "$headers" | head -n1)
    [ -n "$refresh" ] && curl -sS --max-time 10 -o /dev/null -X POST -H "Cookie: $refresh" "$api/api/v1/auth/logout"
    pending=$(jq -r '.data.setup_required // [] | join(", ")' <<<"$body")
    if [ -n "$pending" ]; then
      ok "sign-in accepted, onboarding pending ($pending)"
    else
      ok "sign-in accepted"
    fi
    ;;
  401:bootstrap_expired)
    fail "the install's password expired before anyone used it" \
      "It lasts 72 hours. Give the admin a new one: chore password:reset"
    ;;
  401:*)
    # The deployment's password stops working the moment the admin chooses
    # their own, by design. Not a failure: the install has been taken over.
    ok "the install's password is retired: the admin chose their own, or it was reset"
    ;;
  429:*) fail "too many sign-ins: the server is rate-limiting. Try again in a minute." ;;
  *) fail "unexpected answer ($code)" "$(head -c 300 <<<"$body")" ;;
  esac
fi

step "answers publicly"
# From here on, a failure is outside the server.
fail() {
  say "NO   $1"
  shift
  [ $# -gt 0 ] && { say ""; printf '%s\n' "$@" >&2; }
  exit "$EXIT_OUTSIDE"
}
host=$(release_values | jq -r '.mailserver.hostname // empty')
[ -n "$host" ] || fail "the release has no mailserver.hostname"
public=$(curl -sS --max-time 15 "https://$host/api/v1/server" 2>&1)
case $? in
0) ;;
6) fail "https://$host: the name does not resolve" \
  "The server is fine — this is DNS. Point $host at the cluster's ingress or load balancer." \
  "chore dns:show prints every record the domain needs." ;;
7 | 28) fail "https://$host: nothing answered" \
  "The server is fine in-cluster — this is the way in: an ingress for the API, or a load balancer," \
  "and DNS pointing at it." ;;
35 | 51 | 58 | 60) fail "https://$host: the certificate is not trusted" \
  "The server is fine — this is the certificate. If cert-manager is issuing it, give it a few minutes." ;;
*) fail "https://$host: $public" ;;
esac
[ "$(jq -r '.data.product // empty' <<<"$public" 2>/dev/null)" = rest-mail ] ||
  fail "https://$host answers, but not as rest-mail — something else serves that address" "$(head -c 200 <<<"$public")"
ok "https://$host, certificate valid"
