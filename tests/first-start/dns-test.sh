#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

export _RESTMAIL_LIB=1 EXIT_FAILED=1
export DNS_TEST_RECORDS="$tmp/records.json"
cat > "$DNS_TEST_RECORDS" <<'JSON'
{"domains":[{"domain":"example.com","problem":"HTTPS route and certificate are unverified","records":[{"type":"CNAME","name":"mta-sts.example.com","value":"mail.example.com"},{"type":"TXT","name":"_mta-sts.example.com","value":"v=STSv1; id=abc"}]},{"domain":"no-policy.example","records":[]}]}
JSON

need() { :; }
need_release() { :; }
release_values() { printf '%s\n' '{"mailserver":{"hostname":"mail.example.com"}}'; }
component() { printf '%s' "$1"; }
k() { printf '%s\n' '{"items":[]}'; }
operator() { cat "$DNS_TEST_RECORDS"; }
operator_error() { printf '%s' "$1"; }
say() { printf '%s\n' "$@"; }
die() { printf '%s\n' "$*" >&2; exit 1; }
dig() {
  case "$*" in
    *CNAME*) printf '%s\n' "${DNS_TEST_CNAME-mail.example.com.}" ;;
    *TXT*) printf '%s\n' '"v=STSv1; id=abc"' ;;
    *) return 1 ;;
  esac
}
export -f need need_release release_values component k operator operator_error say die dig

bash "$ROOT/tasks/lifecycle/dns.sh" show > "$tmp/show"
grep -Fq 'mta-sts.example.com' "$tmp/show"
grep -Fq 'HTTPS route and certificate are unverified' "$tmp/show"
bash "$ROOT/tasks/lifecycle/dns.sh" check > "$tmp/check"
grep -Eq 'ok +CNAME mta-sts.example.com' "$tmp/check"
for got in wrong.example.com. ''; do
  if DNS_TEST_CNAME="$got" bash "$ROOT/tasks/lifecycle/dns.sh" check > "$tmp/check"; then
    echo 'FAIL: missing or mismatched CNAME passed dns:check' >&2
    exit 1
  fi
  grep -Eq 'NO +CNAME mta-sts.example.com' "$tmp/check"
done

bash "$ROOT/tasks/lifecycle/dns.sh" mta-sts-hosts > "$tmp/hosts.json"
jq -e '. == {api: {mtaSts: {hosts: ["mta-sts.example.com"]}}}' "$tmp/hosts.json" >/dev/null
printf '%s\n' '{"domains":[]}' > "$DNS_TEST_RECORDS"
bash "$ROOT/tasks/lifecycle/dns.sh" mta-sts-hosts > "$tmp/hosts.json"
jq -e '. == {api: {mtaSts: {hosts: []}}}' "$tmp/hosts.json" >/dev/null
echo 'DNS show, CNAME checks, and MTA-STS Helm values export passed.'
