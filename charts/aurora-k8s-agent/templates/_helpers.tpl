{{- define "aurora-k8s-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "aurora-k8s-agent.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name (include "aurora-k8s-agent.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "aurora-k8s-agent.labels" -}}
app.kubernetes.io/name: {{ include "aurora-k8s-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end }}

{{- define "aurora-k8s-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "aurora-k8s-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "aurora-k8s-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "aurora-k8s-agent.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- required "serviceAccount.name is required when serviceAccount.create=false" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "aurora-k8s-agent.secretName" -}}
{{- default (include "aurora-k8s-agent.fullname" .) .Values.secrets.existingSecret }}
{{- end }}
