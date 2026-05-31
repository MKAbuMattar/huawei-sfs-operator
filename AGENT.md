# AGENT.md

Guidance for AI coding agents (Cursor, Aider, Cline, OpenHands, etc.)
working in this repository. This file is the universal counterpart to
[CLAUDE.md](CLAUDE.md) — same rules, smaller surface, framework-agnostic.

## Project at a glance

- **Type:** Kubernetes operator (Go + Kubebuilder framework)
- **Function:** reconciles `SfsTurboInstance` CRs into HuaweiCloud SFS Turbo
  file systems
- **CRD group/version:** `sfs.huaweicloud.com/v1alpha1`
- **License:** Apache-2.0
- **Release artifact:** Helm chart at `deploy/helm/sfs-operator/`, container
  image at `ghcr.io/mkabumattar/sfs-operator`

## Read first

Before making changes, skim these in order:

1. [README.md](README.md) — what the operator does, quick-start, CRD shape
2. [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — reconcile loop, finalizer,
   state machine
3. [CONTRIBUTING.md](CONTRIBUTING.md) — code style, commit conventions, tests
4. [CLAUDE.md](CLAUDE.md) — extended guidance with do's and don'ts

## Code map

```
api/v1alpha1/         CRD types (source of truth for `make manifests`)
cmd/main.go           manager entrypoint
internal/
  controller/         the reconcile loop
  huawei/             HuaweiCloud SFS Turbo API client + auth
  metrics/            Prometheus metrics
config/               kubebuilder scaffold (CRDs, RBAC, samples)
deploy/helm/          production Helm chart
docs/                 user-facing documentation
test/                 e2e + integration tests
```

## How to run things

```bash
make test              # unit tests
make test-e2e          # e2e on kind
make manifests         # regenerate CRD YAML
make generate          # regenerate deepcopy
make build             # compile binary
make docker-build      # build image
helm lint deploy/helm/sfs-operator
```

## Commit & PR conventions

- [Conventional Commits](https://www.conventionalcommits.org/): `feat:`,
  `fix:`, `docs:`, `chore:`, `refactor:`, `test:`
- Branch names: `feat/<topic>`, `fix/<topic>`
- One concern per PR; no opportunistic refactors mixed with fixes
- Tests required for new logic; coverage stays flat-or-up
- Update [CHANGELOG.md](CHANGELOG.md) under `[Unreleased]` for user-visible
  changes

## Rules of the road

- **Vendor-neutral, no organization-specific names.** If you find any
  organization-prefixed identifier, it's a bug — remove it.
- **Status updates use `Status().Patch`,** never `Update`, to avoid
  resource-version races.
- **Errors wrap with `%w`,** not `%v`. Preserve the chain.
- **Logging is structured.** Use `logr` keys; never `fmt.Sprintf` into
  the message.
- **No long-lived secrets in code.** Auth comes from IAM agency
  (preferred on CCE) or a referenced Kubernetes Secret. Never hardcode.
- **The Helm chart is the public contract.** Anything that changes
  rendered manifests (Deployment, RBAC, CRD) goes through
  `deploy/helm/sfs-operator/`, not cluster-side patches.

## Release flow

1. Bump `version:` in `deploy/helm/sfs-operator/Chart.yaml`
2. Update [CHANGELOG.md](CHANGELOG.md) — move `[Unreleased]` entries under
   a new `[X.Y.Z] - YYYY-MM-DD` heading
3. Commit, tag `vX.Y.Z`, push
4. GitHub Actions builds + signs + publishes image + chart

## Security

See [SECURITY.md](SECURITY.md). Do not commit credentials. Do not put
personal email addresses anywhere in this repo — use GitHub Security
Advisories for private vuln reports.
