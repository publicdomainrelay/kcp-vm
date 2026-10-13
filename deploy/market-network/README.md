# The market network: a MarketNetwork application and its child applications

A market network is one application, `MarketNetwork`, in its own kcp:

```
socialweb-computer dev up --profile market-network [--bidders N] [--with-gateway] --app-values <file>
```

`dev up` brings up the instance's bootstrap layer (kine, kcp behind its front,
the core) and applies one root object, `MarketNetwork network`, into its kcp.
The core's application controller (`internal/appctl`) renders it:

- an OpenBao for the services' certificates, the OpenBaoServer `network`
  (`05-vault.yaml`, the first phase of the `market-network` set);
- the shared atproto services as DenoPods of the network's kcp (the
  `market-network` set: the files 20-60 here);
- one child application per `spec.apps[]` entry: `bidder-1` .. `bidder-N`
  (`MarketBidder`) and, with `--with-gateway`, `gateway` (`Gateway`).

Each child application runs its OWN kcp. Its bootstrap layer (kine, one kcp
slot on port 0, the front, its core) is HostProcess objects in the network's
kcp, in `root:dev:<child>`, placed by the network's supervisor; its core holds a
token valid only in that workspace. Once the child's kcp answers, the network
server-side applies the child's root object (`MarketBidder app`) into the
child's kcp with the MarketNetwork value (`status.network`: plcDirectoryUrl,
ingressProxyHost, relayUrls, firehoseUrls, qrOrigin, guestTlsUrl, caBundleRef)
copied onto it, and reads back only the child's root status (did, listeners,
phase). That copy is the only way a child learns the network: no child reads the
network's kcp, and the child's DNS shim resolves only its own application's
names. The child's core renders its root object into components
(`deploy/apps/market-bidder/bidder.values.yaml` through the service template:
the bidder DenoPod in the child's `root:app`).

The network value names loopback addresses (`https://127.0.0.1:<port>`,
`relay.localhost:<port>`) read back from the services' status, and a CA bundle
(the network PKI root and the guest TLS CA) the children trust.

The network's own services, by name inside the network:

```
root:global   DenoPod/plc            hono-plc            :0  plc.default.global.svc.kcp.local
root:relay    DenoPod/relay          hono-atproto-relay  :0  relay.default.relay.svc.kcp.local
root:relay    DenoPod/dispatcher     ingress proxy       :0  dispatcher.default.relay.svc.kcp.local
root:relay    DenoPod/dispatcher-tls guests' TLS front   :0  dispatcher-tls.default.relay.svc.kcp.local
root:alice    DenoPod/pds            hono-pds            :0  pds.default.alice.svc.kcp.local
root:alice    DenoPod/verifier       the end-to-end check
root:bob      DenoPod/pds            hono-pds            :0  pds.default.bob.svc.kcp.local
```

Each bidder has its own OpenBao, an OpenBaoServer `bidder` in its own kcp
(`deploy/apps/market-bidder/vault.yaml`), placed by its own core. The bidder's
core generates the account password into that vault (client `bidder`, kv mount
`bidder`), signs up for an account of its own on bob's PDS (MarketIdentity mode
pdsAccount, the PDS URL from its root object), and hands the DenoPod a token
that can read only OpenBaoServer objects of its own kcp. No password or key is
in a kcp object, and the network never touches a child's vault.

Every service binds port 0. It writes the port it got to PORT_FILE in its run
directory, the provider reads it back into the DenoPod's status.address, and
peers reach it by name through the kcpdns table the provider rewrites as ports
are reported.

OpenBao objects, one per workspace, each naming the OpenBao namespace that holds
its intermediate CA:

  root:global   OpenBao/openbao  global.default    (15-openbao-global.yaml)
  root:relay    OpenBao/openbao  relay.default     (15-openbao-relay.yaml)
  root:alice    OpenBao/openbao  alice.default     (15-openbao-alice.yaml)
  root:bob      OpenBao/openbao  bob.default       (15-openbao-bob.yaml)
