#!/usr/bin/env bash
# First start, end to end: an agent installs a fresh rest-mail server, signs in
# with the admin password the installer chose, builds it, and hands it over --
# and the owner's redemption must lock the agent out, unexpired access token
# included.
#
# The first admin comes from the bootstrap password (RESTMAIL_BOOTSTRAP_ADMIN_
# PASSWORD); signing in with it forces a password of the agent's own and an
# authenticator. The CLI reaches the server only through its public API, and
# proves ownership for reset-admin with a key the deployment lists in
# RESTMAIL_OWNER_KEYS, and reads what runs with one listed in
# RESTMAIL_STATUS_KEYS: Docker is just how this test runs a server, not
# something the CLI knows about.
#
# Run locally (Docker, curl, jq, oathtool, ssh-keygen):
#   docker build --target prod -t restmail-api:first-start .
#   go build -o /tmp/restmail ./cmd/console
#   RESTMAIL=/tmp/restmail IMAGE=restmail-api:first-start tests/first-start/agent-e2e.sh
#
# CI runs it on every pull request (ci.yml, job first-start).
set -euo pipefail

RESTMAIL=${RESTMAIL:-/tmp/restmail}
IMAGE=${IMAGE:-restmail-api:first-start}
API=http://127.0.0.1:58080/api/v1
SERVER=http://127.0.0.1:58080
NET=restmail-e2e
step() { printf '\n== %s\n' "$*"; }
ok() { printf '   ok: %s\n' "$*"; }
die() { printf '   FAIL: %s\n' "$*" >&2; exit 1; }

cleanup() { docker rm -f e2e-api e2e-pg >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; rm -rf "${KEYS:-}" "${LOG:-}"; }
trap cleanup EXIT
cleanup
KEYS=$(mktemp -d)
LOG=$(mktemp)

step "the agent's key, a deployer's, and someone else's"
ssh-keygen -q -t ed25519 -N '' -C agent@e2e -f "$KEYS/agent"
ssh-keygen -q -t ed25519 -N '' -C deployer@e2e -f "$KEYS/deployer"
ssh-keygen -q -t ed25519 -N '' -C stranger@e2e -f "$KEYS/stranger"
ok "the deployment will list the agent's as an owner key, and the deployer's as a status key"

docker network create "$NET" >/dev/null
docker run -d --name e2e-pg --network "$NET" -e POSTGRES_USER=restmail -e POSTGRES_PASSWORD=restmail \
  -e POSTGRES_DB=restmail postgres:16-alpine >/dev/null
until docker exec e2e-pg pg_isready -U restmail >/dev/null 2>&1; do sleep 1; done

# api [extra docker run flags...] starts the API on a fresh container.
api() {
  docker rm -f e2e-api >/dev/null 2>&1 || true
  docker run -d --name e2e-api --network "$NET" -p 127.0.0.1:58080:8080 \
    -e DB_HOST=e2e-pg -e DB_NAME=restmail -e DB_USER=restmail -e DB_PASS=restmail -e DB_SSLMODE=disable \
    -e JWT_SECRET="$JWT_SECRET" -e MASTER_KEY="$MASTER_KEY" \
    -e RESTMAIL_OWNER_KEYS="$(cat "$KEYS/agent.pub")" \
    -e RESTMAIL_STATUS_KEYS="$(cat "$KEYS/deployer.pub")" \
    -e ENVIRONMENT=development "$@" "$IMAGE" >/dev/null
}
JWT_SECRET=$(openssl rand -hex 32)
MASTER_KEY=$(openssl rand -hex 32)
wait_healthy() { for _ in $(seq 60); do curl -fsS http://127.0.0.1:58080/api/health >/dev/null 2>&1 && return; sleep 1; done; die "API not healthy"; }
# The server's own output, in a file. Not a pipe: `grep -q` stops reading at
# the first match, `docker logs` is then killed by SIGPIPE, and `pipefail`
# reports that as a failed check — a flake that depends on how much the server
# had left to write.
server_output() { docker logs e2e-api >"$LOG" 2>&1; }
# login USERNAME PASSWORD [TOTP] -> the response body; fails on anything but 200.
login() {
  curl -fsS "$API/auth/login" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg u "$1" --arg p "$2" --arg c "${3:-}" \
      '{username:$u, password:$p, client:"native"} + (if $c == "" then {} else {totp_code:$c} end)')"
}
login_status() {
  curl -s -o /dev/null -w '%{http_code}' "$API/auth/login" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg u "$1" --arg p "$2" --arg c "${3:-}" \
      '{username:$u, password:$p, client:"native"} + (if $c == "" then {} else {totp_code:$c} end)')"
}

