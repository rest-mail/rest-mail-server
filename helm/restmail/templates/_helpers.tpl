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
{{- end -}}
