# AT Protocol services and a market bidder on kcp, over TLS, by name

Four KCP workspaces, plus a verifier that drives them. Each workspace also
carries an `OpenBao` object naming the OpenBao namespace that serves it, which is
where its DenoPods' serving certificates are issued from:

```
root:global   DenoPod/plc            hono-plc            :0  plc.default.global.svc.kcp.local
root:relay    DenoPod/relay          hono-atproto-relay  :0  relay.default.relay.svc.kcp.local
root:relay    DenoPod/dispatcher     ingress proxy       :0  dispatcher.default.relay.svc.kcp.local
root:relay    DenoPod/dispatcher-tls guests' TLS front   :0  dispatcher-tls.default.relay.svc.kcp.local
root:alice    DenoPod/pds            hono-pds            :0  pds.default.alice.svc.kcp.local
root:alice    DenoPod/verifier       the end-to-end check
root:bob      DenoPod/pds            hono-pds            :0  pds.default.bob.svc.kcp.local
root:bob      DenoPod/bidder-<n>     hono-bidder         :0  bidder-<n>.default.bob.svc.kcp.local

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

```bash
bash deploy/denokcp/start-openbao.sh  # development mode, in-memory, port 0
./deno-kcp-provider --kubeconfig=... \
  --openbao-addr http://$(cat .kcp-openbao/bao.addr) --openbao-token root
```

The root CA lives in OpenBao's root namespace; each workspace's `OpenBao` object
gives its namespace an intermediate signed by that root; and each DenoPod is
issued a leaf by its namespace's intermediate, for its own name, with the
loopback addresses on it. `docs/OPENBAO_PKI.md` is the whole of it.

The workload is given a leaf **with its chain** and a **trust bundle carrying
the root and kcp's CA**, written as `ca.pem` in the run directory with
`DENO_CERT` pointing at it. One setting, two CAs trusted: peers' leaves, and the
API server. `DENO_CERT` is read at startup, which is why it is injected rather
than installed from inside the script.

OpenBao in development mode is in-memory, like the PLC directory and the PDS, so
a restart empties the hierarchy and every workload's certificate has to be
reissued: restart OpenBao, restart the provider, and re-apply.

The bidder is the exception: it declares no `--tls-cert-file` / `--tls-key-file`
option and its parser rejects unknown flags, so `70-bidder.yaml` names
`tls: trust`: the template writes only `ca.pem` and gives the child the trust
bundle, never a leaf. The dispatcher is `tls: trust` too.

Each service gained `--tls-cert-file` / `--tls-key-file`; `createServe` has taken
`tcp.cert` and `tcp.key` since it was written, but nothing ever passed them, so
every service in the org had only ever served plain HTTP. TLS is what lets the
relay reach the PDS: `resolvePdsIdentity` uses `https` unless given an
insecure-HTTP escape hatch, and now it has nothing to escape from.

## One service template

Every service DenoPod (plc, relay, dispatcher, both PDSes, every bidder) is
rendered from `deploy/denopods/service.yaml`. The files 20, 30, 35, 40, 60 and
70 here are its values, not DenoPods:

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
and `--allow-ffi` render only with a reason (the firecracker bidder names
its VM runtime command, ssh-keygen, bash and deno); every service is denied read and
write of `/dev/kvm` and `$XDG_RUNTIME_DIR`. `internal/manifests` (`service.go`)
checks all of this when it renders.

Every DenoPod carries the `denopod.deno.computer/run` finalizer: the template
renders it, and the deno runtime adds it to any DenoPod that lacks it before it
starts a process, so deleting a DenoPod always stops its deno process.

## Running it

`accept.sh` is the way to run it: one live acceptance, and one market network of
several per host. It takes a market network slot (`SWC_MARKET_NETWORK_SLOTS`,
default 4, flock under /var/tmp/socialweb-market-network-slots, refused below
3 GiB + 2.5 GiB per guest of MemAvailable), starts OpenBao on port 0, brings up
its own dev instance (`socialweb-computer dev up --profile quick --host-backend
proc --openbao-addr ...`: kine, kcp and the core, whose in-process deno runtime
reconciles the DenoPods) under `$MARKET_NETWORK_STATE/market-networks/$MARKET_NETWORK_ID`,
runs `apply.sh` with `MARKET_NETWORK_BIDDERS` bidders, records every port and
pid it read back in `market-network.json`, checks every DenoPod's
status.listeners and finalizer, reaches each service on its own name and from
the host, rents a firecracker guest through the RFP flow (each guest a VM unit of the Linux VM runtime, FC_VMRUNTIME_COMMAND; its user_data and record under
/var/tmp/swc-$MARKET_NETWORK_ID),
and tears the market network down (DenoPods deleted first, then `dev down`),
reporting anything left behind. It exits non-zero unless every check passed.
Two market networks at once:

```bash
MARKET_NETWORK_ID=network1 MARKET_NETWORK_BIDDERS=2 bash deploy/market-network/accept.sh &
MARKET_NETWORK_ID=network2 MARKET_NETWORK_BIDDERS=2 bash deploy/market-network/accept.sh &
wait
```

`apply.sh` applies the market-network set into a running dev instance
(`KUBECONFIG` its admin kubeconfig). It starts OpenBao itself unless
`START_OPENBAO=0`, and waits for each workspace's `OpenBao` object to report
ready before it applies the workloads.

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
  does -- so a relay started before the PDS never learns it. `apply.sh` applies
  the PDS first.
- **The env allowList is load-bearing.** The preload runs inside the workload's
  process, so a CR that does not allow the shim's keys leaves it unable to read
  its own table.
- **kubectl caches discovery per server, and the cache outlives a schema
  change.** A cached `NAMESPACED=false` makes kubectl POST a namespaced resource
  without a namespace, which kcp answers with a NotFound naming only the
  resource. `apply.sh` uses a throwaway cache dir.
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
  is refused rather than guessed at. `apply.sh` waits for each object to report
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
