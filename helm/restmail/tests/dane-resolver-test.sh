#!/usr/bin/env bash
# Container/config smoke test for GitHub's Helm job; no cluster or deployment.
set -euo pipefail
CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
chmod 755 "$tmp"
name="restmail-dane-test-${GITHUB_RUN_ID:-$$}-${GITHUB_RUN_ATTEMPT:-1}"
trap 'docker rm -f "$name" >/dev/null 2>&1 || true' EXIT
helm template restmail "$CHART_DIR" --set mailserver.hostname=mx.example.com \
  --set mailserver.domain=example.com --set api.bootstrapAdminPassword=dane-test-only \
  --set smtp.dane.enabled=true > "$tmp/render.yaml"
yq -r 'select(.kind == "ConfigMap" and .metadata.name == "restmail-smtp-dane") | .data."unbound.conf"' \
  "$tmp/render.yaml" > "$tmp/unbound.conf"
chmod 644 "$tmp/unbound.conf"
image=$(yq -r 'select(.metadata.name == "restmail-smtp-gateway") | .spec.template.spec.containers[] | select(.name == "dane-resolver") | .image' "$tmp/render.yaml")
# The default is an immutable multiarch index. Run with the same uid/capability
# policy as the chart so trust-anchor permissions and paths are exercised too.
docker run --rm --user 101:102 --cap-drop ALL --security-opt no-new-privileges \
  -v "$tmp:/etc/restmail-dane:ro" --entrypoint /usr/sbin/unbound-checkconf \
  "$image" /etc/restmail-dane/unbound.conf
docker run --detach --name "$name" --user 101:102 --cap-drop ALL \
  --security-opt no-new-privileges -v "$tmp:/etc/restmail-dane:ro" \
  --entrypoint /usr/sbin/unbound "$image" -d -c /etc/restmail-dane/unbound.conf
ready=false
for _ in {1..20}; do
  if timeout 3 docker exec "$name" /usr/bin/drill-hc @127.0.0.1 -p 5353 health.dane.invalid. A; then
    ready=true
    break
  fi
  sleep 1
done
if [ "$ready" != true ]; then
  docker logs "$name"
  exit 1
fi
# Exercise actual validation, not merely a resolver that sets the AD bit for
# everything. A signed root response is secure; a deliberately bogus zone is
# SERVFAIL. These public-DNS checks run in CI, never in the local focused suite.
timeout 40 docker exec "$name" /usr/bin/drill -D @127.0.0.1 -p 5353 . DNSKEY > "$tmp/secure.txt"
grep -Eq 'flags:.* ad([ ;]|$)' "$tmp/secure.txt"
timeout 40 docker exec "$name" /usr/bin/drill -D @127.0.0.1 -p 5353 dnssec-failed.org. A > "$tmp/bogus.txt" || true
grep -q 'rcode: SERVFAIL' "$tmp/bogus.txt"
echo "ok: loopback resolver starts unprivileged, validates signed DNS and refuses bogus DNS"
