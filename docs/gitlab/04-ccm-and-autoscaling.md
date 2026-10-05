# 04 - The cloud controller manager, and autoscaling

Two things live here. The first is a controller that gives the tenant cluster's Nodes a cloud
identity. The second is the scale decision growing past one node. They are separate pieces of work
with separate invariants, and this document keeps them apart.

## The problem

A guest boots, runs `k3s agent`, and joins as a Kubernetes `Node`. From the tenant apiserver's point
of view that is all that happens. Nothing links that `Node` object to the guest that backs it:

- `spec.providerID` is empty, so nothing can map a Node back to an instance.
- There are no labels identifying which cluster or instance type the Node is.
- When a guest dies without a teardown, its `Node` object stays, and the scale decision counts it.

Meanwhile the record of the guest already exists: the `Node` CR in kcp
(`api/v1alpha1/types_node.go`), with a phase, a `nodeExpiry` and a `NodeJoined` condition. The two
records describe one guest and are not connected.

## The cloud controller manager

`cmd/socialweb-cloud-controller-manager` reconciles the tenant cluster's `Node` objects against the
instance records. Three jobs:

**Identity.** Stamp `spec.providerID` as `socialweb://<cluster>/<node-name>`, plus the labels an
autoscaler groups by. The provider ID is the mapping key; without it, nothing can tell which
Kubernetes Node is which instance, and every scale-down decision is a guess.

**Lifecycle.** Delete a `Node` object whose instance is definitively gone or draining, which
matters because a `Node` object that outlives its guest is a Node that stops the cluster ever
reaching zero.

**Pressure.** Measure what the cluster needs and publish it: pending pods that cannot be placed,
what the Ready nodes can hold. This is the instrument that shows the scale decision was right.

### Why it runs in the per-cluster process group, not as a Deployment

This is the decision that matters most here. A Deployment needs a node. This cluster's whole point
is that it has zero nodes. A component that cannot run at zero nodes cannot be the component that
decides a node is needed - it would be a control loop that dies exactly when the state it exists to
change is reached.

So the cloud controller manager is a peer of kine, openbao and `k3s server`: part of the cluster's
own process group, always up, started and reaped with the control plane.

Two consequences, both deliberate:

- **It authenticates with a minted tenant credential, never the k3s admin kubeconfig.** Invariant
  20: that file is shared state on a multi-cluster host and its server name is unverified against
  the address a node dials. `cmd/socialweb-tenantcredential` produces the credential it uses.
- **It does not set kubelet `cloud-provider=external`.** That is the textbook external-cloud-provider
  handshake: the kubelet taints its Node, the cloud controller initializes it and removes the taint.
  It is not taken here, because the taint's purpose is to stop workloads scheduling before the
  cloud has been consulted, and a guest in this design is fully initialized and join-confirmed
  before it ever registers. Adopting the taint would make every node unschedulable until a
  controller that may itself need a node runs. This provider takes the half it needs - the identity
  stamping - and leaves the handshake alone.

### The error that must not be smoothed over

If the instance source cannot be read, the controller patches nothing and deletes nothing. It does
not treat "cannot read" as "the guest is gone". Invariant 14 says a list failure is an error, never
an empty observation, and here the cost of getting it wrong is deleting a `Node` object out from
under its own running job. The two branches are covered separately by tests, named so that the
difference is visible at a glance.

## Autoscaling

### What works today, and its exact limit

`internal/scale` decides between **zero and one node**, by construction. The intent test is
deliberately coarse - "is anything running that is not one of k3s's own addons" - because a
predicate that can only fire upward cannot express zero.

The consequence for CI is concrete: one GitLab job is placed. Two concurrent jobs are not, and the
second waits rather than failing. Set `concurrent = 1` in `config.toml` until this changes, so the
runner does not queue work it cannot place.

### Scale-out to N

Landed and measured. `docs/plans/0002-scale-to-n-nodes.md` is the design; the shape is:

