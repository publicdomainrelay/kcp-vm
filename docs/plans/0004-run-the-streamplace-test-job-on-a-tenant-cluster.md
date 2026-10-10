# 0004 - Run streamplace's `test` job on a tenant cluster

Status: in flight, opened 2026-10-06.
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
