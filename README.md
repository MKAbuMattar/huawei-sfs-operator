# huawei-sfs-operator

> Kubernetes operator that reconciles `SfsTurboInstance` Custom Resources into
> HuaweiCloud SFS Turbo file systems. Built with Kubebuilder.

[![CI](https://github.com/MKAbuMattar/huawei-sfs-operator/actions/workflows/ci.yaml/badge.svg)](https://github.com/MKAbuMattar/huawei-sfs-operator/actions/workflows/ci.yaml)
[![CodeQL](https://github.com/MKAbuMattar/huawei-sfs-operator/actions/workflows/codeql.yaml/badge.svg)](https://github.com/MKAbuMattar/huawei-sfs-operator/actions/workflows/codeql.yaml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

---

## What it does

Managing HuaweiCloud SFS Turbo file systems through Terraform or the console
requires keeping a state machine outside Kubernetes. This operator brings the
lifecycle into the cluster itself: declare a `SfsTurboInstance`, get a file
system; delete the CR, optionally tear it down.

Reclaim-policy semantics mirror PV/PVC for muscle memory:

- `Retain` (default) — CR deletion removes the finalizer but **does not**
  delete the SFS Turbo FS. Manual cleanup required.
- `Delete` — CR deletion calls the HuaweiCloud SFS Turbo DELETE API on the
  underlying FS. **Destroys all data. Irreversible. Opt-in only.**

## Quick start

```bash
# 1. Install via Helm (pulls the chart from ghcr.io)
helm install sfs-operator \
  oci://ghcr.io/mkabumattar/charts/sfs-operator \
  --version v0.3.0 \
  --namespace sfs-operator-system --create-namespace \
  --set huawei.projectId=<your-project-id> \
  --set huawei.region=<your-region>

# 2. Create your first SFS Turbo FS via CR
kubectl apply -f docs/EXAMPLES/basic-retain.yaml

# 3. Watch the FS get created
kubectl get sfsturboinstance -A -w
```

## Documentation

- **[ARCHITECTURE.md](docs/ARCHITECTURE.md)** — how the reconcile loop works,
  finalizer logic, status fields
- **[HUAWEI-AUTH.md](docs/HUAWEI-AUTH.md)** — IAM agency setup, AK/SK fallback,
  required IAM permissions
- **[ADDON-INTEGRATION.md](docs/ADDON-INTEGRATION.md)** — the package handoff
  guide for HuaweiCloud CCE addon teams
- **[EXAMPLES/](docs/EXAMPLES/)** — annotated CR samples for common scenarios
- **[CONTRIBUTING.md](CONTRIBUTING.md)** — how to send patches
- **[SECURITY.md](SECURITY.md)** — vulnerability disclosure

## CRD reference

`SfsTurboInstance.sfs.huaweicloud.com/v1alpha1`

```yaml
apiVersion: sfs.huaweicloud.com/v1alpha1
kind: SfsTurboInstance
metadata:
  name: data-volume
  namespace: my-app
spec:
  reclaimPolicy: Retain          # Retain (default) | Delete
  fsName: data-volume            # name as it appears in the HuaweiCloud console
  shareType: STANDARD            # STANDARD | PERFORMANCE
  shareProto: NFS
  size: 500                      # GiB
  availabilityZone: <your-az>
  vpcId: <your-vpc>
  subnetId: <your-subnet>
  securityGroupId: <your-sg>
  cryptKeyId: <your-kms-key>     # optional, server-side encryption
status:
  phase: Ready                   # Pending | Provisioning | Ready | Deleting | Failed
  fsId: <huaweicloud-fs-uuid>
  mountPoint: 192.0.2.10:/share/<id>
  conditions:
    - type: Ready
      status: "True"
```

## Repository layout

```
.
├── api/v1alpha1/                # CRD Go types
├── cmd/                         # manager entrypoint
├── config/                      # Kubebuilder scaffold (raw manifests)
├── deploy/helm/sfs-operator/    # Production Helm chart (the release artifact)
├── docs/                        # User-facing docs + examples
├── internal/
│   ├── controller/              # Reconcile loop
│   ├── huawei/                  # HuaweiCloud SFS Turbo API client
│   └── metrics/                 # Prometheus metrics
└── test/                        # e2e + integration suites
```

## Development

```bash
make build            # compile manager binary
make test             # unit tests (envtest)
make test-e2e         # e2e on local kind
make manifests        # regenerate CRD YAML from Go markers
make generate         # regenerate deepcopy
make docker-build     # build image (sets IMG var)
helm lint deploy/helm/sfs-operator
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
