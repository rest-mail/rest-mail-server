#!/usr/bin/env bash
# First start, end to end: an agent claims a fresh rest-mail server with the
# restmail CLI and nobody else involved, builds it, and hands it over -- and the
# owner's redemption must lock the agent out, unexpired access token included.
#
# Run locally (Docker, curl, jq, oathtool, python3):
#   docker build --target prod -t restmail-api:first-start .
#   go build -o /tmp/restmail ./cmd/console
#   RESTMAIL=/tmp/restmail IMAGE=restmail-api:first-start tests/first-start/agent-e2e.sh
#
# CI runs it on every pull request (ci.yml, job first-start).
set -euo pipefail

RESTMAIL=${RESTMAIL:-/tmp/restmail}
IMAGE=${IMAGE:-restmail-api:first-start}
API=http://127.0.0.1:58080/api/v1
SERVER=https://mail.e2e.test
NET=restmail-e2e
step() { printf '\n== %s\n' "$*"; }
ok() { printf '   ok: %s\n' "$*"; }
die() { printf '   FAIL: %s\n' "$*" >&2; exit 1; }

cleanup() { docker rm -f e2e-api e2e-pg >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

step "a fresh install: Postgres and the API, nothing else"
docker network create "$NET" >/dev/null
docker run -d --name e2e-pg --network "$NET" -e POSTGRES_USER=restmail -e POSTGRES_PASSWORD=restmail \
  -e POSTGRES_DB=restmail postgres:16-alpine >/dev/null
until docker exec e2e-pg pg_isready -U restmail >/dev/null 2>&1; do sleep 1; done
docker run -d --name e2e-api --network "$NET" -p 127.0.0.1:58080:8080 \
  -e DB_HOST=e2e-pg -e DB_NAME=restmail -e DB_USER=restmail -e DB_PASS=restmail -e DB_SSLMODE=disable \
  -e JWT_SECRET="$(openssl rand -hex 32)" -e MASTER_KEY="$(openssl rand -hex 32)" \
  -e ENVIRONMENT=development "$IMAGE" >/dev/null
for _ in $(seq 60); do curl -fsS http://127.0.0.1:58080/api/health >/dev/null 2>&1 && break; sleep 1; done
curl -fsS http://127.0.0.1:58080/api/health | jq -c .data
ok "server up, no admin, no bootstrap password anywhere"

step "restmail setup: a claim link, minted inside the container"
setup=$("$RESTMAIL" setup --docker e2e-api --server "$SERVER" --json --show-secret)
jq -c 'del(.link)' <<<"$setup"
link=$(jq -r .link <<<"$setup")
token=$(python3 -c 'import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.argv[1]).query)["token"][0])' "$link")
[[ $link == restmail://setup\?* ]] || die "link $link"
ok "link is restmail://setup?server=…&token=… (token not shown)"

step "redeem it as the agent: claim with a password the agent chose"
AGENT_PW=$(openssl rand -base64 24)
claim=$(curl -fsS "$API/auth/claim" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg t "$token" --arg p "$AGENT_PW" '{token:$t, username:"agent", password:$p, client:"native"}')")
jq -c '.data | {setup_required, capabilities}' <<<"$claim"
access=$(jq -r .data.access_token <<<"$claim")
[[ $(jq -c .data.setup_required <<<"$claim") == '["two_factor"]' ]] || die "setup_required"
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/admin/stats" -H "Authorization: Bearer $access")
[[ $code == 403 ]] || die "admin API in setup answered $code"
ok "claimed; the admin API refuses the session (403) until 2FA"

step "the same link again"
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/auth/claim" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg t "$token" '{token:$t, username:"thief", password:"a long enough password"}')")
[[ $code == 401 ]] || die "reused link answered $code"
ok "spent: 401"

step "enroll an authenticator, as the app would"
secret=$(curl -fsS -X POST "$API/auth/2fa/enroll" -H "Authorization: Bearer $access" | jq -r .data.secret)
curl -fsS -X POST "$API/auth/2fa/confirm" -H "Authorization: Bearer $access" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg c "$(oathtool --totp -b "$secret")" '{code:$c}')"
ok "enrolled (the agent computes codes from the secret)"

step "sign in for real"
agent=$(curl -fsS "$API/auth/login" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg p "$AGENT_PW" --arg c "$(oathtool --totp -b "$secret")" '{username:"agent", password:$p, totp_code:$c, client:"native"}')")
jq -c '.data | {setup_required, capabilities}' <<<"$agent"
agent_access=$(jq -r .data.access_token <<<"$agent")
agent_refresh=$(jq -r .data.refresh_token <<<"$agent")
ok "full session"

step "build the server: domain and the owner's mailbox"
auth=(-H "Authorization: Bearer $agent_access" -H 'Content-Type: application/json')
curl -fsS "$API/admin/domains" "${auth[@]}" -d '{"name":"e2e.test","server_type":"restmail"}' | jq -c '.data | {name, server_type}'
MAILBOX_PW=$(openssl rand -base64 18)
curl -fsS "$API/admin/mailboxes" "${auth[@]}" \
  -d "$(jq -nc --arg p "$MAILBOX_PW" '{address:"owner@e2e.test", password:$p, display_name:"The Owner"}')" | jq -c '.data | {address}'
curl -fsS "$API/auth/login" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg p "$MAILBOX_PW" '{email:"owner@e2e.test", password:$p, client:"native"}')" | jq -c '.data.user'
ok "mailbox owner@e2e.test signs in, as the phone app would"

step "restmail setup on a claimed server"
set +e; "$RESTMAIL" setup --docker e2e-api --server "$SERVER" --json 2>/tmp/e2e-setup-again.err; status=$?; set -e
cat /tmp/e2e-setup-again.err
[[ $status == 3 ]] || die "exit $status, want 3"
ok "refused with exit 3 and a pointer to reset-admin"

step "hand-over: restmail reset-admin, the link the owner redeems"
reset=$("$RESTMAIL" reset-admin --docker e2e-api --server "$SERVER" --username agent --json --show-secret)
reset_token=$(python3 -c 'import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.argv[1]).query)["token"][0])' "$(jq -r .link <<<"$reset")")
OWNER_PW=$(openssl rand -base64 24)
owner=$(curl -fsS "$API/auth/reset" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg t "$reset_token" --arg p "$OWNER_PW" '{token:$t, password:$p, client:"native"}')")
jq -c '.data | {setup_required}' <<<"$owner"
ok "owner redeemed it: new password, must enroll their own authenticator"

step "the agent is locked out"
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/auth/login" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg p "$AGENT_PW" --arg c "$(oathtool --totp -b "$secret")" '{username:"agent", password:$p, totp_code:$c, client:"native"}')")
[[ $code == 401 ]] || die "agent's password answered $code"
ok "agent's password + its authenticator code: $code"
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/auth/refresh" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg r "$agent_refresh" '{refresh_token:$r}')")
[[ $code == 401 ]] || die "agent's session answered $code"
ok "agent's session: $code"

step "the agent's still-unexpired access token, used against the reset account"
# The reset wiped the authenticator. If an old access token still worked, the
# agent could enroll its own before the owner does, and own the account again.
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/auth/2fa/enroll" -H "Authorization: Bearer $agent_access")
[[ $code == 401 ]] || die "agent's old access token enrolling an authenticator answered $code"
ok "enrolling its own authenticator: $code"
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/admin/stats" -H "Authorization: Bearer $agent_access")
[[ $code == 401 ]] || die "agent's old access token on the admin API answered $code"
ok "admin API: $code"

printf '\nAll steps passed.\n'
