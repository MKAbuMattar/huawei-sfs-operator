# HuaweiCloud CCE Addon — Integration Guide

This document gives the HuaweiCloud CCE addon team everything they need to
package `huawei-sfs-operator` as a CCE addon. It mirrors the structure
HuaweiCloud uses for community-contributed addons.

If you're a user installing the operator, see [README.md](../README.md)
instead.

---

## 1. Addon metadata

| Field                          | Value                                                                               |
| ------------------------------ | ----------------------------------------------------------------------------------- |
| **Addon name**                 | `sfs-operator`                                                                      |
| **Display name**               | SFS Turbo Operator                                                                  |
| **Category**                   | Storage                                                                             |
| **Description**                | Declarative lifecycle for SFS Turbo file systems via the `SfsTurboInstance` CRD     |
| **Supported CCE K8s versions** | v1.28, v1.29, v1.30, v1.31, v1.32, v1.33, v1.34, v1.35                              |
| **Operator image**             | `ghcr.io/mkabumattar/sfs-operator:<version>` (multi-arch: linux/amd64, linux/arm64) |
| **Helm chart**                 | `oci://ghcr.io/mkabumattar/charts/sfs-operator:<version>`                           |
| **License**                    | Apache-2.0                                                                          |
| **Source**                     | https://github.com/MKAbuMattar/huawei-sfs-operator                                  |
| **Maintainer**                 | huawei-sfs-operator authors (community)                                             |
| **Stability**                  | beta (v0.x) — API may have breaking changes before v1.0                             |

## 2. Chart entry point + user-tweakable values

The chart lives at `deploy/helm/sfs-operator/` in the repo. CCE's addon UI
should expose the following values to end users (everything else can stay
on chart defaults):

| Helm key                         | Type   | Required              | Description                                                            |
| -------------------------------- | ------ | --------------------- | ---------------------------------------------------------------------- |
| `huawei.projectId`               | string | ✅                    | HuaweiCloud project ID where SFS Turbo lives                           |
| `huawei.region`                  | string | ✅                    | Region code (e.g. `ap-southeast-1`)                                    |
| `huawei.credentials.source`      | enum   | ✅                    | `agency` (recommended on CCE) \| `secret`                              |
| `huawei.iamAgency`               | string | when `source=agency`  | Name of the IAM agency attached to the SA                              |
| `huawei.credentials.secretName`  | string | when `source=secret`  | K8s Secret name with `HUAWEI_ACCESS_KEY` + `HUAWEI_SECRET_KEY`         |
| `replicaCount`                   | int    | optional, default `1` | Bump to 2+ for HA with leader election                                 |
| `metrics.serviceMonitor.enabled` | bool   | optional              | Create a Prometheus ServiceMonitor (requires prometheus-operator CRDs) |
| `resources`                      | map    | optional              | CPU/memory requests + limits                                           |
| `image.tag`                      | string | optional              | Override operator image version                                        |

A sensible CCE addon UI might surface only `huawei.iamAgency`, `projectId`,
`region`, and `replicaCount` — collapse the rest into "Advanced".

## 3. Required cluster RBAC

The operator's ClusterRole (`deploy/helm/sfs-operator/templates/clusterrole.yaml`)
requests:

```yaml
- apiGroups: ["sfs.huaweicloud.com"]
  resources:
    [sfsturboinstances, sfsturboinstances/status, sfsturboinstances/finalizers]
  verbs: [get, list, watch, create, update, patch, delete]
- apiGroups: [""]
  resources: [events]
  verbs: [create, patch]
- apiGroups: [""]
  resources: [secrets]
  resourceNames: [<chart-rendered secret name>]
  verbs: [get]
```

No cluster-admin. No broad Secret access — only the specific
`huawei-creds` Secret (named via values) when `credentials.source=secret`.

## 4. Required HuaweiCloud IAM permissions

For the agency / user that the operator runs as:

```json
{
  "Version": "1.1",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "sfs:turbo:list",
        "sfs:turbo:get",
        "sfs:turbo:create",
        "sfs:turbo:delete"
      ]
    }
  ]
}
```

CCE addon installation flow should:

1. Prompt the user for an existing agency name, OR
2. Offer to create the agency + policy in one click (the CCE console
   already does this pattern for other addons like `everest`).

## 5. Health probes

The operator exposes:

| Endpoint   | Port                        | Probe type | Threshold                            |
| ---------- | --------------------------- | ---------- | ------------------------------------ |
| `/healthz` | 8081 (metrics-bind-address) | liveness   | failureThreshold=3, periodSeconds=30 |
| `/readyz`  | 8081                        | readiness  | failureThreshold=3, periodSeconds=10 |
| `/metrics` | 8443 (with TLS)             | scrape     | scrape interval per ServiceMonitor   |

CCE's addon health-check polling should target `/readyz`.

## 6. Upgrade strategy

The addon contract:

- **Minor bumps (v0.X.Y → v0.X.Z)** — drop-in upgrade, no CRD schema change,
  no user action needed beyond bumping the addon version.
- **Minor bumps (v0.X → v0.Y)** — may include CRD field additions (always
  optional). The chart's CRD template applies via `kubectl apply
--server-side`; CCE's addon machinery should do the same.
- **Major bumps (v0 → v1)** — promise: at v1.0, the v1alpha1 → v1 group
  migration will ship a conversion webhook + migration script. Until then,
  v0.x is the only supported track.

Test matrix maintained in CI: each release goes through the e2e workflow
on the K8s versions listed in §1. The CCE addon team can use the GitHub
Actions matrix as the canonical compatibility statement.

## 7. Telemetry / metrics for CCE console

Recommend surfacing these in the CCE console addon dashboard:

| Metric (PromQL)                                                                     | Label     | Suggested panel                         |
| ----------------------------------------------------------------------------------- | --------- | --------------------------------------- |
| `sum by (phase) (huawei_sfs_operator_fs_phase)`                                     | phase     | Doughnut: Ready / Provisioning / Failed |
| `rate(huawei_sfs_operator_reconcile_total{result!="success"}[5m])`                  | result    | Time series — alert when > 0            |
| `histogram_quantile(0.99, rate(huawei_sfs_operator_huawei_api_seconds_bucket[5m]))` | operation | p99 SFS Turbo API latency               |
| `huawei_sfs_operator_reconcile_total{result="success"}` rate                        | —         | Reconcile throughput                    |

PrometheusRule samples ship alongside the chart as commented blocks in
`deploy/helm/sfs-operator/templates/_servicemonitor.tpl` — copy them into
your monitoring stack's rule set if you don't already have an
operator-error alert policy.

## 8. Log format

Structured JSON via controller-runtime + zap. Each line has:

```json
{
  "ts": "2026-05-31T12:34:56.789Z",
  "level": "info",
  "msg": "reconciling",
  "controller": "sfsturboinstance",
  "namespace": "my-app",
  "name": "data-volume",
  "reconcileID": "...",
  "phase": "Ready"
}
```

CCE console's log view should filter by `controller=sfsturboinstance` to
isolate operator logs from anything else in the namespace.

## 9. Contact

- Upstream issues: https://github.com/MKAbuMattar/huawei-sfs-operator/issues
- Security disclosure: see [SECURITY.md](../SECURITY.md)
- For the addon integration partnership specifically — please open an
  issue tagged `addon-integration` on the upstream repo, or reach out
  via the maintainer's profile email.

## 10. What we'd like from HuaweiCloud

In return for upstreaming this operator, the contributing maintainer would
appreciate:

- **Confirmation** that the SFS Turbo IAM action names above are correct
  and stable (a release-notes commitment).
- **Visibility** in the CCE addon catalog under "Storage" once integration
  is done.
- **A test channel** — a dev-tier HuaweiCloud account or sandbox CCE cluster
  that lets the e2e CI validate against real SFS Turbo endpoints (not just
  mocked).
- **Feedback** on any CCE-specific behaviour the operator should add — e.g.
  integration with CCE's storage class for dynamic PV provisioning, or
  emitting Huawei-specific events in the format expected by the CCE
  console.

Maintainership can transfer to a HuaweiCloud-owned GitHub org if that
suits the addon ownership model better.
