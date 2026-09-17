{{/*
Expand the name of the chart.
*/}}
{{- define "capv.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "capv.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "capv.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "capv.labels" -}}
helm.sh/chart: {{ include "capv.chart" . }}
{{ include "capv.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
cluster.x-k8s.io/provider: infrastructure-verda
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "capv.selectorLabels" -}}
app.kubernetes.io/name: {{ include "capv.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "capv.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "capv.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "capv.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}

{{- define "capv.credentialsSecretName" -}}
{{- if .Values.credentials.existingSecret }}
{{- .Values.credentials.existingSecret }}
{{- else }}
{{- printf "%s-credentials" (include "capv.fullname" .) }}
{{- end }}
{{- end }}

{{- define "capv.webhookServiceName" -}}
{{- printf "%s-webhook-service" (include "capv.fullname" .) }}
{{- end }}

{{- define "capv.webhookCertSecretName" -}}
{{- if and (not .Values.webhooks.certManager.enabled) .Values.webhooks.certManager.existingSecret }}
{{- .Values.webhooks.certManager.existingSecret }}
{{- else }}
{{- printf "%s-webhook-server-cert" (include "capv.fullname" .) }}
{{- end }}
{{- end }}
