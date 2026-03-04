{{/*
Expand the name of the chart.
*/}}
{{- define "novaroute.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "novaroute.fullname" -}}
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

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "novaroute.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "novaroute.labels" -}}
helm.sh/chart: {{ include "novaroute.chart" . }}
{{ include "novaroute.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.agent.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "novaroute.selectorLabels" -}}
app.kubernetes.io/name: {{ include "novaroute.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name
*/}}
{{- define "novaroute.serviceAccountName" -}}
{{- include "novaroute.fullname" . }}-agent
{{- end }}

{{/*
Agent image
*/}}
{{- define "novaroute.agentImage" -}}
{{- printf "%s:%s" .Values.agent.image.repository (.Values.agent.image.tag | default .Chart.AppVersion) }}
{{- end }}

{{/*
FRR image
*/}}
{{- define "novaroute.frrImage" -}}
{{- printf "%s:%s" .Values.frr.image.repository .Values.frr.image.tag }}
{{- end }}
