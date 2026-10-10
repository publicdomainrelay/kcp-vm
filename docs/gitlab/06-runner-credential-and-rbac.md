# 06 - Credential and RBAC for the runner, as actually run

[02](02-runner-kubernetes.md) has the shape. This is the version that was run against a real
cluster, with the parts that bite named.

Two things, and they are separate:

- a CLIENT CERTIFICATE the runner authenticates with
- RBAC in the tenant cluster that certificate's group is bound to

They must agree. When they do not, every job fails with a 403 that reads as a broken runner.

## 1. Mint a scoped credential

Not the superuser. `system:admin` in `system:masters` is cluster-admin on the tenant cluster and
makes every rule below decorative.

```bash
SOCIALWEB_TENANTCREDENTIAL_STATE_DIR=/var/lib/socialweb \
SOCIALWEB_TENANTCREDENTIAL_CLUSTER=ci \
SOCIALWEB_TENANTCREDENTIAL_SERVER="$(kubectl --kubeconfig <admin> get cluster ci -o jsonpath='{.status.url}')" \
SOCIALWEB_TENANTCREDENTIAL_COMMON_NAME=gitlab-runner \
SOCIALWEB_TENANTCREDENTIAL_ORGANIZATIONS=gitlab-runner \
SOCIALWEB_TENANTCREDENTIAL_OUT_DIR=/etc/gitlab-runner/tenant-ci \
SOCIALWEB_TENANTCREDENTIAL_VALID_FOR=720h \
  ./socialweb-tenantcredential
```

Writes three files, mode 0600:

| File | What |
| --- | --- |
| `ca.crt` | cluster's serving CA, what the apiserver presents |
| `tls.crt` | runner's client cert. CommonName is the username, Organizations are the groups |
| `tls.key` | the private key |

`SERVER` is `status.url`, which is `https://127.0.0.1:<leased port>`. Port is per cluster.

Tool refuses to overwrite an existing set. Rotating is moving the old files aside first.

**A scoped identity can only come from the ClientCA on disk.** The OpenBao-backed path's PKI role
pins the organization to `system:masters`. Refused `COMMON_NAME`/`ORGANIZATIONS` is that
distinction, not a typo. In Go the scoped entry point is
`clusterprovider.MintTenantCredentialForClusterAs`, which signs directly and so dodges the vault.

## 2. Permit that group in the tenant cluster

Apply to the TENANT cluster. The namespace first - the runner does not create it.

```bash
kubectl --kubeconfig <tenant-admin> apply -f - <<'YAML'
apiVersion: v1
kind: Namespace
metadata:
  name: gitlab-runner
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: gitlab-runner
rules:
  - apiGroups: [""]
    resources: ["pods", "pods/exec", "pods/attach", "pods/log"]
    verbs: ["create", "delete", "get", "list", "watch", "patch", "update"]
  - apiGroups: [""]
    resources: ["secrets", "configmaps", "services", "serviceaccounts", "events", "persistentvolumeclaims"]
    verbs: ["create", "delete", "get", "list", "watch", "update"]
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["create", "delete", "get", "list", "update"]
  - apiGroups: ["policy"]
    resources: ["poddisruptionbudgets"]
    verbs: ["create", "delete", "get", "list", "update"]
  - apiGroups: ["scheduling.k8s.io"]
    resources: ["priorityclasses"]
    verbs: ["get", "create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: gitlab-runner
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: gitlab-runner
subjects:
  - kind: Group
    name: gitlab-runner
    apiGroup: rbac.authorization.k8s.io
YAML
```

`Group` is the `ORGANIZATIONS` value from step 1. Change one, change the other.

`priorityclasses` is cluster-scoped, so a namespaced Role cannot grant it. Drop
`apps/deployments` and `scheduling.k8s.io/priorityclasses` if not using pause pods.

## 3. Check it before blaming the runner

Certificate carries the group:

```bash
openssl x509 -in /etc/gitlab-runner/tenant-ci/tls.crt -noout -subject
# subject=O = gitlab-runner, CN = gitlab-runner
```

Certificate authenticates and the group is bound:

```bash
kubectl --kubeconfig <tenant-admin> auth can-i create pods \
  --namespace gitlab-runner \
  --as=gitlab-runner --as-group=gitlab-runner
# yes
```

Reads that mean something:

| Reading | Meaning |
| --- | --- |
| `Unauthorized` at job start | cert is from another cluster's ClientCA, or apiserver `ca_file` is wrong |
| `Forbidden` creating pods | the binding's Group does not match the cert's Organizations |
| Job hangs at "Preparing the environment" | `pods/exec` unreachable. Almost always the runner points at the kcp mount URL, not loopback |
| Job passes, retry fails | credential expired |

## 4. Point the runner at it

```toml
  [runners.kubernetes]
    host = "https://127.0.0.1:<leased port>"
    cert_file = "/etc/gitlab-runner/tenant-ci/tls.crt"
    key_file = "/etc/gitlab-runner/tenant-ci/tls.key"
    ca_file = "/etc/gitlab-runner/tenant-ci/ca.crt"
    namespace = "gitlab-runner"
```

Rest of the file: [05](05-runner-config.md).
