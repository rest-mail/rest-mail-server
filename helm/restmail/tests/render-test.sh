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
# Needs helm and yq 4.46.1 or newer (https://github.com/mikefarah/yq, preinstalled
# on GitHub's runners), which reads the rendered manifests as YAML where grep
# cannot. Older releases fail a correct chart: 4.45.4 adds to an empty stream, and
# 4.44.3 gives no rows at all, so both fail with messages that blame the chart.
#
# shellcheck disable=SC2016 # the $names in single quotes are yq's, not the shell's
set -euo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DEV_VALUES="$CHART_DIR/values-dev.yaml"
# The chart refuses to render without an admin password, so every render here
# supplies one. The refusal itself is a test, below.
TEST_PASSWORD=a-test-bootstrap-password
PROD_ARGS=(--set mailserver.hostname=mx.example.com --set mailserver.domain=example.com
           --set "api.bootstrapAdminPassword=$TEST_PASSWORD")

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

yq_found="$(yq --version 2>&1 || true)"
yq_minor=0
case "$yq_found" in
  *mikefarah*" v4."*) yq_minor="${yq_found##* v4.}"; yq_minor="${yq_minor%%.*}" ;;
esac
[ "$yq_minor" -ge 46 ] || fail "needs yq 4.46.1 or newer from https://github.com/mikefarah/yq (found: $yq_found)"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "== helm lint =="
helm lint "$CHART_DIR" "${PROD_ARGS[@]}"
helm lint "$CHART_DIR" -f "$DEV_VALUES" --set "api.bootstrapAdminPassword=$TEST_PASSWORD"

echo "== render production defaults =="
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" > "$tmp/prod.yaml"

assert_absent "PrometheusRule disabled by default" "$tmp/prod.yaml" 'kind: PrometheusRule'
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set metrics.prometheusRule.enabled=false > "$tmp/metrics-off.yaml"
assert_absent "PrometheusRule explicitly disabled" "$tmp/metrics-off.yaml" 'kind: PrometheusRule'
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set metrics.prometheusRule.enabled=true --set metrics.prometheusRule.labels.release=monitoring > "$tmp/metrics-on.yaml"
assert_contains "PrometheusRule enabled" "$tmp/metrics-on.yaml" 'kind: PrometheusRule'
assert_contains "PrometheusRule selector label" "$tmp/metrics-on.yaml" 'release: monitoring'
assert_contains "DKIM overdue alert" "$tmp/metrics-on.yaml" 'alert: DKIMKeyOverdue'
assert_contains "DKIM publication alert" "$tmp/metrics-on.yaml" 'alert: DKIMPublicationStuck'
assert_contains "PVC disk alert" "$tmp/metrics-on.yaml" 'alert: PVCSpaceCritical'
assert_contains "queue alert" "$tmp/metrics-on.yaml" 'alert: QueueOldestMessageOld'
assert_absent "compose-only disk alert" "$tmp/metrics-on.yaml" 'alert: DiskSpaceCritical'
python3 -B "$CHART_DIR/tests/alerting-rules-test.py" "$tmp/metrics-on.yaml"
if command -v promtool >/dev/null 2>&1; then
  yq -N e 'select(.kind == "PrometheusRule") | {"groups": .spec.groups}' "$tmp/metrics-on.yaml" > "$tmp/dkim-alerts-rules.yml"
  cp "$CHART_DIR/tests/dkim-alerts-test.yml" "$tmp/dkim-alerts-test.yml"
  promtool check rules "$tmp/dkim-alerts-rules.yml"
  promtool test rules "$tmp/dkim-alerts-test.yml"
fi

echo "== render development overlay =="
helm template restmail "$CHART_DIR" -f "$DEV_VALUES" --set "api.bootstrapAdminPassword=$TEST_PASSWORD" > "$tmp/dev.yaml"

echo "== production assertions (deployability + hardening) =="
# Deployability: the production boot validator's acknowledgement knobs are wired.
assert_contains "API_TLS_TERMINATED_BY_PROXY set"       "$tmp/prod.yaml" 'API_TLS_TERMINATED_BY_PROXY: "true"'
assert_contains "DB_SSLMODE=require in DSN env"          "$tmp/prod.yaml" 'DB_SSLMODE'
assert_contains "bundled Postgres serves TLS"            "$tmp/prod.yaml" 'ssl=on'
# Hardening: securityContexts, capabilities, seccomp, token automount, policy.
assert_contains "seccomp RuntimeDefault"                 "$tmp/prod.yaml" 'type: RuntimeDefault'
assert_contains "drop ALL capabilities"                  "$tmp/prod.yaml" '- ALL'
assert_absent   "no capability added back"               "$tmp/prod.yaml" '- NET_BIND_SERVICE'
assert_contains "runAsNonRoot present"                   "$tmp/prod.yaml" 'runAsNonRoot: true'
assert_contains "readOnlyRootFilesystem present"         "$tmp/prod.yaml" 'readOnlyRootFilesystem: true'
assert_contains "token automount disabled"              "$tmp/prod.yaml" 'automountServiceAccountToken: false'
assert_contains "default-deny NetworkPolicy"             "$tmp/prod.yaml" 'kind: NetworkPolicy'
assert_contains "NetworkPolicy default-deny name"        "$tmp/prod.yaml" 'restmail-default-deny'
# A fresh install can create its first admin, from an optional Secret key.
assert_contains "bootstrap admin env"                    "$tmp/prod.yaml" 'RESTMAIL_BOOTSTRAP_ADMIN_PASSWORD'
assert_contains "bootstrap account is admin@ the domain" "$tmp/prod.yaml" 'value: "admin@example.com"'
assert_absent   "no bootstrap username"                  "$tmp/prod.yaml" 'RESTMAIL_BOOTSTRAP_ADMIN_USERNAME'
assert_contains "bootstrap key is optional"              "$tmp/prod.yaml" 'optional: true'

echo "== development assertions =="
assert_contains "dev uses cleartext DB link"             "$tmp/dev.yaml" 'DB_SSLMODE'
assert_absent   "dev has no NetworkPolicy"               "$tmp/dev.yaml" 'kind: NetworkPolicy'
assert_absent   "dev Postgres has no TLS"                "$tmp/dev.yaml" 'ssl=on'
assert_contains "bundled Postgres preloads pg_stat_statements" "$tmp/prod.yaml" 'shared_preload_libraries=pg_stat_statements'
assert_contains "dev Postgres preloads pg_stat_statements" "$tmp/dev.yaml" 'shared_preload_libraries=pg_stat_statements'

