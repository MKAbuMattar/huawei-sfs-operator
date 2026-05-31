{{- define "operator.service" -}}
{{/*
Service exposing the operator's metrics endpoint. Renders only when
metrics are enabled (defaults to off; toggle via metrics.serviceMonitor.enabled).

NodePort/LoadBalancer is not needed — kube-prometheus-stack scrapes
in-cluster via the ServiceMonitor below.
*/}}
{{- if .Values.metrics.enabled }}
apiVersion: v1
kind: Service
metadata:
  name: sfs-operator-metrics
  namespace: {{ .Values.namespace }}
  labels:
    {{- include "operator.labels" . | nindent 4 }}
spec:
  type: ClusterIP
  ports:
    - name: metrics
      port: {{ .Values.metrics.port }}
      targetPort: metrics
      protocol: TCP
  selector:
    {{- include "operator.selectorLabels" . | nindent 4 }}
{{- end }}
{{- end -}}
