# 0002 - The scale decision to N nodes

Status: done. Live reading taken 2026-10-05; `NEXT.yaml` AUTO-1 closed to `docs/ARCHITECTURE.md`
section 15 with its before-and-after readings.
Opened: 2026-10-05
Depends on: 0001 (the cloud controller manager). Continues `NEXT.yaml` AUTO-1.
Repo: socialweb-computer-kcp.

## 1. Goal

Make `internal/scale` able to demand more than one node, and make the driver reconcile the
cluster's actual node count to that demand, without breaking the zero direction.

The request this serves: two concurrent GitLab CI jobs must both be placed, and the cluster must
still return to zero when they finish.

## 2. What is true today, measured

`internal/scale/scale.go:157` `Decide(o Observation, st State, now time.Time, opts Options) Decision`.

- `Observation` (:34) carries `Pods, Jobs, Deployments, StatefulSets, DaemonSets`.
- `Intent(o)` (:42) is a boolean: any non-control-plane-owned Pending/Running/Unknown pod, unfinished
  Job, replicas>0 Deployment/StatefulSet, or a DaemonSet wanting pods. `AppliedByTheControlPlane`
  (:22) excludes k3s's own addons, which is what lets an empty cluster reach zero.
- `Decide` (:157): intent and `st.Nodes == 0` -> `Provision, Nodes: 1`; intent and `st.Nodes > 0` ->
  `None` with the ADR 0014 reason; no intent and `st.Nodes == 0` -> `None`; no intent and
  `st.Nodes > 0` -> hold `DefaultHold = 2m` -> `None`, past it -> `Teardown, Nodes: 0`.
- `State.Nodes` is re-read from the Node CRs each pass and deliberately not remembered (section 10);
  `State.IdleSince` is the only remembered value, because without it the hold never arrives.

The consequence: `Decision.Nodes` is 0 or 1 by construction. One pending pod and fifty pending pods
decide identically.

## 3. What must not break

- **Invariant 14**: a list failure is an error, never an empty observation. The new demand
  computation reads the same one observation; a failure still aborts the pass.
- **Invariant 15**: the state advances only on success.
- **Section 10's coarse intent test**: "a predicate that can only fire upward cannot express" zero.
  The boolean stays a boolean. The count is a SEPARATE function, so a bug in the count cannot make
  zero unreachable.
- **Section 10's one-call rule**: both halves of the observation come from one call, so a pod
  counted before its node's CR landed cannot provision twice.
- **ADR 0014**: a second Provision for a cluster that already has its node is a second node. This is
  resolved by reconciling a TARGET against a re-read count, not by refusing to provision whenever a
  node exists.
- `deploy/check.sh --fast` and the four-step proof.

## 4. Design

### 4.1 A separate demand function, not a boolean made countable

```go
func Demand(o Observation) int
```

`Demand` is a pod-work COUNT, not a node count: the ratio is applied only in `Target` (4.2). It
counts the work that needs a node: non-terminal, non-control-plane-owned pods that are Pending or
Running (one each), plus the pods an unfinished non-suspended Job still needs, plus the replicas of
Deployments and StatefulSets, plus the DaemonSet desired count. It never returns a negative, and
`Demand == 0` must agree with `Intent(o) == false` for every input the table tests cover; a case
where they disagree is a bug in one of them and gets its own table entry.

A pod left Unknown by a departed node counts here, because it is `Intent` that says so and the two
must agree; the "Pending or Running" reading is too narrow to cover it.

`Intent` is then re-expressed as `Demand(o) > 0`, and a table test asserts the two agree on every
existing `Intent` fixture. That keeps one definition of "is there work" instead of two that drift.

