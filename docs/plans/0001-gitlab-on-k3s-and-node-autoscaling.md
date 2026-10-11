# 0001 - GitLab on the virtual k3s clusters, with a cloud controller manager and node autoscaling

Status: done. Closed 2026-10-11; each phase's outcome and where it landed is in the last log entry.
Opened: 2026-10-05
Repo: socialweb-computer-kcp (the k3s product and the CCM). Sibling repos referenced:
publicdomainrelay (RFP / market / request-vm-ssh), gitlab-autoscale-socialweb-computer
(GitLab install prior art).

## 1. Goal, verbatim

> validate and document with copy pasteable examples in docs/gitlab/ how we can use our setup to
> spin a VM and configure it to run gitlab, then configure gitlab to run against one of our
> virtual scale to 0 kubernetes clusters (kubernetes executor). Make absolutely sure that gitlab
> CI works using our k3s setup. Also implement a cloud controller manager we install on the k3s
> clusters so that we know when to add more k3s nodes and make it so that autoscaling to add more
> nodes works.

Split into four deliverables:

- D-A `docs/gitlab/` - copy-pasteable: (1) spin a VM and install GitLab on it, (2) point a
  GitLab Runner using the **kubernetes executor** at a virtual tenant cluster.
- D-B A **proof** that GitLab CI works on our k3s setup (not a claim, a run).
- D-C A **cloud controller manager** installed per tenant cluster.
- D-D **Node autoscaling**: more than one k3s node when the work needs it, back to zero after.

## 2. What already exists, measured

Tenant cluster (this repo):

- `api/v1alpha1/types_node.go:7` - `Node` CR: `spec.cluster`, `status.phase`
  (`Pending|Running|Draining`), `status.nodeExpiry`, `conditions`, finalizer
  `node.socialweb.computer/teardown`. This CR is the guest-VM record. It is NOT the Kubernetes
  `Node` object; nothing links the two today.
- `internal/clusterstack/clusterstack.go:189` `ServerArgv` - `k3s server --disable-agent
  --disable=traefik --datastore-endpoint <kine> --egress-selector-mode cluster
  --https-listen-port <leased> ... --kube-cloud-controller-manager-arg=secure-port=<leased>`.
  k3s's own embedded CCM is not disabled, but no `--cloud-provider-name` is set.
- `cmd/socialweb-nodeagent/agent.go:105` - `k3s agent --server --node-name --token-file
  --data-dir --node-ip --flannel-iface`. No kubelet args.
- `internal/scale/scale.go:157` `Decide(o, st, now, opts) Decision`. Observation already carries
  `Pods, Jobs, Deployments, StatefulSets, DaemonSets`; `Intent()` is true for a pending/running
  pod, an unfinished Job, replicas>0, a DaemonSet wanting pods - all excluding objects stamped
  `AppliedByTheControlPlane`. The decision is: intent and 0 nodes -> `Provision, Nodes: 1`;
  intent and >0 -> `None` (ADR 0014); no intent -> hold `DefaultHold = 2m` -> `Teardown`.
  **`Nodes` is hardwired to 0 or 1.**
- `internal/scaledriver/scaledriver.go:48` drives `Observe` -> `Decide` -> actuator.
- `cmd/socialweb-clusterprovider/nodes.go:328` `tenantNodes.Provision` (confirmJoined polls the
  tenant Node list for `NodeJoinTimeout = 30s`), `:372` `Teardown`; guests come from
  `internal/nodefirecracker/node.go:48` or `internal/nodeqemu/node.go:46`.
- `internal/kubeconfig/kubeconfig.go:34` `Build` - the tenant kubeconfig carries an **exec
  credential** (`client.authentication.k8s.io/v1`, `interactiveMode: Never`, device credential in
  an env var). `AssertClean` (`:103`) refuses `bearer_token`, `token:`,
  `client-certificate-data`, `client-key-data`, `admin.kubeconfig` on any user-facing render.
