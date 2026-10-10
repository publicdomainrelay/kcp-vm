# 0004 - Run streamplace's `test` job on a tenant cluster

Status: done. The `test` job of `streamplace/streamplace` runs green as a Pod on a tenant cluster
this host provisions: job 183, upstream `next`, 2063 s, 1104 tests, 0 failures. See section 6.
Opened 2026-10-06; closed 2026-10-06.
Repo: socialweb-computer-kcp.
Related: `docs/gitlab/` (the operator recipes), `cmd/socialweb-clusterprovider/live_gitlab_pipeline_test.go`
(the only thing in this tree that drives a real GitLab pipeline against a real tenant cluster).

## 1. Goal

Run the `test` job of `streamplace/streamplace`'s `.gitlab-ci.yml` as a Pod on a tenant k3s
cluster this repo provisions on THIS host, with the real GitLab at
`https://gitlab.dorper-matrix.ts.net:8443` as the source of the job.

The branch it runs is `kcp-tenant-ci`, which is upstream `next` (`git.stream.place`, the project's
canonical home; the GitLab instance is a mirror) with every job but `test` removed. `test` is
byte-for-byte upstream's, including the `STREAMPLACE_TEST_POSTGRES_COMMAND` that branch added. The
first attempts ran `eli/moqt-firehose`, the mirror's default branch and three months behind;
`next` is the line upstream actually builds.

kcp runs on this host and only this host. The GitLab host (`ssh gitlab`, 192.168.0.49) runs the
GitLab instance and must not run kcp.

The job, verbatim, is:

```yaml
test:
  stage: build
  interruptible: true
  image: "$DOCKER_REPO:builder-$DOCKERFILE_HASH"
  timeout: 2 hours
  variables:
    STREAMPLACE_TEST_COUNT: "1"
  script:
    - git fetch --unshallow || echo 'already unshallow'
    - make dev-setup && make dev && make dev-test && make ci-lexicons
  artifacts:
    when: always
    paths:
      - test.xml
    reports:
      junit: test.xml
```

`DOCKER_REPO` is `public.ecr.aws/m4j3c0j7/streamplace` and `DOCKERFILE_HASH` comes from the
included `.ci/dockerfile-hash.yaml`.

## 2. What exists, measured on 2026-10-06

- The four-step proof passes on this host, unmodified: control plane Ready at zero nodes, an empty
  cluster that stays at zero, a node provisioned by the scale decision when a tenant workload
  appears, and the workload running on it. See the run in section 6.
- This host carries every live input the proofs want: `/dev/kvm`, Docker, postgres 18,
  `kine`, `k3s`, `kubectl`, and the operator's image store under `/home/johnandersen777/kcp-image/`
  (nodeimage.json, `artifacts/bin/firecracker`, `artifacts/img/ubuntu-24.04.squashfs`, `out/`).
- GitLab is 19.2.7 at `gitlab.dorper-matrix.ts.net:8443`. Project `streamplace/streamplace` is
  id 1, its default branch is `eli/moqt-firehose`, and its five recorded pipelines all FAILED.
- Exactly one runner is registered: id 1, `socialwebcomputer`, instance_type, online, tags
  `[socialwebcomputer]`, `run_untagged` true, version 19.2.1. It is NOT a kubernetes executor.
  It runs on the GitLab host as `executor = "instance"` with the Fleeting plugin
  `fleeting-plugin-socialwebcomputer`, which provisions VMs through the market
  (`request-vm-ssh`). It is a different mechanism from this plan's, and an untagged job is
  eligible for it.

## 3. The blockers, named

- **B1 - the job's image does not exist.** `public.ecr.aws/m4j3c0j7/streamplace` answers
  `NAME_UNKNOWN` for the repository itself, so `builder-4dc3e0c...` cannot be pulled. As written,
  the job fails at image pull before its first script line. Nothing else in this plan can run until
  this is answered.
