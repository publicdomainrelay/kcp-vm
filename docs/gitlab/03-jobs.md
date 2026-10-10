# 03 - Jobs

What a job runs in, what it can ask for, and how many run at once.

Needs [02](02-gitlab-runner.md) done: a runner pointed at a tenant cluster.

## The image a job runs in

A job Pod's image comes from the project's own `.gitlab-ci.yml`:

```yaml
  image: "$DOCKER_REPO:builder-$DOCKERFILE_HASH"
```

`DOCKER_REPO` is a variable in that file. `DOCKERFILE_HASH` comes from the included
`.ci/dockerfile-hash.yaml`, which is generated:

```bash
printf "variables:\n  DOCKERFILE_HASH: `git hash-object docker/build.Dockerfile`" > .ci/dockerfile-hash.yaml
```

So the image is named by a hash of the Dockerfile. Change the Dockerfile, get a different image
name, and the old one keeps its own name forever.

### Check the image exists BEFORE running anything

Do not find this as `ImagePullBackOff` twenty minutes into a node boot. The manifest endpoint needs
a token even for a public repo:

```bash
REPO=streamplace/streamplace
TAG=builder-3e14c389b80a359787d73741f221fdfaacb12f53

TOKEN=$(curl -s "https://ghcr.io/token?scope=repository:${REPO}:pull&service=ghcr.io" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

curl -s -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer ${TOKEN}" \
  -H 'Accept: application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.v2+json' \
  "https://ghcr.io/v2/${REPO}/manifests/${TAG}"
```

`200` means it will pull. `404 MANIFEST_UNKNOWN` means the job cannot start, whatever else is right
about the cluster.

### The case that actually happens

A project can name an image nobody publishes. Measured: a branch named
`public.ecr.aws/m4j3c0j7/streamplace:builder-4dc3e0c...`, and that registry answers
`NAME_UNKNOWN` for the repository ITSELF. Nothing published it and no job in the project built it,
so every job in that file was unrunnable as written. Upstream's own `next` branch pointed
`DOCKER_REPO` at ghcr and published `builder-<hash>` there.

**Read the upstream branch's `variables:` block before concluding an image is missing.** A stale
branch and a missing image look identical from the failure.

If the branch you must run names a tag nobody published, in order of cost:

1. **Move the branch.** Rebase onto the branch that publishes and use its own
   `.ci/dockerfile-hash.yaml`. Its `DOCKER_REPO` and hash already agree with each other. Usually
   free.
2. **Build it and host it.** `docker build --target builder -f docker/build.Dockerfile .` at the
   hash the branch names, push to a registry the GUEST can reach, point `DOCKER_REPO` at it. The
   guest has outbound internet, so a public registry works and a private address does not without a
   route and a registries.yaml.

An image that resolves is not the same as a toolchain that builds. Measured: a five-month-old
builder image had a Rust that predates let-chains, so `make dev-setup` died on
`error[E0658]: let expressions in this position are unstable`, and a corepack that made no `pnpm`
shims, so `pnpm install` died on `pnpm: No such file or directory`.

### What the guest does and does not pull

- The guest HAS outbound internet. Seed-free pulls work: `registry.gitlab.com/gitlab-org/gitlab-runner-helper`
  and `rancher/mirrored-library-busybox` both arrived over the network.
- `image_pull_policy = "if-not-present"` is still right: the node image seeds some images into the
  guest's containerd, and always-pull throws that away.