step "no admin and no bootstrap password: nobody could sign in, and it says so"
api
wait_healthy
server_output
grep -q 'RESTMAIL_BOOTSTRAP_ADMIN_PASSWORD' "$LOG" || die "the server did not say it has no way in"
if grep -q -i -E 'setup code|claim this server|▀' "$LOG"; then die "the server printed a setup code"; fi
ok "development says so and starts (it seeds admins into the running API); production refuses to start"
ok "no setup code, no QR code, no link in its output"

step "a fresh install: the bootstrap password the installer chose"
BOOT_PW=$(openssl rand -base64 24)
api -e RESTMAIL_BOOTSTRAP_ADMIN_PASSWORD="$BOOT_PW"
wait_healthy
health=$(curl -fsS http://127.0.0.1:58080/api/health | jq -c .data)
echo "$health"
[[ $(jq -c 'keys' <<<"$health") == '["db","status"]' ]] || die "/api/health says more than it did: $health"
ok "server up, with its first admin, admin; /api/health says only that"

step "restmail status: what the server says about itself"
status=$("$RESTMAIL" status --server "$SERVER" --json)
jq -c 'del(.server)' <<<"$status"
[[ $(jq -r .product <<<"$status") == rest-mail ]] || die "product"
[[ $(jq -r 'has("claimed")' <<<"$status") == false ]] || die "status still talks of claiming"
# The image was built with a version stamped in (ci.yml); one that reports
# "dev" means the stamp is lost on the way into the binary.
if [ -n "${EXPECT_VERSION:-}" ]; then
  [[ $(jq -r .version <<<"$status") == "$EXPECT_VERSION" ]] || die "version $(jq -r .version <<<"$status"), want $EXPECT_VERSION"
fi
ok "rest-mail ${EXPECT_VERSION:-(version not checked)}"

step "restmail status --key: a status key reads what runs"
signed=$("$RESTMAIL" status --server "$SERVER" --key "$KEYS/deployer" --json)
jq -c 'del(.server)' <<<"$signed"
for field in version commit built_at started_at; do
  [[ -n $(jq -r ".$field // empty" <<<"$signed") ]] || die "no $field"
done
if [ -n "${EXPECT_VERSION:-}" ]; then
  [[ $(jq -r .version <<<"$signed") == "$EXPECT_VERSION" ]] || die "signed version $(jq -r .version <<<"$signed"), want $EXPECT_VERSION"
fi
ok "version, commit, built_at, started_at"
"$RESTMAIL" status --server "$SERVER" --key "$KEYS/agent" --json >/dev/null || die "an owner key could not read the status"
ok "an owner key reads it too"
set +e; "$RESTMAIL" status --server "$SERVER" --key "$KEYS/stranger" --json 2>/tmp/e2e-status.err; status=$?; set -e
cat /tmp/e2e-status.err
[[ $status == 6 ]] || die "a key on neither list: exit $status, want 6"
[[ $(jq -r .error.code /tmp/e2e-status.err) == not_status_key ]] || die "a key on neither list: not refused as not_status_key"
ok "a key on neither list is refused (exit 6)"

step "nothing claims a server any more"
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/auth/claim" -H 'Content-Type: application/json' \
  -d '{"token":"anything","username":"thief","password":"a long enough password"}')
[[ $code == 404 || $code == 405 ]] || die "/auth/claim answered $code"
ok "/auth/claim: $code"
set +e; "$RESTMAIL" setup --server "$SERVER" --key "$KEYS/agent" --json 2>/dev/null; status=$?; set -e
[[ $status == 2 ]] || die "restmail setup: exit $status, want 2 (no such command)"
ok "restmail setup: no such command"

step "sign in with the bootstrap password"
boot=$(login admin "$BOOT_PW")
jq -c '.data | {setup_required, capabilities}' <<<"$boot"
[[ $(jq -c .data.setup_required <<<"$boot") == '["password","two_factor"]' ]] || die "setup_required"
access=$(jq -r .data.access_token <<<"$boot")
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/admin/stats" -H "Authorization: Bearer $access")
[[ $code == 403 ]] || die "admin API in setup answered $code"
ok "a session in setup: the admin API refuses it (403) until a password and 2FA"

step "choose the agent's own password"
AGENT_PW=$(openssl rand -base64 24)
curl -fsS "$API/auth/password" -H "Authorization: Bearer $access" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg c "$BOOT_PW" --arg n "$AGENT_PW" '{current_password:$c, new_password:$n}')" >/dev/null
[[ $(login_status admin "$BOOT_PW") == 401 ]] || die "the bootstrap password still works"
ok "chosen; the bootstrap password is dead (401)"

