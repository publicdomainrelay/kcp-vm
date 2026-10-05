# GitLab on the virtual k3s clusters

Two halves, and one rule that decides whether they can be joined.

- **Half A**: a GitLab instance, somewhere.
- **Half B**: one of this repo's virtual tenant k3s clusters, whose nodes scale to zero while the
  control plane stays up.
- **The rule**: the GitLab Runner must run on a host that can reach the tenant cluster's
  apiserver. See [Reachability](#reachability-the-single-operational-fact) below. Getting this
  wrong is the most common way this integration fails, and it fails as a connection timeout, not
  as an authentication error, so it reads like a GitLab problem.

Read this file first. Then:

| | |
| --- | --- |
| [01 - Spin up the GitLab host](01-vm-and-gitlab.md) | GitLab CE on the kcp host, or on a VM provisioned through the market |
| [02 - Point a Runner at a tenant cluster](02-runner-kubernetes.md) | the kubernetes executor, the credential, the RBAC, `config.toml` |
| [03 - A pipeline that proves it](03-ci-pipeline.md) | `.gitlab-ci.yml` examples and what each one exercises |
| [04 - The cloud controller manager and autoscaling](04-ccm-and-autoscaling.md) | node identity, node lifecycle, scale-out |
| [examples/](examples/) | the literal files, copy them |

## Reachability: the single operational fact

The tenant apiserver **binds loopback on the kcp host**. It is not on a network interface. Two
routes exist to it, and they are not interchangeable:

| Route | URL | Credential that works |
| --- | --- | --- |
| Direct, host-local | `https://127.0.0.1:<k3s-secure port>` | a client certificate signed by that cluster's ClientCA |
| Through the kcp mount | `https://<kcp-host>/clusters/root:<accountHash>:<cluster>:clusters` | a bearer token only |

The mount is a reverse proxy: kcp terminates TLS and proxies to the apiserver. **Only a bearer
credential survives it**; a client certificate does not. It is the right route for a human with
`kubectl` and a minted token, and the wrong route for GitLab CI, which needs the Kubernetes exec
and attach paths (`pods/exec`), an upgraded SPDY connection through a reverse proxy.

So: **run the GitLab Runner where `127.0.0.1:<port>` is reachable - on the kcp host.** That is
Story A, and it is what these documents show.

The tenant apiserver's port is leased per cluster and is not a constant. Read it from the
cluster's own status:

```bash
kubectl --kubeconfig <admin> get cluster <name> -o jsonpath='{.status.url}'
```

The value is the `host` you put in the runner configuration.

### If GitLab must live on a separate VM

Then the kcp host must expose an authenticated front for the apiserver, and that is a deliberate
widening of the trust boundary this project keeps at loopback. Nothing in this repo does it
today, and it is not done silently here: `cmd/socialweb-gateway/config.go` refuses to bind
anything but loopback on purpose, and `deploy/container-entrypoint.sh` publishes exactly one port,
whose only upstream is the gateway. Instructions for the VM case therefore stop at the boundary
and name what has to be added; see [01](01-vm-and-gitlab.md#option-2---gitlab-on-a-provisioned-vm).

## What is measured, and what is documented

This distinction is load-bearing in this repo - a thing that reads green while being wrong is the
recurring defect it keeps finding. Every command block below is marked:

- **verified** - run on this host, with the observed output.
- **not run here** - written from the source and the upstream documentation, with the reason it
  was not executed. Treat it as a recipe to follow, not as a receipt.

### The two proofs, as run

Both are live runs through the real composition root: a real postgres, a real kine, a real
`k3s server --disable-agent`, a real firecracker guest, and a real scale decision. Nothing is
mocked and nothing is hand-provisioned.

| Proof | Test | Result |
| --- | --- | --- |
| The cluster reaches Ready at zero nodes, provisions on demand, and returns to zero | `TestTheFourStepProofRunsThroughTheCompositionRoot` | PASS, 103.10 s. `INSTANCE_START_TO_READY_S=5.430` |
| A **GitLab Runner job pod** is placed on the provisioned node and the cluster returns to zero | `TestAGitLabJobPodIsPlacedOnAProvisionedNode` | PASS, 84.40 s (a second run of the same test reported 86.16 s; two runs, two leased ports, both green) |

Both readings were taken on 2026-10-05 on this host. The port differs between runs - `62002`,
`62302` - which is the point: the apiserver port is leased per cluster and the documents read it
from `status.url` rather than naming one.

The second one applies the Pod shape the kubernetes executor actually creates - one `build`
container and one `helper` container in a single Pod, `RestartPolicy: Never`, the runner's own
labels - and asserts it reached `Succeeded` with `spec.nodeName` equal to the node the scale
decision provisioned. That is the cluster half of "GitLab CI works here", measured.

The GitLab instance itself is **not run here**: `gitlab/gitlab-ce` is a multi-gigabyte image and
this project's gate never pulls it. So "a pipeline ran green end to end" is documented, not
measured; everything from the runner's API calls downward is measured.

## The limit you will hit first

**A tenant cluster runs at most one node today.** `internal/scale/scale.go` decides between zero
and one - the decision is deliberately coarse, and `Decision.Nodes` is hardwired to 0 or 1. One
GitLab job fits on one node. Two concurrent jobs do not, and the second job waits.

[04 - The cloud controller manager and autoscaling](04-ccm-and-autoscaling.md) is the work that
lifts this to N. Until it lands, set `concurrent = 1` in `config.toml` so that the runner does not
queue jobs it cannot place, and read that document for what changes.

Also note the hold: past the idle grace (`SOCIALWEB_CLUSTERPROVIDER_SCALE_HOLD`, default 2m) a
cluster with nothing running goes back to zero nodes. A CI job that arrives after that pays the
node boot again (about 10 s, measured). GitLab's pre-warmed pause pods
([04](04-ccm-and-autoscaling.md#pause-pods-and-why-they-fight-scale-to-zero)) exist to hide exactly this latency.
