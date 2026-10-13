# 01 - The kcp server

End state: a host running kcp, one tenant cluster at Ready, nodes sized for the job, and a runner
credential in hand. Nothing here needs GitLab.

Read [README.md](README.md#reachability-the-single-operational-fact) first: the apiserver binds
loopback, so GitLab has to be able to reach loopback on this host.

## 1. Run the control plane

The gateway is the `socialweb-computer` binary under its own lingering account; the host needs no
`/dev/kvm`, no `/dev/net/tun` and no container runtime. Install it with `scripts/deploy.sh` (see the
repository README, "Run it"): it runs `socialweb-computer install gateway` the first time and
`upgrade` after, and waits until the web app answers `/api/config`.

Check it as the account:

```bash
systemctl --user status socialweb-computer-gateway.service
curl -fsS http://127.0.0.1:8788/api/config
```

## 2. The tenant cluster

The cluster exists before its first node does. That is the point of the design.

```bash
kubectl --kubeconfig <admin> get clusters
kubectl --kubeconfig <admin> get cluster <name> -o jsonpath='{.status.url}'
```

That URL is `https://127.0.0.1:<leased port>`, and the port is per cluster. It is what goes in the
runner's `host`, and it is what the runner's certificate is signed against.

The admin kubeconfig never leaves the provider's hands. It is not what a runner gets.

## 3. Size the nodes

A node boots at 2 vCPU, 2048 MiB, 16 GiB disk. Fits a probe, not a toolchain. Four numbers, three
places:

| What | Where | Why |
| --- | --- | --- |
| `vcpu`, `memMiB` | the node image's builder config | boot shape belongs to the image |
| `rootfs_headroom_mib` | the node image's builder config | sizes the ROOT filesystem, where containerd lives |
| `SOCIALWEB_CLUSTERPROVIDER_NODE_DISK_MIB` | clusterprovider env | per node, at provision time |

`vcpu` and `memMiB` fold into the image's fingerprint, so changing either builds a NEW image under
a NEW name. The old one keeps its name and is never asked for again.

### The root filesystem bites before the disk

The job's image and its build outputs go in the ROOT filesystem, not the node disk. The node disk
is attached and mounted READ-ONLY as the identity disk, so it cannot hold k3s state and
`NODE_DISK_MIB` does not help here.

Root filesystem size is `du(stage) + rootfs_headroom_mib`, and it is cloned per node. Too small and
an image pull dies:

```
failed to pull and unpack image ... no space left on device
```

That is the ROOT filesystem, and it arrives as a Pod that never starts. For a build job:

```json
{
  "vcpu": 12,
  "memMiB": 24576,
  "rootfs_headroom_mib": 55000
}
```

```bash
socialweb-nodeimage -config /path/to/nodeimage.json
```

Measured: at the default headroom that pull dies; at `55000` the root filesystem is
58,401,488,896 bytes and it completes.

For a scale check, where five nodes boot at once, five of the above is 60 vCPU and 120 GiB and does
not fit a workstation:

```json
{
  "vcpu": 2,
  "memMiB": 3072,
  "rootfs_headroom_mib": 1500
}
```

and drop the Pod requests to `100m`/`128Mi`, see [02](02-gitlab-runner.md#the-file).

### The state directory has to be on real disk

Node disks and root filesystems are large files. Where `/tmp` is a tmpfs that is RAM:

```bash
TMPDIR=/var/tmp/kcp    # anywhere on the host's real disk
```

A state directory that cannot hold a node's disks fails as a full filesystem partway through a
boot, not as a configuration error.

### What does NOT go in the image

A file of about 2 GiB or more cannot be staged into a node image - `mkfs.ext4 -d` refuses it with
`Ext2 file too big`. Measured: a 3.3 GB tarball is refused at 6 GB and at 12 GB, while a 3.5 GB
file of zeros is accepted in a 4.9 GB filesystem, so it tracks the file and not the size. Full
table in `docs/plans/0004` section 7. Anything that big is pulled by the guest instead, see
[03](03-jobs.md#what-the-guest-does-and-does-not-pull).

### Check the host before booting anything

```bash
socialweb-computer check-host -role gateway -state-dir <state dir>
```

It exits 1 naming every check that fails, each with its fix (`docs/host-contract.md`).

## 4. Mint the runner's credential

A runner authenticates with a CLIENT CERTIFICATE signed by that cluster's ClientCA. Valid only
against that cluster.

Not the superuser. `system:admin` in `system:masters` is cluster-admin on the tenant cluster and
makes every RBAC rule below decorative.

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

Three files at mode 0600:

| File | What |
| --- | --- |
| `ca.crt` | the cluster's serving CA - what the apiserver presents at that URL |
| `tls.crt` | the runner's client cert. CommonName is the username, Organizations are the groups |
| `tls.key` | the private key |

`VALID_FOR` is optional and defaults to the mint's own validity. This credential has no revocation
path, so its lifetime is how long a leaked copy is useful. Rotating is moving the old files aside
first: the tool refuses to overwrite.

**A scoped identity can only come from the ClientCA on disk.** The OpenBao-backed path's PKI role
pins the organization to `system:masters`, so a refused `COMMON_NAME` or `ORGANIZATIONS` is that
distinction and not a typo. In Go the scoped entry point is
`clusterprovider.MintTenantCredentialForClusterAs`, which signs directly and so dodges the vault.

## 5. Permit that group in the tenant cluster

Apply against the TENANT cluster. The namespace first - the runner does not create it.

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

`Group` is the `ORGANIZATIONS` value from step 4. Change one, change the other. When they disagree
every job fails with a 403 that reads as a broken runner.

`priorityclasses` is cluster-scoped, so a namespaced Role cannot grant it. `apps/deployments` and
`scheduling.k8s.io/priorityclasses` are only needed by the pre-warmed pause pods in
[03](03-jobs.md#pause-pods-and-why-they-fight-scale-to-zero).

Check both halves before blaming the runner:

```bash
openssl x509 -in /etc/gitlab-runner/tenant-ci/tls.crt -noout -subject
# subject=O = gitlab-runner, CN = gitlab-runner

kubectl --kubeconfig <tenant-admin> auth can-i create pods \
  --namespace gitlab-runner --as=gitlab-runner --as-group=gitlab-runner
# yes
```

## Hand-back

What [02](02-gitlab-runner.md) needs from this document:

- the apiserver URL, `https://127.0.0.1:<leased port>`
- `/etc/gitlab-runner/tenant-ci/{ca.crt,tls.crt,tls.key}`
- the group name, `gitlab-runner`
- the namespace, `gitlab-runner`

## GitLab on its own VM does not work

A GitLab on a separate host cannot reach the tenant apiserver, because the apiserver binds loopback
on the kcp host. The runner reports connection timeouts, and no runner configuration fixes it.

Making it work means the kcp host publishing an authenticated front for the apiserver. That is a
deliberate trust-boundary change, not a config step: the gateway refuses to bind anything but
loopback on purpose and the entrypoint publishes exactly one port, whose only upstream is the
gateway. Decide it deliberately.
