# 0003 - Install the cloud controller manager into the per-cluster process group

Status: done. Landed 2026-10-05; `NEXT.yaml` CCM-1 closed to `docs/ARCHITECTURE.md` section 15 with
its readings. `NEXT.yaml` CCM-2 was split out of it.
Opened: 2026-10-05
Depends on: 0001 (the CCM package and binary, built and unit-tested). Closes `NEXT.yaml` CCM-1
together with the live assertion in its instrument.
Repo: socialweb-computer-kcp.

## 1. Goal

Make `cmd/socialweb-cloud-controller-manager` a member of a tenant cluster's own process group, so
it is up whenever the control plane is up - including at zero nodes - and is reaped with the group.

Today the binary exists and its decisions are unit-tested, and nothing starts it. A cloud
controller manager that nothing runs is a design, not an installation.

## 2. What exists

- `internal/procgroup/procgroup.go`: `Component` (`ComponentPostgres`, `ComponentOpenBao`,
  `ComponentKine`, `ComponentK3s`), `Step{Name, Spec, Ready}`, `Spawn`, `Reap`. `Spawn` starts in
  order and requires every step to have a `Ready` gate - a step with no gate is an error, not a
  skipped check. `Reap` walks the order in reverse.
- `internal/clusterstack/clusterstack.go`: `Spec` and one argv builder per component
  (`ServerArgv`, `KineArgv`, `OpenBaoArgv`), plus `Spec.Argv(component)` as the dispatcher.
- `internal/clusterprovider/steps.go`: `StepsFor`-adjacent step assembly. `steps.go:114` shows the
  idiom: `procgroup.Step{Name: ..., Spec: processrunner.Spec{Argv: argv, Output: log}, Ready: gate(...)}`,
  with waiters `waitDial`, `waitK3s`, `waitK3sControllers`, `waitOpenBaoTLS`.
- `internal/clusterprovider/stack.go:hasStepBeforeK3s` and `ErrNoGuardSeam`: the identity guard
  rides a step before k3s, and a stack that puts nothing before k3s makes the guard silently not
  run. **A new step placed BEFORE k3s changes what that guard means. This step goes AFTER k3s.**
- `processrunner.Spec{Argv, Dir, Env, Prefix, Output}` - the CCM's environment rides `Env`, never
  argv (invariant 3 and 13: no secret on argv).
- The binary's own contract, from plan 0001: `SOCIALWEB_CLOUDCONTROLLERMANAGER_TENANT_KUBECONFIG`,
  `CLUSTER`, `INSTANCES_FILE`, optional `MAX_NODES`, `PODS_PER_NODE`, `RESYNC`; it prints a
  readiness line; it refuses a k3s admin kubeconfig by name.

## 3. Decisions

- **D1 - A new `procgroup.ComponentCloudControllerManager`, ordered after `ComponentK3s`.** After,
  because the CCM is useless without an apiserver and because putting it before k3s would move the
  guard seam `ErrNoGuardSeam` exists to protect.
- **D2 - No port lease.** The CCM is an out-of-cluster controller: it dials the apiserver and
  listens on nothing. `portallocator` is untouched, and invariant 8's bookkeeping about who holds a
  lease does not grow.
- **D3 - The credential is minted into the cluster's own data dir, by the provider, at startup.**
  The CCM cannot read the k3s admin kubeconfig (invariant 20). The provider already mints tenant
  credentials (`internal/clusterprovider/tenantcredential.go`) and already knows the data dir and
  the apiserver URL, so it writes `<DataDir>/cloud-controller-manager.kubeconfig` before the step
  runs - the same shape as `WriteOpenBaoConfig`, which is already the precedent for "put a file on
  disk before a step that needs it, and refuse loudly if it cannot be written".
  The identity is a scoped one, not the superuser: the CCM needs to read and patch Nodes and
  nothing else, and its own credential is what should bound it when RBAC is added.
- **D4 - The instance source is a file the provider writes, at `<DataDir>/nodes.json`.** The CCM's
  `Instances` port is already file-backed. The provider is the thing that knows the guests, so it
  writes the snapshot; the CCM re-reads it per call and never caches.
  CORRECTED WHILE IMPLEMENTING, because the original wording named a mechanism that does not exist.
  It said a missing file is the delete path and therefore the provider must write `[]` rather than
  remove the file. The first half is true and the second half protects nothing:
  `internal/cloudprovider/fileinstances.go` answers `(known=false, err=nil)` for an ABSENT file and
  for an EMPTY array alike, so the two are the same answer - the delete path - and `[]` is not safer
  than a deleted file. What protects a live Node is that the snapshot LISTS it. The real asymmetry,
  and the one worth carrying: a snapshot that exists and cannot be PARSED is an error and patches
  nothing (invariant 14), while an absent one is not - so the file is load-bearing infrastructure
  rather than a cache, and the ordinary path is what keeps the absent case out of reach, since the
  provider writes the file before the controller is asked anything. The dangerous reading is kept in
  `NEXT.yaml`'s limits section rather than deleted along with the wrong mitigation.

## 4. Acceptance

- `deploy/check.sh --fast` green.
- The four-step proof still passes, with the process group now carrying a fifth component: the
  bring-up order, the teardown order and the timings are unchanged in kind.
- A new live assertion, in the four-step's own harness: after the node joins, `kubectl get node
  <name> -o jsonpath='{.spec.providerID}'` reads `socialweb://<cluster>/<node-name>`, and the Node
  carries `node.socialweb.computer/cluster`. Without this the CCM is installed and doing nothing,
  which is the failure this repo calls "reads green while wrong".
- A unit test that a process group containing the CCM is reaped in reverse order, with the CCM
  stopped before `k3s server` - a CCM that outlives the apiserver it reconciles against is a
  controller looping on connection errors during every teardown.
- The provider writes `nodes.json` as an empty array, never absent, and a test asserts the
  difference between the two as the CCM sees it.

## 5. Risks

- **Teardown ordering.** `Reap` reverses the spawn order, so a component appended last is stopped
  first. That is the behaviour wanted here, and it is worth an explicit test rather than relying on
  it being true by construction.
- **`ReapSurvivors`.** `internal/materialiser` reaps survivors after the group; a fifth component is
  a fifth thing it must find. The existing live teardown assertions cover this if the component is
  in the group's order, and not if it is started outside it.
- **A CCM that cannot reach its instance file must not delete Nodes.** Invariant 14, and the reason
  D4 states the empty-array rule: absent means gone, so an unwritten snapshot is not a benign
  default. Making the write atomic (temp file plus rename, the `WriteOpenBaoConfig` shape) is part
  of this, not a nicety.
- **The node name.** The CCM derives the provider ID from the Node object's name, which the
  composition root already builds with `nodeprovider.NodeName(cluster, nodeID)`. If the two
  derivations ever disagree the provider ID names a node that does not exist, and the failure looks
  like an autoscaler that cannot map its own Nodes.

## 6. Log

- 2026-10-05 Opened. Not started: it overlaps `cmd/socialweb-clusterprovider/{main,config}.go` with
  plan 0002, and the two are sequenced rather than run together so that neither change can be
  confused for the other's regression.
