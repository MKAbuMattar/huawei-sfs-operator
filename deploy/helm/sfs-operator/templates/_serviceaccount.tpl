{{- define "operator.serviceaccount" -}}
{{/*
ServiceAccount for the operator pod. The cce.huawei.com/agency annotation
is the magic that wires this pod to the IAM agency provisioned by
Terraform in your IAM agency Terraform/console config.
CCE intercepts the pod's 169.254.169.254 calls and returns short-lived
AK/SK signed for the agency's roles (SFS Turbo FullAccess, KMS CMKUser,
VPC ReadOnlyAccess).
*/}}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: sfs-operator
  namespace: {{ .Values.namespace }}
  labels:
    {{- include "operator.labels" . | nindent 4 }}
  annotations:
    cce.huawei.com/agency: {{ .Values.agencyName | quote }}
    argocd.argoproj.io/sync-wave: "-15"
{{- if .Values.imagePullSecret }}
imagePullSecrets:
  - name: {{ .Values.imagePullSecret }}
{{- end }}
{{- end -}}
