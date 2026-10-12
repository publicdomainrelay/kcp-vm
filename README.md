# socialweb-computer-kcp

Virtual Kubernetes clusters whose k3s **nodes** scale to zero while the control plane stays live.

```
per virtual cluster:
  kine over an external store      <- the datastore
  k3s server --disable-agent       <- the apiserver. NOT a node. Always up.
  k3s agent(s)                     <- the ONLY nodes. 0 or 1. Provisioned on apply.
```

**Everything is in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).** The objective, the architecture, every ADR, the decision register, the security register, the measurements, the lessons, what is built, what is not, and what is blocked. One design document on purpose: it was twenty-six, they drifted out of agreement, and the drift was invisible from a green test suite. Two directories sit beside it and are deliberately NOT design documents: [`docs/plans/`](docs/plans/) is the numbered plan series for work in flight, and [`docs/gitlab/`](docs/gitlab/) is operator recipes for running GitLab against a tenant cluster. A plan is a work list and is expected to go stale; the architecture document is the thing that must not.

Agents working in this repo should read [`AGENTS.md`](AGENTS.md) first; it points at the one small file that answers each question.

## Prerequisites

- Linux, and Docker. No `/dev/kvm` and no `/dev/net/tun`: a node is a VM rented from the compute
  market and built by the winning bidder's cloud-init, so nothing about it runs on this host (see
  "Substrates").
- A Go toolchain newer than `go.mod` for building.
- The container publishes only `127.0.0.1:8443` (TLS). Nothing else is exposed.

## Run it

The deployment is one container. This builds the image and starts it detached under the name
`socialweb`:

```bash
deploy/run-container.sh
docker logs -f socialweb
```

The container name and the state volume are `SOCIALWEB_CONTAINER_NAME` and
`SOCIALWEB_STATE_VOLUME`. That script is `docker build` plus `docker run
-v <volume>:/var/lib/socialweb -p 127.0.0.1:8443:8443 <image>`. No host systemd unit, no root on
the host, nothing to install.

Wait for it to be ready (exit 0 on ready, non-zero with the log tail otherwise):

```bash
deploy/wait-ready.sh socialweb 300
```

By hand, if you prefer the flags in front of you:

```bash
docker run -d --name socialweb \
  -v socialweb-state:/var/lib/socialweb \
  -p 127.0.0.1:8443:8443 \
  socialweb-control-plane:local

deploy/wait-ready.sh socialweb 300
```

It comes up with no `-e` at all: the entrypoint defaults every setting so a fresh volume reaches
`phase: Ready` in about 30 s. Check that:

```bash
docker logs socialweb | grep -o '"phase":"[A-Za-z]*"' | tail -1     # "phase":"Ready"

curl -sk https://127.0.0.1:8443/                                     # 404, the gateway
curl -sk -X POST https://127.0.0.1:8443/v1/credential                # 401, the gateway
```

The gateway binds `127.0.0.1:8080` inside the container on purpose (it serves plaintext), so a TLS
terminator inside the container is what listens on the published port. `curl -k` is needed because
the default certificate is self-signed; the entrypoint logs its SHA-256 fingerprint:

```bash
docker logs socialweb | grep -i fingerprint
```

Override the defaults with the documented file, or with `-e`:

```bash
docker run -d --name socialweb \
  -v socialweb-state:/var/lib/socialweb \
  -p 127.0.0.1:8443:8443 \
  --env-file deploy/container.env.example \
  socialweb-control-plane:local
```

Terminate TLS with your own certificate instead of a generated one:

```bash
docker run -d --name socialweb \
  -v socialweb-state:/var/lib/socialweb \
  -v "$PWD/tls:/tls:ro" \
  -p 127.0.0.1:8443:8443 \
  -e SOCIALWEB_CONTAINER_TLS_CERT=/tls/tls.crt \
  -e SOCIALWEB_CONTAINER_TLS_KEY=/tls/tls.key \
  socialweb-control-plane:local
```

Run a one-off command in the image instead of the stack (the entrypoint execs its arguments):

