# HuaweiCloud authentication

The operator needs to call the HuaweiCloud SFS Turbo API. It supports two
authentication modes:

| Mode                          | When to use                                                | Trust boundary                                                                                                            |
| ----------------------------- | ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| **IAM Agency** (Pod-attached) | Running on HuaweiCloud CCE                                 | CCE attaches short-lived credentials to the Pod via the metadata service. No long-lived secrets on disk. **Recommended.** |
| **AK/SK Secret**              | Running on non-CCE clusters (kind, on-prem, another cloud) | Long-lived AK/SK pair in a Kubernetes Secret. The Pod reads it once at startup.                                           |

## Required IAM permissions

The agency or AK/SK user needs the following actions on the SFS Turbo
service in the target project:

```
sfs:turbo:list      # GET /v1/{project_id}/sfs-turbo/shares
sfs:turbo:get       # GET /v1/{project_id}/sfs-turbo/shares/{id}
sfs:turbo:create    # POST /v1/{project_id}/sfs-turbo/shares
sfs:turbo:delete    # DELETE /v1/{project_id}/sfs-turbo/shares/{id}
```

Minimal policy JSON for an IAM custom policy:

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

Do **not** attach `SFS Turbo FullAccess` — that grants extra verbs the
operator never uses (resize, share-policy modification, etc.).

## Mode 1 — IAM Agency (recommended)

On HuaweiCloud CCE, the recommended pattern is:

1. **Create an IAM Agency** in the HuaweiCloud console:
   - Type: "Common account / Cloud service"
   - Cloud service: `CCE`
   - Permissions: attach the custom policy above
   - Name: e.g. `sfs-operator-agency`

2. **Annotate the operator's ServiceAccount** with the agency name:

   ```yaml
   apiVersion: v1
   kind: ServiceAccount
   metadata:
     name: sfs-operator
     namespace: sfs-operator-system
     annotations:
       cce.huawei.com/agency: "sfs-operator-agency"
   ```

   The Helm chart does this automatically when you set:

   ```yaml
   huawei:
     iamAgency: sfs-operator-agency
   ```

3. **At runtime**, the operator's HuaweiCloud client calls the CCE metadata
   service at `http://169.254.169.254/openstack/latest/securitykey` every
   ~15 min to refresh short-lived credentials. No secrets touch disk.

## Mode 2 — AK/SK Secret (non-CCE)

For kind / on-prem / other-cloud Kubernetes clusters:

1. **Create an IAM user** in HuaweiCloud with the custom policy above.
2. **Generate a permanent AK/SK pair** for that user (HuaweiCloud console →
   IAM → My Credentials → Access Keys).
3. **Create the Secret** in the operator's namespace:
   ```bash
   kubectl create namespace sfs-operator-system
   kubectl -n sfs-operator-system create secret generic huawei-creds \
     --from-literal=HUAWEI_ACCESS_KEY=<AK> \
     --from-literal=HUAWEI_SECRET_KEY=<SK>
   ```
4. **Configure the chart** to use Secret-based auth:
   ```yaml
   huawei:
     credentials:
       source: secret
       secretName: huawei-creds
   ```

The operator reads the Secret keys as env vars (`HUAWEI_ACCESS_KEY`,
`HUAWEI_SECRET_KEY`) into the controller container. **Rotate the AK/SK at
least every 90 days** — delete the old AK in HuaweiCloud after creating a
new one in the Secret.

## Endpoint configuration

The HuaweiCloud SFS Turbo API endpoint is regional. Pass your region via
the chart:

```yaml
huawei:
  region: ap-southeast-1 # or your region
  projectId: <project-uuid>
```

The operator constructs the endpoint as
`https://sfs-turbo.<region>.myhuaweicloud.com`.

## Network policy

The chart's NetworkPolicy template (`templates/_networkpolicy.tpl`) allows
egress only to:

- DNS (UDP/TCP 53) to `kube-dns`
- The Kubernetes API server (TCP 443 / 6443 to control-plane pods)
- HuaweiCloud SFS Turbo API endpoints (TCP 443, both IPv4 + IPv6)
- IAM metadata service `169.254.169.254` (TCP 80) when agency mode is on

If you run a stricter cluster-wide NetworkPolicy that denies all egress
by default, ensure the operator's namespace allows the above.

## Troubleshooting

### "401 Unauthorized" on every API call

- Agency mode: verify the SA annotation and that the agency exists in the
  HuaweiCloud console. Check the metadata service is reachable from the
  Pod (`kubectl exec -it ... -- curl http://169.254.169.254/openstack/latest/`).
- Secret mode: verify the Secret has both `HUAWEI_ACCESS_KEY` and
  `HUAWEI_SECRET_KEY`. Check the AK is still active in HuaweiCloud IAM
  (deleted AKs return 401, not 403).

### "403 Forbidden" on create / delete

The user/agency is authenticated but lacks the verb. Double-check the
custom policy JSON above; the most common omission is `sfs:turbo:create`.

### Operator logs `connection refused` to `169.254.169.254`

The metadata service is unreachable. Either:

- You're not on CCE (use Secret mode instead), OR
- A NetworkPolicy is blocking egress (allow port 80 to `169.254.169.254/32`)
