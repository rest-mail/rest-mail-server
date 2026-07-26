{{/*
Expand the name of the chart.
*/}}
{{- define "restmail.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a fully qualified app name.
We use the release name as prefix to avoid collisions in shared namespaces.
*/}}
{{- define "restmail.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Chart label.
*/}}
{{- define "restmail.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels applied to every object.
*/}}
{{- define "restmail.labels" -}}
helm.sh/chart: {{ include "restmail.chart" . }}
{{ include "restmail.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: restmail
{{- end -}}

{{/*
Selector labels (no version — stable across upgrades).
*/}}
{{- define "restmail.selectorLabels" -}}
app.kubernetes.io/name: {{ include "restmail.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Per-component name (used as Service / Deployment object name).
Usage: {{ include "restmail.componentName" (list . "api") }}
*/}}
{{- define "restmail.componentName" -}}
{{- $root := index . 0 -}}
{{- $component := index . 1 -}}
{{- printf "%s-%s" (include "restmail.fullname" $root) $component | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Per-component selector labels.
Usage: {{ include "restmail.componentSelectorLabels" (list . "api") }}
*/}}
{{- define "restmail.componentSelectorLabels" -}}
{{- $root := index . 0 -}}
{{- $component := index . 1 -}}
{{ include "restmail.selectorLabels" $root }}
app.kubernetes.io/component: {{ $component }}
{{- end -}}

{{/*
Per-component full label set.
Usage: {{ include "restmail.componentLabels" (list . "api") }}
*/}}
{{- define "restmail.componentLabels" -}}
{{- $root := index . 0 -}}
{{- $component := index . 1 -}}
{{ include "restmail.labels" $root }}
app.kubernetes.io/component: {{ $component }}
{{- end -}}

{{/*
Service account name.
*/}}
{{- define "restmail.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "restmail.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Image pull policy: per-component override falls back to global.
Usage: {{ include "restmail.imagePullPolicy" (list . .Values.api.image.pullPolicy) }}
*/}}
{{- define "restmail.imagePullPolicy" -}}
{{- $root := index . 0 -}}
{{- $override := index . 1 -}}
{{- default $root.Values.global.imagePullPolicy $override -}}
{{- end -}}

{{/*
Image tag fallback: per-image tag, otherwise Chart.AppVersion.
Usage: {{ include "restmail.imageTag" (list . .Values.api.image.tag) }}
*/}}
{{- define "restmail.imageTag" -}}
{{- $root := index . 0 -}}
{{- $tag := index . 1 -}}
{{- default $root.Chart.AppVersion $tag -}}
{{- end -}}

{{/*
Postgres host = Service DNS name of the embedded postgres.
*/}}
{{- define "restmail.postgres.host" -}}
{{ include "restmail.componentName" (list . "postgres") }}
{{- end -}}

{{/*
Postgres credentials Secret name.
*/}}
{{- define "restmail.postgres.secretName" -}}
{{- if .Values.postgres.existingSecret -}}
{{- .Values.postgres.existingSecret -}}
{{- else -}}
{{ include "restmail.componentName" (list . "postgres") }}
{{- end -}}
{{- end -}}

{{/*
API credentials Secret name.
*/}}
{{- define "restmail.api.secretName" -}}
{{- if .Values.api.existingSecret -}}
{{- .Values.api.existingSecret -}}
{{- else -}}
{{ include "restmail.componentName" (list . "api") }}
{{- end -}}
{{- end -}}

{{/*
Internal API Service URL — used by gateways via cluster DNS.
*/}}
{{- define "restmail.api.url" -}}
http://{{ include "restmail.componentName" (list . "api") }}:{{ .Values.api.service.port }}
{{- end -}}

{{/*
DB env block, reused across api + gateways.
Usage: {{ include "restmail.dbEnv" . | indent N }}
*/}}
{{- define "restmail.dbEnv" -}}
- name: DB_HOST
  value: {{ include "restmail.postgres.host" . | quote }}
- name: DB_PORT
  value: {{ .Values.postgres.service.port | quote }}
- name: DB_NAME
  valueFrom:
    secretKeyRef:
      name: {{ include "restmail.postgres.secretName" . }}
      key: POSTGRES_DB
- name: DB_USER
  valueFrom:
    secretKeyRef:
      name: {{ include "restmail.postgres.secretName" . }}
      key: POSTGRES_USER
- name: DB_PASS
  valueFrom:
    secretKeyRef:
      name: {{ include "restmail.postgres.secretName" . }}
      key: POSTGRES_PASSWORD
- name: DB_SSLMODE
  value: {{ .Values.db.sslMode | quote }}
{{- if .Values.db.allowInsecure }}
- name: DB_ALLOW_INSECURE
  value: "true"
{{- end }}
{{- end -}}

{{/*
TLS env block — paths inside the gateway pod.
*/}}
{{- define "restmail.tlsEnv" -}}
- name: TLS_CERT_PATH
  value: {{ printf "%s/%s" .Values.tls.mountPath .Values.tls.certFilename | quote }}
- name: TLS_KEY_PATH
  value: {{ printf "%s/%s" .Values.tls.mountPath .Values.tls.keyFilename | quote }}
{{- end -}}

{{/*
API credential env (JWT_SECRET + MASTER_KEY), sourced from the API Secret.
The shared config loader requires both in production, so the protocol gateways
need them too — not just the API. Reused across api + all three gateways.
*/}}
{{- define "restmail.apiCredsEnv" -}}
- name: JWT_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ include "restmail.api.secretName" . }}
      key: JWT_SECRET