- **A big image cannot be seeded into the node image.** `mkfs.ext4 -d` refuses a file of about
  2 GiB or more, and a multi-gigabyte builder image is 3.3 GB as a tarball. Pull it instead, and
  size the root filesystem for it - see [01](01-kcp-server.md#the-root-filesystem-bites-before-the-disk).

## The smallest pipeline that proves the integration

Two stages of nothing, and it tells you whether every hop works:

```yaml
stages: [prove]

prove-placement:
  stage: prove
  image: busybox:1.36
  script:
    - echo "running on $(hostname)"
    - echo "namespace $(cat /var/run/secrets/kubernetes.io/serviceaccount/namespace)"
```

What it exercises, in order: the runner authenticates to the apiserver; a Pod is created in the
`gitlab-runner` namespace; the cluster observes intent and provisions a node; the Pod is placed; the
build container runs; the helper container streams the log back through the runner.

If this passes, the integration works. If it hangs at "Preparing the environment", the runner is
pointed at the kcp mount URL instead of loopback - see
[README.md](README.md#reachability-the-single-operational-fact).

To watch the placement from outside, apply the Pod shape the executor actually creates and watch
`spec.nodeName`:

```bash
kubectl --kubeconfig <tenant-admin> get pods -n gitlab-runner -o wide -w
kubectl --kubeconfig <tenant-admin> get nodes -w
```

## Jobs that need a service container

GitLab's `services:` become extra containers in the same Pod, reachable over the Pod's own
localhost and by DNS alias. They share the Pod's resource quota.

```yaml
test-with-postgres:
  stage: prove
  image: postgres:16-alpine
  services:
    - name: postgres:16-alpine
      alias: db
  variables:
    POSTGRES_PASSWORD: ci
    PGHOST: db
    PGUSER: postgres
  script:
    - psql -c 'select 1'
```

Two constraints that come from the cluster, not from GitLab:

- **Ports are per-Pod.** Services cannot reuse a port with each other, and a service binding a
  privileged port needs the Pod's security context relaxed. Prefer non-privileged ports.
- **The node image is small.** Pull policies and image sizes matter more here than on a
  conventional cluster.

## A job that deploys to the same cluster

The Pod already has a service account token mounted. Use it rather than handing the job a
long-lived credential:

```yaml
deploy:
  stage: prove
  image: bitnami/kubectl:1.32
  script:
    - kubectl config set-cluster tenant --server=https://kubernetes.default.svc --certificate-authority=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt
    - kubectl config set-credentials ci --token="$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)"
    - kubectl config set-context tenant --cluster=tenant --user=ci
    - kubectl --context tenant -n gitlab-runner get pods
```

Only works if the runner's `service_account` may read what it reads. The ClusterRole in
[01](01-kcp-server.md#5-permit-that-group-in-the-tenant-cluster) is scoped to what the RUNNER needs
to place jobs, which is not what a JOB needs to deploy. Grant the job's service account separately
and keep the two apart.

## The job Pod cannot reach your GitLab, by default

A job Pod runs **inside a guest**, on the tenant cluster's own network, not on the kcp host. The
repository URL the runner hands the helper container - `CI_REPOSITORY_URL` - is whatever host GitLab
is published on, and `http://127.0.0.1:8080/...` there is the *guest's* loopback. The clone fails:

```
fatal: unable to access 'https://.../repo.git/': ...
ERROR: Job failed: command terminated with exit code 128
```

reported as `script_failure`, which reads like a broken runner.

Three ways out, in the order that costs least:

1. **Do not clone.** `variables: { GIT_STRATEGY: none }` skips the fetch entirely. Right for a job
   that only needs to run commands.
2. **Give the guest a route to GitLab.** Bind GitLab to an address the guest can dial - the
   cluster's own gateway address is the one the guest's default route already names - and set
   `external_url` accordingly, so `CI_REPOSITORY_URL` carries an address that resolves from inside
   the guest.
3. **Run the runner where the pods are**, so the URL it hands out is one the pods can reach.

Option 1 proves the integration. Options 2 and 3 are what a real pipeline needs, and which one is
right depends on whether GitLab is reachable from the tenant network at all.

Related: a node that has just joined may not resolve yet either, because cluster DNS is an addon
that comes up after it. That is what `pre_get_sources_script` in
[02](02-gitlab-runner.md#the-file) waits for, and it is a different problem from this one.

## Artifacts and cache

- **Artifacts** travel back to GitLab over the runner's HTTPS connection to the GitLab instance, not
  through any cluster mechanism. They work unchanged.
- **Cache** does not. There is no object store in this design and no PersistentVolume: the cache
  volume GitLab would use is per-Pod. If you need caching, configure an external S3-compatible
  endpoint in `[runners.cache]`, because a `hostPath` or PVC cache has no node to live on once the
  cluster returns to zero.

## Resource requests are the contract

`cpu_request` and `memory_request` are the runner's declaration of what one job needs. They size the
Pod today, and they are what `PodsPerNode` divides by when the cluster scales out, so set them from
a real measurement rather than a guess.

A job that overrides its requests with `KUBERNETES_CPU_REQUEST` and friends is asking for a
placement decision the runner made one layer up to be re-made, and it will disagree with the node
that was provisioned for it.

## One job per node

The runner creates one Pod per job. The cluster's own scale decision provisions the node that Pod
lands on, so N concurrent jobs is N Pods, and N Pods is what raises the node target.

Two knobs, on two sides.

Runner side: anti-affinity, in `config.toml` (see [02](02-gitlab-runner.md#the-file)):

```toml
    [runners.kubernetes.affinity]
      [runners.kubernetes.affinity.pod_anti_affinity]
        [[runners.kubernetes.affinity.pod_anti_affinity.required_during_scheduling_ignored_during_execution]]
          topology_key = "kubernetes.io/hostname"
          [runners.kubernetes.affinity.pod_anti_affinity.required_during_scheduling_ignored_during_execution.label_selector]
            [[runners.kubernetes.affinity.pod_anti_affinity.required_during_scheduling_ignored_during_execution.label_selector.match_expressions]]
              key = "project.runner.gitlab.com/id"
              operator = "Exists"
```

Use `[runners.kubernetes.affinity]`. It is first-class config; `pod_spec` needs the beta
`FF_USE_ADVANCED_POD_SPEC_CONFIGURATION` flag and this does not. `required`, not `preferred`, or a
Pod co-schedules whenever the scheduler feels like it.

Cluster side:

```bash
SOCIALWEB_CLUSTERPROVIDER_SCALE_MAX_NODES=5   # the default is 10; this is a deliberate five
SOCIALWEB_CLUSTERPROVIDER_SCALE_PODS_PER_NODE=1
SOCIALWEB_CLUSTERPROVIDER_SCALE_HOLD=1m
```

| Variable | Meaning |
| --- | --- |
| `SCALE_MAX_NODES` | Ceiling. Default 10; below one is refused rather than clamped. Every node is a booted guest, so this is a cost knob, not only a behaviour one. |
| `SCALE_PODS_PER_NODE` | How much work one node is assumed to hold. Set it from a measured job. |
| `SCALE_HOLD` | Idle grace before scale-in. Default 2m. |

The decision counts Pods that are Pending, Running or Unknown, skipping k3s's own addons. At one Pod
per node, five live job Pods is a target of five. When the work stops the target is zero and the
cluster goes to zero - not to an intermediate count. Stated limit, see `NEXT.yaml`.

### Prove it

Five jobs, one per node, then back to zero. Jobs echo and sleep. The image is one the node image
already carries, so a slow pull cannot hide the placement.

```yaml
.scale-check:
  stage: build
  interruptible: true
  image: rancher/mirrored-library-busybox:1.37.0@sha256:ad9fa4d07136a83e69a54ef00102f579d04eba431932de3b0f098cc5d5948f9f
  timeout: 30 minutes
  script:
    - echo "SCALE_CHECK job=${CI_JOB_NAME} pod=$(hostname)"
    - sleep 180

scale-check-1:
  extends: .scale-check
scale-check-2:
  extends: .scale-check
scale-check-3:
  extends: .scale-check
scale-check-4:
  extends: .scale-check
scale-check-5:
  extends: .scale-check
```

What a working run reads, measured, PASS 346.11 s:

```
5 job pods exist, all Pending, cluster at zero nodes
the scale decision reads Provision with nodes=5: intent and no node
5 nodes join on demand, all Ready
each job pod is on its own node
all 5 jobs finish success
the job pods are gone
the scale decision reads Teardown with nodes=0:
  nothing running for 1m0.917972328s, past the 1m0s hold
the cluster is back at zero nodes, control plane still Ready
```

Mid-flight, one node at a time: with one node up, several Pods sit `Init` on it and the rest stay
`Pending` - the anti-affinity refusing to co-schedule - and those Pending Pods are what take the
count up. That is the loop working, not scheduling luck.

### The limit that bites multi-node first

**A Pod on one node cannot reach a Pod on another node.** Each guest sits behind its own pasta
instance, pasta is user-mode networking, and it carries a guest's traffic out to the host and
nothing between two guests. The nodes have no path to each other, so k3s's flannel overlay has
nothing to run over.

Cluster DNS is the first thing that needs that path. A Pod's resolver is cluster DNS, cluster DNS is
CoreDNS, and CoreDNS runs on ONE node. Measured on a live five-node cluster, TCP to the CoreDNS Pod
IP:

```
liveweb-node1  (CoreDNS runs here)   REACHABLE
liveweb-node2..5                     UNREACHABLE
```

and `nslookup` on those answers `connection timed out; no servers could be reached`. It does NOT go
away with time.

So anything that resolves a name works on a ONE-node cluster, and on whichever node CoreDNS happens
to sit on. Everywhere else it fails. `GIT_STRATEGY: none` on a job that does not need the repository
sidesteps it - that is a workaround, not a fix, and it is written here so it is not mistaken for
one. Recorded in `NEXT.yaml` as a limit; the shape of a fix is a shared L2 for one cluster's guests,
or a CoreDNS on every node, and both are a change to the node network.

## Pause pods, and why they fight scale-to-zero

GitLab Runner 18.10 and later can keep low-priority **pause pods** on the cluster so a job Pod
preempts one and starts immediately instead of waiting for a node. Real latency win on a
conventional cluster.

On this one it interacts with the scale decision in a way you must plan for. A pause pod is a
Pending pod that is not one of k3s's own addons, so **the scale decision counts it as intent** and
will provision a node to place it - and the cluster will never return to zero while a pause pod
exists.

The way out is the pause-pod policy's own cron. GitLab's `idle_count` is per-period and defaults to
0, so a policy asking for `idle_count = 0` outside working hours scales the pause-pod Deployment to
zero; with no pause pods there is no intent and the node is reaped as usual. Do not set a 24x7
`idle_count > 0` on a cluster you expect to reach zero.

The RBAC for pause pods is in
[01](01-kcp-server.md#5-permit-that-group-in-the-tenant-cluster); `priorityclasses` is
cluster-scoped, so it needs a ClusterRole.