echo "== no workload mounts a CA private key (#193) =="
# Whoever reads a CA's private key can sign a certificate its holders trust: a
# client certificate the API's internal mTLS listener accepts as a gateway, or a
# server certificate for the mail hosts. So no pod mounts one. Each mounts its own
# certificate and key and the CA certificates it verifies peers with, named key by
# key: a Secret given to the chart may hold more (cert-manager keeps a CA issuer's
# key in a Secret, for one), and a whole-Secret mount would hand that over too.
#
# The chart's workloads are Deployments, DaemonSets and StatefulSets; the guard fails if
# another kind appears, so it cannot slip past these checks.
workloads='select(.kind == "Deployment" or .kind == "DaemonSet" or .kind == "StatefulSet")'
# garage.toml is Garage's own config, holding its own secrets and no certificate key.
MOUNTABLE=" tls.crt tls.key ca.crt server.crt server.key client-imap.crt client-imap.key client-pop3.crt client-pop3.key client-smtp-delivery.crt client-smtp-delivery.key client-smtp-submission.crt client-smtp-submission.key AuthKey.p8 garage.toml "
# One line per Secret volume: <workload> <volume> <key,key,...>.
secret_volumes() {
  yq -N e "$workloads"' | .metadata.name as $w | .spec.template.spec.volumes[] | select(has("secret"))
    | $w + " " + .name + " " + ((.secret.items // []) | map(.key) | join(","))' "$1"
}
check_no_ca_key() { # check_no_ca_key <label> <render>
  local other volumes w vol keys k
  other=$(yq -N e 'select(.kind == "Pod" or .kind == "ReplicaSet" or .kind == "Job" or .kind == "CronJob") | .kind' "$2")
  [ -z "$other" ] || fail "$1: a workload this test does not look into: $other"
  other=$(yq -N e "$workloads"' | .metadata.name as $w | .spec.template.spec.volumes[]
    | select(has("hostPath") or has("projected")) | $w + " " + .name' "$2")
  [ -z "$other" ] || fail "$1: a volume this test does not look into: $other"
  other=$(yq -N e "$workloads"' | select([(.spec.template.spec.containers[], .spec.template.spec.initContainers[])
    | .envFrom[] | select(has("secretRef"))] | length > 0) | .metadata.name' "$2")
  [ -z "$other" ] || fail "$1: $other takes a whole Secret into its environment"
  volumes=$(secret_volumes "$2")
  [ -n "$volumes" ] || fail "$1: no Secret volumes found, so nothing was checked"
  while read -r w vol keys; do
    [ -n "$keys" ] || fail "$1: $w mounts all of Secret volume $vol; name the keys it needs"
    for k in ${keys//,/ }; do
      case "$MOUNTABLE" in *" $k "*) ;; *) fail "$1: $w mounts $k from volume $vol" ;; esac
    done
  done <<< "$volumes"
  pass "$1: every pod mounts certificates and its own keys, key by key"
  # And no Secret the chart makes holds a CA's key: the internal CA's lives only
  # while the chart renders.
  other=$(yq -N e 'select(.kind == "Secret") | .metadata.name as $s | ((.data // {}) * (.stringData // {})) | keys | .[]
    | select(test("(^|[-_.])ca([-_.].*)?\\.key$")) | $s + " " + .' "$2")
  [ -z "$other" ] || fail "$1: a Secret holds a CA key: $other"
  pass "$1: no Secret the chart makes holds a CA key"
}
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set internalMtls.existingSecret=mine-mtls > "$tmp/byo-mtls.yaml"
check_no_ca_key "production"             "$tmp/prod.yaml"
check_no_ca_key "development"            "$tmp/dev.yaml"
check_no_ca_key "an internal CA of yours" "$tmp/byo-mtls.yaml"
# Who gets which keypair: a gateway its own service's client keypair (#1586) and
# the mail host's, the API its server keypair and no mail key at all.
secret_volumes "$tmp/prod.yaml" > "$tmp/volumes.txt"
for gw in imap:imap pop3:pop3; do
  service=${gw#*:}; gw=${gw%%:*}
  assert_contains "$gw gateway: the mail host's keypair"   "$tmp/volumes.txt" "restmail-$gw-gateway tls tls.crt,tls.key"
  assert_contains "$gw gateway: its own client keypair"    "$tmp/volumes.txt" "restmail-$gw-gateway internal-mtls ca.crt,client-$service.crt,client-$service.key"
done
# The SMTP gateway is two services: inbound delivery and submission, each with its own keypair.
assert_contains "smtp gateway: the mail host's keypair"   "$tmp/volumes.txt" "restmail-smtp-gateway tls tls.crt,tls.key"
assert_contains "smtp gateway: its delivery and submission keypairs" "$tmp/volumes.txt" \
  "restmail-smtp-gateway internal-mtls ca.crt,client-smtp-delivery.crt,client-smtp-delivery.key,client-smtp-submission.crt,client-smtp-submission.key"
# One client certificate per gateway service, and no shared one.
mtls_keys=$(yq -N e 'select(.kind == "Secret" and .metadata.name == "restmail-internal-mtls") | .data | keys | .[]' "$tmp/prod.yaml" | sort | tr '\n' ' ')
[ "$mtls_keys" = "ca.crt client-imap.crt client-imap.key client-pop3.crt client-pop3.key client-smtp-delivery.crt client-smtp-delivery.key client-smtp-submission.crt client-smtp-submission.key server.crt server.key " ] ||
  fail "internal mTLS Secret keys: $mtls_keys"
pass "the internal mTLS Secret holds one client keypair per gateway service"
assert_contains "API: the server keypair"                  "$tmp/volumes.txt" "restmail-api internal-mtls ca.crt,server.crt,server.key"
assert_contains "API: the mail CA's certificate, no key"   "$tmp/volumes.txt" "restmail-api tls ca.crt"
grep -q '^restmail-internal-mtls$' <(yq -N e 'select(.kind == "Secret") | .metadata.name' "$tmp/prod.yaml") ||
  fail "the chart made no internal mTLS Secret, so its keys went unchecked"
pass "the internal mTLS Secret was checked"
assert_absent   "a Secret of yours is not made"            "$tmp/byo-mtls.yaml" 'name: restmail-internal-mtls'

echo "== everything runs non-root, and the gateways need no capability (#193) =="
check_non_root() { # check_non_root <label> <render>
  local pods containers w user c ape drop add ro nonroot cuser ports p
  # Fields are |-separated, because a field that is not set comes out empty.
  pods=$(yq -N e "$workloads"' | .metadata.name + "|" + (.spec.template.spec.securityContext.runAsNonRoot | tostring)
    + "|" + (.spec.template.spec.securityContext.runAsUser | tostring)' "$2")
  while IFS='|' read -r w nonroot user; do
    [ "$nonroot" = true ] || fail "$1: $w does not set runAsNonRoot"
    case "$user" in ""|0) fail "$1: $w runs as uid ${user:-of its image}" ;; esac
  done <<< "$pods"
  containers=$(yq -N e "$workloads"' | .metadata.name as $w
    | (.spec.template.spec.containers[], .spec.template.spec.initContainers[]) | .securityContext as $s
    | $w + "|" + .name + "|" + ($s.allowPrivilegeEscalation | tostring)
    + "|" + (($s.capabilities.drop // []) | join(",")) + "|[" + (($s.capabilities.add // []) | join(",")) + "]"
    + "|" + ($s.readOnlyRootFilesystem | tostring) + "|" + ($s.runAsNonRoot | tostring) + "|" + ($s.runAsUser | tostring)
    + "|" + ((.ports // []) | map(.containerPort | tostring) | join(","))' "$2")
  while IFS='|' read -r w c ape drop add ro nonroot cuser ports; do
    [ "$ape" = false ]   || fail "$1: $w/$c may escalate privileges"
    [ "$drop" = ALL ]    || fail "$1: $w/$c does not drop every capability (drop: $drop)"
    [ "$add" = "[]" ]    || fail "$1: $w/$c adds capabilities back: $add"
    if [ "$nonroot" = false ] || [ "$cuser" = 0 ]; then fail "$1: $w/$c overrides the pod and runs as root"; fi
    case "$w" in
      *-gateway)
        [ "$ro" = true ] || fail "$1: $w/$c has a writable root filesystem"
        for p in ${ports//,/ }; do
          [ "$p" -ge 1024 ] || fail "$1: $w/$c listens on $p, which needs a capability"
        done ;;
    esac
  done <<< "$containers"
  pass "$1: every pod runs non-root, and no container adds a capability"
  pass "$1: the gateways listen on unprivileged ports on a read-only root filesystem"
}
check_non_root "production"  "$tmp/prod.yaml"
check_non_root "development" "$tmp/dev.yaml"
# The mail ports are still the public ones: each Service maps them onto the
# port the gateway listens on, and the NetworkPolicy opens that port.
gateway_ports() { # gateway_ports <render> <kind> <name> <expression>
  yq -N e "select(.kind == \"$2\" and .metadata.name == \"$3\") | $4" "$1" | paste -sd, -
}
for gw in smtp:25,465:10025,10465 imap:993:10993 pop3:995:10995; do
  IFS=: read -r name public listening <<< "$gw"
  svc=$(gateway_ports "$tmp/prod.yaml" Service "restmail-$name-gateway" '.spec.ports[].port')
  [ "$svc" = "$public" ] || fail "$name gateway's Service exposes $svc, want $public"
  pod=$(gateway_ports "$tmp/prod.yaml" Deployment "restmail-$name-gateway" '.spec.template.spec.containers[0].ports[].containerPort')
  [ "$pod" = "$listening" ] || fail "$name gateway listens on $pod, want $listening"
  targets=$(gateway_ports "$tmp/prod.yaml" Service "restmail-$name-gateway" '.spec.ports[].targetPort')
  names=$(gateway_ports "$tmp/prod.yaml" Deployment "restmail-$name-gateway" '.spec.template.spec.containers[0].ports[].name')
  [ "$targets" = "$names" ] || fail "$name gateway's Service targets $targets, but the pod's ports are $names"
  policy=$(gateway_ports "$tmp/prod.yaml" NetworkPolicy "restmail-$name-gateway" '.spec.ingress[].ports[].port')
  [ "$policy" = "$listening" ] || fail "$name gateway's NetworkPolicy opens $policy, want $listening"
  pass "$name gateway: public $public onto $listening, and the policy opens $listening"
done

echo "== gateway Deployment defaults and opt-in DaemonSets (#372) =="
DAEMONSET_ARGS=(--set smtpGateway.workloadKind=DaemonSet --set imapGateway.workloadKind=DaemonSet
                --set pop3Gateway.workloadKind=DaemonSet)
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" "${DAEMONSET_ARGS[@]}" \
  --set smtpGateway.replicaCount=7 --set imapGateway.replicaCount=7 \
  --set pop3Gateway.replicaCount=7 > "$tmp/daemonsets.yaml"

# Compare structured objects: the controller kind must not change pod security,
# probes, ports, credentials, metadata or any Service field.
gateway_object() { # gateway_object <render> <kind> <name> <expression>
  yq -N -o=json -I=0 e "select(.kind == \"$2\" and .metadata.name == \"$3\") | $4 | sort_keys(..)" "$1"
}
for gw in smtp imap pop3; do
  name="restmail-$gw-gateway"
  gateway_object "$tmp/prod.yaml" Deployment "$name" '.' > "$tmp/default-gateway.json"
  gateway_object "$tmp/daemonsets.yaml" DaemonSet "$name" '.' > "$tmp/daemonset-gateway.json"
  [ -s "$tmp/default-gateway.json" ] || fail "$gw: default Deployment missing"
  [ -s "$tmp/daemonset-gateway.json" ] || fail "$gw: opt-in DaemonSet missing"
  [ "$(yq -r e '.spec.replicas' "$tmp/default-gateway.json")" = 1 ] || fail "$gw: default replicas changed"
  yq -e '.spec | (has("replicas") or has("strategy") or has("progressDeadlineSeconds")) == false' \
    "$tmp/daemonset-gateway.json" > /dev/null || fail "$gw: DaemonSet has Deployment-only fields"
  for field in '.metadata' '.spec.selector' '.spec.template'; do
    yq -o=json -I=0 e "$field" "$tmp/default-gateway.json" > "$tmp/default-field.json"
    yq -o=json -I=0 e "$field" "$tmp/daemonset-gateway.json" > "$tmp/daemonset-field.json"
    diff -u "$tmp/default-field.json" "$tmp/daemonset-field.json" || fail "$gw: $field changed with workload kind"
  done
  for render in prod daemonsets; do
    gateway_object "$tmp/$render.yaml" Service "$name" '.' > "$tmp/$render-service.json"
    [ "$(yq -r e '.spec.externalTrafficPolicy' "$tmp/$render-service.json")" = Local ] ||
      fail "$gw: $render Service lost Local traffic policy"
    yq -o=json -I=0 e '.spec.selector' "$tmp/$render-service.json" > "$tmp/service-selector.json"
    yq -o=json -I=0 e '.spec.selector.matchLabels' "$tmp/default-gateway.json" > "$tmp/workload-selector.json"
    diff -u "$tmp/workload-selector.json" "$tmp/service-selector.json" || fail "$gw: Service selector mismatch"
    yq -o=json -I=0 e '.spec.template.metadata.labels' "$tmp/default-gateway.json" > "$tmp/pod-labels.json"
    diff -u "$tmp/workload-selector.json" "$tmp/pod-labels.json" || fail "$gw: pod labels mismatch"
  done
  diff -u "$tmp/prod-service.json" "$tmp/daemonsets-service.json" || fail "$gw: Service changed with workload kind"
  pass "$gw: Deployment defaults, DaemonSet without replicas/strategy, identical pod and Local Service"
done
check_no_ca_key "DaemonSets" "$tmp/daemonsets.yaml"
check_non_root "DaemonSets" "$tmp/daemonsets.yaml"

# Each gateway opts in independently; the others must remain Deployments.
for chosen in smtp imap pop3; do
  helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
    --set "${chosen}Gateway.workloadKind=DaemonSet" > "$tmp/mixed.yaml"
  for gw in smtp imap pop3; do
    kind=Deployment
    if [ "$gw" = "$chosen" ]; then kind=DaemonSet; fi
    object=$(gateway_object "$tmp/mixed.yaml" "$kind" "restmail-$gw-gateway" '.kind')
    [ "$object" = "\"$kind\"" ] || fail "$chosen opt-in: $gw is not $kind"
  done
  pass "$chosen: workload kind is independent"
done

cat > "$tmp/scheduling.yaml" <<'YAML'
nodeSelector:
  restmail-role: mail
tolerations:
  - key: dedicated
    operator: Equal
    value: mail
    effect: NoSchedule
affinity:
  nodeAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
        - matchExpressions:
            - key: kubernetes.io/os
              operator: In
              values: [linux]
YAML
for kind in Deployment DaemonSet; do
  helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" -f "$tmp/scheduling.yaml" \
    --set "smtpGateway.workloadKind=$kind" --set "imapGateway.workloadKind=$kind" \
    --set "pop3Gateway.workloadKind=$kind" > "$tmp/scheduled.yaml"
  for gw in smtp imap pop3; do
    for field in nodeSelector tolerations affinity; do
      yq -o=json -I=0 e ".$field | sort_keys(..)" "$tmp/scheduling.yaml" > "$tmp/expected-scheduling.json"
      gateway_object "$tmp/scheduled.yaml" "$kind" "restmail-$gw-gateway" \
        ".spec.template.spec.$field" > "$tmp/actual-scheduling.json"
      diff -u "$tmp/expected-scheduling.json" "$tmp/actual-scheduling.json" ||
        fail "$gw $kind: $field not retained"
    done
    pass "$gw $kind: nodeSelector, tolerations and affinity retained"
  done
done

for gw in smtp imap pop3; do
  for invalid in StatefulSet daemonset '' true 42; do
    refusal=$(helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
      --set "${gw}Gateway.workloadKind=$invalid" 2>&1) && fail "$gw: accepted workloadKind=$invalid"
    case "$refusal" in
      *"${gw}Gateway.workloadKind must be Deployment or DaemonSet"*) ;;
      *) fail "$gw: invalid kind refused without useful error: $refusal" ;;
    esac
  done
  pass "$gw: invalid kinds refused with the setting name and allowed kinds"
done

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

echo "== the gateways ask for no cleartext client port =="
# In production a gateway refuses to start if one of these is set (#279), so a
# chart that sets them deploys gateways that crash on boot.
for var in SMTP_PORT_SUBMISSION IMAP_PORT POP3_PORT; do
  if grep -Eq -- "- name: $var\$" "$tmp/prod.yaml"; then fail "$var is set"; fi
  pass "$var not set"
done
# Listened on or exposed. (587 stays open for egress: delivering to a remote
# smart host is not a client port of ours.)
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --show-only templates/smtp-gateway-service.yaml \
  --show-only templates/imap-gateway-service.yaml \
  --show-only templates/pop3-gateway-service.yaml > "$tmp/gateway-services.yaml"
if grep -Eq -- "containerPort: (587|143|110)\$" "$tmp/prod.yaml" ||
   grep -Eq -- "port: (587|143|110)\$" "$tmp/gateway-services.yaml"; then
  fail "a cleartext client port is listened on or exposed"
fi
pass "587, 143 and 110 neither listened on nor exposed"
# The probes knock on a port that is listening, or the pods never go ready.
assert_contains "IMAP probed on its TLS port" "$tmp/prod.yaml" 'port: imaps'
assert_contains "POP3 probed on its TLS port" "$tmp/prod.yaml" 'port: pop3s'

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
# Disabling a gateway also wins over a valid DaemonSet opt-in.
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" "${DAEMONSET_ARGS[@]}" \
  --set smtpGateway.enabled=false --set imapGateway.enabled=false \
  --set pop3Gateway.enabled=false > "$tmp/disabled-daemonsets.yaml"
for gw in smtp imap pop3; do
  assert_absent "$gw DaemonSet gateway gone" "$tmp/disabled-daemonsets.yaml" "restmail-$gw-gateway"
done
# A Service left behind would sit pending for an address nobody will give it.
assert_absent   "no LoadBalancer remains" "$tmp/api-only.yaml" 'type: LoadBalancer'
# What the API needs must survive: turning the gateways off is not a teardown.
assert_contains "API survives"            "$tmp/api-only.yaml" 'restmail-api'
assert_contains "Postgres survives"       "$tmp/api-only.yaml" 'restmail-postgres'
assert_contains "JS filter survives"      "$tmp/api-only.yaml" 'restmail-js-filter'
assert_contains "default-deny survives"   "$tmp/api-only.yaml" 'restmail-default-deny'

echo "== the web apps: each on a host of its own, with the API on it =="
# One object's document: doc <file> <kind> <name>.
doc() {
  awk -v k="kind: $2" -v n="  name: $3" 'BEGIN{RS="\n---\n"} index($0, "\n"k"\n") && index($0, "\n"n"\n") {print}' "$1"
}
app_version=$(sed -n 's/^appVersion: "\(.*\)"$/\1/p' "$CHART_DIR/Chart.yaml")
for app in webmail admin; do
  doc "$tmp/prod.yaml" Deployment    "restmail-$app" > "$tmp/$app-deploy.yaml"
  doc "$tmp/prod.yaml" Service       "restmail-$app" > "$tmp/$app-svc.yaml"
  doc "$tmp/prod.yaml" Ingress       "restmail-$app" > "$tmp/$app-ing.yaml"
  doc "$tmp/prod.yaml" NetworkPolicy "restmail-$app" > "$tmp/$app-np.yaml"
  assert_contains "$app is deployed by default"     "$tmp/$app-deploy.yaml" 'kind: Deployment'
  assert_contains "$app has a Service"              "$tmp/$app-svc.yaml"    'kind: Service'
  assert_contains "$app has an Ingress"             "$tmp/$app-ing.yaml"    'kind: Ingress'
  assert_contains "$app has a NetworkPolicy"        "$tmp/$app-np.yaml"     'kind: NetworkPolicy'
  # The host comes from mailserver.domain (example.com in PROD_ARGS).
  assert_contains "$app is at $app.example.com"     "$tmp/$app-ing.yaml"    "host: \"$app.example.com\""
  # /api goes to the API, so the app reaches it on its own origin: no CORS.
  grep -A4 -- '- path: /api$' "$tmp/$app-ing.yaml" | grep -q 'name: restmail-api$' ||
    fail "$app's host does not send /api to the API"
  pass "$app's host sends /api to the API"
  grep -A4 -- '- path: /$' "$tmp/$app-ing.yaml" | grep -q "name: restmail-$app\$" ||
    fail "$app's host does not send / to the app"
  pass "and / to the app"
  assert_absent   "no TLS unless asked for"         "$tmp/$app-ing.yaml"    'tls:'
  # The images follow appVersion, like every other image.
  assert_contains "$app's image is appVersion"      "$tmp/$app-deploy.yaml" "image: \"ghcr.io/rest-mail/$app:$app_version\""
  assert_contains "served at the root of its host"  "$tmp/$app-deploy.yaml" 'value: "/"'
  assert_contains "$app runs as nginx's uid"        "$tmp/$app-deploy.yaml" 'runAsUser: 101'
  assert_contains "on a read-only root filesystem"  "$tmp/$app-deploy.yaml" 'readOnlyRootFilesystem: true'
  assert_contains "$app is probed over HTTP"        "$tmp/$app-deploy.yaml" 'httpGet:'
done

# Another domain moves both hosts; a host that is given wins.
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set mailserver.domain=example.org \
  --set admin.ingress.host=console.example.net > "$tmp/hosts.yaml"
assert_contains "webmail follows mailserver.domain" "$tmp/hosts.yaml" 'host: "webmail.example.org"'
assert_contains "a given admin host wins"           "$tmp/hosts.yaml" 'host: "console.example.net"'
assert_absent   "and replaces the default"          "$tmp/hosts.yaml" 'host: "admin.example.org"'

# TLS through values, as the API ingress does.
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --set webmail.ingress.tls.enabled=true \
  --set admin.ingress.tls.enabled=true --set admin.ingress.tls.secretName=admin-cert \
  --set webmail.ingress.className=traefik > "$tmp/apps-tls.yaml"
doc "$tmp/apps-tls.yaml" Ingress restmail-webmail > "$tmp/webmail-tls.yaml"
doc "$tmp/apps-tls.yaml" Ingress restmail-admin   > "$tmp/admin-tls.yaml"
assert_contains "webmail's TLS is for its host"      "$tmp/webmail-tls.yaml" '- "webmail.example.com"'
assert_contains "in a Secret of its own by default"  "$tmp/webmail-tls.yaml" 'secretName: "restmail-webmail-tls"'
assert_contains "or the one given"                   "$tmp/admin-tls.yaml"   'secretName: "admin-cert"'
assert_contains "the ingress class is passed on"     "$tmp/webmail-tls.yaml" 'ingressClassName: "traefik"'

# The API's own ingress, when it is on: its path, and the documents other mail servers fetch
# from its host's root, RESTMAIL discovery and TLS-RPT reports, each exactly.
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --set api.ingress.enabled=true --set api.ingress.host=mail.example.com > "$tmp/api-ingress-all.yaml"
doc "$tmp/api-ingress-all.yaml" Ingress restmail-api > "$tmp/api-ingress.yaml"
assert_contains "the API ingress is on its host"     "$tmp/api-ingress.yaml" 'host: "mail.example.com"'
assert_contains "and sends /api to the API"          "$tmp/api-ingress.yaml" 'path: /api'
assert_contains "RESTMAIL discovery at the root"     "$tmp/api-ingress.yaml" 'path: /.well-known/restmail'
assert_contains "TLS-RPT reports at the root"        "$tmp/api-ingress.yaml" 'path: /.well-known/smtp-tlsrpt'
[ "$(grep -c 'pathType: Exact' "$tmp/api-ingress.yaml")" -eq 2 ] || fail "the two documents are not matched exactly"

helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --set api.ingress.enabled=true --set api.ingress.host=mail.example.com \
  --set-json 'api.mtaSts.hosts=[]' --set-json 'api.mailDiscovery.domains=[]' > "$tmp/api-ingress-empty-all.yaml"
doc "$tmp/api-ingress-empty-all.yaml" Ingress restmail-api > "$tmp/api-ingress-empty.yaml"
cmp "$tmp/api-ingress.yaml" "$tmp/api-ingress-empty.yaml" || fail "empty MTA-STS hosts or discovery domains changed the ingress"
pass "empty MTA-STS hosts and discovery domains leave the ingress byte-for-byte unchanged"

helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --set api.ingress.enabled=true --set api.ingress.host=mail.example.com \
  --set api.ingress.tls.enabled=true --set api.ingress.tls.secretName=api-cert \
  --set 'api.ingress.annotations.cert-manager\.io/cluster-issuer=letsencrypt' \
  --set api.service.port=8181 \
  --set-json 'api.mtaSts.hosts=["mta-sts.example.com","mta-sts.xn--bcher-kva.example"]' > "$tmp/mtasts-all.yaml"
doc "$tmp/mtasts-all.yaml" Ingress restmail-api > "$tmp/mtasts.yaml"
assert_contains "cert-manager issuer is retained" "$tmp/mtasts.yaml" 'cert-manager.io/cluster-issuer: letsencrypt'
[ "$(yq '.spec.tls[0].secretName' "$tmp/mtasts.yaml")" = api-cert ] || fail "MTA-STS uses a different TLS secret"
[ "$(yq '.spec.rules | length' "$tmp/mtasts.yaml")" -eq 3 ] || fail "MTA-STS needs separate host rules"
[ "$(yq '.spec.tls[0].hosts | length' "$tmp/mtasts.yaml")" -eq 3 ] || fail "certificate lost ingress names"
for host in mta-sts.example.com mta-sts.xn--bcher-kva.example; do
  HOST="$host" yq '.spec.rules[] | select(.host == strenv(HOST)) | .http.paths' "$tmp/mtasts.yaml" > "$tmp/mtasts-paths.yaml"
  [ "$(yq 'length' "$tmp/mtasts-paths.yaml")" -eq 1 ] || fail "$host exposes more than the policy file"
  [ "$(yq '.[0].path' "$tmp/mtasts-paths.yaml")" = /.well-known/mta-sts.txt ] || fail "$host lost the policy path"
  [ "$(yq '.[0].pathType' "$tmp/mtasts-paths.yaml")" = Exact ] || fail "$host matches more than the policy path"
  [ "$(yq '.[0].backend.service.name' "$tmp/mtasts-paths.yaml")" = restmail-api ] || fail "$host does not reach the API"
  [ "$(yq '.[0].backend.service.port.number' "$tmp/mtasts-paths.yaml")" -eq 8181 ] || fail "$host lost the configured API port"
  [ "$(HOST="$host" yq '[.spec.tls[0].hosts[] | select(. == strenv(HOST))] | length' "$tmp/mtasts.yaml")" -eq 1 ] || fail "$host is not covered by the certificate request"
  pass "$host routes only its policy file and is requested on the API certificate"
done
pass "each exactly"

# Mail app discovery (#1706): each listed domain gets autoconfig.<domain>, routing only the
# Thunderbird document, and autodiscover.<domain>, routing only Outlook's, both on the API
# certificate beside the API host and the MTA-STS hosts.
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --set api.ingress.enabled=true --set api.ingress.host=mail.example.com \
  --set api.ingress.tls.enabled=true --set api.ingress.tls.secretName=api-cert \
  --set api.service.port=8181 \
  --set-json 'api.mtaSts.hosts=["mta-sts.example.com"]' \
  --set-json 'api.mailDiscovery.domains=["example.com","xn--bcher-kva.example"]' > "$tmp/discovery-all.yaml"
doc "$tmp/discovery-all.yaml" Ingress restmail-api > "$tmp/discovery.yaml"
[ "$(yq '.spec.tls[0].secretName' "$tmp/discovery.yaml")" = api-cert ] || fail "discovery uses a different TLS secret"
[ "$(yq '.spec.rules | length' "$tmp/discovery.yaml")" -eq 6 ] || fail "discovery needs a host rule per name"
[ "$(yq '.spec.tls[0].hosts | length' "$tmp/discovery.yaml")" -eq 6 ] || fail "certificate lost ingress names"
for domain in example.com xn--bcher-kva.example; do
  for pair in autoconfig:/mail/config-v1.1.xml autodiscover:/autodiscover/autodiscover.xml; do
    host="${pair%%:*}.$domain" path="${pair#*:}"
    HOST="$host" yq '.spec.rules[] | select(.host == strenv(HOST)) | .http.paths' "$tmp/discovery.yaml" > "$tmp/discovery-paths.yaml"
    [ "$(yq 'length' "$tmp/discovery-paths.yaml")" -eq 1 ] || fail "$host exposes more than its document"
    [ "$(yq '.[0].path' "$tmp/discovery-paths.yaml")" = "$path" ] || fail "$host lost $path"
    [ "$(yq '.[0].pathType' "$tmp/discovery-paths.yaml")" = Exact ] || fail "$host matches more than $path"
    [ "$(yq '.[0].backend.service.name' "$tmp/discovery-paths.yaml")" = restmail-api ] || fail "$host does not reach the API"
    [ "$(yq '.[0].backend.service.port.number' "$tmp/discovery-paths.yaml")" -eq 8181 ] || fail "$host lost the configured API port"
    [ "$(HOST="$host" yq '[.spec.tls[0].hosts[] | select(. == strenv(HOST))] | length' "$tmp/discovery.yaml")" -eq 1 ] || fail "$host is not covered by the certificate request"
    pass "$host routes only $path and is requested on the API certificate"
  done
done

# Both off: nothing of either is left, and nothing else goes with them.
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --set webmail.enabled=false --set admin.enabled=false > "$tmp/no-apps.yaml"
assert_absent   "webmail gone"           "$tmp/no-apps.yaml" 'restmail-webmail'
assert_absent   "admin gone"             "$tmp/no-apps.yaml" 'restmail-admin'
assert_absent   "no web app Ingress"     "$tmp/no-apps.yaml" 'kind: Ingress'
assert_contains "API survives"           "$tmp/no-apps.yaml" 'name: restmail-api'
assert_contains "default-deny survives"  "$tmp/no-apps.yaml" 'restmail-default-deny'
# One off leaves the other.
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set webmail.enabled=false > "$tmp/admin-only.yaml"
assert_absent   "webmail off"            "$tmp/admin-only.yaml" 'restmail-webmail'
assert_contains "admin stays"            "$tmp/admin-only.yaml" 'host: "admin.example.com"'

echo "== the JS filter sidecar: only the API, with the token (#160) =="
doc "$tmp/prod.yaml" Deployment    restmail-api       > "$tmp/api-deploy.yaml"
doc "$tmp/prod.yaml" Deployment    restmail-js-filter > "$tmp/jsf-deploy.yaml"
doc "$tmp/prod.yaml" NetworkPolicy restmail-js-filter > "$tmp/jsf-np.yaml"
doc "$tmp/prod.yaml" Secret        restmail-api       > "$tmp/api-secret.yaml"
assert_contains "the API Secret holds the token"     "$tmp/api-secret.yaml" 'JS_FILTER_TOKEN:'
assert_contains "the API sends the token"            "$tmp/api-deploy.yaml" 'key: JS_FILTER_TOKEN'
assert_contains "to the sidecar's Service"           "$tmp/api-deploy.yaml" 'value: "http://restmail-js-filter:3100"'
assert_contains "the sidecar checks it"              "$tmp/jsf-deploy.yaml" 'key: JS_FILTER_TOKEN'
assert_absent   "and gets no other secret"           "$tmp/jsf-deploy.yaml" 'key: JWT_SECRET'
assert_absent   "nor the master key"                 "$tmp/jsf-deploy.yaml" 'key: MASTER_KEY'
assert_absent   "nor the SRS secret"                 "$tmp/jsf-deploy.yaml" 'key: SRS_SECRET'
# Exactly one peer may reach it: the API's pods.
[ "$(grep -c -- '- podSelector:' "$tmp/jsf-np.yaml")" = 1 ] || fail "the sidecar's NetworkPolicy admits more than one peer"
grep -A4 -- '- podSelector:' "$tmp/jsf-np.yaml" | grep -q 'app.kubernetes.io/component: api$' ||
  fail "the sidecar's NetworkPolicy does not admit the API"
pass "only the API may reach the sidecar"
assert_absent   "the sidecar opens no egress"        "$tmp/jsf-np.yaml"     'Egress'
# A managed Secret is used as is, and must hold the token.
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set api.existingSecret=my-api > "$tmp/existing.yaml"
doc "$tmp/existing.yaml" Deployment restmail-js-filter > "$tmp/jsf-existing.yaml"
assert_contains "the sidecar reads a managed Secret" "$tmp/jsf-existing.yaml" 'name: my-api'

echo "== owner keys reach the API =="
KEY='ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOwnerKeyForTheRenderTestOnlyxxxxxxxxxxxxxx owner@laptop'
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set-string api.ownerKeys="$KEY" > "$tmp/owner.yaml"
assert_contains "RESTMAIL_OWNER_KEYS set"   "$tmp/owner.yaml" "RESTMAIL_OWNER_KEYS: \"$KEY\""
assert_absent   "no owner keys by default"  "$tmp/prod.yaml"  'RESTMAIL_OWNER_KEYS'

echo "== status keys reach the API =="
SKEY='ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIStatusKeyForTheRenderTestOnlyxxxxxxxxxxxxx deployer@ci'
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set-string api.statusKeys="$SKEY" > "$tmp/status.yaml"
assert_contains "RESTMAIL_STATUS_KEYS set"  "$tmp/status.yaml" "RESTMAIL_STATUS_KEYS: \"$SKEY\""
assert_absent   "status keys are not owner keys" "$tmp/status.yaml" 'RESTMAIL_OWNER_KEYS'
assert_absent   "no status keys by default" "$tmp/prod.yaml"  'RESTMAIL_STATUS_KEYS'

echo "== push notifications are off, until given an APNs key from a Secret =="
assert_absent "no APNs by default"        "$tmp/prod.yaml" 'RESTMAIL_APNS'
assert_absent "no APNs key mounted"       "$tmp/prod.yaml" 'apns-key'
APNS_ARGS=(--set api.push.apns.enabled=true --set api.push.apns.existingSecret=restmail-apns
           --set api.push.apns.keyId=KEY1234567 --set api.push.apns.teamId=TEAM123456)
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" "${APNS_ARGS[@]}" > "$tmp/apns.yaml"
assert_contains "the key file is set"     "$tmp/apns.yaml" 'RESTMAIL_APNS_KEY_FILE: "/etc/restmail/apns/AuthKey.p8"'
assert_contains "the key id"              "$tmp/apns.yaml" 'RESTMAIL_APNS_KEY_ID: "KEY1234567"'
assert_contains "the team id"             "$tmp/apns.yaml" 'RESTMAIL_APNS_TEAM_ID: "TEAM123456"'
assert_contains "the app's topic"         "$tmp/apns.yaml" 'RESTMAIL_APNS_TOPIC: "com.antimatterstudios.restmail"'
assert_contains "the key from the Secret" "$tmp/apns.yaml" 'secretName: restmail-apns'
assert_contains "mounted where it is read" "$tmp/apns.yaml" 'mountPath: /etc/restmail/apns'
nosecret=$(helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set api.push.apns.enabled=true \
  --set api.push.apns.keyId=KEY1234567 --set api.push.apns.teamId=TEAM123456 2>&1) &&
  fail "push was turned on with no Secret for its key"
case "$nosecret" in *"existingSecret"*) pass "no Secret, no push" ;; *) fail "refused, but not for the right reason: $nosecret" ;; esac
noids=$(helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set api.push.apns.enabled=true \
  --set api.push.apns.existingSecret=restmail-apns 2>&1) && fail "push was turned on with no key id or team id"
case "$noids" in *"keyId"*) pass "no ids, no push" ;; *) fail "refused, but not for the right reason: $noids" ;; esac

echo "== no setup links =="
# A server prints no setup code, so it has no link to point anywhere: the first
# admin comes from the bootstrap password.
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set-string api.setupLinkBase=https://mail.example.com/claim > "$tmp/link.yaml"
assert_absent   "an old setting reaches nothing" "$tmp/link.yaml" 'RESTMAIL_SETUP_LINK_BASE'

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
assert_contains "with an SRS_SECRET"             "$tmp/api-secret.yaml" 'SRS_SECRET:'
# A previous SRS secret only comes from a rotation, never from an install.
assert_absent   "and no previous SRS secret"     "$tmp/api-secret.yaml" 'SRS_PREVIOUS_SECRET:'
doc "$tmp/bare.yaml" Deployment restmail-smtp-gateway > "$tmp/smtp-deploy-srs.yaml"
assert_contains "the SMTP gateway signs with it" "$tmp/smtp-deploy-srs.yaml" 'key: SRS_SECRET'
grep -A1 'key: SRS_PREVIOUS_SECRET' "$tmp/smtp-deploy-srs.yaml" | grep -q 'optional: true' ||
  fail "the previous SRS secret is not optional, so a Secret without one would stop the gateway"
pass "the previous SRS secret is optional"
doc "$tmp/bare.yaml" Deployment restmail-api > "$tmp/api-deploy-srs.yaml"
assert_absent   "the API does not get it"        "$tmp/api-deploy-srs.yaml" 'key: SRS_SECRET'
assert_contains "Postgres's Secret is made"      "$tmp/pg-secret.yaml"  'POSTGRES_PASSWORD:'
assert_contains "for user restmail"              "$tmp/pg-secret.yaml"  'POSTGRES_USER: "restmail"'
# MASTER_KEY must survive an uninstall: a new one makes stored keys unreadable.
assert_contains "the API's Secret survives uninstall"  "$tmp/api-secret.yaml" 'helm.sh/resource-policy: keep'
assert_contains "so does Postgres's"                   "$tmp/pg-secret.yaml"  'helm.sh/resource-policy: keep'
# And so must the data it unlocks: uninstalling removes the software, not the
# mail. Keeping the key while deleting the volumes would be backwards.
pvc() { # pvc <file> <name> -> that PersistentVolumeClaim's document
  awk -v n="  name: $2" 'BEGIN{RS="\n---\n"} /\nkind: PersistentVolumeClaim\n/ && index($0, n"\n") {print}' "$1"
}
pvc "$tmp/bare.yaml" restmail-postgres-data  > "$tmp/pg-pvc.yaml"
pvc "$tmp/bare.yaml" restmail-api-attachments > "$tmp/att-pvc.yaml"
assert_contains "the database's volume is made"      "$tmp/pg-pvc.yaml"  'kind: PersistentVolumeClaim'
assert_contains "and survives uninstall"             "$tmp/pg-pvc.yaml"  'helm.sh/resource-policy: keep'
assert_contains "the attachments' volume is made"    "$tmp/att-pvc.yaml" 'kind: PersistentVolumeClaim'
assert_contains "and survives uninstall too"         "$tmp/att-pvc.yaml" 'helm.sh/resource-policy: keep'
# Generated, not a placeholder: 64 characters of a random alphabet.
key=$(sed -n 's/^  MASTER_KEY: "\(.*\)"$/\1/p' "$tmp/api-secret.yaml")
[ "${#key}" = 64 ] || fail "MASTER_KEY is ${#key} characters, want 64"
pass "a generated MASTER_KEY is 64 characters"

echo "== an install without an admin password is refused =="
# The chart will not invent one. Whoever installs has somewhere to keep a
# secret; a chart does not, so a generated password would be one nobody could
# retrieve — out of a Secret they may not yet have access to, a pipeline log,
# or a terminal nobody was watching. Refusing is the only honest answer, and it
# has to happen at render time, before anything is created.
refusal=$(helm template restmail "$CHART_DIR" \
  --set mailserver.hostname=mx.example.com --set mailserver.domain=example.com 2>&1) &&
  fail "the chart rendered with no admin password"
case "$refusal" in
  *"api.bootstrapAdminPassword is required"*) pass "no password, no install" ;;
  *) fail "refused, but not in words anybody can act on: $refusal" ;;