```

Each service serves **TLS**, is reached by a **cluster-local name**, and trusts
its peers through a CA the provider injects. The verifier proves the whole path:

```
verifier phase=Succeeded
createAccountStatus: 200   did: did:plc:fsf622toocae2nrdhuij7b37
plcStatus: 200             relayFrames: 3
relaySawCommit: yes        verdict: pass
```

## The name

```
<name>.<namespace>.<workspace labels>.svc.<service domain>
pds.default.alice.svc.kcp.local
```

The workspace labels are the logical cluster path with `root` dropped and the
rest reversed, because a colon is not legal in a DNS label and the path is the
only name kcp gives a workspace. `--service-domain` is the cluster domain in the
Kubernetes sense, defaulting to `kcp.local`, so `svc` is structural.

**Objects carry the cluster ID, not the path.** The `kcp.io/cluster` annotation
holds `2j35eh7jjhsc8ny9`; the path lives on the workspace's own `LogicalCluster`
object under `kcp.io/path`. The provider resolves one to the other once per
workspace and caches it, so a name reads `pds.default.alice` rather than
`pds.default.2j35eh7jjhsc8ny9`.

## TLS, and which CA signs what

`kcp start` generates its CA **in memory** and persists only the serving key, so
the root's private key is not on disk and leaves cannot be signed by it. That is
why the workloads' certificates come from OpenBao instead, and why a
`CertificateAuthority`-shaped thing had to live somewhere that survives a
provider restart:

The network's OpenBao is the OpenBaoServer `network` of the network's own kcp
(`05-vault.yaml`): one listener on 127.0.0.1:0 with mutual TLS, file storage under
the instance's state (`openbaoservers/network`), no clustering. The core's
OpenBaoServer controller initialises it, keeps the unseal key in a mode 600 file
of its state, and revokes the initial root token after its first sync; no root
token is kept anywhere. The deno runtime is the client `denoruntime`: it waits
for the server to be Ready, logs in with its client certificate, and gets a
token that holds only the `network-pki` policy (namespaces, the `pki` mounts,
the root CA, the intermediates, the roles and the leaves). The application
controller reads only the PKI root CA (`pki/ca/pem`), for the CA bundle it
hands the children.

The root CA lives in OpenBao's root namespace; each workspace's `OpenBao` object
gives its namespace an intermediate signed by that root; and each DenoPod is
issued a leaf by its namespace's intermediate, for its own name, with the
loopback addresses on it. `docs/OPENBAO_PKI.md` is the whole of it.

The workload is given a leaf **with its chain** and a **trust bundle carrying
the root and kcp's CA**, written as `ca.pem` in the run directory with
`DENO_CERT` pointing at it. One setting, two CAs trusted: peers' leaves, and the
API server. `DENO_CERT` is read at startup, which is why it is injected rather
than installed from inside the script.

The network's OpenBao keeps its hierarchy across a restart of the core, but the
PLC directory and the PDSes are in-memory: take the network down and up again
rather than restarting it.

The bidder is the exception: it declares no `--tls-cert-file` / `--tls-key-file`
option and its parser rejects unknown flags, so
`deploy/apps/market-bidder/bidder.values.yaml` names `tls: trust` and
`trust: [<caBundleRef>]`: the template writes only `ca.pem` (its own kcp's CA
plus the network's CA bundle) and never a leaf. The dispatcher is `tls: trust`
too.

Each service gained `--tls-cert-file` / `--tls-key-file`; `createServe` has taken
`tcp.cert` and `tcp.key` since it was written, but nothing ever passed them, so
every service in the org had only ever served plain HTTP. TLS is what lets the
relay reach the PDS: `resolvePdsIdentity` uses `https` unless given an
insecure-HTTP escape hatch, and now it has nothing to escape from.

## One service template

Every service DenoPod (plc, relay, dispatcher, both PDSes, every bidder) is
rendered from `deploy/denopods/service.yaml`. The files 20, 30, 35, 40 and 60
here and `deploy/apps/market-bidder/bidder.values.yaml` are its values,
not DenoPods:

```
name, entry (a name in deploy/ts-entries.yaml), stateDir, serviceAccount,
tls (serve | trust | none), args, env, permissions, probe
```

The template's supervisor runs the entry on the pinned deno with
`--no-code-cache --node-modules-dir=none --frozen`, the root `deno.json` and
`deno.lock` (plus `--cached-only` in production), from the source root the deno
runtime names in `SOCIALWEB_TS_ROOT`, never from an absolute host path. Its
working directory is its state dir, never the source tree: the policy engine
inside the bidder writes `.cache` and `.tempdir` into its working directory, and
a production source tree is read-only. Preloads are
the kcpdns shim first, then the entry's own from `deploy/ts-entries.yaml`, and so
are the `--unstable-*` flags. The child gets a clean environment: no `HOME` of
the user (its `HOME` is its own state dir, `TMPDIR` is under it), `PORT_FILE`,
`LISTENERS_FILE` and `STATUS_FILE` in its run directory, and the kcpdns keys.

Permissions are explicit. `-A` is refused; read and write are the source root,
the run directory, the state dir and the paths the values name; `--allow-run`
and `--allow-ffi` render only with a reason (the bidder names bash and deno, for
the gha-lite policy engine; its provider is compute-provider-kcp and it verifies ssh
signatures in TypeScript); every service is denied read and
write of `/dev/kvm` and `$XDG_RUNTIME_DIR`. `internal/manifests` (`service.go`)
checks all of this when it renders.

Every DenoPod carries the `denopod.deno.computer/run` finalizer: the template
renders it, and the deno runtime adds it to any DenoPod that lacks it before it
starts a process, so deleting a DenoPod always stops its deno process.

## Running it

A market network is driven by `socialweb-computer` alone:

```bash
socialweb-computer dev up --instance mn1 --profile market-network --bidders 2 [--with-gateway] \
  --guest-image-source file:///var/tmp/guest-image --vm-host-backend systemd-user
