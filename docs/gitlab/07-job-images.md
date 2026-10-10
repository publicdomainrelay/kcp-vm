# 07 - The image a job runs in, and what to do when it does not exist

Job Pod's image comes from the project's own `.gitlab-ci.yml`:

```yaml
  image: "$DOCKER_REPO:builder-$DOCKERFILE_HASH"
```

`DOCKER_REPO` is a variable in that file. `DOCKERFILE_HASH` comes from the included
`.ci/dockerfile-hash.yaml`, which is generated:

```bash
printf "variables:\n  DOCKERFILE_HASH: `git hash-object docker/build.Dockerfile`" > .ci/dockerfile-hash.yaml
```

So the image is named by a hash of the Dockerfile. Change the Dockerfile, get a different image
name, and the old one keeps its own name forever.

## Check the image exists BEFORE running anything

Do not discover this as `ImagePullBackOff` twenty minutes into a node boot. The manifest endpoint
needs a token even for a public repo:

```bash
REPO=streamplace/streamplace
TAG=builder-3e14c389b80a359787d73741f221fdfaacb12f53

TOKEN=$(curl -s "https://ghcr.io/token?scope=repository:${REPO}:pull&service=ghcr.io" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

curl -s -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer ${TOKEN}" \
  -H 'Accept: application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.v2+json' \
  "https://ghcr.io/v2/${REPO}/manifests/${TAG}"
```

`200` means it will pull. `404 MANIFEST_UNKNOWN` means the job cannot start, whatever else is
right about the cluster.

## The case that actually happens

A project can name an image nobody publishes. Measured on 2026-10-06:

- `streamplace/streamplace`'s `eli/moqt-firehose` branch named
  `public.ecr.aws/m4j3c0j7/streamplace:builder-4dc3e0c...`, and
  `public.ecr.aws/m4j3c0j7/streamplace` answers `NAME_UNKNOWN` for the repository ITSELF. Nothing
  publishes it. No job in that project builds or pushes it either, so every job in that file was
  unrunnable as written.
- upstream `next` sets `DOCKER_REPO: "ghcr.io/streamplace/streamplace"` and upstream's
  `.github/workflows/docker.yaml` publishes `builder-<DOCKERFILE_HASH>` there. The tag that
  branch's own `.ci/dockerfile-hash.yaml` names was built two days earlier.

Read the upstream branch's `variables:` block before concluding the image is missing. A stale
branch and a missing image look identical from the failure.

## If the branch you must run names a tag nobody published

Two ways, in order of cost:

1. **Move the branch.** Rebase onto the branch that publishes, and use its own
   `.ci/dockerfile-hash.yaml`. Its `DOCKER_REPO` and hash already agree with each other. This is
   usually free and it is what was done here.
2. **Build it and host it.** `docker build --target builder -f docker/build.Dockerfile .` at the
   hash the branch names, then push to a registry the GUEST can reach, and point `DOCKER_REPO` at
   it. The guest has outbound internet - the runner's own helper image arrives that way - so a
   public registry works and a registry on a private address does not, unless the guest is given a
   route and a registries.yaml.

## What the guest does and does not pull

- The guest HAS outbound internet. Seed-free pulls work: `registry.gitlab.com/gitlab-org/gitlab-runner-helper`
  and `rancher/mirrored-library-busybox` both arrived over the network in these runs.
- `image_pull_policy = "if-not-present"` is still right: the node image seeds some images into the
  guest's containerd, and an always-pull policy throws that away.
- **A big image cannot be seeded into the node image.** The node image is written with
  `mkfs.ext4 -d`, which refuses a file of about 2 GiB or more:

  ```
  __populate_fs: Ext2 file too big while writing file "127.0.0.1-5000-...tar"
  ```

  A multi-gigabyte builder image is 3.3 GB as a tarball, so it is refused. Pull it instead. See
  [09](09-sizing-nodes.md) for the space that pull then needs inside the guest.

- **The image must match the branch's toolchain, not just exist.** A published image whose
  Dockerfile hash is old is an old toolchain. Measured: a five-month-old builder image had a Rust
  that predates let-chains and `make dev-setup` died on
  `error[E0658]: let expressions in this position are unstable`, and a corepack that created no
  `pnpm` shims, so `pnpm install` died on `pnpm: No such file or directory`. A hash that resolves
  is not the same as a toolchain that builds.

## Per-image tooling the job assumes

A job written for one image assumes that image's tools. When it runs in another, the failure is
`exit code 127` at the job's first line, and it reads as a broken job. Patch the runner, not the
job, so the job definition stays the thing being measured:

```toml
pre_build_script = "if ! command -v pnpm >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then npm install -g pnpm@10.11.0; fi; true"
```

`pre_build_script` is a string. Ending it `true` means the guard never fails the job it protects.
See [05](05-runner-config.md#traps).