esac
# And the refusal says what to do about it, which is the whole point of failing
# here rather than letting a server start that nobody can sign in to.
case "$refusal" in
  *"chore install:password"*) pass "and it says how to make one" ;;
  *) fail "the refusal does not say how to make a password: $refusal" ;;
esac

# A password too short to be worth having is refused for its own reason, not
# silently padded or accepted.
short=$(helm template restmail "$CHART_DIR" \
  --set mailserver.hostname=mx.example.com --set mailserver.domain=example.com \
  --set api.bootstrapAdminPassword=short 2>&1) &&
  fail "a five-character admin password was accepted"
case "$short" in
  *"at least 16 characters"*) pass "a too-short password is refused" ;;
  *) fail "refused, but not for the right reason: $short" ;;
esac

echo "== the password given is the password stored =="
# Nothing transforms it on the way through: what you typed is what you sign in
# with. The Secret is the only place it lands.
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" > "$tmp/fresh.yaml"
secret() { awk -v n="  name: $2" 'BEGIN{RS="\n---\n"} /\nkind: Secret\n/ && index($0, n"\n") {print}' "$1"; }
stored=$(secret "$tmp/fresh.yaml" restmail-api | sed -n 's/^  BOOTSTRAP_ADMIN_PASSWORD: "\(.*\)"$/\1/p')
[ "$stored" = "$TEST_PASSWORD" ] ||
  fail "stored '"'"'$stored'"'"', gave '"'"'$TEST_PASSWORD'"'"'"
