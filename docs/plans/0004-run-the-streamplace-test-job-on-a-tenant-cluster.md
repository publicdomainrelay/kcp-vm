# 0004 - Run streamplace's `test` job on a tenant cluster

Status: in flight, opened 2026-10-06.
Repo: socialweb-computer-kcp.
Related: `docs/gitlab/` (the operator recipes), `cmd/socialweb-clusterprovider/live_gitlab_pipeline_test.go`
(the only thing in this tree that drives a real GitLab pipeline against a real tenant cluster).

## 1. Goal

Run the `test` job of `streamplace/streamplace`'s `.gitlab-ci.yml` (branch `eli/moqt-firehose`,
job at `.gitlab-ci.yml:104`) as a Pod on a tenant k3s cluster this repo provisions on THIS host,
with the real GitLab at `https://gitlab.dorper-matrix.ts.net:8443` as the source of the job.

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
- **D4 - the job image comes from the registry this project already publishes to.** B1 has no
  upstream fix: `public.ecr.aws/m4j3c0j7/streamplace` does not exist and no job in the project
  builds or pushes it, so every job in that file is unrunnable as written.
  `ghcr.io/streamplace/streamplace` carries the image the builder stage of
  `docker/build.Dockerfile` produces, under the tag `builder-`, and the guest can pull it over the
  internet -- measured, because the runner's own helper image already arrives that way. So the
  branch sets `DOCKER_REPO` to that registry and empties `DOCKERFILE_HASH`, which makes the job's
  untouched `image: "$DOCKER_REPO:builder-$DOCKERFILE_HASH"` resolve to the published tag.
  **Seeding the image into the node image was tried first and does not work**: the node image is
  written with `mkfs.ext4 -d`, and it refuses a file larger than about 2 GiB (`Ext2 file too big
  while writing file ...`), while the builder tarball is 3.3 GB. That is measured, not assumed --
  see section 7.
- **D5 - the job runs from a branch that carries only the job.** The runner is `concurrent = 1`,
  this host runs one node, and every other job in `streamplace/.gitlab-ci.yml` is untagged, so the
  other build jobs would each take the only slot for up to their own 2h timeout and `test` would
  never be reached -- it sorts last in its stage. A branch `kcp-tenant-ci` therefore carries the
  `test` job verbatim and nothing else, and points `DOCKER_REPO` at the seeded image. The job's
  own keys -- image, script, timeout, artifacts -- are byte-for-byte what the branch this is
  measured against has.
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