- **B2 - the guest is smaller than the job.** A node boots at `DefaultVcpu = 2`,
  `DefaultMemMiB = 2048`, `DefaultDiskMiB = 16` (`internal/nodevm/spec.go:41-47`). The job builds
  Rust (cargo), meson/C, and Go, `-p 1`, in one Pod. The image alone is multi-gigabyte and the
  disk is 16 GiB.
- **B3 - CLOSED 2026-10-06.** A guest is addressed behind pasta (`internal/nodevm/spec.go`) and
  the node agent writes the guest's only nameserver as the gateway
  (`cmd/socialweb-nodeagent/agent.go:228`), so the guest's resolution and its default route both
  go through this host. Measured, not reasoned: a job pod inside a firecracker guest cloned
  `johnandersen777/kcp-ci-probe` from the real GitLab. See section 6.
- **B4 - CLOSED 2026-10-06.** The kubernetes executor's `host` is the apiserver's own loopback
  address on THIS host, and its port is leased per cluster. Solved by running the runner here, on
  the same host: nothing has to be tunnelled, and the port comes from the cluster's own lease. The
  existing instance runner is paused for a run, not deleted.
- **B5 - CLOSED 2026-10-06.** `Vcpu`/`MemMiB` live on the image manifest
  (`internal/nodefirecracker/manifest.go:18-22`) and the builder wrote neither, so they always took
  the defaults; `DiskMiB` was a runtime `Options` field with no clusterprovider config key. Both
  are now config: `vcpu` and `memMiB` on the builder's configuration, folded into the image
  fingerprint, and `SOCIALWEB_CLUSTERPROVIDER_NODE_DISK_MIB` for the disk. `inputs_test.go`'s
  declaration table is what caught that they had to be recorded as well as read.
- **B7 - the guest's root filesystem is where the job's image and its build outputs have to fit.**
  The node's root filesystem is `rootfs.ext4`, cloned per node
  (`internal/nodefirecracker/node.go:153`), and containerd's content store and every container
  layer live inside it. Its size is the staged tree plus `rootfs_headroom_mib`, and nothing else
  sizes it. The node disk is NOT an option: it is attached and mounted READ-ONLY as the identity
  disk (`cmd/socialweb-nodeagent/agent.go:173`), so `SOCIALWEB_CLUSTERPROVIDER_NODE_DISK_MIB`
  cannot hold state. Measured twice: at the default headroom the pull of the job's image dies with
  `no space left on device` writing
  `/var/lib/rancher/k3s/agent/containerd/io.containerd.content.v1.content/ingest/...`, and at
  `rootfs_headroom_mib: 55000` the root filesystem is 58,401,488,896 bytes and the pull completes.
  A corollary that bit once: those files are written into the test's state directory, which is
  `os.MkdirTemp("", ...)` and therefore `/tmp`. On this host `/tmp` is a 24 GiB tmpfs, so the 58 GB
  root filesystem and the 150 GiB node disk have to land somewhere else -- the run is made with
  `TMPDIR` set to a directory on the host's own disk. A state directory that cannot hold a node's
  disks fails as a full filesystem partway through a boot, not as a configuration error.
- **B10 - the job's own log exceeds the RUNNER's cap, not GitLab's.** `make dev-test` runs
  `go test -p 1 -timeout 30m ./pkg/... -v | tee /dev/stderr`, so the trace is the whole verbose
  test log, and it stops at 4,194,304 bytes: `Job's log exceeded limit of 4194304 bytes. Job
  execution will continue but no more output will be collected.` That string is in the
  `gitlab-runner` binary, and the key behind it is `output_limit`, "Maximum build trace size in
  kilobytes", default 4096 -- so the runner, not the server, was the binding constraint. Answering
  it the wrong way first is recorded because the wrong answer is plausible and cost a GitLab
  reconfigure: `ci_jobs_trace_size_limit` exists in `plan_limits` and `project.actual_limits`
  (`app/services/ci/append_build_trace_service.rb:87`) and this instance already had it at 100 MB,
  and `/application/settings` does not carry it at all. The runner config now sets
  `output_limit = 51200` (50 MB), between the 20 MB wanted and GitLab's own 100 MB ceiling so the
  server stays the outer bound. `gitlab.rb` is restored byte-for-byte; the key it briefly carried
  was never read by GitLab, so the generated `gitlab.yml` did not change.