pass "what was given is what is stored"

# NOTES.txt is what tells whoever installed it how to sign in. It must NOT
# print the password: they already have it — they made it — and a chart that
# echoes a secret puts it in every pipeline log that ever runs an install.
assert_contains "the notes name the admin user" "$CHART_DIR/templates/NOTES.txt" 'admin'
grep -q 'bootstrapAdminPassword' "$CHART_DIR/templates/NOTES.txt" &&
  fail "NOTES.txt prints the admin password back"
pass "the notes do not echo the password"

echo "== what is given wins =="
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --set api.jwtSecret=given-jwt --set api.masterKey=given-master \
  --set api.bootstrapAdminPassword=a-bootstrap-password-of-length \
  --set postgres.auth.password=given-pg > "$tmp/given.yaml"
assert_contains "a given JWT_SECRET"          "$tmp/given.yaml" 'JWT_SECRET: "given-jwt"'
assert_contains "a given MASTER_KEY"          "$tmp/given.yaml" 'MASTER_KEY: "given-master"'
assert_contains "a given bootstrap password"  "$tmp/given.yaml" 'BOOTSTRAP_ADMIN_PASSWORD: "a-bootstrap-password-of-length"'
# Given, so the installer already has it; the notes must not repeat a secret
# back at someone who supplied it.
given_notes=$(helm install restmail "$CHART_DIR" "${PROD_ARGS[@]}" --dry-run \
  --set api.bootstrapAdminPassword=a-bootstrap-password-of-length 2>/dev/null || true)
