# socialweb-computer-kcp

socialweb-computer-kcp serves virtual Kubernetes clusters. A tenant cluster's k3s nodes are virtual machines rented on the atproto compute market, and the nodes scale to zero while the control plane keeps serving. One atproto account can own several clusters.

One binary, `socialweb-computer`, runs every part of the software. Each running application is a separate instance with its own kcp control plane, a manifest set, and a profile: a requester (`run request`), a bidder, the gateway, a market network, or a local development instance. Application state lives in that instance's kcp as objects such as `ComputeVM`, `ComputeContract`, `NodeClaim`, `DenoPod`, and `HostProcess`.

- Design and decisions: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- Working rules for this repository: [`AGENTS.md`](AGENTS.md)
- Plans and their status: [`docs/plans/`](docs/plans/), [`NEXT.yaml`](NEXT.yaml)

## Prerequisites

- A 64-bit Linux host with `/dev/kvm`.
- To run a release binary: `curl` and `sha256sum` (coreutils). The binary embeds every asset it runs, so it needs neither this checkout nor Go and Deno.
- To build from source or run the scripts: Go and Deno on `PATH`, and this repository checked out, since the scripts run from their own checkout.
- To rent VMs, an account with VM slots; run the live commands as that account (on hat-8, `test-0001`).

> Warning: never use `/tmp` for a build or for state. It is tmpfs and spills to swap. Use a directory under `~/.tmp` or under `/var/tmp`.

## Install a release binary

Every tagged release publishes Linux binaries and the guest image to a public bucket. The bucket needs no credentials.

| Path | Holds |
| --- | --- |
| `releases/<version>/binaries/socialweb-computer-linux-amd64` | The host binary for Linux amd64. |
| `releases/<version>/binaries/socialweb-computer-linux-arm64` | The host binary for Linux arm64. |
| `releases/<version>/binaries/SHA256SUMS` | The sha256 of both binaries. |
| `releases/<version>/binaries/release.json` | The commit, the build mode and one entry per platform. |
| `releases/<version>/binaries/version-linux-<arch>.txt` | The pinned version and sha256 of every asset in that binary. |
| `releases/<version>/guest-image/firecracker-x86_64/` | `vmlinux`, `rootfs.ext4` and `image.json`: the guest image a bidder's VMs boot. |
| `releases/<version>/manifest.json` | Every file under the version, with its size and sha256. |

`<version>` is a git tag, for example `v0.0.0-rc0`. Only `releases/` is readable without credentials. `main/<commit>/` holds every build of `main`, and `main/latest/` is a copy of the newest one, but both answer `403 AccessDenied` to an anonymous reader: use a release.

Take the newest release, verify it, and put it on your PATH:

```bash
BUCKET=https://socialweb-computer-kcp.sfo3.digitaloceanspaces.com
V=$(curl -s "$BUCKET/?list-type=2&prefix=releases/&delimiter=/" \
  | grep -o 'releases/v[^<]*' | sed 's|releases/||; s|/$||' | sort -V | tail -1)
ARCH=linux-$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
mkdir -p ~/.tmp/swc-release && cd ~/.tmp/swc-release
curl -sSfO "$BUCKET/releases/$V/binaries/socialweb-computer-$ARCH"
curl -sSfO "$BUCKET/releases/$V/binaries/SHA256SUMS"
grep " socialweb-computer-$ARCH\$" SHA256SUMS | sha256sum -c -
mkdir -p ~/.local/bin && install -m 0755 "socialweb-computer-$ARCH" ~/.local/bin/socialweb-computer
socialweb-computer version
```

If `~/.local/bin` is not on your PATH, add it, or install to `/usr/local/bin` instead.

`socialweb-computer version` prints the build, the commit and the sha256 of every asset in the binary. The release tag is in `binaries/release.json` and `manifest.json`.

To pin a version instead of taking the newest, set `V` yourself: `V=v0.0.0-rc0`.

`socialweb-computer install gateway`, `install bidder` and `upgrade --binary --sha256` take this same file and its sha256: `SHA=$(grep " socialweb-computer-$ARCH\$" SHA256SUMS | cut -d' ' -f1)`.

On a bidder host, `--guest-image-source` takes this release's guest image. It accepts a `file://` or `https://` base, fetches `image.json`, `vmlinux` and `rootfs.ext4` from `<base>/<name>`, and verifies each against the sha256 pinned in the bidder's build, so a guest image that does not match those pins is refused:

```bash
sudo ~/.local/bin/socialweb-computer install bidder --handle <handle> \
  --guest-image-source "$BUCKET/releases/$V/guest-image/firecracker-x86_64"
```