- Gate: `deploy/check.sh --fast` (~9 s), `--local` (~75 s, names its skips), `--full` (fails on a
  skip). Four-step proof PASSES end to end.

GitLab prior art (`../gitlab-autoscale-socialweb-computer`):

- GitLab CE in docker-compose on one host, `setup/gitlab/{docker-compose.yml,setup.sh,
  gitlab-omniauth.rb,sshd-reconfig.sh,tailscale-serve.service}`; hostname
  `gitlab.dorper-matrix.ts.net`, UI on `:8443` over tailscale, sshd moved to 1997.
- Runner is a host user systemd unit (`systemd/gitlab-runner.service`), `executor = "instance"`,
  autoscaled by the Fleeting plugin `fleeting-plugin-socialwebcomputer`, which execs
  `request-vm-ssh` per instance. **Nothing in that repo touches Kubernetes.**

Vendored reference: `gitlab-kubernetes-executor.md` (2811 lines, GitLab's kubernetes executor
page). Facts taken from it: the runner creates one Pod per CI job through the Kubernetes API;
RBAC for `pods` (create/delete/get/list/watch), `pods/exec`, `pods/log`, `secrets`,
`serviceaccounts`, `services`, plus `apps/deployments` and `scheduling.k8s.io/priorityclasses`
(cluster-scoped, so a ClusterRole) when pre-warmed pause pods are used; pause pods are
Runner 18.10+ and exist to hide node scale-up latency, not to create nodes.

Host facts (measured on this machine):

- `kubectl`, `k3s`, `kine`, `docker`, `podman`, `deno`, `yq`, `jq` present. `gitlab-runner`
  MISSING. `crane` MISSING. `/dev/kvm` and `/dev/net/tun` present. 1.3 T free on /home.
- `/home/johnandersen777/kcp-image/` present (nodeimage.json, out/, kcpagent).
- `~/.kube/k3s.yaml` is a STALE symlink into a deleted `/tmp/TestTheMountShuts...` directory.
  Do not read it, do not trust it.
- Module cache has NO `k8s.io/cloud-provider` and NO `k8s.io/autoscaler`. `GOPROXY` is
  proxy.golang.org, so a new module is a network dependency.

## 3. The gaps

- G1 **Reachability.** What a GitLab Runner on a VM puts in `host =` is not established. The
  tenant apiserver binds a leased port on a private bind address and advertises a host-carried
  address; the tenant kubeconfig authenticates through an exec plugin. Whether any path exists
  from a VM to that apiserver, and what must be added, is the first thing to settle. See the
  reachability verdict in the log.
- G2 **No cloud controller manager.** Nothing stamps `spec.providerID`, sets addresses or
  labels on the tenant's Kubernetes `Node` objects, and nothing deletes a `Node` when its guest
  is gone. The `Node` CR and the `Node` object are unlinked.
- G3 **Scale is binary.** `Decision.Nodes` is 0 or 1 by construction, so two concurrent GitLab
  jobs cannot both be served. There is also no notion of capacity: "one pending pod" and "fifty
  pending pods" decide identically.
- G4 **No GitLab documentation at all.**

## 4. Decisions

Each decision names the alternative it rejected.

- **D1 - The CCM runs in the per-cluster process group, not as a pod.** Alternative: a
  Deployment in `kube-system`. Rejected because a Deployment needs a node, and the whole point of
  this cluster is that it has zero nodes; a component that cannot run at zero nodes cannot be the
  component that decides a node is needed. The CCM is a peer of kine, openbao and `k3s server`:
  always up. It is "installed on the cluster" in the only sense that survives the zero state -
  one process group per tenant cluster.
  **It authenticates with a MINTED tenant credential, never the k3s-written admin kubeconfig**
  (invariant 20: that file is shared state on a multi-cluster host and its server name is
  unverified against the address a node dials). The minting path already exists at
  `internal/clusterprovider/tenantcredential.go`.
  For the same reason the CCM must not read an unreadable instance source as "the guest is gone":
  invariant 14. `Instances.Get` returning an error means patch nothing and delete nothing.