case "$given_notes" in
  *a-bootstrap-password-of-length*) fail "the notes echoed a password the installer supplied" ;;
esac
pass "a supplied password is not echoed back"
assert_contains "a given Postgres password"   "$tmp/given.yaml" 'POSTGRES_PASSWORD: "given-pg"'

echo "== or a bcrypt hash of the password, which a deploy tool can hold as plain config =="
# What `restmail password-hash` makes: bare bcrypt, cost 10.
HASH='$2a$10$CwTycUXWue0Thq9StjUM0uJ8.dLw6p0Gq1sYk9YlFQd0uB2X3m7nG'
HOST_ARGS=(--set mailserver.hostname=mx.example.com --set mailserver.domain=example.com)
helm template restmail "$CHART_DIR" "${HOST_ARGS[@]}" \
  --set-string "api.bootstrapAdminPasswordHash=$HASH" > "$tmp/hash.yaml"
secret "$tmp/hash.yaml" restmail-api > "$tmp/hash-secret.yaml"
assert_contains "the hash is kept"            "$tmp/hash-secret.yaml" "BOOTSTRAP_ADMIN_PASSWORD_HASH: \"$HASH\""
assert_absent   "and no password beside it"   "$tmp/hash-secret.yaml" 'BOOTSTRAP_ADMIN_PASSWORD:'
assert_contains "the API is given it"         "$tmp/hash.yaml" 'name: RESTMAIL_BOOTSTRAP_ADMIN_PASSWORD_HASH'
both=$(helm template restmail "$CHART_DIR" "${HOST_ARGS[@]}" --set-string "api.bootstrapAdminPasswordHash=$HASH" \
  --set api.bootstrapAdminPassword=a-bootstrap-password-of-length 2>&1) && fail "the password and its hash were both taken"