- name: MASTER_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "restmail.api.secretName" . }}
      key: MASTER_KEY
{{- end -}}

{{/*
Pod DNS spec — applied to every pod template.
*/}}
{{- define "restmail.dnsSpec" -}}
dnsPolicy: {{ .Values.networking.dnsPolicy }}
{{- with .Values.networking.dnsConfig }}
dnsConfig:
{{ toYaml . | indent 2 }}
{{- end }}
{{- end -}}

{{/*
Validate required production values.
*/}}
{{- define "restmail.validate" -}}
{{- if not .Values.mailserver.hostname -}}
{{- fail "mailserver.hostname is required (the public FQDN gateways announce)" -}}
{{- end -}}
{{- if not .Values.mailserver.domain -}}
{{- fail "mailserver.domain is required (the primary mail domain served)" -}}
{{- end -}}
{{- /*
Coherence guard: a secure DB sslmode (require / verify-ca / verify-full) only
connects when the server actually serves TLS. When the chart bundles Postgres
(postgres.enabled) but its TLS listener is off, a secure sslmode would fail to
connect at runtime. Fail rendering early with an actionable message rather than
shipping a chart that crash-loops on the DB handshake.
*/ -}}
{{- $secureMode := has .Values.db.sslMode (list "require" "verify-ca" "verify-full") -}}
{{- if and .Values.postgres.enabled $secureMode (not .Values.postgres.tls.enabled) -}}
{{- fail "db.sslMode requires TLS but the bundled Postgres has postgres.tls.enabled=false. Enable postgres.tls.enabled, or set db.sslMode=disable together with db.allowInsecure=true to acknowledge the in-cluster cleartext link." -}}
{{- end -}}
{{- /*
Secure-by-construction: in production the API's gateway-facing internal routes
must be protected. Either internal mTLS is on, or the operator acknowledges an
out-of-band network trust boundary. Neither → the API would refuse to boot, so
fail rendering early with an actionable message instead of shipping a crash-loop.
*/ -}}
{{- if and (eq .Values.api.environment "production") (not .Values.internalMtls.enabled) (not .Values.api.internalRoutesAllowPublic) -}}
{{- fail "internalMtls.enabled=false requires api.internalRoutesAllowPublic=true in production: the API refuses to serve its gateway-facing routes unauthenticated unless you acknowledge an out-of-band network trust boundary (NetworkPolicy/firewall). Enable internalMtls (recommended), or set api.internalRoutesAllowPublic=true." -}}
{{- end -}}
{{- end -}}