To place the files yourself instead, download them and pass a `file://` base:

```bash
for f in vmlinux rootfs.ext4 image.json; do
  curl -sSfO "$BUCKET/releases/$V/guest-image/firecracker-x86_64/$f"
done
sudo install -d -m 0755 /var/lib/socialweb-computer/guest-image
sudo install -m 0644 vmlinux rootfs.ext4 image.json /var/lib/socialweb-computer/guest-image/
```

Only `firecracker-x86_64` is published. An arm64 host builds its own with `scripts/build-guest-image.sh`. See [Run and install a bidder](#run-and-install-a-bidder) for the rest of the install.

## The RFP flow

A requester rents a VM by running the RFP flow. The requester posts a `compute.vm` record and a signed `market.rfp`; bidders bid; the requester accepts the winning bid; the winning bidder boots the guest from cloud-init `user_data` that installs the guest agent (sshd, the tunnel subscriber, and the client that reaches the guest). The requester reaches the guest only through the relay, over an SSH `ProxyCommand` on the websocket tunnel. The relay is the registry.

```
requester --market.rfp--> relay/PDS <--bid-- bidder (own kcp: MarketBidder, Bid, ComputeVM)
    | accept                                    | cloud-init user_data
    v                                            v
gateway (own kcp: Gateway, Cluster, NodeClaim)   firecracker VM = k3s node
    tenant k3s apiserver (kine + front) <-------- node joins over the iroh link
```

Every listener behind the edge binds port 0 and reports its bound address into the status of its kcp object, and peers find each other by service name through kcp. Only the stable public entry points (caddy on 443, an application's front) keep a fixed address.

## Build the socialweb-computer binary

Build from source when you change the code. To run a published release instead, see [Install a release binary](#install-a-release-binary).

Build a development binary from your working tree, uncommitted changes included. This is the binary for live runs.

```bash
scripts/build-socialweb-computer.sh --dev --publish /var/tmp/swc
```

The build writes the host binary to `/var/tmp/swc/bin/socialweb-computer`, mirrors the working tree to `/var/tmp/swc/src`, and runs the TypeScript from that mirror. A development binary prints `dev build, not reproducible` with its commit in `socialweb-computer version`.

Build a release binary of one commit, offline and cached by input hash:

```bash
C=$(git rev-parse HEAD)
scripts/build-socialweb-computer.sh --platform linux/amd64 --commit "$C"
B=~/.tmp/socialweb-computer-build/release/$C/socialweb-computer-linux-amd64
```

`$B` and its sha256 are what `socialweb-computer install` and `socialweb-computer upgrade --binary --sha256` take.

CI publishes the same outputs to the public bucket: `releases/<tag>/` on a `v*` tag, and `main/<commit>/` with a copy at `main/latest/` on `main`. See [Install a release binary](#install-a-release-binary).

Build script options:

| Option | Effect |
|---|---|
| `--dev` | Build the working tree for the host platform, for live runs while iterating. |
| `--publish DIR` | With `--dev`, copy the host binary to `DIR/bin/socialweb-computer` and mirror the working tree to `DIR/src`. |
| `--sync-only` | With `--publish`, mirror the working tree again without rebuilding. |
| `--platform P` | A release platform (`linux/amd64`, `linux/arm64`); repeat for more. Default: every release platform. |
| `--commit REV` | The commit to build. Default `HEAD`; uncommitted changes are not built. |
| `--build-root DIR` | Checkouts, caches, and outputs. Default `$HOME/.tmp/socialweb-computer-build`. |
| `--verify-reproducible` | Build the commit twice from two clean checkouts and refuse a platform whose builds differ. |
| `--no-cache` | With `--verify-reproducible`, build both from scratch. |

## Run a market network on one host

A market network is one instance, profile `market-network`, in its own kcp. It runs the shared atproto services (fake PLC, relay, PDSes) and composes child applications, each with its own kcp: one bidder per `--bidders`, and, with `--with-gateway`, one gateway.

```bash
swc=/var/tmp/swc/bin/socialweb-computer
$swc dev up --instance mn1 --profile market-network --bidders 1 --with-gateway \
  --guest-image-source file:///var/lib/socialweb-computer/guest-image --vm-host-backend systemd-user
```

Inspect the network, use it, and stop it:

```bash
$swc dev status --instance mn1 | jq '.network.status.apps'
$swc status --instance mn1
eval "$($swc dev env --instance mn1)"
$swc dev logs core --instance mn1 --tail 50
$swc dev down --instance mn1
```

- `dev status` prints the `MarketNetwork` object and each child application's roots and contracts; `status` prints the market counters and contract steps.
- `dev env` exports `KUBECONFIG` and the ports of the network's kcp.
- `dev logs` takes one log name; `core` is the log of the `socialweb-computer` process that runs kcp and supervises the DenoPods and HostProcesses.
- Run the network as the VM-slot account. On a shared host, take a live slot first.

`dev up` options for a market network:

| Option | Meaning | Default |
|---|---|---|
| `--instance` | The instance id. | derived from the worktree |
| `--profile` | `market-network`. | |
| `--bidders N` | How many `MarketBidder` children to compose (`bidder-1` .. `bidder-N`). | 1 |
| `--with-gateway` | Also compose a `Gateway` child. | |
| `--guest-image-source` | The `file://` or `https://` base the pinned guest image is fetched from and verified against by sha256. | |
| `--vm-host-backend` | The placement backend of the VM units: `systemd-user` or `proc`. | the instance's own |
| `--app-values` | A YAML file merged over the market-network values. | |
| `--network-timeout` | How long `dev up` waits for the network and every child to report Ready. | `3m` |

## Rent a VM through the RFP flow

`run request` posts a `compute.vm` record and a signed `market.rfp`, waits for bids, accepts one, boots the guest, opens an SSH session over the relay, and deletes the VM. Pass `--network` to use a running market network.

A secrets bundle is a JSON array of `{ "path": "/abs/path", "value": ... }`. The secrets are written only into the invocation's own OpenBao; the guest fetches them at boot, and they never enter a kcp object, an argument, a log, or a record.

```bash
echo '[{"path":"/root/x","value":"hello"}]' > secrets.json
$swc run request --network mn1 --secrets secrets.json --exec 'cat /root/x'
$swc run request --network mn1 --secrets secrets.json --hold --exec 'uname -a'
$swc run request list
$swc run request resume <name>
$swc run request release <name>
```

`run request` options:

| Option | Meaning |
|---|---|
| `--network` | The market network instance to run under. Unset: a standalone instance under `<state-dir>/requests`. |
| `--name` | The invocation's name, a DNS label. Default `req-<UTC time>`. |
| `--secrets` | The secrets bundle file. |
| `--exec` | Run this program over SSH on the guest instead of an interactive session. |
| `--hold` | Keep the contract when this process ends; `release` or `resume <name>` starts it again. |
| `--skip-ssh` | Do not open an SSH session once the guest is on the network. |
| `--contract-timeout` | How long to wait for the guest's `onNetwork` and host keys, and for a release. Default `3m`. |

SSH runs with `StrictHostKeyChecking=yes` against the host keys the guest reported (the guest's `vm.hostKeys`) and reaches the guest only through the relay.

## Run and install a bidder

Run a bidder inside a market network as a child application. `run bidder` requires `--guest-image-source`.

```bash
$swc run bidder --host-backend proc --profile market-network --network mn1 --handle b2.mn1.bob.test \
  --guest-image-source file:///var/lib/socialweb-computer/guest-image --vm-host-backend systemd-user
```

Install a bidder on a host (one explicit root step), then it runs as its own account, `swc-bidder`:

```bash
sudo $B install bidder --handle bobvmbuilder.bsky.social --adopt-account \
  --guest-image-source file:///var/lib/socialweb-computer/guest-image
```

Upgrade an installed bidder as its account:

```bash
sudo runuser -u swc-bidder -- env XDG_RUNTIME_DIR=/run/user/$(id -u swc-bidder) \
  CONFIG_PATH_SOCIALWEB_COMPUTER=/var/lib/socialweb-computer/swc-bidder/config.json $B upgrade --binary $B --sha256 <sha256>
```

- A production bidder gates which RFPs it answers with the policy named in `config.json` `bidder-policy`; the production bidder uses `tangled-vouch`.
- The guest image is pinned by sha256 in [`internal/versions/versions.json`](internal/versions/versions.json); the bidder verifies every file against that pin.
- A bidder that signs in to its atproto account through a QR code uses the `prod-qr` profile. See [Sign in with OAuth](#sign-in-with-oauth).

`install bidder` options:

| Option | Meaning | Default |
|---|---|---|
| `--handle` | The atproto handle(s) of the bidder instances, comma-separated. A re-run with another handle adds that instance. | |
| `--account` | The system account the bidder runs as. | `swc-bidder` |
| `--adopt-account` | Take over an existing account that no install record names. | |
| `--guest-image-source` | The guest image source, written to `config.json`. | |
| `--vm-slots N` | VM slots of 65536 subordinate ids reserved in `/etc/subuid` and `/etc/subgid`. | 256 |
| `--dry-run` | Print the computed step list and change nothing. | |
| `--root DIR` | Install into a staged host under `DIR` as the invoking user, without root. | |

## Run the gateway and its web app

A development gateway is a child of a market network: start the network with `--with-gateway`.

A production gateway is built as a release, shipped, and installed or upgraded with `scripts/deploy.sh`, which also writes the caddy sites.

```bash
BINARY=$B SHA256=$SHA SSH_KEY=~/.ssh/id_ed25519_no_password \
  GATEWAY_ENV_FILE=~/.tmp/deploy/gateway.env GATEWAY_SPEC_FILE=~/.tmp/deploy/gateway-spec.yaml scripts/deploy.sh
```

`scripts/deploy.sh` reads these environment variables:

| Variable | Meaning | Default |
|---|---|---|
| `BINARY` | The release binary to ship. Unset: the script builds the current commit. | |
| `SHA256` | The published sha256 of `BINARY`; a mismatch stops the deploy. | |
| `SSH_KEY` | The SSH key for the deploy host. | |
| `SSH_TARGET` | The deploy host. | `root@socialweb.computer` |
| `SSH_PORT` | The deploy host's SSH port. | `1997` |
| `ACCOUNT` | The account the gateway runs as. | `socialweb` |
| `STATE_DIR` | The gateway's state directory. | `/var/lib/socialweb-computer/socialweb` |
| `DOMAIN` | The base domain. | `socialweb.computer` |
| `ORIGIN` | The web app's host. | `k8s.<DOMAIN>` |
| `KCP_ORIGIN` | The kcp host. | `kcp.<DOMAIN>` |
| `GATEWAY_ENV_FILE` | The gateway's environment file, shipped as `gateway.env`. | |
| `GATEWAY_SPEC_FILE` | A YAML `GatewaySpec`, shipped as the Gateway spec. | |

The script installs the gateway on its first run and upgrades it after. The web app (`socialweb-computer-k8s`) serves `k8s.<domain>`, and caddy proxies the web app and the `/xrpc/computer.socialweb.k8s.*` routes to the gateway.

After a fresh deploy the requester account has no web app session, so cluster claims fail with a `no oauth session stored` message. Sign in with [Sign in with OAuth](#sign-in-with-oauth) and `--url https://k8s.socialweb.computer/`.

## Manage a tenant cluster over XRPC

The gateway serves the cluster API over atproto XRPC. Every method takes an atproto service JWT: `iss` is your DID, `aud` is the gateway DID, and `lxm` is the method NSID.

Methods (prefix `com.publicdomainrelay.temp.cluster.`):

| Method | What it does |
|---|---|
| `createCluster` | Create the named cluster if it is missing and leave it running, in one call. Idempotent. |
| `getKubeconfig` | Return a kubeconfig. Creates the cluster if it is missing but does not start the control plane. |
| `listClusters` | List the account's clusters. |
| `controlPlaneUp` | Start the control plane. |
| `controlPlaneDown` | Stop the control plane. |
| `deleteCluster` | Delete the cluster. |
| `subscribeClusters` | Stream cluster state as an event stream. |
| `revokeIdentity` | Revoke a cluster identity. |

### Create a cluster and get a kubeconfig with goat

```bash
go install github.com/bluesky-social/goat@latest
( . ~/.aliceoa_bsky_social_source; GOAT_USERNAME="$ALICE_ATPROTO_HANDLE" GOAT_PASSWORD="$ALICE_ATPROTO_PASSWORD" goat account login )
GW=https://k8s.socialweb.computer AUD=did:web:k8s.socialweb.computer N=com.publicdomainrelay.temp.cluster
call() { local m=$1 nsid=$N.$2; shift 2; goat xrpc $m $GW $nsid "$@" "Authorization:Bearer $(goat account service-auth --aud $AUD --lxm $nsid)"; }
call procedure createCluster name=demo                       # create if missing and start the control plane (idempotent)
call query listClusters | jq '.clusters[] | {name, phase, running}'
call procedure getKubeconfig name=demo | jq -r .kubeconfig > demo.kubeconfig; chmod 600 demo.kubeconfig
# the kubeconfig exec plugin is socialweb-credential; the web app lists its builds at /api/config
curl -sfo socialweb-credential "$GW$(curl -s $GW/api/config | jq -r '.credentialHelpers[] | select(.os=="linux" and .arch=="amd64") | .path')" && chmod +x socialweb-credential
PATH=$PWD:$PATH KUBECONFIG=demo.kubeconfig kubectl get ns
call procedure controlPlaneDown name=demo; call procedure deleteCluster name=demo
```

The kubeconfig carries a device key: keep it `0600` and never commit it. `getKubeconfig` alone creates a cluster but leaves it stopped (`running=false`); call `controlPlaneUp` or `createCluster` to start it. Cluster phases are `Unavailable`, `Initializing`, `Ready`, and `Draining`.

### Watch cluster state with subscribeClusters

`com.publicdomainrelay.temp.cluster.subscribeClusters` is an atproto event stream: a `GET` with a websocket upgrade, binary frames, each frame a DRISL-CBOR header then a DRISL-CBOR payload. Authenticate on the upgrade request with the same service JWT (`Authorization: Bearer`, `lxm` = `subscribeClusters`). The account is the token's `iss`. Pass `?name=<cluster>` to watch one cluster.

On connect the stream sends a snapshot (one `#cluster` per cluster, one `#member` per control-plane member, one `#nodeClaim` per claim, each with `snapshot:true`), then one `#info` named `SnapshotComplete`, then live changes. There is no cursor: reconnecting starts a fresh snapshot. The token is checked once at connect, and the stream lives until the client closes, the gateway restarts, or the 1 hour stream bound is reached (`TokenExpired`). An error frame is followed by a close.

| Message | Fields | Phase values |
|---|---|---|
| `#cluster` | name, phase, running, reason, message, server, instance, deleted | Unavailable, Initializing, Ready, Draining |
| `#member` | cluster, member, phase, reason, message | Pending, Starting, Running, Ready, Sealed, Stopping, Stopped, Deleting, Failed, Lost |
| `#nodeClaim` | cluster, name, nodeName, phase, message, contractPhase, outcome, bidder, fqdn, deleted | Pending, Bidding, Provisioning, Joining, Running, Failed, Released, Lost, Adopting |

Error frames: `InvalidRequest`, `Unauthorized`, `TokenExpired`, `ClusterNotFound`, `IdentityRevoked`, `ClusterUnavailable`, `ConsumerTooSlow`.

Decode the stream with the bundled client, or check connectivity with `websocat`:

```bash
T=$(goat account service-auth --aud $AUD --lxm $N.subscribeClusters)
# one JSON line per event; exit 0 on --until, 2 on --fail-on, 1 on an error frame
deno run -A --no-lock --node-modules-dir=none scripts/subscribe-clusters.ts --gateway $GW --token "$T" --name demo \
  --until cluster=Ready --fail-on nodeClaim=Failed
# every cluster of the account, run forever
deno run -A --no-lock --node-modules-dir=none scripts/subscribe-clusters.ts --gateway $GW --token "$T"
# raw connectivity check (CBOR bytes, not text)
websocat -b -n -H="Authorization: Bearer $T" "wss://k8s.socialweb.computer/xrpc/$N.subscribeClusters?name=demo" < /dev/null | xxd
```

`subscribe-clusters.ts` options: `--token`, `--gateway` (default `https://k8s.socialweb.computer`), `--name`, `--until <type>=<phase>`, `--fail-on <type>=<phase>`.

### Apply objects and run Jobs without a kubeconfig

`computer.socialweb.k8s.kubectlApplyServerSide` (procedure) applies one object as `kubectl apply --server-side` would. `computer.socialweb.k8s.getJob` (query) reads a Job's status, its pods, and the tail of each container's log. Neither needs a kubeconfig, `socialweb-credential`, or `kubectl`. Both authenticate with the same service JWT (`aud` = gateway DID, `lxm` = the method), and the account is the token's `iss`.

`kubectlApplyServerSide` inputs:

| Field | Meaning |
|---|---|
| `cluster` | The account's cluster label. Required. |
| `manifest` | One Kubernetes object as JSON, with `apiVersion`, `kind`, and `metadata.name`. Required. A `List` kind or `generateName` is refused. |
| `namespace` | A namespaced object's namespace. Order: `metadata.namespace`, then this field, then `default`; both set must match. A cluster-scoped kind takes none. |
| `name` | Optional check; must equal `metadata.name`. |
| `fieldManager` | The server-side apply field manager. Default `socialweb-computer`. The value `kubectl` shares field ownership with `kubectl apply --server-side`. |
| `force` | Take ownership of fields another field manager owns. Without it such a field is an `ApplyConflict`. |
| `dryRun` | Run admission and validation and return the object without storing it. |

Output: `apiVersion`, `kind`, `namespace`, `name`, `uid`, `resourceVersion`, `generation`, and `object` (as the apiserver stored it, `managedFields` included).

Errors: `InvalidRequest`, `Unauthorized`, `IdentityRevoked`, `ClusterNotFound`, `ClusterNotRunning` (409, call `controlPlaneUp`), `ClusterInitializing` (429, retry), `ClusterDraining` (409), `ClusterUnavailable` (503), `KindNotFound` (400), `ApplyConflict` (409), `KubernetesError` (the apiserver's status and message).

`getJob` parameters: `cluster`, `namespace`, `name`, and `tailLines` (default 100, maximum 5000, `0` returns no logs; each log is at most 64 KiB). Output: `phase` (Pending, Running, Succeeded, Failed), `active`, `succeeded`, `failed`, `conditions`, and `pods[].containers[] { state, exitCode, reason, log }`. Errors include `ObjectNotFound` (404). A Job on a cluster at zero nodes first rents a market VM for its pod.

Apply a ConfigMap:

```bash
K=computer.socialweb.k8s
curl -sS -X POST "$GW/xrpc/$K.kubectlApplyServerSide" \
  -H "Authorization: Bearer $(goat account service-auth --aud $AUD --lxm $K.kubectlApplyServerSide)" \
  -H "Content-Type: application/json" -H "Accept: application/json" \
  -d '{
    "cluster": "demo",
    "namespace": "default",
    "name": "app-config",
    "fieldManager": "kubectl",
    "manifest": {
      "apiVersion": "v1", "kind": "ConfigMap",
      "metadata": {"name": "app-config", "namespace": "default"},
      "data": {"environment": "production", "log_level": "info"}
    }
  }' | jq '{kind, namespace, name, uid, resourceVersion}'
```

Apply a Job and poll it until it finishes:

```bash
jq -n '{cluster: "demo", manifest: {apiVersion: "batch/v1", kind: "Job",
  metadata: {name: "hello", namespace: "default"},
  spec: {backoffLimit: 0, template: {spec: {restartPolicy: "Never",
    containers: [{name: "run", image: "docker.io/library/busybox:1.36.1", command: ["sh", "-c", "echo hello from $(uname -m)"]}]}}}}}' |
  curl -sS -X POST "$GW/xrpc/$K.kubectlApplyServerSide" -H "Content-Type: application/json" \
    -H "Authorization: Bearer $(goat account service-auth --aud $AUD --lxm $K.kubectlApplyServerSide)" -d @- | jq -c '{kind, name, uid}'
# poll until Succeeded or Failed (each call needs a fresh token: service-auth tokens are short-lived)
until s=$(curl -sS "$GW/xrpc/$K.getJob?cluster=demo&namespace=default&name=hello&tailLines=50" \
    -H "Authorization: Bearer $(goat account service-auth --aud $AUD --lxm $K.getJob)"); \
  echo "$s" | jq -c '{phase, active, succeeded, failed}'; echo "$s" | jq -e '.phase == "Succeeded" or .phase == "Failed"' >/dev/null; do sleep 5; done
echo "$s" | jq -r '.pods[].containers[] | "\(.state) exit=\(.exitCode) \(.log)"'
```

## Sign in with OAuth

`scripts/oauth-signin.ts` drives a headless `/usr/bin/google-chrome` (puppeteer-core) through the flow: the page, the PDS authorize page, the password, then "Authorize". The script reads the handle and password from the credential file itself, so they never appear on the command line or in a log. It writes one screenshot per step to `~/.tmp/oauth-signin/<ts>/` (mode 0700).

The credential file (mode 0600) exports `<ACCOUNT>_ATPROTO_HANDLE` and `<ACCOUNT>_ATPROTO_PASSWORD`.

```bash
# the web app user (alice) signs in to the gateway; do this after every fresh gateway deploy
deno run -A --no-lock scripts/oauth-signin.ts --creds ~/.aliceoa_bsky_social_source --account ALICE --url https://k8s.socialweb.computer/
# the bidder signs in through the QR url (U) from its MarketIdentity
deno run -A --no-lock scripts/oauth-signin.ts --creds ~/.bobvmbuilder_bsky_social_source --account BOB --url "$U"
```

For the bidder, read the current QR url from the `MarketIdentity` object's `status.pendingAction.url` in the bidder's kcp (see [Inspect the kcp of an application](#inspect-the-kcp-of-an-application)).

`oauth-signin.ts` options: `--creds`, `--account`, and `--url` (all required), `--out` (the screenshot directory), `--done "<text>"` (wait for this text after consent), `--chrome` (the browser path, default `/usr/bin/google-chrome`).

A QR url expires after 5 minutes, so read the current one from the MarketIdentity status right before running the script.

To force the bidder to sign in again, for example after the OAuth client scope changes on qr.fedfork.com:

1. Delete `bidder/metadata/identity/bidder/session` in the bidder's OpenBao: log in with the certificate at `<handle state>/openbaoservers/bidder/openbao/tls` as the client `bidder` (its certificate is under `clients/bidder`).
2. Restart the bidder unit: `systemctl --user -M swc-bidder@ restart 'socialweb-computer-bidder@<handle>.service'`. The `MarketIdentity` reports `AwaitingScan` with a new url.
3. Run `oauth-signin.ts` against that url.

Check the result with `kubectl get marketidentity bidder -o jsonpath='{.status.phase}'` on the bidder's kcp: `Ready`.

Symptoms: a gateway log line `no oauth session stored for did:...` means the web app user must sign in. A bidder log line `ScopeMissingError` means the OAuth client scope must be deployed to qr.fedfork.com (a separate host) and the bidder re-signed.

## Deploy a release to production

> Warning: a production deployment preserves nothing. Losing sessions, VMs, clusters, claims, or receipts during a deploy is acceptable, so deploy fresh. Only the lead runs a production deployment.

```bash
# 1. confirm the commit's run is green on GitHub (.github/workflows/ci.yml)
# 2. build the release
C=$(git rev-parse HEAD); TMPDIR=~/.tmp/swc-lead-tmp scripts/build-socialweb-computer.sh --platform linux/amd64 --commit $C
B=~/.tmp/socialweb-computer-build/release/$C/socialweb-computer-linux-amd64; SHA=$(sha256sum $B | cut -d' ' -f1)
# 3. gateway (socialweb.computer): ship, install the first time or upgrade after, write the caddy sites
BINARY=$B SHA256=$SHA SSH_KEY=~/.ssh/id_ed25519_no_password \
  GATEWAY_ENV_FILE=~/.tmp/deploy/gateway.env GATEWAY_SPEC_FILE=~/.tmp/deploy/gateway-spec.yaml scripts/deploy.sh
# 4. bidder (hat-8): stage the binary, then upgrade it as the account
R=/var/tmp/swc-release/socialweb-computer-${C:0:8}; sudo install -m 0755 $B $R
sudo runuser -u swc-bidder -- env XDG_RUNTIME_DIR=/run/user/$(id -u swc-bidder) \
  CONFIG_PATH_SOCIALWEB_COMPUTER=/var/lib/socialweb-computer/swc-bidder/config.json $R upgrade --binary $R --sha256 $SHA
# 5. verify: scale a tenant cluster from zero and back (~60 s up, ~3 min down)
#    apply the pod finish-line to the canary cluster, then:
cd ~/.tmp/deploy && deno run -A finish.ts up     # exits when the pod is Running on a market node
#    delete the pod finish-line, then:
deno run -A finish.ts down                       # exits at 0 nodes and 0 ComputeVMs
```

A tenant cluster scales from zero when you apply a pod to it: the pod raises a `NodeClaim`, the cluster runs the RFP flow for a market VM, the node joins, and the pod runs. Deleting the pod returns the cluster to zero nodes and deletes the VM. Applying a Job on a cluster at zero nodes works the same way; poll `getJob` or watch `#nodeClaim` on `subscribeClusters`.

Roll back as the account with `socialweb-computer rollback`.

Once per host, or when the item changes:

```bash
# guest image on a bidder host (pinned sha256 in internal/versions/versions.json)
scripts/build-guest-image.sh --target firecracker-x86_64 --out /var/tmp/guest-image
sudo install -d -m 0755 /var/lib/socialweb-computer/guest-image && sudo install -m 0644 /var/tmp/guest-image/firecracker-x86_64/{vmlinux,rootfs.ext4,image.json} /var/lib/socialweb-computer/guest-image/
# OAuth client scope on qr.fedfork.com (the did-key-associator frontend, a separate host); needed when a bidder writes a new record type
cd ../did-key-associator && SSH_KEY=~/.ssh/id_ed25519_no_password scripts/deploy.sh --static-only
```

The deploy inputs (gateway.env, gateway-spec.yaml) live on the orphan branch `swc-deploy`, in the worktree `~/.tmp/deploy`. The credential files `~/.aliceoa_bsky_social_source` and `~/.bobvmbuilder_bsky_social_source` are mode 0600; never print, log, or commit their values.

## Inspect the kcp of an application

Every application has its own kcp. A kcp admin kubeconfig's server URL ends in `/clusters/<workspace>`; swap the workspace to walk the kcp. Define two helpers:

```bash
kc() { local k=$1 ws=$2; shift 2; local b; b=$(kubectl --kubeconfig $k config view -o jsonpath='{.clusters[0].cluster.server}' | sed 's#/clusters/.*##'); kubectl --kubeconfig $k --server "$b/clusters/$ws" "$@"; }
tree() { local k=$1 ws=${2:-root}; echo "$ws"; for c in $(kc $k $ws get workspaces -o name 2>/dev/null | sed 's#.*/##'); do tree $k "$ws:$c"; done; }
```

Find every kcp and every tenant cluster kubeconfig under a state root:

```bash
STATE=/var/tmp/swcit/<run>                                 # harness runs; the default dev state is $XDG_STATE_HOME/socialweb-computer
find $STATE /var/lib/socialweb-computer -name admin.kubeconfig -path '*kcp/admin.kubeconfig'   # every kcp front kubeconfig
find $STATE /var/lib/socialweb-computer -path '*server/cred/admin.kubeconfig'                   # every tenant k3s kubeconfig
for K in $(find $STATE -path '*kcp/admin.kubeconfig' -not -path '*slot-*' -not -path '*shared*'); do echo "== $K"; tree $K; done
kc $K root api-resources --api-group=socialweb.computer    # the kinds this layer serves
```

- `dev env` prints the network's kcp kubeconfig and ports.
- A child application's kcp is under the network's state dir, in `apps/`.
- A production bidder's kcp is under `/var/lib/socialweb-computer/swc-bidder/handles/<hash>/kcp`, and a production gateway's kcp is under its state dir (default `/var/lib/socialweb-computer/socialweb/kcp`). A claim lives in the cluster's workspace: `kc $K root:<acct>:<cluster> get nodeclaims`.
- A production host has no `kubectl`; use the gateway's embedded k3s: `K3S=$(find /var/lib/socialweb-computer/socialweb/runtime -name k3s -type f | head -1); $K3S kubectl ...`.

DenoPod logs live under the run directory (`runs/`), one directory per pod, with `stdout.txt` and `stderr.txt` (gone on restart). The `socialweb-computer` process logs to `journalctl --user -u <unit>`, or use `dev logs core`.

SSH to a production host: `ssh -i ~/.ssh/id_ed25519_no_password -p 1997 root@socialweb.computer`.

## Run the integration tests

The integration tests are Go tests under `test/integration` with the `integration` build tag. Each test starts its own market network as the VM account, drives it with the `socialweb-computer` verbs, and tears it down. Run them as an account that may `sudo -n -u test-0001`.

```bash
go test -tags integration -count=1 -v -timeout 60m ./test/integration/
# or one test
go test -tags integration -count=1 -v -timeout 60m ./test/integration/ -run TestRequestSecretsReachTheGuestThroughTheRFPFlow
go test -tags integration -count=1 -v -timeout 60m ./test/integration/ -run TestTenantClusterScalesFromZeroOnMarketVMsAndBack
go test -tags integration -count=1 -v -timeout 60m ./test/integration/ -run TestClusterEventsStreamFromSubscribeClusters
```

Environment variables:

| Variable | Meaning | Default |
|---|---|---|
| `SOCIALWEB_IT_ACCOUNT` | The VM account the tests run as. | `test-0001` |
| `SOCIALWEB_IT_GUEST_IMAGE` | The guest image source. | `file:///var/lib/socialweb-computer/guest-image` |
| `SOCIALWEB_IT_PUBLISH` | Where the built binary is published for the run. | |
| `SOCIALWEB_IT_SKIP_BUILD` | Reuse the published binary instead of rebuilding. | |
| `SOCIALWEB_IT_RUN_ROOT` | The run root. Keep it short, since a tenant cluster's front socket path must fit in 107 bytes. | `/var/tmp/swcit` |
| `SOCIALWEB_IT_ARTIFACTS` | The directory for logs. | `~/.tmp/swc-it/<run>` |
| `SOCIALWEB_IT_LIVE_SLOT` | The host's live-slot script; `none` turns it off. | the lead's `~/.tmp/swc-lead/live-slot.ts` when it exists |

The integration job on the hat-8 runner runs the whole suite on `main` and on `v*` tags, on its own market networks, as that runner's account with its own VM slots.

## License

Unlicense (public domain).