- The boolean stays a boolean. A separate `Demand(o)` counts the work as a pod count, so a bug in
  the count cannot make zero unreachable.
- `Options` gains `MaxNodes` (default 1) and `PodsPerNode` (default 8); `Decision.Nodes` is a
  target total, and the ratio is applied only in `Target`.
- The driver reconciles the cluster's actual node count - re-read from the Node CRs, never
  remembered - to that target.
- **It defaults to 1.** A cluster configured as before behaves exactly as before, which is why the
  four-step proof was unchanged by the change.

Set the two knobs per cluster:

```
SOCIALWEB_CLUSTERPROVIDER_SCALE_MAX_NODES=3
SOCIALWEB_CLUSTERPROVIDER_SCALE_PODS_PER_NODE=1
```

With those, three concurrent GitLab job pods produced three nodes, all three pods reached
`Succeeded`, and the cluster returned to zero past the hold - `TestThreeGitLabJobPodsScaleToThreeNodes`,
PASS, 90.01 s. One honest limit on that reading: the scheduler placed all three pods on one of the
three nodes, which is permitted, so the proof is that the cluster *reached* three nodes and not that
three guests each hosted work.

One limit is stated rather than left implicit: while there is work, the target only grows, up to the
cap, and when the work stops the cluster goes to **zero**, not to some intermediate count. Partial
scale-in needs an answer to "which node, and does its work move", and that is a follow-on.

Raise the cap deliberately: every node is a booted guest, so `MaxNodes` changes the cost profile and
not only the behaviour. `PodsPerNode` is what a job's resource requests mean in practice, so set it
from a measured job rather than a guess.

### Pause pods, and why they fight scale-to-zero

GitLab Runner 18.10 and later can keep low-priority **pause pods** on the cluster so that a job pod
preempts one and starts immediately instead of waiting for a node. That is a real latency win on a
conventional cluster.

On this one it interacts with the scale decision in a way you must plan for. A pause pod is a
Pending pod that is not one of k3s's own addons, so **the scale decision counts it as intent** and
will provision a node to place it - and the cluster will never return to zero while a pause pod
exists.

The way out is the pause-pod policy's own cron. GitLab's `idle_count` is per-period and defaults to
0, so a policy that asks for `idle_count = 0` outside working hours scales the pause-pod Deployment
to zero; with no pause pods there is no intent, and the node is reaped as usual. Configure the
periods with that in mind, and do not set a 24x7 `idle_count > 0` on a cluster you expect to reach
zero.

The RBAC for pause pods is in [02](02-runner-kubernetes.md#3-permit-the-runner-in-the-tenant-cluster);
`priorityclasses` is cluster-scoped, so it needs a ClusterRole.

## Watching it work

```bash
kubectl --kubeconfig <admin> get nodes -w
```

```bash
kubectl --kubeconfig <admin> get node <name> -o jsonpath='{.spec.providerID}'
```

expects `socialweb://<cluster>/<node-name>` once the controller has reconciled it.

The end-to-end proof that this all holds is the same one this repo already gates on: a tenant
workload applied at zero nodes provisions a node, the workload runs, and past the hold the node is
reaped with the control plane still `Ready`. The GitLab-specific variant of that proof applies the
Pod shape the kubernetes executor actually creates - a build container and a helper container in
one Pod - so the thing being proven is the runner's Pod, not an approximation of it. It passes:
`TestAGitLabJobPodIsPlacedOnAProvisionedNode`, 86.16 s, asserting the pod reached `Succeeded` with
`spec.nodeName` equal to the node the scale decision provisioned, and that the cluster returned to
zero afterwards.

The identity half of this document is **built and unit-tested but not yet installed**: the
controller exists, nothing starts it. `docs/plans/0003-install-the-ccm-in-the-process-group.md` is
that step, and until it lands `NEXT.yaml` CCM-1 stays open - a provider ID you cannot observe on a
live Node is a design, not a feature.
