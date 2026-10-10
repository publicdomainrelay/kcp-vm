# 08 - One job per node, and scaling to N

Runner creates one Pod per job. Cluster's own scale decision provisions the node that Pod lands
on. So N concurrent jobs is N Pods, and N Pods is what raises the node target.

Two knobs make that one job one node, and they are on different sides:

- the RUNNER says the Pods may not share a node (anti-affinity)
- the CLUSTER says how many nodes it may have (scale ceiling)

## Runner side: anti-affinity

Use `[runners.kubernetes.affinity]`. It is first-class config. Do not reach for `pod_spec` for
this - `pod_spec` needs the beta `FF_USE_ADVANCED_POD_SPEC_CONFIGURATION` flag, affinity does not.

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

`project.runner.gitlab.com/id` is a label the runner puts on every job Pod and nothing else
carries. So job Pods repel each other and k3s's own Pods are untouched. `required` - not
`preferred` - or a Pod co-schedules whenever the scheduler feels like it.

Raise `concurrent` to the number of jobs in flight. With `concurrent = 1` a second job waits even
when nodes are free, and one stale job from a cancelled pipeline holds the only slot.

## Cluster side: the ceiling

```bash
SOCIALWEB_CLUSTERPROVIDER_SCALE_MAX_NODES=5
SOCIALWEB_CLUSTERPROVIDER_SCALE_PODS_PER_NODE=1
SOCIALWEB_CLUSTERPROVIDER_SCALE_HOLD=1m
```

| Variable | Meaning |
| --- | --- |
| `SCALE_MAX_NODES` | Ceiling. Default 1. Every node is a booted guest, so this is a cost knob, not only a behaviour one. |
| `SCALE_PODS_PER_NODE` | How much work one node is assumed to hold. Set it from a measured job, not a guess. |
| `SCALE_HOLD` | Idle grace before scale-in. Default 2m. |

The decision counts Pods that are Pending, Running or Unknown, skipping k3s's own addons. At one
Pod per node, five live job Pods is a target of five. When the work stops the target is zero and
the cluster goes to zero - not to an intermediate count. That is a stated limit, see `NEXT.yaml`.

## Prove it

Five jobs, one per node, then back to zero. The jobs echo and sleep. `image` is one the node image
already carries, so a slow pull cannot hide the placement.

```yaml
.scale-check:
  stage: build
  interruptible: true
  image: rancher/mirrored-library-busybox:1.37.0@sha256:ad9fa4d07136a83e69a54ef00102f579d04eba431932de3b0f098cc5d5948f9f
  timeout: 30 minutes
  # See the limit below before removing this. A clone asks the node for a name.
  variables:
    GIT_STRATEGY: none
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

Watch it, in three terminals:

```bash
kubectl --kubeconfig <tenant-admin> get pods -n gitlab-runner -o wide -w
kubectl --kubeconfig <tenant-admin> get nodes -w
kubectl --kubeconfig <tenant-admin> get pods -n gitlab-runner \
  -o custom-columns='POD:.metadata.name,PHASE:.status.phase,NODE:.spec.nodeName'
```

What a working run reads, measured 2026-10-06, pipeline 69, PASS 346.11 s:

```
5 job pods exist, all Pending, cluster at zero nodes
the scale decision reads Provision with nodes=5: intent and no node
5 nodes join on demand, all Ready
each job pod is on its own node:
  liveweb-node1 <- concurrent-0    liveweb-node4 <- concurrent-3
  liveweb-node2 <- concurrent-1    liveweb-node5 <- concurrent-4
  liveweb-node3 <- concurrent-2
all 5 jobs finish success
the job pods are gone
the scale decision reads Teardown with nodes=0:
  nothing running for 1m0.917972328s, past the 1m0s hold
the cluster is back at zero nodes, control plane still Ready
```

Mid-flight, one node at a time: with one node up, three Pods sit `Init` on it and two stay
`Pending` - the anti-affinity refusing to co-schedule - and the two Pending Pods are what take the
count to three, four and five. That is the loop working, not scheduling luck.

## The limit that bites multi-node first

**A Pod on one node cannot reach a Pod on another node.** Each guest sits behind its own pasta
instance, pasta is user-mode networking, and it carries a guest's traffic out to the host and
nothing between two guests. The nodes have no path to each other, so k3s's flannel overlay has
nothing to run over.

Cluster DNS is the first thing that needs that path. A Pod's resolver is cluster DNS, cluster DNS
is CoreDNS, and CoreDNS runs on ONE node. Measured on a live five-node cluster, TCP to the CoreDNS
Pod IP:

```
liveweb-node1  (CoreDNS runs here)   REACHABLE
liveweb-node2                        UNREACHABLE
liveweb-node3                        UNREACHABLE
liveweb-node4                        UNREACHABLE
liveweb-node5                        UNREACHABLE
```

and `nslookup` on those four answers `connection timed out; no servers could be reached`.

It does NOT go away with time. Seen from a job:

```
fatal: unable to access 'https://.../streamplace.git/':
Could not resolve host: gitlab.dorper-matrix.ts.net
(Timeout while contacting DNS servers)
ERROR: Job failed: command terminated with exit code 128
```

reported as `script_failure`, which reads as a broken job.

So:

- Anything that resolves a name works on a ONE-node cluster, and on whichever node CoreDNS happens
  to sit on. Everywhere else it fails.
- `GIT_STRATEGY: none` on a job that does not need the repository sidesteps it, and that is why the
  scale check above uses it. That is a workaround, not a fix, and it is written down here so it is
  not mistaken for one.
- A wait for the job's own resolver - `pre_get_sources_script`, see [05](05-runner-config.md) -
  bounds the failure and names it. It does not fix it either.

Recorded in `NEXT.yaml` as a limit. The shape of a fix is a shared L2 for one cluster's guests, or
a CoreDNS on every node, and both are a change to the node network rather than a bug in it.