{{/*
Pod-level securityContext for a component. Produces a hardened baseline keyed by
component name, then deep-merges the operator's global .Values.podSecurityContext
on top (operator values win), so a cluster can override any field globally.

The protocol gateways (smtp/imap/pop3) bind privileged ports (<1024). The current
gateway images run as root and carry no file capabilities, so they cannot run as
a non-root UID and still bind those ports (Kubernetes does not yet populate the
ambient capability set from securityContext.capabilities). They therefore keep
runAsNonRoot unset here and rely on the container-level "drop ALL, add only
NET_BIND_SERVICE" posture below. The API, js-filter and Postgres images ship a
non-root user, so they run fully non-root.

Usage: {{ include "restmail.podSecurityContext" (list . "api") }}
*/}}
{{- define "restmail.podSecurityContext" -}}
{{- $root := index . 0 -}}
{{- $comp := index . 1 -}}
{{- $base := dict "seccompProfile" (dict "type" "RuntimeDefault") -}}
{{- if eq $comp "api" -}}
{{- $base = merge $base (dict "runAsNonRoot" true "runAsUser" 10001 "runAsGroup" 10001 "fsGroup" 10001) -}}
{{- else if eq $comp "js-filter" -}}
{{- /* A numeric runAsUser is required: the image declares a NAMED user
       (USER jsfilter), which the kubelet cannot verify as non-root under
       runAsNonRoot=true (it fails CreateContainerConfigError). Pin the same
       UID the Dockerfile assigns jsfilter. */ -}}
{{- $base = merge $base (dict "runAsNonRoot" true "runAsUser" 10001 "runAsGroup" 10001 "fsGroup" 10001) -}}
{{- else if eq $comp "postgres" -}}
{{- $base = merge $base (dict "runAsNonRoot" true "runAsUser" 70 "runAsGroup" 70 "fsGroup" 70) -}}
{{- end -}}
{{- $merged := merge (deepCopy $root.Values.podSecurityContext) $base -}}
{{- toYaml $merged -}}
{{- end -}}

{{/*
Container-level securityContext for a component. Hardened baseline keyed by
component, deep-merged under the operator's global .Values.securityContext
(operator values win).

Gateways: drop ALL, add back only NET_BIND_SERVICE so a root process can bind
25/587/465/143/993/110/995 and nothing else; read-only rootfs is safe because the
queue is DB-backed and the CA-trust entrypoint only writes when /certs/ca.crt is
present (the chart projects tls.crt/tls.key only). API: read-only rootfs is off
because its entrypoint may run update-ca-certificates. js-filter: read-only rootfs
on (its image is built for it). Postgres: read-only rootfs off (writes PGDATA,
sockets, runtime config).

Usage: {{ include "restmail.containerSecurityContext" (list . "smtp-gateway") }}
*/}}
{{- define "restmail.containerSecurityContext" -}}
{{- $root := index . 0 -}}
{{- $comp := index . 1 -}}
{{- $base := dict "allowPrivilegeEscalation" false -}}
{{- if or (eq $comp "smtp-gateway") (eq $comp "imap-gateway") (eq $comp "pop3-gateway") -}}
{{- $base = merge $base (dict "readOnlyRootFilesystem" true "capabilities" (dict "drop" (list "ALL") "add" (list "NET_BIND_SERVICE"))) -}}
{{- else if eq $comp "api" -}}
{{- $base = merge $base (dict "runAsNonRoot" true "readOnlyRootFilesystem" false "capabilities" (dict "drop" (list "ALL"))) -}}
{{- else if eq $comp "js-filter" -}}
{{- $base = merge $base (dict "runAsNonRoot" true "readOnlyRootFilesystem" true "capabilities" (dict "drop" (list "ALL"))) -}}
{{- else if eq $comp "postgres" -}}
{{- $base = merge $base (dict "runAsNonRoot" true "readOnlyRootFilesystem" false "capabilities" (dict "drop" (list "ALL"))) -}}
{{- end -}}
{{- $merged := merge (deepCopy $root.Values.securityContext) $base -}}
{{- toYaml $merged -}}
{{- end -}}

