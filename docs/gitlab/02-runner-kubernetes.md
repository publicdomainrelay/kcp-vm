# 02 - Point a GitLab Runner at a tenant cluster (kubernetes executor)

The runner creates one Pod per CI job through the Kubernetes API. It does not create nodes; the
tenant cluster's own scale decision does that when it sees a Pod that cannot be placed. This
document gets the runner connected, authenticated and permitted.

Read [README.md](README.md#reachability-the-single-operational-fact) first. The runner must run on
the kcp host, because the tenant apiserver binds loopback there.

Verified on this host: the credential minting, the apiserver URL, the RBAC and the runner's
connection to the cluster. Not run here: the GitLab instance that hands out the runner token.

## 1. A tenant cluster to point at

Create one through the gateway (XRPC `controlPlaneUp`) or by running the composition root. The
cluster exists before its first node does; that is the point of the design.

```bash
kubectl --kubeconfig <admin kubeconfig> get clusters
```

The admin kubeconfig is the one the control plane writes; it never leaves the provider's own
hands and is never handed to a runner (invariant 20).

## 2. Mint the runner's credential

A runner authenticates with a client certificate signed by **that cluster's** ClientCA. It is
valid only against that cluster. Do not hand a runner the provider's admin credential, and do not
hand it `system:masters`.

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

`VALID_FOR` is optional and defaults to the mint's own validity. Set it deliberately: this is a
client certificate with no revocation path, so its lifetime is how long a leaked copy is useful.
A runner credential wants a shorter one than the mint's default, and rotating it is a re-run after
the old files are moved aside, because the tool refuses to overwrite.

That writes three files at mode 0600:

| File | What it is |
| --- | --- |
| `ca.crt` | the cluster's serving CA - what the apiserver presents at that URL |
| `tls.crt` | the runner's client certificate; the CommonName is the username, the Organizations are the groups |
| `tls.key` | the matching private key |

The tool refuses to overwrite an existing set. Rotating a credential is a deliberate act, not a
re-run.

`SERVER` must be the apiserver's own address, `https://127.0.0.1:<port>`, which is exactly what
`status.url` holds. The port is leased per cluster; there is no constant to hardcode.

### Why a scoped identity, and not the superuser

The cluster's own credential path also offers a superuser identity - CommonName `system:admin`,
Organization `system:masters`. **Do not give that to a runner.** It is cluster-admin on the tenant
cluster, it makes every RBAC rule below decorative, and it is exactly the "wider credential" the
provider refuses to reach for when its ClientCA is unreadable.

Two different entry points produce the two identities, and they are not interchangeable:

| Identity | Where it comes from |
| --- | --- |
| `system:admin` / `system:masters` | the default cluster credential path, including the OpenBao-backed one |
| any CommonName / Organizations | the ClientCA on disk, signed directly |

A scoped identity can only come from the disk signer, because the OpenBao path's PKI role pins the
organization to `system:masters`. If `COMMON_NAME` or `ORGANIZATIONS` is refused, that is this
distinction and not a typo: the tool mints only what the path can actually issue, and refuses
rather than handing back a certificate whose groups the RBAC below does not match.

## 3. Permit the runner in the tenant cluster

The runner needs to create Pods in one namespace, read their logs, exec into them, and manage the
Secrets and Services it creates for each job. Apply this against the tenant cluster - the
`socialweb-tenantcredential` output above is one way to authenticate; the provider's own
credential is another.

Most of this is namespaced. The exception is `priorityclasses`, which is cluster-scoped: a
namespaced Role cannot grant it, and a missing grant is a common startup failure.

```yaml
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
```

The `Group` subject is the `ORGANIZATIONS` value from step 2. If the two disagree, every job fails
with a 403 that reads like a broken runner rather than a mis-bound certificate, which is why the
minting tool refuses a mismatch.

`apps/deployments` and `scheduling.k8s.io/priorityclasses` are only needed by the pre-warmed pause
pods in [04](04-ccm-and-autoscaling.md#pause-pods-and-why-they-fight-scale-to-zero). Drop them if you do not use those.

## 4. `config.toml`

```toml
concurrent = 1

[[runners]]
  name = "socialweb-tenant-ci"
  url = "https://gitlab.example.com/"
  token = "<runner authentication token>"
  executor = "kubernetes"
  [runners.kubernetes]
    # https://127.0.0.1:<leased port>. The port is per cluster and is not a constant:
    # read it from `status.url` on the Cluster, do not copy this example's number.
    host = "https://127.0.0.1:16443"
    cert_file = "/etc/gitlab-runner/tenant-ci/tls.crt"
    key_file = "/etc/gitlab-runner/tenant-ci/tls.key"
    ca_file = "/etc/gitlab-runner/tenant-ci/ca.crt"
    namespace = "gitlab-runner"
    image_pull_policy = "if-not-present"
    cpu_request = "500m"
    memory_request = "512Mi"
    cpu_limit = "2"
    memory_limit = "2Gi"
    helper_cpu_limit = "250m"
    helper_memory_limit = "250Mi"
    poll_timeout = 600
    cleanup_grace_period_seconds = 30
```

- `host` is the cluster's `status.url`. There is no default that works: auto-discovery is for a
  runner running inside the cluster, which this one is not.
- `concurrent = 1` until [04](04-ccm-and-autoscaling.md) lands. A second concurrent job with one
  node waits, which is correct behaviour and not a bug in the runner.
- `cpu_request`/`memory_request` are what the scale decision and, later, the autoscaler size
  against. They are the runner's declaration of what a job needs. Set them from a real job, not
  from a guess.
- The build and helper containers share one Pod's quota, so `cpu_limit` must cover both.

## 5. Register and run

`gitlab-runner` is not installed on this host; obtain it from `packages.gitlab.com`, which answers
from here.

```bash
gitlab-runner register \
  --non-interactive \
  --config /etc/gitlab-runner/config.toml \
  --url https://gitlab.example.com/ \
  --token <runner authentication token> \
  --executor kubernetes
```

Create the runner in the GitLab UI first and copy the token from there; the legacy
registration-token flow is deprecated. Run it as a service under a user that can read
`/etc/gitlab-runner/tenant-ci/`, or pass the directory in as a secret.

## 6. Verify

Apply this in the tenant cluster and watch the node appear:

```bash
kubectl --kubeconfig <admin> get nodes --watch
```

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: placement-check
  namespace: gitlab-runner
spec:
  restartPolicy: Never
  containers:
    - name: check
      image: busybox:1.36
      command: ["sh", "-c", "echo placed on $(hostname)"]
      resources:
        requests:
          cpu: 100m
          memory: 64Mi
```

Sequence, and each step is observable:

1. The Pod is Pending: there is no node to place it on.
2. `internal/scale` sees intent and decides `Provision`.
3. A guest boots and joins; `kubectl get nodes` shows one node.
4. The Pod runs on it.
5. Past the hold with nothing running, the node is reaped and `kubectl get nodes` is empty again,
   while the apiserver keeps answering and `status.phase` stays `Ready`.

That is the four-step proof this repo already gates on, with a GitLab-shaped Pod in place of the
Deployment. To run the real pipeline instead, see [03](03-ci-pipeline.md).

## Failure modes, in the order you will meet them

| Symptom | Cause |
| --- | --- |
| Job stays `pending`, no node appears | The runner reached a *different* apiserver, or the Pod was created outside a namespace the observer watches. Check `kubectl get pods -A` against the cluster's own port. |
| `x509: certificate signed by unknown authority` | Wrong `ca_file`, or a `host` port that belongs to another cluster. Ports are leased per cluster and are not shared. |
| `Unauthorized` at job start | The client certificate is from another cluster's ClientCA, or the Organizations do not match the RBAC group. |
| `Forbidden` creating pods | The ClusterRoleBinding subject does not match the certificate's Organizations. |
| Job hangs at "Preparing the environment" | `pods/exec` is not reachable. This is what happens if the runner is pointed at the kcp mount URL instead of the direct loopback URL. |
| Job passes, then a retry fails | The credential expired. The minting tool sets a long validity by default; check `ValidFor`. |