- **B9 - CLOSED 2026-10-06, by rebasing.** The published builder image for `eli/moqt-firehose`'s
  Dockerfile hash is a `builder-` tag built 2025-05-23, and its Rust predates let-chains (stable
  1.88): `make dev-setup` died at `FAILED: rust/iroh-streamplace/libiroh_streamplace.so` with
  `error[E0658]: let expressions in this position are unstable` compiling `iroh-smol-kv`. The repo
  pins no `rust-toolchain.toml`, so rustup used the image's default. Upstream's `next` names a
  `DOCKERFILE_HASH` whose image was built 2026-10-04, so the branch is rebased onto `next` and the
  stale image is not used at all.
- **B8 - the `eli/moqt-firehose` builder image has no `pnpm`.** The job's first make target is
  `dev-setup` -> `app-cached` -> `make install` -> `pnpm install`, and in
  `ghcr.io/streamplace/streamplace:builder-` that fails with
  `make[2]: pnpm: No such file or directory`. A fresh build of the SAME
  `docker/build.Dockerfile` on this host has `/usr/local/bin/pnpm`, a symlink to corepack's
  `pnpm.js`, so the published tag predates the corepack that writes those shims -- the builder
  image is named by the Dockerfile's hash, and no published tag matches this branch's hash. The
  project's own macOS job runs `npm install -g corepack@latest --force && corepack enable`
  explicitly, which is the same step. Answered runner-side, see D7.
- **B6 - the node image cannot carry a large image.** `layDownRootfs` writes the root filesystem
  with `mkfs.ext4 -d`, which refuses a file of about 2 GiB or more (`__populate_fs: Ext2 file too
  big while writing file ...`). A 3.3 GB tarball of the builder image is refused at 6 GB and at
  12 GB, with the file alone and with the whole stage; a 3.5 GB file of zeros is accepted at 4.9 GB
  and 6.5 GB, and 1 GB of the same tarball is accepted. Measured, not assumed; see section 7. So
  the node image cannot seed this job's image, and the guest pulls it instead.

## 4. Decisions

- **D1 - kcp runs here; the runner is the part that moves.** The apiserver binds loopback on this
  host, so a kubernetes-executor runner is run here. An `ssh -R` tunnel from here to the GitLab
  host is the fallback if the runner has to live beside GitLab; it is a raw TCP forward, so it
  preserves the SPDY `pods/exec` paths the kcp mount URL does not, and `docs/gitlab/README.md`'s
  warning is about the mount, not about a tunnel.
- **D2 - prove the integration with a small job before the real one.** A separate GitLab project
  carries the probe job, so the real project's `.gitlab-ci.yml` is never overwritten by an
  experiment and the real job changes nothing until it is the thing being measured.
- **D3 - pause, do not delete, the existing runner.** Runner 1 is someone's working setup for a
  different mechanism. An untagged job is eligible for it, so it is paused for the duration of a
  run and restored after. Deleting it is not this plan's to do.
- **D4 - the job image comes from the registry upstream already publishes to, and upstream already
  says so.** B1 has no fix to make: `public.ecr.aws/m4j3c0j7/streamplace` answers `NAME_UNKNOWN`,
  but upstream's own `.gitlab-ci.yml` sets `DOCKER_REPO: "ghcr.io/streamplace/streamplace"` and
  publishes `builder-<DOCKERFILE_HASH>` there from `.github/workflows/docker.yaml`. On
  `eli/moqt-firehose` -- the June branch this plan started from -- `DOCKER_REPO` still named the
  ECR repo, which is why the first attempts had to work out the ghcr path at all. Rebasing onto
  `upstream/next` removed that problem rather than solving it: the branch now carries upstream's
  own `variables:` block, and the tag its own `.ci/dockerfile-hash.yaml` names
  (`builder-3e14c389b80a359787d73741f221fdfaacb12f53`) exists, built 2026-10-04.
  **Seeding the image into the node image was tried first and does not work**: the node image is
  written with `mkfs.ext4 -d`, and it refuses a file larger than about 2 GiB (`Ext2 file too big
  while writing file ...`), while the builder tarball is 3.3 GB. That is measured, not assumed --
  see section 7.