- **D2 - Do NOT set kubelet `cloud-provider=external`.** Alternative: the textbook external-CCM
  handshake (kubelet taints the node, CCM initializes and removes the taint). Rejected for two
  reasons: the taint is applied so that no workload schedules before cloud-init has been
  confirmed, and our guests are fully initialized and
  `confirmJoined`-confirmed before they join, so the guarantee buys nothing; and the handshake
  makes every node unschedulable until a CCM that may itself need a node runs. We take only the
  half we need: the CCM stamps providerID and addresses on nodes that joined normally.
- **D3 - Hand-rolled node controller, not `k8s.io/cloud-provider`.** Alternative: pull in the
  upstream framework and implement `cloudprovider.Interface`. Rejected on design, not on
  availability - `proxy.golang.org` answers 200 from this host, so the module could be added. The
  framework requires a LoadBalancer/Zones/Clusters surface with no meaning here, its node
  controller is written for the in-cluster handshake D2 rejects, and this repo's whole idiom is a
  pure decision plus a thin actuator. The ceiling and the upgrade path are recorded as a
  `ponytail:` comment on the node controller, which is the repo's one permitted comment form.
- **D4 - The scale decision extends to N and stays pure.** Alternative: import the Kubernetes
  cluster-autoscaler and implement its external gRPC provider. Rejected for this iteration: it
  adds a large dependency and an out-of-process protocol to answer a question the repo already
  answers in a pure function. `scale.Decide` gains a capacity input and returns a target count;
  `scaledriver` reconciles actual against target. The external-gRPC shape stays available as a
  later adapter over the same decision - that is the upgrade path, not a fork of it.
- **D5 - The GitLab Runner runs outside the tenant cluster, on a host that can reach BOTH the
  kcp mount URL and the credential mint URL.** Alternative: the runner as an in-cluster
  Deployment (GitLab supports it). Rejected for the zero-node reason and because the tenant
  kubeconfig authenticates through an exec plugin that must be present where the runner runs.
  See D7 for what "can reach both" costs.
