# sfs-operator Helm chart

Production-ready Helm chart for [huawei-sfs-operator](https://github.com/MKAbuMattar/huawei-sfs-operator).
Ships the controller Deployment, CRD, RBAC, agency-annotated ServiceAccount,
NetworkPolicy, PodDisruptionBudget, optional Prometheus ServiceMonitor, and
config for HuaweiCloud project ID, region, and credentials.

For architecture and reconcile-loop details see
[../../docs/ARCHITECTURE.md](../../docs/ARCHITECTURE.md). For installation
modes (IAM agency vs AK/SK Secret) see
[../../docs/HUAWEI-AUTH.md](../../docs/HUAWEI-AUTH.md).

## What this chart deploys

| Resource                                                         | Purpose                                                                                                                                            |
| ---------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Namespace`                                                      | Operator pod's home (cluster-scoped resources ignore this)                                                                                         |
| `CustomResourceDefinition/sfsturboinstances.sfs.huaweicloud.com` | The API the operator watches. ServerSideApply makes schema bumps non-destructive.                                                                  |
| `ServiceAccount`                                                 | Optionally carries `cce.huawei.com/agency` annotation → CCE injects short-lived AK/SK into the pod's metadata service. No static AK/SK in cluster. |
| `ClusterRole` + `ClusterRoleBinding`                             | Full lifecycle on `sfsturboinstances + /status + /finalizers` plus leases (leader-election) + events. Nothing else.                                |
| `ConfigMap` (env)                                                | `HUAWEI_PROJECT_ID` + `HUAWEI_REGION`. Operator fails fast at boot if either is empty.                                                             |
| `Deployment`                                                     | Single replica by default (override `replicaCount`), distroless, non-root, read-only-root-fs. Probes on `/healthz` + `/readyz`.                    |
| `Service` + `ServiceMonitor`                                     | Optional Prometheus scrape target (enable via `metrics.serviceMonitor.enabled=true`).                                                              |
| `NetworkPolicy`                                                  | Egress allow-list: DNS, K8s API, HuaweiCloud SFS Turbo API, metadata service.                                                                      |
| `PodDisruptionBudget`                                            | Created automatically when `replicaCount > 1`.                                                                                                     |

## Install

```bash
helm install sfs-operator \
  oci://ghcr.io/mkabumattar/charts/sfs-operator \
  --version v0.3.0 \
  --namespace sfs-operator-system --create-namespace \
  --set huawei.projectId=<your-project-id> \
  --set huawei.region=<your-region> \
  --set huawei.credentials.source=agency \
  --set huawei.iamAgency=<your-agency-name>
```

Or with a values file:

```bash
helm install sfs-operator oci://ghcr.io/mkabumattar/charts/sfs-operator \
  --version v0.3.0 \
  --namespace sfs-operator-system --create-namespace \
  -f my-values.yaml
```

## Verify

```bash
kubectl -n sfs-operator-system rollout status deploy/sfs-operator
kubectl -n sfs-operator-system logs deploy/sfs-operator --tail=50
# Expected: "Huawei client ready" region=<region> projectId=<id>

# CRD is registered
kubectl get crd sfsturboinstances.sfs.huaweicloud.com
kubectl explain sfsturboinstance.spec
```

## Common knobs

| Knob                             | Default                            | What it does                                                                       |
| -------------------------------- | ---------------------------------- | ---------------------------------------------------------------------------------- |
| `huawei.projectId`               | (required)                         | HuaweiCloud project where SFS Turbo lives                                          |
| `huawei.region`                  | (required)                         | Region code (e.g. `ap-southeast-1`)                                                |
| `huawei.credentials.source`      | `agency`                           | `agency` (CCE-attached) or `secret` (AK/SK in K8s Secret)                          |
| `huawei.iamAgency`               | ""                                 | When `source=agency`, the agency name attached via SA annotation                   |
| `huawei.credentials.secretName`  | ""                                 | When `source=secret`, the Secret holding `HUAWEI_ACCESS_KEY` + `HUAWEI_SECRET_KEY` |
| `replicaCount`                   | `1`                                | Bump to 2+ for HA (with leader election)                                           |
| `image.repository`               | `ghcr.io/mkabumattar/sfs-operator` | Override to mirror to a private registry                                           |
| `image.tag`                      | Chart `appVersion`                 | Pin to a specific operator build                                                   |
| `imagePullSecrets`               | `[]`                               | List of pull secrets for private registries                                        |
| `resources`                      | small (50m / 64Mi)                 | Increase for clusters with many CRs                                                |
| `metrics.serviceMonitor.enabled` | `false`                            | Create a Prometheus ServiceMonitor                                                 |
| `networkPolicy.enabled`          | `true`                             | Disable if your cluster doesn't use NetworkPolicies                                |

See [values.yaml](values.yaml) for the full reference.

## Tightening RBAC

The default ClusterRole grants only what the controller needs:
`sfsturboinstances + /status + /finalizers` (full lifecycle), `events` (write),
and `coordination.k8s.io/leases` (leader-election). Any extra verb is a
smell — review `templates/clusterrole.yaml` if you find a PR adding more.

## Uninstall

```bash
helm uninstall sfs-operator -n sfs-operator-system
# CRD is intentionally NOT deleted (would orphan existing CRs).
# To also delete the CRD (destroys all SfsTurboInstance objects — irreversible):
kubectl delete crd sfsturboinstances.sfs.huaweicloud.com
```