- **D5 - the job runs from a branch that carries only the job.** The runner is `concurrent = 1`,
  this host runs one node, and every other job in `streamplace/.gitlab-ci.yml` is untagged, so the
  other build jobs would each take the only slot for up to their own 2h timeout and `test` would
  never be reached -- it sorts last in its stage. A branch `kcp-tenant-ci` therefore carries the
  `test` job verbatim and nothing else, and names the registry this project publishes its builder
  image to. The job's own keys -- image, script, timeout, artifacts -- are byte-for-byte what the
  branch this is measured against has.
- **D8 - the runner is not pinned to one job, and the harness stopped dropping the scale knobs.**
  `concurrent = 1` was taken from `docs/gitlab/README.md`, which says to set it until the cluster
  can place more than one job. That limit is gone -- the scale decision carries a target total --
  so a run with the old value has a single slot that a job from any earlier, cancelled pipeline can
  take, and the reading then comes from a pipeline nobody was watching. Measured: a cancelled run's
  job held the slot and the current pipeline sat `pending`. The harness also passed
  `SCALE_MAX_NODES` and `SCALE_PODS_PER_NODE` through but not `NODE_DISK_MIB`, so the disk knob
  this plan added was dropped by the code written to exercise it.
- **D7 - pnpm is provided by the runner, not by the job.** B8 is a property of the image, and the
  image is named by a Dockerfile hash nothing published matches. Editing the job to add a setup
  step would change the thing being measured, so the step goes where the project already puts it
  for macOS -- a `pre_build_script` in the runner's `config.toml`, which runs in the build
  container before the job's script. It prefers `corepack enable`, which honours the
  `packageManager` field the project pins, and falls back to installing the version that field
  names. Against upstream `next`'s image it is a no-op -- the step prints nothing, because
  `command -v pnpm` succeeds -- which is the reading that says the `next` image carries the shims
  the `eli/moqt-firehose` one did not. It is left in as the guard it turned out to be.
- **D6 - the runner gets a scoped credential, not the superuser.** `docs/gitlab/02` is explicit
  that handing a runner `system:admin`/`system:masters` makes every RBAC rule decorative. The
  existing live test mints the superuser credential; the new one mints `gitlab-runner` /
  `gitlab-runner` from the ClientCA on disk and applies the ClusterRole and ClusterRoleBinding the
  document lists, so the identity under test is the one a real deployment would use.

## 5. Steps

1. Four-step proof on this host. DONE (section 6).
2. Egress: a guest reaching GitLab. DONE (section 6), by running a cloning job rather than by
   dialing -- the clone is the thing the job actually does.
3. A small pipeline from a probe project, run by a kubernetes-executor runner registered for it,
   against a tenant cluster this host provisioned. DONE (section 6), with the scoped identity.
4. Sizing: `vcpu`, `memMiB` and `diskMiB`, plumbed to the manifest and the node provider, so a node
   can be sized for a real job. DONE, committed. Closes B5 and B2.
5. The job image. DONE, by pointing the branch at the registry this project already publishes the
   builder stage to. Closes B1; B6 is the reason seeding was abandoned.
6. The real `test` job, run through the real GitLab, with the job's own trace and `test.xml` as the
   reading. IN FLIGHT.

## 6. Readings

**2026-10-06, a real GitLab pipeline runs a job on a tenant cluster.**
`TestAGitLabPipelineRunsOnATenantCluster` against the real GitLab and the probe project,
PASS, 37.58 s. Pipeline 46 succeeded; GitLab's runner created
`runner-8t2uishn2-project-5-concurrent-0-ny1rvwue` and this cluster placed it on
`liveweb-node1`, the node the scale decision provisioned. The existing instance runner
(`socialwebcomputer`, id 1) was paused for the run so the reading could not have come from it,
and is restored after.

