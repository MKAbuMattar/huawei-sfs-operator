# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Initial public release of `huawei-sfs-operator`.
- `SfsTurboInstance` v1alpha1 CRD under `sfs.huaweicloud.com/v1alpha1`.
- Reconciler that creates / deletes HuaweiCloud SFS Turbo file systems.
- Reclaim policies: `Retain` (default) and `Delete`.
- HuaweiCloud authentication via Pod-attached IAM agency or AK/SK Secret.
- Helm chart at `deploy/helm/sfs-operator/` (single repo, no external deps).
- Prometheus metrics: reconcile counters, API latency, FS-phase gauge.
- GitHub Actions workflows: CI on PR, multi-arch release on tag, CodeQL.
- Apache-2.0 license.