case "$both" in *"not both"*) pass "the password or its hash, not both" ;; *) fail "refused, but not for the right reason: $both" ;; esac
nohash=$(helm template restmail "$CHART_DIR" "${HOST_ARGS[@]}" --set-string api.bootstrapAdminPasswordHash=a-bootstrap-password-of-length 2>&1) &&
  fail "a password was taken for its hash"
case "$nohash" in *"must be a bcrypt hash"*) pass "a hash that is not one is refused" ;; *) fail "refused, but not for the right reason: $nohash" ;; esac

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
echo "== the bundled Garage =="
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" > "$tmp/garage.yaml"
assert_contains "Garage runs by default" "$tmp/garage.yaml" 'name: restmail-garage'
assert_contains "the API uses Garage" "$tmp/garage.yaml" 'ATTACHMENT_S3_ENDPOINT: "http://restmail-garage:3900"'
assert_contains "the bucket is made before the API starts" "$tmp/garage.yaml" 'name: garage-bootstrap'
yq 'select(.kind == "ConfigMap")' "$tmp/garage.yaml" > "$tmp/garage-configmaps.yaml"
assert_absent "Garage's keys never enter a ConfigMap" "$tmp/garage-configmaps.yaml" 'rpc_secret'
yq 'select(.kind == "Secret" and .metadata.name == "restmail-garage") | .metadata.annotations' "$tmp/garage.yaml" > "$tmp/garage-keep.yaml"
assert_contains "Garage's keys outlive an uninstall" "$tmp/garage-keep.yaml" 'keep'
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set garage.enabled=false --set api.attachments.storageType=filesystem > "$tmp/nogarage.yaml"
assert_absent "no Garage when turned off" "$tmp/nogarage.yaml" 'restmail-garage'
if helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set api.attachments.storageType=filesystem > /dev/null 2>&1; then
  echo "FAIL: the filesystem was taken with Garage on"; exit 1
