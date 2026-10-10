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

echo "== development assertions =="
assert_contains "dev uses cleartext DB link"             "$tmp/dev.yaml" 'DB_SSLMODE'
assert_absent   "dev has no NetworkPolicy"               "$tmp/dev.yaml" 'kind: NetworkPolicy'
assert_absent   "dev Postgres has no TLS"                "$tmp/dev.yaml" 'ssl=on'

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

echo "== coherence guard: secure sslMode requires server TLS =="
if helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
     --set db.sslMode=require --set postgres.tls.enabled=false > /dev/null 2>&1; then
  fail "expected render to fail when db.sslMode=require but postgres.tls.enabled=false"
fi
pass "guard rejects secure sslMode against a non-TLS bundled Postgres"

echo
echo "All chart render tests passed."