socialweb-computer dev status --instance mn1
socialweb-computer run request --network mn1 --secrets secrets.json --exec 'cat /root/x'
socialweb-computer dev down --instance mn1
```

Every bidder's provider is compute-provider-kcp (`spec.kcp`: the ComputeVM
controller in the bidder's own core boots each guest as a VM unit, and the bidder
runs `--compute-provider-kcp`); `--guest-image-source` and `--vm-host-backend` fill
it. `--app-values` still overrides the NetworkApp fields, for example the owner
handle of the bidders' ComputeVMs:

```json
{"bidder": {"kcp": {"handle": "{app}.mn1.bob.test"}}}
```

`{app}` becomes the bidder's name; make the handle unique per worktree on a shared
account. `dev up` then also needs the VM runtime's executables
(`--userns-stage-path`, `--pasta-path`, `--vmexec-path`, `--firecracker-path`, or
their `SOCIALWEB_*_PATH` variables) and runs as the account with the subordinate id
range. `bidder.firecracker` is refused: the bidder DenoPod runs no VM runtime command.

A bidder is a child application of the network too (`--bidders 0` starts a
network with none):

```bash
socialweb-computer run bidder --host-backend proc --profile market-network --network mn1 \
  --handle mybidder-1.bob.test --guest-image-source file:///var/tmp/guest-image --vm-host-backend systemd-user
