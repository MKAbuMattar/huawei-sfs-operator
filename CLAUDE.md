# CLAUDE.md

Guidance for [Claude Code](https://claude.com/claude-code) and other Claude
agents working in this repository.

## What this project is

Kubernetes operator (Go + Kubebuilder) that reconciles `SfsTurboInstance`
custom resources into HuaweiCloud SFS Turbo file systems.

**One-line elevator:** `kubectl apply -f sfsturboinstance.yaml` → an SFS Turbo
file system exists on HuaweiCloud; delete the CR → the FS is either retained
(default) or destroyed (opt-in).

## Repository layout (where to look first)

| Path                                                 | Purpose                                                          |
| ---------------------------------------------------- | ---------------------------------------------------------------- |
| `api/v1alpha1/sfsturboinstance_types.go`             | CRD spec & status types (kubebuilder markers)                    |
| `cmd/main.go`                                        | Manager entrypoint                                               |
| `internal/controller/sfsturboinstance_controller.go` | The reconcile loop                                               |
| `internal/huawei/`                                   | HuaweiCloud SFS Turbo API client + auth providers                |
| `internal/metrics/metrics.go`                        | Prometheus metrics                                               |
| `config/`                                            | Kubebuilder scaffold (used by `make install/deploy` for testing) |
| `deploy/helm/sfs-operator/`                          | **The release artifact.** Production Helm chart.                 |
| `docs/`                                              | User docs (README, ARCHITECTURE, HUAWEI-AUTH, ADDON-INTEGRATION) |
| `test/e2e/`                                          | Kind-based e2e tests                                             |

## Commands

```bash
make manifests generate     # regenerate CRD YAML + deepcopy after editing api/
make test                   # unit tests (envtest)
make test-e2e               # e2e on local kind
make build                  # compile the manager binary to bin/manager
make lint                   # golangci-lint
make docker-build IMG=...   # build container image
helm lint deploy/helm/sfs-operator
helm template sfs-operator deploy/helm/sfs-operator -n sfs-operator-system
```

## Conventions

- **Go 1.25+**, format with `gofmt`, lint with `golangci-lint` (config in
  `.golangci.yml`).
- **Errors:** wrap with `fmt.Errorf("context: %w", err)`. Don't lose the
  original.
- **Status updates:** always via `Status().Patch(...)` (not `Update`) to
  avoid resource-version races on concurrent reconciles.
- **Logging:** structured `logr` keys, not `fmt.Sprintf`. Standard keys:
  `controller`, `namespace`, `name`, `reconcileID`, `phase`.
- **Comments:** explain _why_, not _what_. Names should already say _what_.
- **No organization-specific references.** This repo is public
  and vendor-neutral. If you find any leftover internal names, treat it as
  a bug — fix it.

## Releases

A git tag `v<X.Y.Z>` triggers `.github/workflows/release.yaml`:

1. Builds multi-arch image (linux/amd64 + arm64)
2. Pushes to `ghcr.io/<owner>/sfs-operator:v<X.Y.Z>` + `:latest`
3. Cosign-signs the image (keyless via OIDC)
4. Generates SBOM
5. Packages + pushes the Helm chart to `oci://ghcr.io/<owner>/charts/sfs-operator`
6. Creates a GitHub Release with changelog excerpt

The tag MUST equal `v<Chart.yaml version>`. The release workflow enforces
this — bumping `Chart.yaml` and the tag are inseparable steps.

## When asked to add a feature

1. **Open the CRD types first** (`api/v1alpha1/sfsturboinstance_types.go`).
   New fields go on `Spec` (or `Status` for read-only data). Add kubebuilder
   markers for validation, defaults, and printer columns.
2. **Run `make manifests generate`** to regenerate the CRD YAML +
   deepcopy.
3. **Implement the controller logic** in `internal/controller/`.
4. **Add a controller test** in the same dir; use `envtest` + the mock
   Huawei client in `internal/huawei/podagency_test.go` as a template.
5. **Run `make test`** before committing.
6. **Update [CHANGELOG.md](CHANGELOG.md)** under `## [Unreleased]`.
7. **Helm chart:** if the feature exposes a knob, add it to
   `deploy/helm/sfs-operator/values.yaml` with a comment.

## When asked to fix a bug

1. **Reproduce in a test first.** A failing test that passes after the fix
   is worth more than a regression.
2. Keep the fix minimal — no opportunistic refactors in the same commit.
3. Update [CHANGELOG.md](CHANGELOG.md) under `## [Unreleased]` →
   `### Fixed`.

## What not to do

- **Don't commit organization-specific names back into this repo.** This is
  a public, vendor-neutral project. If asked to add organization-internal
  references, push back and suggest doing it in a fork instead.
- **Don't bypass the chart for deploy.** The chart in `deploy/helm/` is the
  contract with the HuaweiCloud CCE addon team. Anything that affects the
  rendered StatefulSet/Deployment/RBAC needs to go through the chart, not
  through cluster-side patches.
- **Don't add long-lived secrets in code.** Auth flows through either the
  IAM agency path (recommended on CCE) or a Secret reference.
- **Don't add a SECURITY.md disclosure target other than the GitHub Security
  Advisory feature.** Avoids putting personal email addresses in a public
  repo.

## License & copyright

Apache-2.0. Headers say `Copyright YYYY The huawei-sfs-operator Authors.`
Don't introduce personal copyright lines — community contributions stay
attributed via git history, not file headers.