**2026-10-06, a job pod clones its repository from inside the guest.** `TestALiveGitLabJobRunsOnATenantCluster`
against the probe project, PASS, 42.51 s. The job's own trace:

```
Getting source from Git repository
Fetching changes with git depth set to 20...
Initialized empty Git repository in /builds/johnandersen777/kcp-ci-probe/.git/
Checking out c6d139c1 as detached HEAD (ref is main)
...
GIT_DIR_PRESENT
Job succeeded
```

This is the reading that closes B3. It also shows the runner's helper image being pulled from
the internet by the guest, because the helper image is not one the node image seeds.

**2026-10-06, four-step proof, this host, firecracker substrate.** PASS. The control plane reached
`Ready` at `https://127.0.0.1:62202` with zero nodes; `kubectl get nodes` was empty and stayed
empty across 10 passes 2 s apart over 20 s; the scale decision read `Provision: intent and no
node`; the node joined and the cloud controller manager stamped it
`providerID=socialweb://liveweb/liveweb-node1`; `INSTANCE_START_TO_READY_S=5.504` measured from the
firecracker VMM's instance record to the Node's `Ready` condition at 250 ms resolution.

**2026-10-06, the job's own script, as far as it has run.** The `test` job of project 1 on
`kcp-tenant-ci`, pipeline 57, job 181, running as a Pod on `liveweb-node1`:

```
$ make dev-setup && make dev && make dev-test && make ci-lexicons
pnpm install
! Corepack is about to download https://registry.npmjs.org/pnpm/-/pnpm-10.11.0.tgz
  Content-addressable store is at: /builds/.pnpm-store/v10
FFmpeg| Has header "pthread_np.h" : NO
HEAD is now at 20cbb63 meson: fix x264 version check without stdint.h header
proxy-libintl| ... meson.build:14: WARNING: ...
Progress: resolved 0, reused 0, downloaded 2917, added 3875
```

That is the target itself: the job's first `make` target, the pinned pnpm, the meson configure of
FFmpeg from source, and the subproject fetches, all inside a firecracker guest this host booted
because a Pod could not be placed.

**2026-10-06, the streamplace `test` job PASSES on a tenant cluster.** Pipeline 57, job 183,
branch `kcp-tenant-ci` (upstream `next` with only `test` kept), project 1 on the real GitLab at
`gitlab.dorper-matrix.ts.net:8443`. Job status `success`, duration **2063 s**, `failure_reason`
`None`. `TestALiveGitLabJobRunsOnATenantCluster` PASS, 2095.10 s, and it records the placement:

```
STEP 10 job test ran in pod(s)
  map[runner-hhvwl7zg6-project-1-concurrent-0-tifm253t:liveweb-node1],
  placed by this cluster
```

`liveweb-node1` is a firecracker guest this host booted because the job's Pod was Pending and
`internal/scale` read that as intent. The Job's own junit artifact, `test.xml`, downloaded from
`/projects/1/jobs/183/artifacts` and 19,820,634 bytes:

```
testsuites 82   tests 1104   failures 0   errors 0   skipped 15
  stream.place/streamplace/pkg/api   67 tests  0 failures
  stream.place/streamplace/pkg/aqtime 38 tests  0 failures
  stream.place/streamplace/pkg/viewlog 38 tests 0 failures
  stream.place/streamplace/pkg/vod    26 tests  0 failures
```

That is `make dev-setup && make dev && make dev-test && make ci-lexicons` run to exit 0: FFmpeg,
GStreamer, x264 and libav built from source in the guest, the Rust `iroh-streamplace` cdylib
built, the Go tree built, and 1104 tests green -- on a cluster whose control plane was at zero
nodes when the job arrived. The console trace is truncated at 4 MiB by the runner's `output_limit`
(B10); the junit artifact is the complete record and is the better instrument anyway.

## 6a. Autoscaling to five nodes, and back to zero

