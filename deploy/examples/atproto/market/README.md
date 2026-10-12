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
option and its parser rejects unknown flags, so `70-bidder.yaml` writes only
`ca.pem` and gives the child the trust bundle, never a leaf.

Each service gained `--tls-cert-file` / `--tls-key-file`; `createServe` has taken
`tcp.cert` and `tcp.key` since it was written, but nothing ever passed them, so
every service in the org had only ever served plain HTTP. TLS is what lets the
relay reach the PDS: `resolvePdsIdentity` uses `https` unless given an
insecure-HTTP escape hatch, and now it has nothing to escape from.

## Running it

```bash
bash deploy/denokcp/start-kcp.sh      # kine and kcp on port 0, read back
KUBECONFIG_PATH=$PWD/.kcp-demo/admin.kubeconfig bash deploy/denokcp/install-provider.sh
bash deploy/denokcp/build-provider.sh ./deno-kcp-provider
OPENBAO_DIR=$PWD/.kcp-demo/openbao bash deploy/denokcp/start-openbao.sh
RUNS_DIR=$PWD/runs ./deno-kcp-provider --kubeconfig=$KUBECONFIG \
  --openbao-addr http://$(cat .kcp-demo/openbao/bao.addr) --openbao-token root &
START_OPENBAO=0 bash deploy/examples/atproto/market/apply.sh
```

`apply.sh` starts OpenBao itself unless `START_OPENBAO=0`, and waits for each
workspace's `OpenBao` object to report ready before it applies the workloads.

`accept.sh` is that whole sequence as one live acceptance, and one market test
bed of several per host: it takes a bed slot (`SWC_BED_SLOTS`, default 4, flock
under /var/tmp/socialweb-bedslots, refused below 3 GiB + 2.5 GiB per guest of
MemAvailable), brings up its own kcp, kine, OpenBao and provider under
`$BED_STATE/beds/$BED_ID` on port 0, runs `apply.sh` with `BED_BIDDERS`
bidders, records every port and pid it read back in `bed.json`, reaches each
service on its own name and from the host, rents a firecracker guest through
the RFP flow (guests under /var/tmp/swc-$BED_ID, labelled swc.bed=$BED_ID), and
tears the bed down, reporting anything left behind. It exits non-zero unless
every check passed. Two beds at once:

```bash
BED_ID=bed1 BED_BIDDERS=2 bash deploy/examples/atproto/market/accept.sh &
BED_ID=bed2 BED_BIDDERS=2 bash deploy/examples/atproto/market/accept.sh &
wait
```

Every service runs from this repository: `ts/atproto-market`, `ts/atproto-relay`,
`ts/hono-pds`, `ts/did-key-ingress-proxy` and the root `deno.json` and `deno.lock`.
Building the provider needs a deno-kcp checkout (`DENO_KCP_DIR`) unless
`DENO_KCP_PROVIDER` names a built one.

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