step "enroll an authenticator, as the app would"
access=$(login admin "$AGENT_PW" | jq -r .data.access_token)
secret=$(curl -fsS -X POST "$API/auth/2fa/enroll" -H "Authorization: Bearer $access" | jq -r .data.secret)
curl -fsS -X POST "$API/auth/2fa/confirm" -H "Authorization: Bearer $access" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg c "$(oathtool --totp -b "$secret")" '{code:$c}')"
ok "enrolled (the agent computes codes from the secret)"

step "sign in for real"
agent=$(login admin "$AGENT_PW" "$(oathtool --totp -b "$secret")")
jq -c '.data | {setup_required, capabilities}' <<<"$agent"
[[ $(jq -c '.data.setup_required // []' <<<"$agent") == '[]' ]] || die "still in setup"
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

step "restmail reset-admin with a key the server does not list"
set +e; "$RESTMAIL" reset-admin --server "$SERVER" --key "$KEYS/stranger" --username admin --json 2>/tmp/e2e-stranger.err; status=$?; set -e
cat /tmp/e2e-stranger.err
[[ $status == 6 ]] || die "exit $status, want 6"
ok "refused with exit 6 (not an owner)"

step "restmail reset-admin with a status key"
set +e; "$RESTMAIL" reset-admin --server "$SERVER" --key "$KEYS/deployer" --username admin --json 2>/tmp/e2e-deployer.err; status=$?; set -e
cat /tmp/e2e-deployer.err
[[ $status == 6 ]] || die "exit $status, want 6"
ok "refused with exit 6: a status key reads the status and nothing else"

step "hand-over: restmail reset-admin, the link the owner redeems"
reset=$("$RESTMAIL" reset-admin --server "$SERVER" --key "$KEYS/agent" --username admin --json --show-secret)
link=$(jq -r .link <<<"$reset")
[[ $link == rest-mail://reset\?* ]] || die "link $link"
reset_token=$(python3 -c 'import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.argv[1]).query)["token"][0])' "$link")
OWNER_PW=$(openssl rand -base64 24)
owner=$(curl -fsS "$API/auth/reset" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg t "$reset_token" --arg p "$OWNER_PW" '{token:$t, password:$p, client:"native"}')")
jq -c '.data | {setup_required}' <<<"$owner"
[[ $(jq -c .data.setup_required <<<"$owner") == '["two_factor"]' ]] || die "setup_required"
ok "owner redeemed it: new password, must enroll their own authenticator"

step "the agent is locked out"
code=$(login_status admin "$AGENT_PW" "$(oathtool --totp -b "$secret")")
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