```

`run bidder --network` adds a `MarketBidder` entry to `spec.apps[]` (its name is the
handle's first label), waits until the network reports it Ready with its did and
returns; `--fg` stays and removes it from the network on Ctrl-C. `--handle` names
the owner of its ComputeVMs; its atproto handle in the network is `<name>.bob.test`.

N market networks on one host are N dev instances: every listener binds port 0,
so two `dev up --instance` with different names never collide.

### What the applications report

`dev up` waits for the MarketNetwork to be Ready and prints it; `dev status`
prints it again (`.network`). The network is Ready only when its conditions hold:

- `ServicesReady`: every service DenoPod (plc, relay, dispatcher,
  dispatcher-tls, both PDSes) is Running and ready, reports at least one
  listener in `status.listeners`, and carries the `denopod.deno.computer/run`
  finalizer;
- `VerifierPassed`: `alice/verifier` Succeeded (it created an account on
  alice's PDS and saw the commit on the relay);
- `OwnKcp`: the network and every child report their own kcp front and kcp
  root.

`status.apps[]` holds each child's kcp (`kcpUrl`, `kcpRoot`), phase, did,
listeners and components. A bidder on `spec.kcp` lists its ComputeVMs and their
HostProcesses there, so a released guest is gone when its bidder lists none.

A requester invocation reports in its own kcp: the ComputeContract's
`status.contract` (winner, bids, receipt, the guest's host keys, `release` with
the vm.delete status, `termination` when the bidder ended the contract, for
example a guest that powered itself off: phase Released and no vm.delete), and
`run request` prints the summary of it. `dev down` deletes the MarketNetwork,
whose finalizer deletes each child's root object (a bidder's ComputeVMs first),
then the child's workspace in the network's kcp (the supervisor stops its kcp,
kine, front and core), then the services and the OpenBaoServer, before devsup
stops; it then stops any VM unit a bidder child left (`vmsStopped`, `vmsLeft`)
and any process left under the instance (`leftoverKilled`, `remaining`).

A requester invocation is a child application of the network too:

```bash
socialweb-computer run request --network mn1 --secrets secrets.json --hold --exec 'cat /root/x'
socialweb-computer run request list
socialweb-computer run request release <name>        # or resume <name>
```

`run request --network` adds a `MarketRequester` entry to `spec.apps[]`; the network places its kcp,
copies the network value (with `hosts`, the network's service names and their loopback addresses, and
the bidders' DIDs) onto its root object, and the CLI then talks only to the invocation's own kcp. The
guest comes up only through the RFP flow: the requester DenoPod posts the RFP, a bidder bids, the
requester accepts, and the winner boots the guest from cloud-init `user_data`; `--secrets` is written
into the invocation's own OpenBao and the guest fetches it from the requester's secrets server; the CLI
reaches the guest only over its iroh ticket, with StrictHostKeyChecking=yes against the host keys the
guest reported. Without `--hold` the CLI releases the contract (vm.delete) and removes the invocation.
At the end of a held invocation the entry is marked `stopped`: its kcp, kine and core stop and its state
dir is kept until `release` or `resume` starts them again.

Every service runs from this repository: `ts/atproto-market`, `ts/atproto-relay`,
`ts/hono-pds`, `ts/did-key-ingress-proxy` and the root `deno.json` and `deno.lock`.

## How a name resolves

Deno resolves `fetch`, `WebSocket` and `Deno.connect` in Rust and never consults
`Deno.resolveDns` -- a canary counted zero calls during a failed fetch, connect and
WebSocket attempt -- so the shim patches all four entry points, and
`Deno.resolveDns` only so a caller that asks it directly agrees with the table.

The provider injects `KCP_DNS_TABLE`, mapping every advertised name to its
address, read from each peer's own `SERVICE_ARGS`/`SERVICE_ENV`. On a miss the
shim asks the kcp API with `KCP_TOKENS`, one token per workspace minted **in the
workspace being read**, because a token from one workspace is not authorised in
another. The relay spawns its child with the same shim, since a workload's own
argv cannot carry `--preload`.

The shim preserves the scheme and sets `Host` back to the original name, so a
service that derives its identity from `Host` -- as the PDS does for its `did:web`
and OAuth issuer -- still sees the name it was configured with. The client
authenticates the address rather than the name, which is the price of having no
way to make the name resolve; every leaf carries `127.0.0.1` for that reason.

## Things that will bite

- **Order matters, and only for the shim.** A workload's table is written once,
  at start. The relay subscribes to the PDS over a WebSocket, and `WebSocket` is
  constructed synchronously so it cannot fall back to discovery the way `fetch`
  does -- so a relay started before the PDS never learns it. The market-network set applies
  the PDS first.
- **The env allowList is load-bearing.** The preload runs inside the workload's
  process, so a CR that does not allow the shim's keys leaves it unable to read
  its own table.
- **kubectl caches discovery per server, and the cache outlives a schema
  change.** A cached `NAMESPACED=false` makes kubectl POST a namespaced resource
  without a namespace, which kcp answers with a NotFound naming only the
  resource. Use a throwaway `--cache-dir` when you read the network with kubectl.
- **Waiting for a resource to be listable is not waiting for its binding to be
  Ready.** A binding that is not Ready lists empty but rejects creates.
- **kcp does not send the bookmark a client-go streaming list needs**, so an
  informer's initial list never completes and the provider reconciles nothing
  while looking healthy. The provider sets `KUBE_FEATURE_WatchListClient=false`
  itself.
- **A pod's own name has to be seeded into its table**: its probe resolves that
  name and it cannot be in the informer cache before it has started.
- **A namespace with no `OpenBao` object serves plain HTTP.** `SERVICE_TLS=true`
  is not enough on its own: the provider finds the authority by looking for the
  single `OpenBao` object in the pod's namespace, and a namespace that names two
  is refused rather than guessed at. The market-network set waits for each object to report
  ready before it applies the workloads, which is the state to check first when a
  service comes up without TLS.

## Ceilings

- **WebSocket names must be in the table**: no discovery fallback, as above.
- **Real DNS for humans** is not available: a DenoPod cannot carry
  `/etc/resolv.conf`, so `curl pds.default.alice.svc.kcp.local` from a shell does
  not resolve. Only the workloads see these names.
- **The PLC directory is in-memory**, so a restart empties it, and the PDS keeps
  accounts in memory too.
- **Anything bypassing the four patched entry points** -- a native library, or a
  subprocess the workload spawns -- does not see virtual DNS.
- **The bidder registers with an XRPC dispatcher**, so it needs one reachable at
  the URL it is given; without a dispatcher it comes up but can list nothing.
