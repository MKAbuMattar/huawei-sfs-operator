{{- define "operator.networkpolicy" -}}
{{/*
NetworkPolicies for sfs-operator-system (egress allow-list).

Default-deny on the namespace, then curated allows for what the
operator actually needs:

  Ingress
    - monitoring ns → :8443/metrics (Prometheus ServiceMonitor)
    - health probes from kubelet (default allowed by K8s — no NP rule)

  Egress
    - kube-system → :53 (CoreDNS)
    - K8s API server (10.247.0.1:443) — for watch + status writes +
      leader election Leases
    - Huawei API endpoints (port 443 to public internet via NAT) —
      SFS Turbo, KMS, VPC, IAM. NetworkPolicy can't match DNS names,
      so we allow all egress on 443 with the standard "not private
      RFC1918" exclusion.
    - 169.254.169.254 link-local — pod-agency metadata service.
      Required only when huawei.credentials.source: agency. Cheap
      to allow unconditionally.

Rendered only when networkPolicy.enabled (default true). Disable on
clusters that don't have an NP-enforcing CNI (e.g. dev experiments).
*/}}
{{- if .Values.networkPolicy.enabled | default true }}
---
# Baseline: deny everything in/out unless another NP allows it.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: sfs-operator-default-deny
  namespace: {{ .Values.namespace }}
  labels:
    {{- include "operator.labels" . | nindent 4 }}
spec:
  podSelector: {}
  policyTypes: [Ingress, Egress]
---
# DNS — every other egress depends on resolving names first.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: sfs-operator-allow-dns
  namespace: {{ .Values.namespace }}
  labels:
    {{- include "operator.labels" . | nindent 4 }}
spec:
  podSelector: {}
  policyTypes: [Egress]
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
      ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
---
# K8s API server — controller-runtime needs this for watch + status
# subresource patches + leader-election Lease writes.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: sfs-operator-allow-apiserver
  namespace: {{ .Values.namespace }}
  labels:
    {{- include "operator.labels" . | nindent 4 }}
spec:
  podSelector: {}
  policyTypes: [Egress]
  egress:
    # Most CCE clusters expose the apiserver via the K8s service
    # (10.247.0.1:443). The Service IP range is configurable; we
    # allow the standard CCE range (10.247.0.0/16 covers svc + cluster).
    - to:
        - ipBlock:
            cidr: {{ .Values.networkPolicy.apiserverCidr | default "10.247.0.0/16" | quote }}
      ports:
        - protocol: TCP
          port: 443
---
# Huawei API endpoints. NetworkPolicy can't match DNS; the SFS Turbo +
# KMS + VPC + IAM endpoints all live on Huawei's public IP space and
# we reach them via the cluster's NAT gateway. Allow all egress on 443
# EXCEPT to RFC1918 (the cluster's internal traffic stays inside the
# K8s API allow above).
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: sfs-operator-allow-huawei-api
  namespace: {{ .Values.namespace }}
  labels:
    {{- include "operator.labels" . | nindent 4 }}
spec:
  podSelector: {}
  policyTypes: [Egress]
  egress:
    - to:
        - ipBlock:
            cidr: 0.0.0.0/0
            except:
              - 10.0.0.0/8
              - 172.16.0.0/12
              - 192.168.0.0/16
      ports:
        - protocol: TCP
          port: 443
---
# Pod-agency metadata service. Required only when
# huawei.credentials.source: agency; harmless to allow always.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: sfs-operator-allow-metadata
  namespace: {{ .Values.namespace }}
  labels:
    {{- include "operator.labels" . | nindent 4 }}
spec:
  podSelector: {}
  policyTypes: [Egress]
  egress:
    - to:
        - ipBlock:
            cidr: 169.254.169.254/32
      ports:
        - protocol: TCP
          port: 80
---
# Prometheus scrape — ingress from monitoring namespace to :8443.
# Rendered only when metrics scraping is on (otherwise no Service
# exists to receive the traffic anyway).
{{- if and .Values.metrics.enabled .Values.metrics.serviceMonitor.enabled }}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: sfs-operator-allow-metrics-scrape
  namespace: {{ .Values.namespace }}
  labels:
    {{- include "operator.labels" . | nindent 4 }}
spec:
  podSelector:
    matchLabels:
      {{- include "operator.selectorLabels" . | nindent 6 }}
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: monitoring
      ports:
        - protocol: TCP
          port: {{ .Values.metrics.port }}
{{- end }}
{{- end }}
{{- end -}}
