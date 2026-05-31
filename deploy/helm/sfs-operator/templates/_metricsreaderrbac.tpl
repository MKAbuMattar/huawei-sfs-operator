{{- define "operator.metricsreaderrbac" -}}
{{/*
Metrics RBAC. Two pieces, both required for kube-prometheus-stack to
successfully scrape the operator's /metrics endpoint while
controller-runtime's `--metrics-secure=true` (default) is in effect:

  1. system:auth-delegator → sfs-operator SA
     The operator's auth filter does *delegated* authentication: it
     forwards incoming bearer tokens to the K8s API's TokenReview
     endpoint for validation. That requires the operator SA to be
     able to `create tokenreviews` + `create subjectaccessreviews`,
     which is exactly what `system:auth-delegator` grants.

  2. sfs-operator-metrics-reader → Prometheus SA
     Once the operator validates the incoming token, it then checks
     whether the caller has `get` on the `/metrics` nonResourceURL
     via a SubjectAccessReview. This ClusterRole grants exactly that
     to the Prometheus SA.

Both render only when metrics scraping is on (metrics.enabled +
serviceMonitor.enabled). Standard kubebuilder pattern.
*/}}
{{- if and .Values.metrics.enabled .Values.metrics.serviceMonitor.enabled }}
# ─── 1. operator SA → tokenreviews / subjectaccessreviews ─────────────
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: sfs-operator-auth-delegator
  labels:
    {{- include "operator.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:auth-delegator
subjects:
  - kind: ServiceAccount
    name: sfs-operator
    namespace: {{ .Values.namespace }}
---
# ─── 2. Prometheus SA → /metrics nonResourceURL ───────────────────────
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: sfs-operator-metrics-reader
  labels:
    {{- include "operator.labels" . | nindent 4 }}
rules:
  - nonResourceURLs:
      - /metrics
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: sfs-operator-metrics-reader
  labels:
    {{- include "operator.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: sfs-operator-metrics-reader
subjects:
  - kind: ServiceAccount
    name: {{ .Values.metrics.prometheusServiceAccount.name | default "kube-prometheus-stack-prometheus" }}
    namespace: {{ .Values.metrics.prometheusServiceAccount.namespace | default "monitoring" }}
{{- end }}
{{- end -}}
