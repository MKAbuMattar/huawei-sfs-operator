# SfsTurboInstance examples

Six runnable scenarios covering the common patterns. Each file is a complete
`SfsTurboInstance` manifest — pick the one closest to your need and replace
the `<your-…>` placeholders.

| Example | What it shows | When to use |
|---|---|---|
| **[01-minimal-retain.yaml](01-minimal-retain.yaml)** | Smallest practical CR. 500 GiB STANDARD, Retain. | Most production data volumes |
| **[02-ephemeral-delete.yaml](02-ephemeral-delete.yaml)** | Delete reclaim — FS gone on CR deletion. | CI / preview envs / scratch space |
| **[03-performance-tier.yaml](03-performance-tier.yaml)** | PERFORMANCE tier, 4 TiB, KMS encryption. | ML training, high-throughput batch |
| **[04-hardened-with-custom-sg.yaml](04-hardened-with-custom-sg.yaml)** | Pre-created tight SG, KMS, compliance tags. | Regulated data, prod best-practice |
| **[05-with-pvc-binding.yaml](05-with-pvc-binding.yaml)** | Auto-creates PV + PVC + mounts in a Pod. | Standard app deployments |
| **[06-paused-reconcile.yaml](06-paused-reconcile.yaml)** | `pausedReconcile: true` for maintenance. | Manual ops on the underlying FS |

## Common placeholders

All examples reference these — get them from your HuaweiCloud setup:

| Placeholder | Where it comes from |
|---|---|
| `<your-az>` | HuaweiCloud availability zone code (e.g. `ap-southeast-1a`). Must match where the CCE cluster's nodes run. |
| `<your-vpc-id>` | VPC ID. Console → VPC → Virtual Private Cloud, copy the `vpc-...` ID. |
| `<your-subnet-id>` | Subnet ID within that VPC. Must be in the same AZ as `<your-az>` for in-VPC mounts to work. |
| `<your-tight-sg-id>` | (Example 04) A pre-created SG with NFS ports open from your Pod CIDR only. |
| `<your-kms-cmk-id>` | (Examples 03, 04) KMS Customer Master Key ID for server-side FS encryption. |

## Quickstart

```bash
# Pick example 01 as the starting point
curl -fsSL https://raw.githubusercontent.com/MKAbuMattar/huawei-sfs-operator/main/docs/EXAMPLES/01-minimal-retain.yaml \
  -o my-fs.yaml

# Edit placeholders
$EDITOR my-fs.yaml

# Apply
kubectl apply -f my-fs.yaml

# Watch it provision
kubectl get sfsturboinstance -A -w

# Inspect status
kubectl describe sfsturboinstance app-data
```

## CRD field reference

For the full field list with kubebuilder validation markers, see
[`api/v1alpha1/sfsturboinstance_types.go`](../../api/v1alpha1/sfsturboinstance_types.go).

## Notes on field defaults

- `reclaimPolicy` → defaults to `Retain` (safer)
- `shareType` → defaults to `STANDARD`
- `shareProtocol` → defaults to `NFS`
- `securityGroupId: ""` → HuaweiCloud auto-creates a per-FS SG
- `autoCreateSgRules: true` → operator may add NFS rules to the SG
- `pausedReconcile` → defaults to `false`
- `enhanced` → defaults to `false` (only meaningful with `shareType: PERFORMANCE`)
- `pvcName: ""` → PV/PVC auto-creation OFF; consumer manages mount-side
  resources separately