```bash
docker run --rm \
  socialweb-control-plane:local \
  socialweb-computer-kcp-check-host -config /home/johnandersen777/kcp-image/nodeimage.json
```

Stop and clean up:

```bash
docker rm -f socialweb
docker volume rm socialweb-state
```

`docker restart socialweb` works: the provider adopts the store it already started instead of
refusing, and a recycled pid no longer reads as a live postmaster.

## Build the image

```bash
deploy/control-plane-container/build.sh controlplane
```

Targets: `controlplane` (the control-plane half, which is what `run-container.sh` builds and tags
`socialweb-control-plane:local`) and `k8s`. Guest images are not built here: `scripts/build-guest-image.sh`
builds them on a Linux build host and `internal/versions/versions.json` pins them.

## Gate

Three modes, one script. `--fast` is the loop and takes about 9 s:

```bash
deploy/check.sh            # same as --fast
deploy/check.sh --fast
```

`--local` runs the proofs this host can carry -- kcp, kine, k3s, postgres, openbao -- takes about
75 s, and names every artifact-gated test it skipped:

```bash
deploy/check.sh --local
```

`--full` runs everything and treats a skip of a live input as a failure, so it only passes on a host
that carries the artifacts; on one that does not it exits non-zero and names the input each failing
test wants:

```bash
deploy/check.sh --full
```

The raw equivalents, without the script:

```bash
go test ./...                                   # the fast loop
SOCIALWEB_LOCAL=1 go test ./...                 # the proofs this host carries
SOCIALWEB_REQUIRE_LIVE=1 go test ./...          # everything; a skip is a failure
```

`SOCIALWEB_LIVE=1` is an alias of `SOCIALWEB_REQUIRE_LIVE=1`. `-short` still skips, and under the
gate it FAILS rather than skips, so the fast loop can never be mistaken for the gate. A skip is not
a pass in any mode: `--local` names every skip it takes, and `--full` fails on them.

## The full live gate

The strict gate needs a tenant cluster and a real guest. The market network is where that is driven
now: the vendored `deploy/market-network` tree runs a local dispatcher, a bidder and a
requester, and the guest is born from the RFP flow rather than from any rig this repo hosts. The
privileged rig that used to do it here (`deploy/test-rig`, and `cmd/socialweb-rig-inputs` with it)
existed only to boot the local substrates with `/dev/kvm` and `/dev/net/tun`, and was deleted with
them on 2026-10-07 (plan 0005 M1-MARKET-ONLY).

The gate it drove is unchanged and still runs against a live cluster:

```bash
SOCIALWEB_REQUIRE_LIVE=1 go test ./...     # everything; a skip is a failure
```

## Validate the containerized control plane

Runs the host-contract probe inside the image, then drives the real zero-node proof
(`TestTheControlPlaneReachesReadyAtZeroNodes`) inside the container:

```bash
deploy/control-plane-container/validate.sh
```

It needs Docker, the Go toolchain and caches, and prints `PROOF: --- PASS: ...` on success. It takes
about ten seconds and needs no root and no `/dev/kvm`.

## Probe the host contract

One binary answers what a host can run, and the same binary inside a container answers what the
container was handed. It prints one summary line in three categories -- HOLDS, MISSING, MUST BE
PASSED IN:

```bash
go run ./cmd/socialweb-computer-kcp-check-host -config /home/johnandersen777/kcp-image/nodeimage.json
```

On this host it answers `21 checks: 21 HOLDS, 0 MISSING, 0 MUST BE PASSED IN` and exits 0; the count
grows as checks are added, so read the line rather than a number written down here.

## Standalone kcp (no container)

For running just kcp against your own kine, outside the container:

```bash
KCP_INSTANCE=dev KCP_SECURE_PORT=6444 KCP_KINE_ENDPOINT=http://127.0.0.1:20001 \
  deploy/run-kcp-instance.sh
```

## Binaries

