{{- define "operator.poddisruptionbudget" -}}
{{/*
PodDisruptionBudget — keep at least 1 operator pod up during voluntary
disruptions (node drain, kubelet upgrades, descheduler). Only renders
when replicaCount > 1; a single-replica deployment with a PDB of
minAvailable=1 would block ALL disruptions, which isn't what we want
on a single-replica setup.

Controller-runtime's leader election survives the disruption: the
surviving pod grabs the lease lock when the drained pod's lease
expires (default LeaseDuration 15s). Reconciles continue.
*/}}
{{- if gt (int (.Values.replicaCount | default 1)) 1 }}
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: sfs-operator
  namespace: {{ .Values.namespace }}
  labels:
    {{- include "operator.labels" . | nindent 4 }}
spec:
  minAvailable: 1
  selector:
    matchLabels:
      {{- include "operator.selectorLabels" . | nindent 6 }}
{{- end }}
{{- end -}}
