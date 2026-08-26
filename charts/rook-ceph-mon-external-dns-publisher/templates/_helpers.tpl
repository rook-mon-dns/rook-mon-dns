{{/*
Chart name (truncated to 63 chars, trailing dashes trimmed).
*/}}
{{- define "rook-ceph-mon-external-dns-publisher.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name: <release>-<chart>.
*/}}
{{- define "rook-ceph-mon-external-dns-publisher.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "rook-ceph-mon-external-dns-publisher.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "rook-ceph-mon-external-dns-publisher.labels" -}}
helm.sh/chart: {{ include "rook-ceph-mon-external-dns-publisher.chart" . }}
{{ include "rook-ceph-mon-external-dns-publisher.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "rook-ceph-mon-external-dns-publisher.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rook-ceph-mon-external-dns-publisher.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end }}

{{- define "rook-ceph-mon-external-dns-publisher.serviceAccountName" -}}
{{- include "rook-ceph-mon-external-dns-publisher.fullname" . }}-manager
{{- end }}