**The sum double-counts a workload whose pods are also in the observation, and that is a decision.**
A Deployment of N replicas contributes N as replicas AND its N pods contribute N more if they are
Pending or Running, so a Deployment workload reports roughly twice its pod count. A bare-pod
workload is exact: N pods report N. The over-count is bounded by `MaxNodes`, so it over-provisions
within the cap rather than unbounded, and a later pass corrects as pods terminate. The live
acceptance uses bare pods for that reason, and the readings it produces are exact. If the
double-count is judged too costly, the fix is to subtract pods already owned by a counted
Deployment or StatefulSet, which needs `OwnerReferences` the observation does not carry today.

### 4.2 The target

```go
type Options struct {
    Hold        time.Duration
    MaxNodes    int  // ceiling; 0 means the default
    PodsPerNode int  // 0 means the default
}
```

```go
func Target(o Observation, opts Options) int   // 0 when there is no intent
```

`ceil(Demand / PodsPerNode)`, clamped to `[1, MaxNodes]` when there is intent, and `0` when there is
not. The clamp's lower bound of 1 when intent exists preserves today's behaviour exactly at
`MaxNodes == 1`, which is how the existing tests keep passing unchanged.

Defaults live in `scale` as named constants (`DefaultMaxNodes`, `DefaultPodsPerNode`) and are
resolved by `Options` accessors in the existing `o.hold()` style, so a zero value is never a
surprise: it is the default.

### 4.3 The decision

`Decision` keeps `Action` and `Nodes`, and `Nodes` becomes the TARGET TOTAL rather than a delta:

- intent, `st.Nodes < target` -> `Provision, Nodes: target`
- intent, `st.Nodes > target` -> not reachable in this iteration: see 4.4, where the target while
  intent exists only grows. The branch is written to `None, Nodes: st.Nodes` with a reason naming
  4.4, so a future partial scale-in has one place to change rather than a silent gap.
- intent, `st.Nodes == target` -> `None, Nodes: st.Nodes`
- no intent -> unchanged: hold, then `Teardown, Nodes: 0`

### 4.4 The one genuinely new action

`Teardown` today means "take the cluster to zero". Scaling in from three nodes to two is a
different act with different consequences (which node, is it draining, does its work move), and
conflating them is how a scale-in becomes a lost job. So the first iteration does NOT scale in
partially: while intent exists the target only grows to the cap, and the down direction remains the
all-or-nothing `Teardown` past the hold, exactly as today. Partial scale-in is a follow-on item with
its own plan, and it is only worth doing once something measures which node to drain.

This is a deliberate limit, stated rather than left implicit: the cluster scales out to `MaxNodes`
and in to zero, and never to an intermediate count.

### 4.5 The driver and the actuator

`internal/scaledriver` compares the re-read node count against `Decision.Nodes` and calls
`Provision` once per missing node (`for counted := observedNodes; counted < dec.Nodes; counted++`).
The actuator interface is unchanged - `scale.Nodes` still has `Provision(ctx) error` - because the
driver, not the actuator, owns the reconcile, so no host package changes. This corrects an earlier
claim in this section that `tenantNodes.Provision` already loops per node; it did not and does not:
`cmd/socialweb-clusterprovider/nodes.go:328` provisions exactly one node per call, which is why the
loop lives in the driver.

