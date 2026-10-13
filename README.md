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

- Linux with systemd >= 253 and a lingering user manager (the host contract, `docs/host-contract.md`).
  The gateway and bidder hosts need no container runtime: the gateway runs no containers.
- A gateway host needs no `/dev/kvm` and no `/dev/net/tun`: a node is a VM rented from the compute
  market and built by the winning bidder's cloud-init, so nothing about it runs on the gateway (see
  "Nodes"). A bidder host needs both (`check-host -role bidder`).
- A Go toolchain newer than `go.mod`, deno, and docker or podman (the `swc-userns-stage` asset and
  the guest image builds), for building.

## Build

```bash
scripts/build-socialweb-computer.sh --dev                     # one build of the working tree
scripts/build-socialweb-computer.sh --platform linux/amd64 --commit "$(git rev-parse HEAD)"
```

The result is one `socialweb-computer` binary with its pinned assets and the TypeScript closure
embedded (`internal/versions/versions.json`, `deploy/ts-entries.yaml`). Guest images are not built
here: `scripts/build-guest-image.sh` builds them on a Linux build host and `versions.json` pins them.

## Run it

A gateway: `scripts/deploy.sh` builds or takes the release binary (`BINARY`, `SHA256`), ships it
over ssh (`SSH_TARGET`, `SSH_KEY`), runs `socialweb-computer install gateway` (the one root step)
the first time and `upgrade` after, and writes the caddy sites for `https://k8s.<domain>` (the web
app) and `https://kcp.<domain>` (the kcp front). `GATEWAY_ENV_FILE` becomes the account's
`gateway.env` and `GATEWAY_SPEC_FILE` the Gateway spec:

```bash
SSH_KEY=~/.ssh/key GATEWAY_ENV_FILE=gateway.env GATEWAY_SPEC_FILE=gateway-spec.yaml scripts/deploy.sh
```

A bidder: `socialweb-computer install bidder` as root, then `socialweb-computer run bidder` per handle
as the account.

A market network for development (its own fake PLC, relay, PDS, bidders and requesters):
`socialweb-computer dev up --profile market-network` (`deploy/market-network/README.md`).

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

## Probe the host contract

`install` runs it as the account; run it by hand the same way:

```bash
socialweb-computer check-host -role gateway -state-dir /var/lib/socialweb-computer/socialweb
```

It exits 1 naming every check that fails, each with its fix (`docs/host-contract.md`).

## Standalone kcp

For running just kcp against your own kine:

```bash
KCP_INSTANCE=dev KCP_SECURE_PORT=6444 KCP_KINE_ENDPOINT=http://127.0.0.1:20001 \
  deploy/run-kcp-instance.sh
```

## Binaries

| Binary | What it is |
| --- | --- |
| `socialweb-computer` | the one host binary; the gateway, the clusterprovider, the vault, the CCM and check-host are its verbs, and a dev build adds `nodeimage guest-rootfs` and `nodeimage guest-manifest`, the two Go steps of `scripts/build-guest-image.sh` |
| `socialweb-credential` | the kubeconfig exec plugin the gateway hands a tenant |
| `socialweb-vmexec` | the tiny VM-unit asset: creates the tap and bridge in pasta's namespace and execs firecracker |

## Nodes

**The compute market is the only node path.** A node is a VM rented from the compute market: the
clusterprovider writes a `NodeClaim`, the market's door fulfils it under the cluster owner's own
atproto OAuth session, and the winning bidder boots the guest from the requester's cloud-init
`user_data`. Nothing about a node runs on the gateway -- no VMM, no guest network, no image, no
`/dev/kvm`.

The local `firecracker` and `qemu` substrates were retired on 2026-10-07 (plan
[`0005-zero-ssh-market-nodes.yaml`](docs/plans/0005-zero-ssh-market-nodes.yaml), M1-MARKET-ONLY), and
their last code (`internal/nodefirecracker`, `internal/nodevm`, `internal/noderecord`, the
`SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE` setting) was deleted with the container path (plan 0006
M5-RETIRE). A bidder boots the guest the market rents as a VM unit of the Linux VM runtime
(`internal/vmruntime`, `socialweb-computer internal vm`), on the bidder's host.

## Where things are

| | |
| --- | --- |
| The objective, architecture, decisions, measurements | [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) |
| Rules for a change, and for an agent | [`AGENTS.md`](AGENTS.md) |
| The live backlog | [`NEXT.yaml`](NEXT.yaml) |
| The operator handoff | [`handoff.yaml`](handoff.yaml) |
| The plan series for work in flight | `docs/plans/` |
| Running GitLab CI against a tenant cluster | [`docs/gitlab/`](docs/gitlab/) |
| Everything the deployment is | `deploy/`, `scripts/deploy.sh` |

## Troubleshooting

- **`/dev/kvm` or `/dev/net/tun` errors on a gateway**: nothing there asks for them. A node is a
  market guest (see "Nodes"); only a bidder host needs them.
- **`--full` fails**: it names the missing variable for each failing test. It is the only mode that
  may be called "everything ran".
- **Gateway not ready**: `scripts/deploy.sh` waits for `/api/config` and prints the last 60 lines of
  `socialweb-computer-gateway.service` when it never answers.

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
