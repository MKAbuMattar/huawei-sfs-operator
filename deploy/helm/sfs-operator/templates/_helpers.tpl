{{/*
Common labels — every rendered object carries these. The "operator name"
piece is `.Values.operatorName` (default `.Chart.Name`) so the consumer
can keep label-stable identity even if the umbrella chart is renamed.
*/}}
{{- define "operator.labels" -}}
{{- $opName := .Values.operatorName | default .Chart.Name -}}
app.kubernetes.io/name: {{ $opName }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: {{ $opName }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: controller
{{- if .Chart }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end -}}

{{/*
Selector labels — narrower set used in Deployment.spec.selector and
Service.spec.selector. Must NOT include version-y labels so a chart
upgrade doesn't break the selector match.
*/}}
{{- define "operator.selectorLabels" -}}
{{- $opName := .Values.operatorName | default .Chart.Name -}}
app.kubernetes.io/name: {{ $opName }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller
{{- end -}}
