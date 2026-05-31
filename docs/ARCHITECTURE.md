# Architecture

This document describes how `huawei-sfs-operator` reconciles
`SfsTurboInstance` CRs into HuaweiCloud SFS Turbo file systems. It targets
developers reading the code for the first time and operators debugging
reconcile behaviour.

## Components

```
┌────────────────────────────────────────────────────────────────────┐
│                    Kubernetes cluster (CCE or any K8s)             │
│                                                                    │
│   ┌─────────────────────┐         ┌──────────────────────────┐    │
│   │ User-applied        │ watch   │  sfs-operator manager    │    │
│   │ SfsTurboInstance CR ├────────►│  (this binary)           │    │
│   └─────────────────────┘         │                          │    │
│                                   │   1. read spec           │    │
│                                   │   2. ensure finalizer    │    │
│                                   │   3. call Huawei API     │    │
│                                   │   4. patch status        │    │
│                                   └──────────┬───────────────┘    │
│                                              │                    │
└──────────────────────────────────────────────┼────────────────────┘
                                               │ HTTPS (SFS Turbo API)
                                               ▼
                                  ┌─────────────────────────┐
                                  │ HuaweiCloud SFS Turbo   │
                                  │ region.myhuaweicloud.com│
                                  └─────────────────────────┘
```

## Reconcile loop (controller)

State machine for a single `SfsTurboInstance`:

```
            ┌─────────┐   spec.fsId == ""
            │  (none) │──────────────────────┐
            └─────────┘                       ▼
                                       ┌──────────────┐
                                       │   Pending    │  add finalizer
                                       └──────┬───────┘
                                              ▼
                                       ┌──────────────┐
                                       │ Provisioning │  POST /sfs-turbo-shares
                                       └──────┬───────┘
                                              ▼
                                       ┌──────────────┐
                                       │    Ready     │  status.fsId set
                                       └──────┬───────┘
                                              │ CR deleted
                                              ▼
                                       ┌──────────────┐
                              ┌────────│  Deleting    │
                              │        └──────────────┘
                              │ reclaimPolicy=Delete
                              │ → DELETE /sfs-turbo-shares/<id>
                              │ reclaimPolicy=Retain
                              │ → drop finalizer immediately
                              ▼
                          (CR gone)
```

Key invariants:

- **The CR is the source of truth for `spec`. The HuaweiCloud API is the
  source of truth for `status.fsId` + `status.mountPoint`.**
- Reconcile is idempotent: it's safe to call repeatedly with the same input.
- A reconcile that fails to reach the HuaweiCloud API requeues with
  exponential backoff (1s → 32s cap).
- A reconcile that gets a 4xx from the API treats the FS as terminally bad
  and marks `status.phase=Failed`. It does not requeue forever.

## Finalizer

The operator adds the finalizer `sfs.huaweicloud.com/finalizer` to every CR
on first reconcile. This guarantees:

- On `kubectl delete sfsturboinstance ...`, the CR enters a `Terminating`
  state but stays in the API until the operator processes the deletion.
- The operator can call `DELETE /sfs-turbo-shares/<id>` before the CR is
  fully removed — no leaked file systems if a user deletes the CR while
  the operator is mid-reconcile.

The finalizer is removed once:

- For `reclaimPolicy=Delete`: the HuaweiCloud API returns 204 (deleted) or
  404 (already gone) on the underlying FS.
- For `reclaimPolicy=Retain`: immediately on first reconcile of the
  deletion request. The FS stays alive on HuaweiCloud; cleaning it up is
  the user's responsibility.

## Status fields

| Field               | Source                        | Meaning                                                                     |
| ------------------- | ----------------------------- | --------------------------------------------------------------------------- |
| `status.phase`      | controller                    | One of `Pending`, `Provisioning`, `Ready`, `Deleting`, `Failed`             |
| `status.fsId`       | HuaweiCloud `id`              | UUID returned by SFS Turbo API on create                                    |
| `status.mountPoint` | HuaweiCloud `export_location` | `<ip>:/share/<id>` NFS mount path                                           |
| `status.message`    | controller                    | Human-readable last action / error (sanitized — no API stack traces)        |
| `status.conditions` | controller                    | Standard K8s conditions (`Ready`, `Provisioning`) with `lastTransitionTime` |

## HuaweiCloud authentication

See [HUAWEI-AUTH.md](HUAWEI-AUTH.md). Two supported modes:

1. **Pod-attached IAM agency** (preferred on CCE): the operator's SA carries
   a `cce.huawei.com/agency` annotation. CCE's metadata service returns
   short-lived AK/SK + securityToken at `169.254.169.254/openstack/latest/`.
2. **AK/SK Secret**: for non-CCE clusters, an `huawei-creds` Secret with
   `HUAWEI_ACCESS_KEY` + `HUAWEI_SECRET_KEY` mounted as env vars.

The auth provider abstraction is in `internal/huawei/auth.go`.

## Metrics

Prometheus metrics exposed on `:8443/metrics` (TLS by default, via the
controller-runtime metrics server). Key series:

| Metric                                   | Type      | Labels                                         |
| ---------------------------------------- | --------- | ---------------------------------------------- |
| `huawei_sfs_operator_reconcile_total`    | counter   | `result={success,error,requeue}`               |
| `huawei_sfs_operator_huawei_api_seconds` | histogram | `operation={list,get,create,delete}`, `result` |
| `huawei_sfs_operator_fs_phase`           | gauge     | `phase`, `namespace`, `name`                   |

ServiceMonitor template ships in the Helm chart at
`deploy/helm/sfs-operator/templates/_servicemonitor.tpl`. Enable via
`metrics.serviceMonitor.enabled=true` in your values.

## Logging

Structured JSON via `logr` + zap (controller-runtime defaults). Each
reconcile carries:

- `controller` (always `sfsturboinstance`)
- `name` + `namespace` (the CR)
- `reconcileID` (UUID per reconcile invocation, useful when correlating
  with HuaweiCloud API logs)
- `phase` (current state at start of reconcile)

In production, set `--log-level=info`. For debugging, `--log-level=debug`
dumps the full HuaweiCloud API request/response (with secrets masked).

## Testing strategy

- **Unit tests** (`go test ./...`) — fake HuaweiCloud client, envtest for
  the K8s API. Fast, no network.
- **Controller tests** (`internal/controller/*_test.go`) — exercise the
  reconcile loop against envtest + a mock Huawei client.
- **E2E** (`test/e2e/`) — kind cluster, full reconcile cycle, can optionally
  hit a real HuaweiCloud account if creds are provided.

## Performance

The reconciler is single-worker by default. For clusters managing >100
SfsTurboInstance CRs, bump `--max-concurrent-reconciles=N` via the chart's
`controller.workers` value. The HuaweiCloud SFS Turbo API rate-limit is
~30 req/s per project; the operator respects it via a token-bucket
limiter in `internal/huawei/client.go`.