The same instrument, pointed at the scale decision instead of at one job. The branch carries five
`scale-check-N` jobs in place of `test` -- `test` is commented out rather than deleted, because
the two need different cluster shapes: a toolchain build wants one large node, an autoscale check
wants five small ones at once. Each job sleeps, so all five Pods are alive together; a job that
exits immediately is a Pod that is gone before the second one is scheduled.

**One job per node is configured, not hoped for.** The runner's `podAntiAffinity` is first-class
config, unlike `pod_spec` which needs the beta `FF_USE_ADVANCED_POD_SPEC_CONFIGURATION` flag:

```toml
    [runners.kubernetes.affinity]
      [runners.kubernetes.affinity.pod_anti_affinity]
        [[...required_during_scheduling_ignored_during_execution]]
          topology_key = "kubernetes.io/hostname"
          [...label_selector.match_expressions]
            key = "project.runner.gitlab.com/id"
            operator = "Exists"
```

`project.runner.gitlab.com/id` is a label the runner puts on every job Pod and that nothing else
carries, so the Pods repel each other and nothing else. `Demand` counts Pods that are Pending,
Running or Unknown, so five live job Pods are a target of five at one Pod per node, and zero once
they are gone.

One thing about the harness that costs a run to learn: **the scale decision is DRIVEN, not
running.** `fourstepStack.pass` calls `prov.Reconcile` and `driver.RunPass` explicitly, so a test
that stops asking never sees a decision and waits for a teardown no one requested -- which is why
the other live tests call `stack.choose(t, ctx, scale.Teardown)`. The first version of this test
waited for the cluster to fall to zero on its own and sat at five nodes until the deadline.

`TestFiveGitLabJobPodsScaleToFiveNodesAndBackToZero` PASS on 2026-10-06, against the real GitLab,
pipeline 61, nodes of 2 vCPU and 3072 MiB:

```
STEP 3  the cluster is at zero nodes before anything is applied: ""
STEP 5  5 job pods exist in gitlab-runner
STEP 6  the scale decision read Provision with nodes=5: intent and no node
STEP 7  5 nodes joined on demand; `kubectl get nodes`:
        liveweb-node1 .. liveweb-node5, all Ready
STEP 8  each job pod is on its own node:
        liveweb-node1 <- ...-concurrent-3    liveweb-node4 <- ...-concurrent-1
        liveweb-node2 <- ...-concurrent-4    liveweb-node5 <- ...-concurrent-2
        liveweb-node3 <- ...-concurrent-0
```

Observed mid-flight, one node at a time: with one node up, three Pods sat `Init` on it and two
stayed `Pending` -- the anti-affinity refusing to co-schedule -- and the two Pending Pods are what
took the count to three, four and five. That is the loop working, not a coincidence of scheduling.

The nodes of a scale check are small on purpose: five of the nodes the `test` job needs would be
60 vCPU and 120 GiB, which this host does not have. The Pod requests drop to 100m/128Mi for the
same reason -- requests are what the scheduler places against, and limits stay generous because
limits are not placement.

**Two things the first attempt at this got wrong, both worth keeping.**

- **A limit is a request when no request is given.** `helper_cpu_limit = 1` with no helper request
  made every job Pod ask for about 2.1 CPU -- the build container's own request plus the helper's
  and the init container's, each pulled up from its limit -- and a two-vCPU node answers that with
  `Unschedulable: "0/1 nodes are available: 1 Insufficient cpu"`. The reading is a Pod that sits
  `Pending` while nodes join around it, which looks like a slow cluster and is actually a Pod that
  cannot fit on any of it. `helper_cpu_request` and `helper_memory_request` exist; setting them
  says where the Pod is placed without capping what it may burst to.
