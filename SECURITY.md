# Security Policy

## Supported versions

| Version          | Supported                                                        |
| ---------------- | ---------------------------------------------------------------- |
| `v0.x` (current) | ✅ — pre-1.0, security fixes land on `main` and the latest minor |

We aim to cut a patched release within 7 days of a confirmed vulnerability for
the current minor.

## Reporting a vulnerability

**Do not** open a public GitHub issue. Instead use one of these channels:

1. **GitHub Security Advisory** (preferred): open a private advisory at
   <https://github.com/MKAbuMattar/huawei-sfs-operator/security/advisories/new>.
2. **Email**: contact via the email listed on the maintainer's GitHub profile.

Include:

- A description of the issue and its impact
- Step-by-step reproduction (operator version, CR YAML, observed behavior)
- Whether you've notified anyone else

We'll acknowledge within 72 hours, provide a fix-or-update plan within 7 days,
and credit you in the release notes (unless you'd prefer otherwise).

## Threat model

The operator runs with permissions to:

1. Watch / patch `SfsTurboInstance` custom resources in the cluster
2. Call the HuaweiCloud SFS Turbo API as either:
   - the Pod's attached IAM agency, OR
   - an AK/SK pair read from a Kubernetes Secret (`huawei-creds`)

Realistic attack vectors we worry about:

| Vector                                                                  | Mitigation                                                                                                                                                                     |
| ----------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| AK/SK exfiltration via Secret read                                      | Operator reads the Secret once at startup; doesn't expose it in env or logs. NetworkPolicy in chart restricts egress to HuaweiCloud API endpoints + DNS + k8s API server only. |
| CR-injection of malicious `fsName` triggering API errors that leak data | All HuaweiCloud API responses are passed through a sanitizer before becoming `status.message`; raw stack traces stay in logs.                                                  |
| Reclaim-policy abuse causing data deletion                              | Default reclaim policy is `Retain`. `Delete` is opt-in per CR.                                                                                                                 |
| RBAC over-permissioning                                                 | Chart's `ClusterRole` requests only the verbs the controller actually uses. No cluster-admin.                                                                                  |

## Out of scope

- Misconfiguration where the operator is granted broader RBAC than the chart
  recommends.
- HuaweiCloud IAM policy issues where the agency is granted more than
  `sfs:turbo:*`.
- Supply-chain attacks on dependencies before they reach our `go.mod` —
  please report those upstream.
