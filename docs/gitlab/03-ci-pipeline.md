# 03 - A pipeline that proves it

Every job below runs as a Pod in the tenant cluster. The runner creates the Pod; the cluster's own
scale decision provisions the node that runs it.

Not run here: the GitLab instance is not part of this repo's gate, so the commands below are a
recipe. What IS measured is the whole integration once: `TestAGitLabPipelineRunsOnATenantCluster`
(`cmd/socialweb-clusterprovider/live_gitlab_pipeline_test.go`) ran a real pipeline on a real GitLab
against a tenant cluster at zero nodes, and it passed in 34.12 s - the job pod's Pending state
drove the scale decision, a node joined, GitLab's runner placed its pod on `liveweb-node1`, and the
job exited 0.

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

## The job pod cannot reach your GitLab by default

Found by running it, not by reading it. A job Pod runs **inside a guest**, on the tenant cluster's
own network, not on the kcp host. So the repository URL the runner hands the helper container -
`CI_REPOSITORY_URL`, which is whatever host GitLab is published on - resolves from inside the guest,
and `http://127.0.0.1:8080/...` there is the *guest's* loopback, not yours. The clone fails with
`exit code 128` and the job reports `script_failure`, which reads like a broken runner.

Three ways out, in the order that costs least:

1. **Do not clone.** `variables: { GIT_STRATEGY: none }` skips the fetch entirely. Right for a job
   that only needs to run commands, and it is what the integration proof uses.
2. **Give the guest a route to GitLab.** Bind GitLab to an address the guest can dial - the
   cluster's own gateway address is the one the guest's default route already names - and set
   `external_url` accordingly, so `CI_REPOSITORY_URL` carries an address that resolves from inside
   the guest.
3. **Run the runner where the pods are**, so the URL it hands out is one the pods can reach.

Option 1 proves the integration. Options 2 and 3 are what a real pipeline needs, and which one is
right depends on whether GitLab is reachable from the tenant network at all.

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
