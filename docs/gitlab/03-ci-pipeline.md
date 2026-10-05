# 03 - A pipeline that proves it

Every job below runs as a Pod in the tenant cluster. The runner creates the Pod; the cluster's own
scale decision provisions the node that runs it.

Not run here: the GitLab instance that schedules these. The Pods they produce are the same shape
this repo's own gate places on a real node.

## The smallest pipeline that proves the integration

`.gitlab-ci.yml`:

```yaml
stages: [prove]

prove-placement:
  stage: prove
  image: busybox:1.36
  tags: [socialweb-tenant-ci]
  script:
    - echo "running on $(hostname)"
    - echo "namespace $(cat /var/run/secrets/kubernetes.io/serviceaccount/namespace)"
    - nslookup kubernetes.default.svc.cluster.local || true
```

What it exercises, in order: the runner authenticates to the apiserver; a Pod is created in the
`gitlab-runner` namespace; the cluster observes intent and provisions a node; the Pod is placed; the
build container runs; the helper container streams the log back through the runner.

If this passes, the integration works. If it hangs at "Preparing the environment", the runner is
pointed at the kcp mount URL instead of the direct loopback URL - see
[README.md](README.md#reachability-the-single-operational-fact).

## Jobs that need a service container

GitLab's `services:` become extra containers in the same Pod, reachable over the Pod's own
localhost and by DNS alias. They share the Pod's resource quota, so a job with a database service
needs `cpu_limit`/`memory_limit` sized for both.

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

- **Ports are per-Pod.** Services cannot reuse a port with each other, and a service that binds a
  privileged port needs the Pod's security context relaxed. Prefer non-privileged ports.
- **The node image is small.** Pull policies and image sizes matter more here than on a
  conventional cluster; `image_pull_policy = "if-not-present"` is set for that reason.

## A job that deploys to the same cluster

The Pod already has a service account token mounted. Use it rather than handing the job a long-lived
credential:

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

This only works if the runner's `service_account` has permission to read what it reads. The
ClusterRole in [02](02-runner-kubernetes.md#3-permit-the-runner-in-the-tenant-cluster) is scoped to
what the *runner* needs to place jobs, which is not automatically what a *job* needs to deploy.
Grant the job's service account separately, and keep the two apart.

## Artifacts and cache

- **Artifacts** travel back to GitLab over the runner's HTTPS connection to the GitLab instance,
  not through any cluster mechanism. They work unchanged.
- **Cache** does not. There is no object store in this design, and no PersistentVolume: the cache
  volume GitLab would use is per-Pod. If you need caching, configure an external S3-compatible
  endpoint in the runner's `[runners.cache]`, because a `hostPath` or PVC cache has no node to live
  on once the cluster returns to zero.

## Resource requests are the contract

`cpu_request` and `memory_request` in `config.toml` are the runner's declaration of what one job
needs. Today they only size the Pod. Once node scale-out lands
([04](04-ccm-and-autoscaling.md)) they also decide how many nodes the cluster provisions, so setting
them from a real measurement - not from a guess - is what keeps the autoscaler honest.

A job that overrides its requests with `KUBERNETES_CPU_REQUEST` and friends is asking for a
placement decision the runner made one layer up to be re-made, and it will disagree with the node
that was provisioned for it.

## Knowing whether the node was created for your job

```bash
kubectl --kubeconfig <admin> get nodes -w
```

Run that in another terminal while the pipeline runs. A job that arrives on an idle cluster shows a
node appear, then the Pod, then - past the hold - the node disappear again. A job that arrives on a
cluster inside the hold window shows no node change at all, because the node was still there. Both
are correct; only the second is fast.