{{/*
Internal mTLS: Secret name (operator-provided existingSecret, else chart-generated).
*/}}
{{- define "restmail.internalMtls.secretName" -}}
{{- if .Values.internalMtls.existingSecret -}}
{{- .Values.internalMtls.existingSecret -}}
{{- else -}}
{{- printf "%s-internal-mtls" (include "restmail.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
Internal mTLS: the URL the gateways use for the two tokenless machine calls
(recipient existence + inbound delivery), served on the API's mTLS listener.
*/}}
{{- define "restmail.internalMtls.url" -}}
https://{{ include "restmail.componentName" (list . "api") }}:{{ .Values.internalMtls.port }}
{{- end -}}

{{/*
Internal mTLS env for the API (server role): enable, listener port, and the
mounted CA + server keypair paths.
*/}}
{{- define "restmail.internalMtls.serverEnv" -}}
- name: INTERNAL_MTLS_ENABLED
  value: "true"
- name: INTERNAL_MTLS_PORT
  value: {{ .Values.internalMtls.port | quote }}
- name: INTERNAL_MTLS_CA_CERT
  value: {{ printf "%s/ca.crt" .Values.internalMtls.mountPath | quote }}
- name: INTERNAL_MTLS_SERVER_CERT
  value: {{ printf "%s/server.crt" .Values.internalMtls.mountPath | quote }}
- name: INTERNAL_MTLS_SERVER_KEY
  value: {{ printf "%s/server.key" .Values.internalMtls.mountPath | quote }}
{{- end -}}

{{/*
Internal mTLS env for the gateways (client role): enable, the mounted CA +
client keypair paths, and the internal base URL of the API's mTLS listener. The
public API_BASE_URL (plaintext, token calls) stays untouched.
*/}}
{{- define "restmail.internalMtls.clientEnv" -}}
- name: INTERNAL_MTLS_ENABLED
  value: "true"
- name: INTERNAL_MTLS_CA_CERT
  value: {{ printf "%s/ca.crt" .Values.internalMtls.mountPath | quote }}
- name: INTERNAL_MTLS_CLIENT_CERT
  value: {{ printf "%s/client.crt" .Values.internalMtls.mountPath | quote }}
- name: INTERNAL_MTLS_CLIENT_KEY
  value: {{ printf "%s/client.key" .Values.internalMtls.mountPath | quote }}
- name: API_INTERNAL_BASE_URL
  value: {{ include "restmail.internalMtls.url" . | quote }}
{{- end -}}

{{/*
Internal mTLS volumeMount (shared by API + gateways).
*/}}
{{- define "restmail.internalMtls.volumeMount" -}}
- name: internal-mtls
  mountPath: {{ .Values.internalMtls.mountPath }}
  readOnly: true
{{- end -}}

{{/*
Internal mTLS volume projecting only the keys a role needs (least privilege):
"server" → ca+server, "client" → ca+client. Usage:
{{ include "restmail.internalMtls.volume" (list . "server") }}
*/}}
{{- define "restmail.internalMtls.volume" -}}
{{- $root := index . 0 -}}
{{- $role := index . 1 -}}
- name: internal-mtls
  secret:
    secretName: {{ include "restmail.internalMtls.secretName" $root }}
    items:
      - key: ca.crt
        path: ca.crt
{{- if eq $role "server" }}
      - key: server.crt
        path: server.crt
      - key: server.key
        path: server.key
{{- else }}
      - key: client.crt
        path: client.crt
      - key: client.key
        path: client.key
{{- end }}
{{- end -}}