- **D6 - Reachability is stated as it is, not as it would be convenient for it to be.** Measured
  (see the log): a tenant cluster is reached at `https://<kcp-host>/clusters/<mountPath>`, kcp
  terminates TLS and reverse-proxies to `Cluster.status.URL` (`https://127.0.0.1:<k3s-secure>`),
  and the credential is an exec plugin that POSTs a device key to the gateway's
  `/v1/credential`. **Only a bearer survives the mount** (a client certificate does not), which
  is exactly why the credential is minted per request. kcp binds `127.0.0.1:6443` and the
  gateway `127.0.0.1:8080`; the only published port is `127.0.0.1:8443`, and its sole upstream is
  the gateway. **No reverse ingress into the host exists anywhere in the tree.**
  Therefore:
  - Story A, which works today and is what the docs will show: GitLab and the runner run **on
    the kcp host**, where both loopback endpoints are reachable. This is also what the existing
    GitLab prior art does (the GitLab host is the host that runs the requester).
  - Story B, GitLab on a separate VM: needs an authenticated front for the mount and the mint.
    That widens the trust boundary the repo deliberately keeps at loopback ("Nothing else is
    exposed"), so it is documented as an operator decision with its tradeoff spelled out, NOT
    silently implemented. Outward-facing exposure is not this plan's to grant.
- **D7 - `deno-kcp` and `kcp-libs` were read for reuse, and the applicable surface is empty.** The
  request said to reference them and reuse libraries as needed, so this is the answer rather than a
  silence. What was read: `kcp-libs`' 21 library packages and 11 `deno-kcp` internal packages, both
  their `README`/`AGENTS` and their exported API.
  - `kcp-libs` is kcp-shaped throughout. `impl/kcpstore` is a typed REST store over kcp's
    `/clusters/*` paths; `impl/informerwatch` is dynamic informers over `/clusters/*`;
    `common/kcp` builds kcp service labels and FQDNs; `common/denocomputer` is the
    `deno.computer` group vocabulary; `factory/admission` and `abc/queue` are a per-parent
    concurrency cap for kcp workloads. The cloud controller manager and the scale path work
    against a plain `k8s.io/client-go` client on a tenant apiserver and against this repo's own
    registry types, so none of those reach sites is the same shape.
  - `abc/reconcile`, `abc/store`, `abc/probe`, `common/ttl`, `common/condition` and
    `common/statuspatch` are the closest to generic, and they are small enough that adopting them
    would mean either a `deno.computer`-typed dependency for a `socialweb.computer` cluster, or a
    refactor of `kcp-libs` itself. Neither is this change's to make.
  - A grep for cloud-provider surfaces across both repos returns **zero hits in their own code**
    (every match is vendored OpenBao, and about Vault namespaces): no `cloudprovider.Interface`,
    no node controller, no provider ID, no node lifecycle. There is nothing to reuse for the part
    of this work that is new.
  - What WAS reused is the org-root idiom rather than the code: a pure decision plus a thin
    actuator, a port with one file-backed implementation for tests, and error strings that name
    the failure. That is the same shape as `kcp-libs`' `abc/` + `impl/` split, applied to a
    different subject.
- **D8 - The new documents live in this repo.** `docs/gitlab/` and `docs/plans/` under
  socialweb-computer-kcp, because this repo holds the k3s product and the CCM, and the GitLab
  material is an integration against them. `docs/ARCHITECTURE.md` stays the single design
  document; these two directories are the plan series and the operator recipes, and section 15
  records that as a decision.

## 5. Phases and acceptance

- **P1 - CCM.** `cmd/socialweb-cloud-controller-manager` plus `internal/cloudprovider/`
  (node-init: providerID `socialweb://<cluster>/<node>`, addresses and labels from the Node CR;
  node-lifecycle: a Node whose CR is gone or Draining is deleted; a scale-pressure reporter that
  counts Pending-but-unschedulable pods and node allocatable). Wired into the per-cluster process
  group as a new `procgroup` component. Acceptance: `deploy/check.sh --fast` green, table tests
  for every decision, one live test that a joined guest's Node object carries the providerID and
  disappears when the guest is reaped.
- **P2 - Autoscaling N>1.** `scale.Observation` gains capacity; `Decision.Nodes` becomes a
  target in `[0, MaxNodes]`; `internal/nodeprovider` names nodes per index; `scaledriver`
  provisions the difference and reaps the excess. Acceptance: `--fast` green, table tests for
  the new decision including the zero and the max cases, and the four-step proof still passing.
- **P3 - `docs/gitlab/`.** `README.md` (the story), `01-vm-gitlab.md`,
  `02-runner-kubernetes.md`, `03-ci-pipeline.md`, `04-ccm-and-autoscaling.md`, plus
  `examples/` holding the literal files (argocd-free: `config.toml`, `rbac.yaml`,
  `deployment-or-processgroup.yaml`, `.gitlab-ci.yml`). Every command block must be either
  executed or explicitly marked as not executed on this host, with the reason.
- **P4 - The proof.** `deploy/gitlab-ci-proof.sh`: bring a tenant cluster up through the
  composition root, apply a GitLab-shaped job pod (helper + build containers, the runner's labels,
  annotations, tolerations and resource requests), assert the node is provisioned, the CCM
  stamped it, the pod reaches Running, a second concurrent job gets a second node, and past the
  hold the nodes go back to zero with the control plane Ready throughout.

## 6. What is measured versus what is documented

The distinction is load-bearing (see docs/ARCHITECTURE.md section 15, "reads green while wrong").

- Measured on this host: everything under P1, P2, P4 that does not need GitLab CE itself;
  `config.toml` syntactic validity if `gitlab-runner` can be installed.
- Documented, not executed here, unless the network allows: pulling
  `gitlab/gitlab-ce` and running a real pipeline. The doc will say so at the top of the section
  rather than implying a run.

## 7. Risks and traps

- The stale `~/.kube/k3s.yaml` symlink (2). Ignore it.
- `test/nocomments_test.go` fails any byte above 0x7F anywhere it scans. Every new file ASCII.
- Changing `ServerArgv` or the agent argv touches the invariants in ARCHITECTURE.md section 13
  and at least two live tests. Read section 13 before editing either.
- Adding a `procgroup` component touches the teardown ordering that
  `internal/materialiser` and `ReapSurvivors` depend on. A component that is up when the group
  dies is a leak.
- The scale decision's idempotency note (ADR 0014) exists because a second `Provision` for a
  cluster that already has a node is a second node. Extending to N must not reintroduce it.

## 8. Log

- 2026-10-05 Opened. Discovery done against this repo, deno-kcp, kcp-libs, deno-kcp-specs,
  gitlab-autoscale-socialweb-computer, the vendored GitLab executor doc, the k3s advanced docs
  and the cluster-autoscaler external-gRPC provider docs. Decisions D1-D6 taken.
- 2026-10-05 G1 CLOSED - reachability verdict, measured, not reasoned:
  - `internal/kcpclient/names.go:27-33` MountPath = `root:<accountHash>:<cluster>:clusters`;
    `internal/kcpclient/kcpclient.go:397-408` renders the kubeconfig with `server` =
    `https://<kcp-host>/clusters/<mountPath>`. Shipped `<kcp-host>` = `https://127.0.0.1:6443`
    (`deploy/container-entrypoint.sh:26`).
  - `internal/kubeconfig/kubeconfig.go:34-93`: the tenant kubeconfig carries an exec credential
    only - no client certificate, no token, and for a device grant no CA either.
  - `cmd/socialweb-credential/main.go:70-98` POSTs the device key to `SOCIALWEB_MINT_URL`;
    `internal/xrpcserver/xrpcserver.go:31` MintPath = `/v1/credential`;
    `internal/tenantclient/tenantclient.go:164-196` issues the tenant SA token.
  - `docs/ARCHITECTURE.md:308-314`: only a bearer crosses the mount; a client certificate does
    not. `:302-306`: the minted kubeconfig's own server is loopback and works only from the host
    that wrote it.
  - `cmd/socialweb-gateway/config.go:145-156` `requireLoopbackAddr`: the gateway must bind
    loopback. `deploy/container-entrypoint.sh:285`: the only published port, 8443, terminates
    into the gateway and nothing else.
  - Verdict: **no path exists from an off-host VM to a tenant apiserver.** Nothing in
    `internal/`, `cmd/` or `deploy/` provides reverse ingress. D7 records the two stories.
- 2026-10-05 New constraint found before the CCM was written: invariant 20 forbids the provider
  reading the k3s admin kubeconfig, and invariant 14 forbids reading an unreadable instance
  source as an empty one. The CCM brief was corrected to mint its credential and to treat
  `Instances.Get` errors as "patch nothing, delete nothing".
- 2026-10-05 Baseline re-measured before anything was built on it:
  `TestTheFourStepProofRunsThroughTheCompositionRoot` PASSES in 103.10 s, all four transitions,
  against a node image already in the store. STEP 1 Ready at `https://127.0.0.1:62002` with zero
  nodes; STEP 2 empty and still empty across 10 passes 2 s apart over 20 s; STEP 3
  `INSTANCE_START_TO_READY_S=5.430` (host-timed, resolution 250 ms); STEP 4 node gone, control
  plane still Ready, still listing `default kube-node-lease kube-public kube-system users`. The
  leased port in the log confirms the port is not a constant, which is why the docs read it from
  `status.url` instead of printing a number. The reading is recorded in `handoff.yaml` with its
  date; the earlier 97.65 s entry is kept, because the two are different builds.
- 2026-10-05 Reaching the tenant apiserver from a runner: the direct loopback URL with a minted
  client certificate is the route that works, not the kcp mount. The mount is a reverse proxy and
  carries bearers only, so a CI runner's exec and attach paths are not guaranteed to traverse it.
  This is why `cmd/socialweb-tenantcredential` exists rather than a documented use of the mount.
- 2026-10-05 Opened `docs/plans/0002-scale-to-n-nodes.md` for the autoscaling half, because the
  decision between zero and one is a separate change from the CCM and carries its own invariants
  (sections 10 and 13) and its own limit, stated in that plan at section 4.4.
- 2026-10-05 P4 MEASURED. `TestAGitLabJobPodIsPlacedOnAProvisionedNode`
  (`cmd/socialweb-clusterprovider/live_gitlab_ci_test.go`) PASSES in 84.40 s on this host, a second
  run of the same test having reported 86.16 s against a different leased port (62002 vs 62302).
  It applies the Pod shape the kubernetes executor actually creates - one build container and one
  helper container, `RestartPolicy: Never`, the runner's own labels, no control-plane annotation -
  and asserts the scale decision reads `Provision` with `nodes=1`, the pod reaches `Succeeded`,
  `spec.nodeName` equals the node the decision provisioned, and that deleting the pod returns the
  cluster to zero with the control plane still `Ready`. What this proves is the CLUSTER half of
  GitLab CI; the GitLab instance half is documented in `docs/gitlab/` and not run here.
  A sibling of the four-step harness rather than a modification of it, so the existing proof is
  untouched: `fourstepBringUp` and its helpers are reused as-is, and the test consumes one more of
  the eight `liveports` FourStepProof slots, which is why the two live tests are sequenced and
  never run concurrently.
- 2026-10-05 P1a landed: `internal/cloudprovider/` (providerid, instances port, pure `Desired`,
  pure `Measure`/`Target`, the node controller, a file-backed instance source, the node writer) and
  `cmd/socialweb-cloud-controller-manager`, with tests. **Not installed**: nothing starts it.
  `docs/plans/0003-install-the-ccm-in-the-process-group.md` is that step and `NEXT.yaml` CCM-1
  stays open until it lands.
- 2026-10-05 GL-1's precondition closed: `MintTenantCredentialForClusterAs` mints an arbitrary
  CommonName and Organizations from the cluster's own ClientCA, because the vault's
  `tenant-superuser` PKI role pins `organization: system:masters` and can therefore issue only the
  superuser. A runner with cluster-admin would have made every RBAC rule in
  `docs/gitlab/02-runner-kubernetes.md` decorative, so the docs' recipe depends on this.
- 2026-10-11 Closed. Every phase landed, two of them in a different shape than planned:
  - P1 CCM: built here (P1a), installed by plan 0003 into the per-cluster process group; the live
    STEP 3 reading is `spec.providerID = socialweb://liveweb/liveweb-node1` (ARCHITECTURE section
    15). Three tenant CCMs ran on prod on 2026-10-11.
  - P2 autoscaling N>1: plan 0002 (Demand, Target, Decision.Nodes, per-node scale-in); plan 0004
    section 6a scaled five concurrent jobs to five nodes and back to zero.
  - P3 docs/gitlab/: shipped as README.md, 01-kcp-server.md, 02-gitlab-runner.md, 03-jobs.md and
    04-spa-cluster-runner.md rather than the file names listed in section 5; the VM-and-GitLab
    install is the existing GitLab on hat-10, not a recipe this repo runs.
  - P4 the proof: `deploy/gitlab-ci-proof.sh` was never written. A real GitLab pipeline replaced the
    GitLab-shaped pod: plan 0004 job 183 (streamplace `test`, 1104 tests, 0 failures) on a local
    substrate, then plan 0005 pipelines 78 and 79 on market nodes of an SPA-created prod cluster
    (0 nodes idle, scale up, per-node scale-in back to 0, control plane Ready, no ssh to guests).

