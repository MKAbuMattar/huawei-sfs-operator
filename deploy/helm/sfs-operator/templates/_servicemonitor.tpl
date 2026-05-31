{{- define "operator.servicemonitor" -}}
{{/*
ServiceMonitor for kube-prometheus-stack. Picks up the metrics Service
(rendered above) and scrapes /metrics every 30s. Metrics surface:

  huawei_sfs_operator_reconcile_total{namespace, result}
    success / error_transient / requeued

  huawei_sfs_operator_huawei_api_seconds_bucket{op, result}
    op=create|get|delete  result=ok|error

  huawei_sfs_operator_fs_phase{namespace, name, phase}
    phase = Ready | Provisioning | Deleting | Failed

Plus controller-runtime's built-in metrics (workqueue depth, reconcile
duration, etc.).

PrometheusRules (alerts) live in the cluster admin's monitoring stack
alerts/sfs-operator.yaml so they version with the rest of the alert
catalog.
*/}}
{{- if and .Values.metrics.enabled .Values.metrics.serviceMonitor.enabled }}
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: sfs-operator
  namespace: {{ .Values.metrics.serviceMonitor.namespace | default .Values.namespace }}
  labels:
    {{- include "operator.labels" . | nindent 4 }}
    # Match kube-prometheus-stack's serviceMonitorSelector — keep this
    # in sync with the chart's `prometheusOperator.serviceMonitorSelector`
    # config (defaults to release=kube-prometheus-stack in a typical install).
    release: kube-prometheus-stack
spec:
  selector:
    matchLabels:
      {{- include "operator.selectorLabels" . | nindent 6 }}
  namespaceSelector:
    matchNames:
      - {{ .Values.namespace }}
  endpoints:
    - port: metrics
      interval: 30s
      scrapeTimeout: 10s
      path: /metrics
      # controller-runtime serves /metrics over HTTPS with a self-signed
      # cert in v1. A future release will wire cert-manager for a trusted cert; until
      # then Prometheus skips verification. SA auth is still enforced
      # (the bearer token below proves we're the prometheus SA).
      scheme: https
      tlsConfig:
        insecureSkipVerify: true
      bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
{{- end }}
{{- end -}}