- **A job Pod resolves through CoreDNS, and CoreDNS is an addon that comes up after the node it
  runs on.** The scale-check jobs cloned like every other job, and the first attempt lost three of
  five to
  `fatal: unable to access '...': Could not resolve host: gitlab.dorper-matrix.ts.net (Timeout
  while contacting DNS servers)`,
  reported as `script_failure` -- which reads as a broken job. A Pod's `/etc/resolv.conf` is
  k3s's `kube-dns`, so the query goes Pod -> CoreDNS -> the node's own resolver -> pasta -> the
  host, and CoreDNS is created when the first node joins and is not serving for the first seconds
  after. A job that starts the moment its node joins is a job that queries a resolver that is not
  there yet.

  **pasta is not the fault, and it was the first thing suspected.** Querying its forwarder at the
  gateway address from inside its own namespace answers `gitlab.dorper-matrix.ts.net`, `github.com`
  and `ghcr.io` in about ten milliseconds each. The host's `nameserver 127.0.0.53` is
  systemd-resolved's stub and pasta reaches it fine, tailscale MagicDNS included. The fault is one
  hop further in.

  The fix is `pre_get_sources_script`, which runs BEFORE the clone; `pre_build_script` runs after
  it, which is why the pnpm step could never have covered this. The gate waits for the job's own
  nameserver to answer for `CI_SERVER_HOST` -- the name the clone is about to need -- and gives up
  quietly rather than failing the job it exists to protect.

  **It is not a startup delay, and it does not go away.** Measured on a live five-node cluster,
  from a Pod on each node, TCP to the CoreDNS Pod's own IP:

  ```
  liveweb-node1  <- CoreDNS runs here   REACHABLE
  liveweb-node2                         UNREACHABLE
  liveweb-node3                         UNREACHABLE
  liveweb-node4                         UNREACHABLE
  liveweb-node5                         UNREACHABLE
  ```

  and `nslookup` inside those four answers `connection timed out; no servers could be reached`.
  The reason is not CoreDNS: **a guest cannot reach another guest.** Each sits behind its own
  pasta instance, pasta is user-mode networking, and it carries a guest's traffic out to the host
  and nothing between two guests, so the nodes have no path to each other and k3s's flannel
  overlay has nothing to run over. Cluster DNS is one hop that happens to need that path.

  So this is a limit of the node network and not a race in the job: **anything that resolves a
  name works on a one-node cluster and on whichever node CoreDNS is on, and fails on the others.**
  It is recorded in `NEXT.yaml` as a limit, with the pod-to-pod reading above as its instrument.
  The scale-check jobs therefore carry `GIT_STRATEGY: none` -- not to route around the failure but
  because a check of the scale decision must not depend on a network path the cluster does not
  have -- and the `pre_get_sources_script` wait stays for the jobs that do clone. Routing around
  this silently was the first response, by giving those jobs `GIT_STRATEGY: none` before the cause
  was known; that is recorded because it is the tempting answer, it hides the failure in the one
  check whose whole job is to find it, and it leaves every other job exposed.

## 7. The mkfs measurement, in full

B6 is the reason the job's image is pulled rather than seeded, so it is written down rather than
summarized. Every line is `mkfs.ext4 -q -F -d <dir> -L <label> <image on an existing sparse
file>`, on this host, mke2fs 1.47.3, and the reading is the exit status and the message.

| Directory populated | Filesystem | Result |
| --- | --- | --- |
| one 3,500,000,000-byte file of zeros | 4,900 MB | accepted, file present at that size |
| the same file, 4 KiB blocks, 256-byte inodes | 6,553,600,000 bytes | accepted, file present at that size |
| the 3,336,075,264-byte builder tarball alone | 6,000 MB | `Ext2 file too big while writing file "127.0.0.1-5000-...tar"` |
| the same tarball alone, 4 KiB blocks, 256-byte inodes | 6,000 MB | same refusal |
| the same tarball, renamed to `short.tar` | 6,000 MB | same refusal |
| the whole 3,877 MiB stage directory, as the builder lays it down | 5,377 MiB and 12,000 MB | same refusal |
| the first 1,000,000,000 bytes of the same tarball | 3,000 MB | accepted |

So the refusal tracks the tarball and not the filesystem size, the block size, the inode size or
the file's name, and it is not a hard 2 GiB limit on files in general -- a larger file of zeros is
accepted in a smaller filesystem. What was not pursued is the exact property of that tarball that
triggers it; the job does not need seeding, and saying so is cheaper than a wrong explanation.
