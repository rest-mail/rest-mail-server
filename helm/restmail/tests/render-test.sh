#!/usr/bin/env bash
# Chart render tests for the restmail Helm chart.
#
# Infra-level tests (no cluster required): they lint the chart, render it with
# both the production defaults and the development overlay, and assert that the
# deployability fixes and security hardening from issue #192 are present in the
# rendered manifests. Run locally or in CI:
#
#   helm/restmail/tests/render-test.sh
#
set -euo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DEV_VALUES="$CHART_DIR/values-dev.yaml"
PROD_ARGS=(--set mailserver.hostname=mx.example.com --set mailserver.domain=example.com)

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

assert_contains() {
  # assert_contains <label> <file> <pattern>
  grep -Fq -- "$3" "$2" || fail "$1 (expected to find: $3)"
  pass "$1"
}

assert_absent() {
  # assert_absent <label> <file> <pattern>
  if grep -Fq -- "$3" "$2"; then fail "$1 (unexpected: $3)"; fi
  pass "$1"
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "== helm lint =="
helm lint "$CHART_DIR" "${PROD_ARGS[@]}"
helm lint "$CHART_DIR" -f "$DEV_VALUES"

echo "== render production defaults =="
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" > "$tmp/prod.yaml"

echo "== render development overlay =="
helm template restmail "$CHART_DIR" -f "$DEV_VALUES" > "$tmp/dev.yaml"

echo "== production assertions (deployability + hardening) =="
# Deployability: the production boot validator's acknowledgement knobs are wired.
assert_contains "API_TLS_TERMINATED_BY_PROXY set"       "$tmp/prod.yaml" 'API_TLS_TERMINATED_BY_PROXY: "true"'
assert_contains "DB_SSLMODE=require in DSN env"          "$tmp/prod.yaml" 'DB_SSLMODE'
assert_contains "bundled Postgres serves TLS"            "$tmp/prod.yaml" 'ssl=on'
# Hardening: securityContexts, capabilities, seccomp, token automount, policy.
assert_contains "seccomp RuntimeDefault"                 "$tmp/prod.yaml" 'type: RuntimeDefault'
assert_contains "drop ALL capabilities"                  "$tmp/prod.yaml" '- ALL'
assert_contains "gateways add NET_BIND_SERVICE"          "$tmp/prod.yaml" '- NET_BIND_SERVICE'
assert_contains "runAsNonRoot present"                   "$tmp/prod.yaml" 'runAsNonRoot: true'
assert_contains "readOnlyRootFilesystem present"         "$tmp/prod.yaml" 'readOnlyRootFilesystem: true'
assert_contains "token automount disabled"              "$tmp/prod.yaml" 'automountServiceAccountToken: false'
assert_contains "default-deny NetworkPolicy"             "$tmp/prod.yaml" 'kind: NetworkPolicy'
assert_contains "NetworkPolicy default-deny name"        "$tmp/prod.yaml" 'restmail-default-deny'
# A fresh install can create its first admin, from an optional Secret key.
assert_contains "bootstrap admin env"                    "$tmp/prod.yaml" 'RESTMAIL_BOOTSTRAP_ADMIN_PASSWORD'
assert_contains "bootstrap key is optional"              "$tmp/prod.yaml" 'optional: true'

echo "== development assertions =="
assert_contains "dev uses cleartext DB link"             "$tmp/dev.yaml" 'DB_SSLMODE'
assert_absent   "dev has no NetworkPolicy"               "$tmp/dev.yaml" 'kind: NetworkPolicy'
assert_absent   "dev Postgres has no TLS"                "$tmp/dev.yaml" 'ssl=on'

echo "== the API's rollout can finish with a single-node volume =="
# A rolling update cannot hand a ReadWriteOnce volume from the old pod to a new
# one on another node; Recreate can. With shared (ReadWriteMany) storage, or
# none, the rolling update stays.
assert_contains "Recreate with a ReadWriteOnce attachments volume" "$tmp/prod.yaml" 'type: Recreate'
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --set api.attachments.persistence.accessMode=ReadWriteMany > "$tmp/rwx.yaml"
assert_absent "rolling update with ReadWriteMany" "$tmp/rwx.yaml" 'type: Recreate'
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --set api.attachments.persistence.enabled=false > "$tmp/nopvc.yaml"
assert_absent "rolling update with no volume" "$tmp/nopvc.yaml" 'type: Recreate'

echo "== API only: the gateways can be turned off =="
# Without this the chart could only be installed somewhere able to give three
# LoadBalancers an address. The REST API is a whole product on its own; the
# gateways are how traditional clients reach it.
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --set smtpGateway.enabled=false \
  --set imapGateway.enabled=false \
  --set pop3Gateway.enabled=false > "$tmp/api-only.yaml"

for gw in smtp imap pop3; do
  assert_absent "$gw gateway gone"        "$tmp/api-only.yaml" "restmail-$gw-gateway"
done
# A Service left behind would sit pending for an address nobody will give it.
assert_absent   "no LoadBalancer remains" "$tmp/api-only.yaml" 'type: LoadBalancer'
# What the API needs must survive: turning the gateways off is not a teardown.
assert_contains "API survives"            "$tmp/api-only.yaml" 'restmail-api'
assert_contains "Postgres survives"       "$tmp/api-only.yaml" 'restmail-postgres'
assert_contains "JS filter survives"      "$tmp/api-only.yaml" 'restmail-js-filter'
assert_contains "default-deny survives"   "$tmp/api-only.yaml" 'restmail-default-deny'

echo "== owner keys reach the API =="
KEY='ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOwnerKeyForTheRenderTestOnlyxxxxxxxxxxxxxx owner@laptop'
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set-string api.ownerKeys="$KEY" > "$tmp/owner.yaml"
assert_contains "RESTMAIL_OWNER_KEYS set"   "$tmp/owner.yaml" "RESTMAIL_OWNER_KEYS: \"$KEY\""
assert_absent   "no owner keys by default"  "$tmp/prod.yaml"  'RESTMAIL_OWNER_KEYS'

echo "== where setup links point =="
assert_absent   "unset by default"   "$tmp/prod.yaml" 'RESTMAIL_SETUP_LINK_BASE'
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set-string api.setupLinkBase=https://mail.example.com/claim > "$tmp/link.yaml"
assert_contains "another page"       "$tmp/link.yaml" 'RESTMAIL_SETUP_LINK_BASE: "https://mail.example.com/claim"'
# Empty is a choice, not an absence: it asks for restmail://, so it must reach
# the container rather than falling back to the default.
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set-string api.setupLinkBase="" > "$tmp/nolink.yaml"
assert_contains "no site at all"     "$tmp/nolink.yaml" 'RESTMAIL_SETUP_LINK_BASE: ""'

echo "== a bare install needs no secrets prepared =="
# Only a hostname and a domain: the chart must make both Secrets itself, so
# nothing has to exist before `helm install`.
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" > "$tmp/bare.yaml"
secret() { # secret <file> <name> -> that Secret's document
  awk -v n="  name: $2" 'BEGIN{RS="\n---\n"} /\nkind: Secret\n/ && index($0, n"\n") {print}' "$1"
}
secret "$tmp/bare.yaml" restmail-api      > "$tmp/api-secret.yaml"
secret "$tmp/bare.yaml" restmail-postgres > "$tmp/pg-secret.yaml"
assert_contains "the API's Secret is made"       "$tmp/api-secret.yaml" 'JWT_SECRET:'
assert_contains "with a MASTER_KEY"              "$tmp/api-secret.yaml" 'MASTER_KEY:'
assert_contains "Postgres's Secret is made"      "$tmp/pg-secret.yaml"  'POSTGRES_PASSWORD:'
assert_contains "for user restmail"              "$tmp/pg-secret.yaml"  'POSTGRES_USER: "restmail"'
# MASTER_KEY must survive an uninstall: a new one makes stored keys unreadable.
assert_contains "the API's Secret survives uninstall"  "$tmp/api-secret.yaml" 'helm.sh/resource-policy: keep'
assert_contains "so does Postgres's"                   "$tmp/pg-secret.yaml"  'helm.sh/resource-policy: keep'
# Generated, not a placeholder: 64 characters of a random alphabet.
key=$(sed -n 's/^  MASTER_KEY: "\(.*\)"$/\1/p' "$tmp/api-secret.yaml")
[ "${#key}" = 64 ] || fail "MASTER_KEY is ${#key} characters, want 64"
pass "a generated MASTER_KEY is 64 characters"
# No bootstrap admin unless asked for: one would turn the setup code off.
assert_absent "no bootstrap password by default" "$tmp/api-secret.yaml" 'BOOTSTRAP_ADMIN_PASSWORD'

echo "== what is given wins =="
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --set api.jwtSecret=given-jwt --set api.masterKey=given-master \
  --set api.bootstrapAdminPassword=a-bootstrap-password-of-length \
  --set postgres.auth.password=given-pg > "$tmp/given.yaml"
assert_contains "a given JWT_SECRET"          "$tmp/given.yaml" 'JWT_SECRET: "given-jwt"'
assert_contains "a given MASTER_KEY"          "$tmp/given.yaml" 'MASTER_KEY: "given-master"'
assert_contains "a given bootstrap password"  "$tmp/given.yaml" 'BOOTSTRAP_ADMIN_PASSWORD: "a-bootstrap-password-of-length"'
assert_contains "a given Postgres password"   "$tmp/given.yaml" 'POSTGRES_PASSWORD: "given-pg"'

echo "== a Secret you manage is used, and none is made =="
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --set api.existingSecret=mine-api --set postgres.existingSecret=mine-pg > "$tmp/existing.yaml"
assert_absent   "no API Secret made"      "$tmp/existing.yaml" 'MASTER_KEY:'
assert_absent   "no Postgres Secret made" "$tmp/existing.yaml" 'POSTGRES_PASSWORD:'
assert_contains "the API reads yours"     "$tmp/existing.yaml" 'name: mine-api'
assert_contains "and the database yours"  "$tmp/existing.yaml" 'name: mine-pg'

echo "== an external database's password cannot be made up =="
if helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set postgres.enabled=false > /dev/null 2>&1; then
  fail "expected an external database with no password to be refused"
fi
pass "an external database with no password is refused"

echo "== coherence guard: secure sslMode requires server TLS =="
if helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
     --set db.sslMode=require --set postgres.tls.enabled=false > /dev/null 2>&1; then
  fail "expected render to fail when db.sslMode=require but postgres.tls.enabled=false"
fi
pass "guard rejects secure sslMode against a non-TLS bundled Postgres"

echo
echo "All chart render tests passed."
