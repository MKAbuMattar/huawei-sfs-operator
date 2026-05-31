# Contributing to huawei-sfs-operator

Thanks for considering a contribution! This project welcomes issues and pull
requests from anyone running Kubernetes on HuaweiCloud who wants to manage SFS
Turbo file systems declaratively.

## Quick start

```bash
git clone https://github.com/MKAbuMattar/huawei-sfs-operator.git
cd huawei-sfs-operator
make manifests generate         # regenerate CRDs + deepcopy if you touched api/
make test                       # unit tests
make docker-build IMG=local:dev # verify Dockerfile builds
helm lint deploy/helm/sfs-operator
```

## How to propose a change

1. **Open an issue first** for anything bigger than a typo. Brief is fine — we
   just want to avoid two people implementing the same idea.
2. **Branch from `main`**, name like `feat/<topic>` or `fix/<topic>`.
3. **Commit messages** follow [Conventional Commits](https://www.conventionalcommits.org/):
   `feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`.
4. **Tests** — new logic needs a test. We use `envtest` for controller tests
   and `kind`-based e2e for the full reconcile cycle.
5. **Run `make lint`** before pushing. Our `.golangci.yml` is the source of
   truth.
6. **Open a PR** against `main`. CI must pass before merge.

## Project layout

| Path                        | Contents                                                                   |
| --------------------------- | -------------------------------------------------------------------------- |
| `api/v1alpha1/`             | CRD Go types (`SfsTurboInstance`). Run `make generate` after edits.        |
| `cmd/`                      | `manager` entrypoint                                                       |
| `config/`                   | Kubebuilder scaffold (CRDs, RBAC, samples). Used by `make install/deploy`. |
| `deploy/helm/sfs-operator/` | Production Helm chart. The release artifact.                               |
| `docs/`                     | User-facing docs                                                           |
| `internal/controller/`      | Reconcile loop                                                             |
| `internal/huawei/`          | HuaweiCloud SFS Turbo API client                                           |
| `internal/metrics/`         | Prometheus metrics                                                         |
| `test/e2e/`                 | End-to-end tests (kind + envtest)                                          |

## Code style

- Go 1.25+, formatted with `gofmt` (enforced by `golangci-lint`).
- Errors wrapped with `fmt.Errorf("context: %w", err)`.
- Reconciler functions return `(ctrl.Result, error)`; status updates via
  `Status().Patch()` to avoid resource-version races.
- Public-facing field comments are kubebuilder markers + user-facing
  descriptions; private internal comments explain "why", not "what".

## Reporting bugs

Use the [issue tracker](https://github.com/MKAbuMattar/huawei-sfs-operator/issues).
Include:

- Operator version (`kubectl -n sfs-operator-system describe deploy sfs-operator | grep Image`)
- Kubernetes version (`kubectl version --short`)
- CCE cluster type (CCE Standard / CCE Turbo) if HuaweiCloud-hosted
- Minimal `SfsTurboInstance` YAML reproducing the issue
- Operator logs (`kubectl -n sfs-operator-system logs deploy/sfs-operator`)

## Security

Please **do not** open public issues for security vulnerabilities. See
[SECURITY.md](SECURITY.md) for the responsible-disclosure process.

## License

By contributing, you agree your contributions will be licensed under the
Apache License 2.0 (see [LICENSE](LICENSE)).
