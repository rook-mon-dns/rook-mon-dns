{{/*
Chart name (truncated to 63 chars, trailing dashes trimmed).
*/}}
{{- define "rook-mon-dns.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name: <release>-<chart>.
*/}}
{{- define "rook-mon-dns.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "rook-mon-dns.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "rook-mon-dns.labels" -}}
helm.sh/chart: {{ include "rook-mon-dns.chart" . }}
{{ include "rook-mon-dns.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "rook-mon-dns.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rook-mon-dns.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end }}

{{- define "rook-mon-dns.serviceAccountName" -}}
{{- include "rook-mon-dns.fullname" . }}-manager
{{- end }}
