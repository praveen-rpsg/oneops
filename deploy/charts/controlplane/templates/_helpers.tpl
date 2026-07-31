{{- define "controlplane.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "controlplane.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "controlplane.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "controlplane.labels" -}}
app.kubernetes.io/name: {{ include "controlplane.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "controlplane.selectorLabels" -}}
app.kubernetes.io/name: {{ include "controlplane.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "controlplane.secretName" -}}
{{- if .Values.existingSecret -}}
{{ .Values.existingSecret }}
{{- else -}}
{{ include "controlplane.fullname" . }}
{{- end -}}
{{- end -}}

{{- define "controlplane.metricsServiceName" -}}
{{- printf "%s-metrics" (include "controlplane.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