fi

echo "== private S3 attachments and retained migration volume =="
cat > "$tmp/s3-values.yaml" <<'YAML'
garage:
  enabled: false
api:
  attachments:
    storageType: s3
    s3:
      endpoint: http://garage-s3.garage.svc:3900
      region: garage
      bucket: mail-attachments
      accessKeyId: fixture-key-id
      secretAccessKey: fixture-private-secret
      networkPolicy:
        port: 3900
        to:
          - namespaceSelector:
              matchLabels:
                kubernetes.io/metadata.name: garage
            podSelector:
              matchLabels:
                app: garage
YAML
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" -f "$tmp/s3-values.yaml" > "$tmp/s3.yaml"
assert_contains "S3 driver is configured" "$tmp/s3.yaml" 'ATTACHMENT_STORAGE_TYPE: "s3"'
assert_contains "S3 credentials use a Secret" "$tmp/s3.yaml" 'name: restmail-api-attachments-s3'
yq 'select(.kind == "ConfigMap")' "$tmp/s3.yaml" > "$tmp/s3-configmaps.yaml"
assert_absent "S3 credential never enters a ConfigMap" "$tmp/s3-configmaps.yaml" 'fixture-private-secret'
assert_contains "old attachment PVC remains during migration" "$tmp/s3.yaml" 'mountPath: /attachments'
yq 'select(.kind == "NetworkPolicy" and .metadata.name == "restmail-api") | .spec.egress[] | select(.ports[0].port == 3900) | .to' "$tmp/s3.yaml" > "$tmp/s3-egress.yaml"
assert_contains "Garage egress selects its namespace" "$tmp/s3-egress.yaml" 'kubernetes.io/metadata.name: garage'
assert_contains "Garage egress selects its pods" "$tmp/s3-egress.yaml" 'app: garage'

helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" -f "$tmp/s3-values.yaml" \
  --set api.attachments.persistence.enabled=false \
  --set api.attachments.s3.existingSecret=my-s3-secret \
  --set api.attachments.s3.accessKeyId= --set api.attachments.s3.secretAccessKey= > "$tmp/s3-no-pvc.yaml"
assert_contains "external S3 Secret is referenced" "$tmp/s3-no-pvc.yaml" 'name: my-s3-secret'
assert_absent "S3 after migration has no attachment mount" "$tmp/s3-no-pvc.yaml" 'mountPath: /attachments'
assert_absent "external S3 Secret creates no inline credentials" "$tmp/s3-no-pvc.yaml" 'fixture-private-secret'
if helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set garage.enabled=false --set api.attachments.storageType=s3 > /dev/null 2>&1; then
  fail "S3 without bucket and credentials was accepted"
fi
pass "incomplete S3 configuration is refused"
if helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" -f "$tmp/s3-values.yaml" \
  --set api.attachments.s3.existingSecret=my-s3-secret > /dev/null 2>&1; then
  fail "conflicting S3 credential sources were accepted"
fi
pass "conflicting S3 credential sources are refused"

echo "All chart render tests passed."

echo "== optional loopback DANE resolver (#725) =="
assert_absent "DANE stays off by default" "$tmp/prod.yaml" 'name: dane-resolver'
assert_absent "no resolver env by default" "$tmp/prod.yaml" 'name: DANE_RESOLVER'
for kind in Deployment DaemonSet; do
  helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set smtp.dane.enabled=true \
    --set "smtpGateway.workloadKind=$kind" > "$tmp/dane-$kind.yaml"
  assert_contains "DANE resolver on $kind" "$tmp/dane-$kind.yaml" 'name: dane-resolver'
  assert_contains "DANE uses loopback on $kind" "$tmp/dane-$kind.yaml" 'value: "127.0.0.1:5353"'
  assert_contains "DANE config rolls pods on $kind" "$tmp/dane-$kind.yaml" 'checksum/dane-config:'
  assert_contains "DNSSEC validator on $kind" "$tmp/dane-$kind.yaml" 'module-config: "validator iterator"'
  assert_contains "DNSSEC never permissive on $kind" "$tmp/dane-$kind.yaml" 'val-permissive-mode: no'
  resolver=$(yq -N e 'select(.metadata.name == "restmail-smtp-gateway") | .spec.template.spec.containers[] | select(.name == "dane-resolver") | .securityContext.runAsUser' "$tmp/dane-$kind.yaml")
  [ "$resolver" = 101 ] || fail "DANE resolver must run as image uid101"
  ports=$(yq -N e 'select(.kind == "Service") | .spec.ports[] | select(.port == 5353 or .targetPort == 5353)' "$tmp/dane-$kind.yaml")
  [ -z "$ports" ] || fail "DANE loopback resolver must have no public Service"
done
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" --set smtp.dane.enabled=true \
  --set smtpGateway.enabled=false > "$tmp/dane-disabled-gateway.yaml"
assert_absent "no resolver without SMTP gateway" "$tmp/dane-disabled-gateway.yaml" 'name: dane-resolver'
assert_absent "no DANE ConfigMap without SMTP gateway" "$tmp/dane-disabled-gateway.yaml" 'name: restmail-smtp-dane'

# Tracing (#1580): off by default, so no OTEL_* variable and no egress; with an
# endpoint, the API and all three gateways get it, and the collector egress opens.
assert_absent "no OTLP endpoint by default" "$tmp/prod.yaml" 'OTEL_EXPORTER_OTLP_ENDPOINT'
assert_absent "no OTLP egress by default" "$tmp/prod.yaml" 'restmail-otlp-egress'
helm template restmail "$CHART_DIR" "${PROD_ARGS[@]}" \
  --set tracing.otlpEndpoint=http://otel-collector.observability:4318 \
  --set 'tracing.networkPolicy.to[0].namespaceSelector.matchLabels.name=observability' > "$tmp/tracing.yaml"
for component in api smtp-gateway imap-gateway pop3-gateway; do
  endpoint=$(yq -N e "select(.kind == \"Deployment\" and .metadata.name == \"restmail-$component\") | .spec.template.spec.containers[0].env[] | select(.name == \"OTEL_EXPORTER_OTLP_ENDPOINT\") | .value" "$tmp/tracing.yaml")
  [ "$endpoint" = http://otel-collector.observability:4318 ] || fail "$component must get the OTLP endpoint (got '$endpoint')"
done
assert_contains "OTLP egress with an endpoint" "$tmp/tracing.yaml" 'name: restmail-otlp-egress'
pass "tracing is off by default and passed to every Go service when set"