| Binary | What it is |
| --- | --- |
| `socialweb-clusterprovider` | the reconciler: owns each `Cluster` CR, spawns and reaps its process group and writes its nodes' claims |
| `socialweb-gateway` | the front door: device-key minting and the XRPC surface |
| `socialweb-credential` | the kubeconfig exec plugin the gateway hands a tenant |
| `socialweb-vault` | applies the root vault layout (init, unseal, mounts, policies, roles, secrets) |
| `socialweb-computer-kcp-check-host` | the host-contract probe above |
| `socialweb-computer` | the one host binary; the gateway, the clusterprovider, the vault and check-host are its verbs, and a dev build adds `nodeimage guest-rootfs` and `nodeimage guest-manifest`, the two Go steps of `scripts/build-guest-image.sh` |
| `socialweb-vmexec` | the tiny VM-unit asset: creates the tap and bridge in pasta's namespace and execs firecracker |

## Substrates

**`market` is the only node path.** A node is a VM rented from the compute market: the
clusterprovider writes a `NodeClaim`, the market's door fulfils it under the cluster owner's own
atproto OAuth session, and the winning bidder boots the guest from the requester's cloud-init
`user_data`. Nothing about a node runs on this host -- no VMM, no guest network, no image, no
`/dev/kvm`.

The local `firecracker` and `qemu` substrates were retired on 2026-10-07 (plan
[`0005-zero-ssh-market-nodes.yaml`](docs/plans/0005-zero-ssh-market-nodes.yaml), M1-MARKET-ONLY);
their code, their tests and their deployment paths are gone, and a configuration that still names
one is refused at startup by a message that says so. A bidder boots the guest the market rents as
a VM unit of the Linux VM runtime (`internal/vmruntime`, `socialweb-computer internal vm`), on the
bidder's host rather than this one; `socialweb-nodeboot` and its runner container are deleted.

## Where things are

| | |
| --- | --- |
| The objective, architecture, decisions, measurements | [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) |
| Rules for a change, and for an agent | [`AGENTS.md`](AGENTS.md) |
| The live backlog | [`NEXT.yaml`](NEXT.yaml) |
| The operator handoff | [`handoff.yaml`](handoff.yaml) |
| The plan series for work in flight | `docs/plans/` |
| Running GitLab CI against a tenant cluster | [`docs/gitlab/`](docs/gitlab/) |
| Everything the deployment is | `deploy/`, `deploy/control-plane-container/` |

## Troubleshooting

- **`/dev/kvm` or `/dev/net/tun` errors**: nothing asks for them any more. A node is a market guest
  (see "Substrates"), so a message naming either is from a build or a script older than the
  retirement on 2026-10-07.
- **`curl` to 8080 fails**: 8080 is container-local. Publish and use 8443; that is the TLS
  terminator, which forwards to the loopback gateway.
- **`--full` fails**: it names the missing variable for each failing test. It is the only mode that
  may be called "everything ran".
- **Container not ready**: `deploy/wait-ready.sh <name> <seconds>` prints the last 40 log lines and
  exits non-zero, rather than hanging.

## Experiments tree

The `experiments/` prototype tree is **gone**, deleted once `internal/` implemented everything it
prototyped. `git log` and `git show` keep every line beside the commit messages that say what each
experiment established, and every conclusion it reached is carried in `docs/ARCHITECTURE.md` — some
of the supporting per-configuration tables are not, and live only in this history. Its firecracker
prototype was a second Go module the build never saw, so `./...` neither built, vetted nor tested
it; it is gone with the rest.

The two artifacts that tree held and that are NOT regenerable from git — the firecracker binary and
the Ubuntu 24.04 base rootfs — live outside the repo, under `/home/johnandersen777/kcp-image/artifacts/`,
and the operator's `nodeimage.json` names them there. Everything else it held (kernels, rootfs
images, snapshots, run dirs) was gigabytes of build output and is regenerable.

Vendored reference material, read rather than authored, cited by line from `docs/ARCHITECTURE.md`: `gitlab-kubernetes-executor.md`, `kcp/`, `cluster-api/`.

## License

Unlicense (public domain). See `LICENSE`.