The node id is a fixed value today: `cmd/socialweb-clusterprovider/main.go` declares
`const nodeID = "node1"`, and `newNodeHost` passed it to `nodeprovider.NodeName(cluster, nodeID)`
for the cluster's one node. It becomes distinct per node: `newNodeHost` takes the id as a
parameter, the first node is still `node1`, and each further node is `node2`, `node3`, ... The next
id is derived by reading the tenant cluster's own Node list and taking the lowest ordinal not
present, so it is re-derived from the cluster's state on every provision rather than remembered
from a variable (per section 10's rule), and a repeated provision after a slow join reuses the same
node instead of booting a second one under a new name. `tenantNodes` holds every node it has
started - the pre-built first node plus a map of the later ones - and its `Teardown` stops all of
them and deletes all of their tenant Node objects, so the cluster still reaches zero nodes. The
actuator is still memoised once per cluster, for the lifetime of the process.

### 4.6 The cloud controller manager's part

`internal/cloudprovider/pressure.go` (plan 0001) already computes `Pressure` and `Target` from the
tenant cluster's own pods and nodes. Where the two disagree, the observation wins: `scale.Decide` is
the single decider, and the CCM's numbers are the instrument that shows the decision was right.

## 5. Acceptance

- `deploy/check.sh --fast` green.
- Table tests: `Demand` and `Intent` agree on every existing `Intent` fixture; `Target` returns 0 at
  zero demand, 1 at one pod with `PodsPerNode == 8`, 3 at 17 pods, and `MaxNodes` at 100 pods with
  `MaxNodes == 4`; `Decide` at `MaxNodes == 1` reproduces every existing case byte for byte.
- The four-step proof still passes unchanged, because `MaxNodes` defaults to 1.
- A new live case, in the same harness, where `MaxNodes` is 3 and three concurrent pods are applied:
  three nodes appear, all three pods reach Running, and past the hold all three nodes are reaped and
  `kubectl get nodes` is empty with the control plane still Ready.

## 6. Risks

- The demand count reading pods that are being garbage-collected inflates the target for one pass.
  The clamp bounds the damage at `MaxNodes`, and the next pass corrects it; the test for a
  transiently-inflated observation is worth having.
- `MaxNodes` interacts with port leases: each node takes addresses from the cluster's /24
  (`subnetallocator`). The default ceiling must be small enough that the default /24 holds it, and
  the code should refuse a `MaxNodes` the cluster's range cannot address rather than failing at the
  third node's boot.
- Raising `MaxNodes` changes the cost profile, not just the behaviour: each node is a booted guest.
  It defaults to 1 for that reason, and the operator raises it deliberately.

## 7. Log

- 2026-10-05 Opened from plan 0001's discovery. `NEXT.yaml` AUTO-1 is this plan. Design settled at
  section 4; the partial-scale-in limit is stated in 4.4 rather than left latent.
- 2026-10-05 Implemented, then corrected twice against the code rather than against this document.
  Section 4.5's claim that `tenantNodes.Provision` already looped per node was WRONG - it provisioned
  exactly one - so the driver owns the reconcile and calls `Provision` once per missing node;
  section 4.5 is corrected to say so. Section 4.1 asked for
  `Demand(o, podsPerNode)` while 4.2 applied the ratio in `Target`; the unused parameter was removed
  and the missing sentence ("`Demand` is a pod-work COUNT, not a node count: the ratio is applied
  only in `Target`") added, because a parameter that is never read is a second way of saying
  something false. The Deployment double-count (its pods plus its replicas) is now recorded in 4.1
  with its bound rather than living only in a report.
- 2026-10-05 MEASURED. `TestThreeGitLabJobPodsScaleToThreeNodes` PASSES in 95.65 s at
  `MaxNodes=3, PodsPerNode=1`: the decision reads `Provision, Nodes: 3` with reason
  `intent and no node`, `kubectl get nodes` reports three, all three GitLab Runner job pods reach
  `Succeeded`, and past the 1m hold the nodes are reaped with the control plane still `Ready` at
  `https://127.0.0.1:62102`. The two prior live proofs were re-run after the harness change and
  still pass - GitLab job pod 84.93 s, four-step 100.53 s. The one honest weakness is recorded in
  section 15 too: the scheduler placed all three pods on `liveweb-node1`, so the reading proves the
  CLUSTER reached three nodes, not that three guests each hosted work.
- 2026-10-05 A harness defect was found while wiring the knobs and fixed: `fourstepBringUp` built
  `scale.Options{Hold: cfg.ScaleHold}` and dropped the new ceiling fields, so an env passthrough
  alone would have been inert and the three-node test would have failed for a harness reason rather
  than for the defect it exists to catch. The live harness now forwards them, which is what
  production `main.go` already did.
