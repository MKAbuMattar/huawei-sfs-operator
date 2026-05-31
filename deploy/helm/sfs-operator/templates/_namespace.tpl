{{- define "operator.namespace" -}}
{{/*
Namespace for the operator. ArgoCD's CreateNamespace=true sync-option
also creates it but renders here so a non-ArgoCD `helm install` works too.
*/}}
apiVersion: v1
kind: Namespace
metadata:
  name: {{ .Values.namespace }}
  labels:
    {{- include "operator.labels" . | nindent 4 }}
  annotations:
    argocd.argoproj.io/sync-wave: "-20"
{{- end -}}
