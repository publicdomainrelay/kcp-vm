# socialweb-computer-kcp - architecture

Service which serves virtual Kubernetes clusters. A tenant cluster's k3s **nodes** scale to zero
while its control plane stays live. A single ATProto account may have multiple clusters.

Module `github.com/johnandersen777/socialweb-computer-kcp`, Go. CRDs in API group `socialweb.computer`.

---

## 1. The objective, stated once

```
kcp + openbao + kine + rustfs + embedded-postgres + k3s, as gateway-side processes.
A tenant cluster's k3s NODES scale to zero while the kcp + k3s CONTROL PLANE stays live.
```

Per virtual cluster:

```
kine over an external store      <- the datastore
k3s server --disable-agent       <- the apiserver. NOT a node. Always up.
k3s agent(s)                     <- the ONLY nodes. 0 or 1. Provisioned on apply.
```

`--disable-agent` is the piece that makes "zero nodes" a real state rather than a contradiction: a
single-node k3s where the server IS the node can never produce zero nodes.

**One apiserver per store, no exceptions.** kine's revision is a monotonic sequence with dummy fill
records specifically so no second client can create a lagging event, and two apiservers over one
store is unsafe. This constraint forces the shape.

The user and the nodes talk to the SAME API. There is no handover and no second store: the mount's
`status.URL` points at our apiserver at zero nodes and at N nodes alike.

**Zero nodes is not a cluster state.** The cluster stays `Ready` while nodes go to zero.

## 2. Scope boundary

```
IN   the virtual cluster: workspaces, the zero state, the store, the gateway, the trigger,
     identity, reachability, the CRDs, the ports, the lifecycle
OUT  how a guest COMES TO EXIST in production: the requester, the RFP record, the bid, the
     accept, the bidder, the market's money
SEAM ComputeProvider - provision(vm{user_data}, requesterDid, spec?) / destroy(id)
```

The seam is "given this cloud-init, make me a guest and give me back an id". A provider supplies a
CLOUD; it never supplies the CONNECTION SEMANTICS.

Two things this does not change:

- The cloud-init is the real composer's (`buildUserData` plus the module registry, sibling repo).
  The substitution is *who won the bid*, never *what the guest is told to install*.
- A guest is born from cloud-init `user_data`, never hand-assembled. Provisioning goes through the
  declared path, never a shell.

`kcp + k3s` here are **gateway-side processes**, not market VMs. Where text elsewhere says "market
VM", read it as the substrate a provider made, wherever it came from.

**The market is now reached through a NodeClaim, and that is a scope change.** This document used to
end at the `ComputeProvider` seam: "given this cloud-init, make me a guest and give me back an id",
with the requester, the RFP, the bid and the accept declared out of scope. A tenant cluster's nodes
are rented from the market AS THE CLUSTER'S OWNER, through that owner's single atproto OAuth session,
which is held in a SEPARATE Deno process (the k8s door) and not in this repo. So the Go side no
longer stops at a seam it cannot cross: it writes a `NodeClaim` into the cluster's home workspace,
and the door reads it, runs the RFP flow with the owner's session, boots the winning bidder's guest
from cloud-init, reverse-forwards the advertised address to this host's loopback apiserver and
writes the claim's status back. `internal/nodemarket` is the node host that does the writing; the
`internal/scale` decision that says WHEN a node is needed is unchanged.

Three things this does not change:

- The cloud-init is still the real composer's (`buildUserData` plus the module registry, sibling
  repo). The substitution is still *who won the bid*, never *what the guest is told to install*.
- A guest is still born from cloud-init `user_data`, never hand-assembled. The market substrate adds
  no provisioning path of its own: it writes a record and waits.
- The claim carries PATHS, never secrets. The join token, the per-node password and the server CA are
  read out of the cluster's data dir by the door and handed to the guest on the SSH session's own
  standard input (invariant 29).

## 3. The zero state - the mechanism

```
zero nodes  -> server up. No kubelet, so it never registers a Node.
               kubectl get pods -> objects, Pending, Unschedulable
apply       -> provision an agent -> it joins the SAME server -> the pod runs
idle        -> agent torn down -> back to zero nodes, server still up
```

The honest failures at zero, which are the correct failures:

```
exec          -> unable to upgrade connection: pod <name> does not have a host assigned
port-forward  -> unable to forward port because pod is not running. Current status=Pending
```

**Because the store is external, the control plane is cattle.** A destroyed server rebuilds against
the same store: no `bootstrap data ... different token`, identical CA fingerprint, objects intact,
and a kubeconfig issued before the rebuild still valid. The token is the only local-disk secret that
matters.

**Why this deletes the syncer.** An earlier design needed bidirectional propagation, drift handling,
conflict resolution and per-cluster GC, because it assumed TWO apiservers: ours for the user, k3s's
for the nodes. With one apiserver there is nothing to propagate. Intent goes in, kubelet sees it.
What remains is the binder:

```
podStrategy.PrepareForCreate  ->  Phase: Pending, free
our binder writes             ->  PodScheduled=False, FailedScheduling reason
                                 ...and later spec.nodeName + PodScheduled=True
```

Same reconcile loop in both states.

**Two requirements `--disable-agent` adds**, both from k3s source:

- `--egress-selector-mode`: use `cluster`. It is a static authorizer (node IPs plus cluster/pod
  CIDR) with no per-pod watch dependency. `agent` suffices for apiserver->kubelet but fails for
  service webhooks and aggregated APIs with a 10 s hang rather than a fast refusal. All modes
  produce byte-identical config files - the mode changes the TUNNEL, not the config.
- The identity bundle must be pre-seeded. See section 8.

## 4. The store

`kine` plus `t4` over the central `rustfs`, bucket per tenant, prefix per cluster.

atproto is the durable **mirror**, not the datastore: it cannot be one. No compare-and-swap, no
durable watch (the sequencer resets `seq` to 0 on restart), no compaction, and every write is a
signed commit. What it carries is **intent and identity** - the `Cluster` CR mirror and account
records. Not data, and not portability: the export is deferred.

Empty `--datastore-endpoint` was a defect: k3s then keeps its objects in its own sqlite, so a cluster
that scales to zero and comes back comes back EMPTY. The join cannot reach that state:
`clusterstack.Spec.DatastoreEndpoint` is REQUIRED and `StepsFor` refuses an empty value, because
empty is the one value k3s resolves SILENTLY into a brand-new cluster.

**The store is durable against the launcher.** Durability rests on the data directory under
`SOCIALWEB_CLUSTERPROVIDER_STATE_DIR`. "The cluster is gone" means someone removed the data
directory - a caller decision, not an automatic consequence of a process exiting.

## 5. Components

Everything is either a controller-runtime reconcile loop or a deploy config. No new language.

| # | Component | Layer | Why it exists |
|---|---|---|---|
| 1 | `Cluster` CRD - the mount object | Go | Carries `status.URL` and `status.phase`, which are the real contract. Owns `Draining`, `conditions[TenantPathShut]`, `conditions[Reachable]`, `status.nodeExpiry` |
| 2 | **Materialiser** - controller watching kcp with a host selector | Go | Spawns and reaps the per-cluster process group. This IS the gateway's supervisor |
| 3 | Cluster provider | Go | Owns the `Cluster` CR lifecycle: mints the instance token, fetches the identity bundle, sets `status.URL` once the apiserver answers. ONE process serves EVERY cluster in the kcp tree, and a cluster is named inside it by its home workspace, not by the bare name the owner chose |
| 4 | Node provider | Go | Watches the home workspace for intent, actuates one node, writes `Node` state |
| 5 | kcp provider workspace | YAML | `APIResourceSchema` plus `APIExport` for our CRDs; tenants `APIBinding` |
| 6 | k3s cloud-init module | sibling repo, `UserDataModule` | Installs k3s in a market guest. The only sanctioned way a guest-side component arrives |
| 7 | OpenBao plus ESO | deploy config | The vault, at two levels: a root vault one per deployment and a cluster vault one per ON cluster, both in the production stack. OpenBao holds the per-cluster 13-entry identity bundle and is its AUTHORITATIVE store — the wake reads it out of the vault and materialises the file tree at `<DataDir>/server/tls`+`cred/`, because k3s has no vault plugin and must read files at runtime (sections 9, 14 and 15). ESO materialises vault-backed Secrets into the tenant cluster; the bundle is custody and wake-time restore, not the runtime location |

**Not building**, each rejected on evidence: a CNI, a CAPI provider, an OIDC issuer, an ATProto
bridge, a syncer, a virtual workspace, a second reverse proxy, a reachability substrate, an underlay,
a warm node pool, an admission plugin (RBAC already enforces the identity classes exactly),
a registry plus volume-commit machinery (S3-backed PVs ride the CSI driver on the central rustfs,
so there is nothing to commit).

### The per-cluster process group

```
embedded-postgres  ->  openbao  ->  kine  ->  k3s server --disable-agent  ->  cloud-controller-manager
```

That order is `procgroup.StackOrder` — every component a stack may name, in bring-up order — and
reap is reverse order. It is deliberately a DIFFERENT list from `procgroup.DefaultOrder`, which is the
default SET (postgres, kine, k3s, cloud-controller-manager). One list was doing both jobs until the vault made them come apart:
`StepsFrom` walked `DefaultOrder` as a whitelist, so removing the vault from the default set would
have made an explicitly opted-in stack lease its port, render its config and **spawn nothing** — a
vault that is configured, leased and never started. The two lists are pinned against drifting by a
test asserting every member of `DefaultOrder` appears in `StackOrder`, in order. There is deliberately
**no
supervisor process**: the reconcile loop is the supervisor.

The cloud controller manager is the LAST member, and its position is the whole reason it is a member
at all. It is useless without an apiserver, so it comes after `k3s server`; a step placed before k3s
would also move the seam `ErrNoGuardSeam` protects, which is why the guard's own rule is that the
identity check rides a step ahead of k3s and nothing else may be inserted between. Reverse-order reap
therefore stops the controller FIRST, which is what is wanted: a controller that outlives the
apiserver it reconciles against is a loop of connection errors during every teardown. It is
PORTLESS — it dials the apiserver and listens on nothing — so `portallocator` gained no purpose and
no lease for it, and invariant 8's bookkeeping about who holds one does not grow. Its credential,
its cluster and its instance-file path ride `Spec.Env` and never argv (invariants 3 and 13), and its
readiness gate is its own `phase=Ready` log line, taken once its Node informer has synced: it binds
no port, so that line is the only signal it can offer, and it is a positive marker that can fail.

A portless member is not swept, and that is a STATED LIMIT rather than an open item. The startup
sweep (`cmd/socialweb-clusterprovider/nodesweep.go`) finds a previous gateway's survivors by the node
record each substrate leaves under the state dir, and it matches that record against the argv the
process was started with; a step that binds no port and leaves no record is invisible to it. The
cloud controller manager is exactly that: it binds no port and leases nothing, deliberately, because
its work is to reconcile Node objects against the instance snapshot rather than to own a node. A
sweep that found processes by PID alone would find every process on the host, which is the failure
mode the sweep exists to avoid, and a pid record can be reused by an unrelated process. So a
controller left behind by a gateway that died WITHOUT its reverse-order reap is visible only to that
gateway's own process group (invariant 21 stops it in the ordinary case), and a host that sees one
stops it by hand. The alternatives were considered and are worse than the limit. `NEXT.yaml` CCM-2.

Embedded-postgres is a special case: it is a Go **library** that runs the server as a child of
whoever links it, so there is no argv for a step and `processrunner.Start` refuses the empty argv
such a step would have. `procgroup.Options.PostgresUp func() bool` exists for this: the component
holding the library handle answers for it, and a **nil predicate is a refusal, not a skip**.

### The vault's config is rendered, not shipped

`clusterstack.OpenBaoServerConfig` renders it and `clusterprovider.WriteOpenBaoConfig` writes it —
0600, idempotent, before the step that runs `bao server -config=…`. Each line is measured on v2.6.2:

```hcl
ui = false
disable_clustering = true
api_addr = "https://127.0.0.1:<leased openbao port>"
listener "tcp" { address       = "127.0.0.1:<leased openbao port>"
               tls_cert_file = "<data dir>/server/tls/openbao.crt"
               tls_key_file  = "<data dir>/server/tls/openbao.key" }
storage "postgresql" {
  connection_url = "postgres://postgres:<generated>@127.0.0.1:<leased postgres port>/openbao?sslmode=disable"
}
```

The listener is loopback on the cluster's leased port, and the port is NEVER defaulted: bao's own 8200
is one port for the whole host, which is the store's byte-identical-DSN defect class. The listener is
TLS against the cluster's own certificate and key, and a spec with either absent is refused: a
listener with no certificate is what an earlier revision measured as an ACME/plaintext listener whose
`GET /v1/sys/health` never answers the readiness gate. `disable_clustering = true` removes the cluster
listener (default host-global 8201) and makes "one vault per cluster, no HA" a property of the file.
The file carries **no `disable_mlock`**: 2.6.2 logs `unknown or unsupported field disable_mlock` and
starts anyway, because it does not mlock at all. The config file must be inside the cluster's data
dir, and the certificate and key paths are per-cluster: a shared config is written by whichever
cluster reconciles last, so its port becomes that cluster's.

Storage is the **postgres backend**, and each word of that stanza was read off the bundled `bao`
rather than remembered: `storage "postgres"` answers `unknown storage type postgres`, `storage
"postgresql"` with the cluster's DSN answers, and the health body's own `Storage Type` field then
reads `postgresql`. The key is `connection_url`. The URL is ALWAYS this cluster's own DSN -- loopback
host, the port its lease covers, the password `postgres.pw` generated for this cluster -- and NEVER a
defaulted one, because two clusters' DSNs are otherwise byte-identical. It names the vault's OWN
database, `openbao` (`clusterstack.OpenBaoDatabase`); kine's objects go to the database `k3s`, so the
vault's tables and the objects kine writes never share one database. `OpenBaoServerConfig` refuses a
DSN that is empty, names no host:port, carries no user or password, is not a postgres URL, or names
any database but `openbao`. The database itself is created empty by `StartPostgres` before the vault
step (bao creates its own tables); measured on 2.6.2, the first vault write creates
`openbao_kv_store` in `openbao` and nothing in `k3s`. This replaced `storage "file"`, whose expiry
2.6.2 had already logged (`storage.file: the file physical backend is deprecated; use bao operator
migrate ... by v2.7.0`) and which section 6 had already decided against.

### A child's output is drained, never left in a pipe

`processrunner.Start` hands a child an `io.Writer` for BOTH stdout and stderr, never a pipe. `exec`
drains a writer itself: it copies on its own goroutine and `Wait` returns only after the child has
exited AND its output has been copied, so a child may write without bound and still reach its exit.

`cmd.StdoutPipe()` is forbidden here. A pipe makes the READING the caller's job, and nothing in this
tree ever read one, so the child blocked inside `write(2)` the moment the kernel's 64 KiB buffer
filled. `k3s server` and `k3s agent` both emit far more than that during startup, so the block lands
mid-boot and the readiness gate reports `k3s server at https://…: no answer` — a readiness fault over
a child that was alive, healthy, and had nowhere to put its log. The failure is also load-dependent
and therefore intermittent, which is the worst property a startup fault can have. Measured: a child
writing 500 KB never finished before the fix and finishes immediately after.

What outlives the child is bounded and named: `OutputTailBytes`, 256 KiB, a ring over the LAST bytes
written, read through `Process.Output()`. **The bound is the point** — a cluster's k3s logs for the
life of the cluster, and a tail that grows with it is a leak in the gateway, so the ring EVICTS rather
than accumulating. Four children per cluster, so a whole process group costs at most 1 MiB. The tail
is also what a positive assertion about a node reads: `AssertAgentDialledOurPort` needs the agent's
own dial line, and a tail that has already evicted it fails loudly rather than passing on an absence.

`Spec.Output` is optional and independent: when set, the output goes there as well, and that sink is
BEST-EFFORT with its errors dropped, because returning them would make `exec` stop copying and close
the child's stdout — a full log disk must not SIGPIPE the apiserver.

## 6. CRDs and the API surface

Every CRD here is INTERNAL. No CRD is user-facing: the user's surface is the mounted Kubernetes API
(standard kinds), and cluster lifecycle is an **XRPC call**. None of these schemas carries a
compatibility promise.

| CRD | For | Writer | Tenant-visible |
|---|---|---|---|
| **Cluster** | the mount object - `is-mount` annotation, `status.URL`/`phase`, `Draining`, conditions, `status.nodeExpiry` | cluster provider, from an XRPC lifecycle call - never a kubectl apply | no |
| **Vault** | OpenBao, per ON cluster over its own embedded-postgres | system | no |
| **Postgres** | embedded-postgres - the vault's store and the one per-cluster local state | system | no |
| **Kine** | kine plus t4 to central S3 | system | no |
| **Node** | one node. The only bound on leaks | node provider | no |
| **NodeClaim** | one node RENTED FROM THE COMPUTE MARKET for a cluster, as that cluster's owner. Carries paths (`tokenFile`, `nodePasswordFile`, `serverCAFile`) and never a secret; the door in the sibling Deno process is the only writer of its `status` | cluster provider, spec and metadata only | no |
| **AtprotoIdentity** | the DID to identity mapping, in the home workspace | us | no |
| **Account** | the DID to shard map. Not a CRD - a real kcp `Workspace` of an account `WorkspaceType` in root | us | no |

Do **not** use kcp's shipped home (`~`): it is a bare LogicalCluster with no owner and no
`directlyDeletable`, and `LogicalCluster` delete is Forbidden without that field, so the shipped home
can never be deleted.

`ClusterPhase`: `Initializing`, `Ready`, `Unavailable`, `Draining`.
`NodePhase`: `Pending`, `Running`, `Draining`.

### The XRPC surface

How a user gets a kubeconfig. Seven procedures under `com.publicdomainrelay.temp.cluster.`:

```
createCluster       create the named cluster and leave it RUNNING, in one call. Idempotent.
                    The create-on-read path getKubeconfig walks, then the desired-state flip
                    controlPlaneUp performs. Answers the phase.
controlPlaneUp      bring the named cluster's control plane up. Idempotent. 429 + Retry-After
                    while Initializing. Refused once Draining. DOES NOT provision nodes.
controlPlaneDown    the inverse
getKubeconfig       the credential the user runs kubectl with
deleteCluster       the lifecycle end
listClusters        this account's clusters
revokeIdentity      revoke an identity: write the deny record first, then fan out
```

Auth is a service-auth token whose `aud` is the service and whose `lxm` is the method NSID. A
verifier on this path MUST be passed a real NSID: it compares against the token's `lxm`, and a nil
there skips the check.

**A door that wants a usable cluster gets it in ONE call.** `createCluster` exists because the only
other way to a live cluster was two round trips through two different verbs — `getKubeconfig`, whose
kubeconfig a caller that is provisioning has no use for, and then `controlPlaneUp` — and because a
caller had to know that order. It is not a new path: it calls the same `revocation.CreateOnRead`
around `Store.Ensure` that `getKubeconfig` does, and then the same code `controlPlaneUp` runs, so the
deny is consulted before anything is created and the errors are the same six. `getKubeconfig` keeps
its create-on-read, which is what makes a first login work at all.

**`getKubeconfig` is served by a granter the composition root must be handed.** It renders an exec
credential whose helper is named by configuration, so a gateway that is not configured for it refuses
to START rather than routing a verb that can never answer. A caller may also ask for a chosen
credential lifetime; the settings and the reasoning behind each are in section 15.

**A routed verb is not a working verb, and the difference was invisible here for as long as it took to
notice.** `getKubeconfig` was on this surface, routed, documented and unit-tested, while the
composition root handed the client no granter — so it answered `ErrNoDeviceKey` to every caller that
reached it. It failed CLOSED, which is why it read as a gap rather than a hole, and it is the reason
the answerability check in `cmd/socialweb-gateway` now drives every verb the lexicons declare past
authentication and requires its ANSWER rather than its presence.

**The mount requires `status.phase == "Ready"`, and that is a second, undocumented gate.** kcp's
mount controller reads the referenced object's PHASE, not its conditions, before it will serve:
measured, with `status.URL` written and `phase: Initializing`, the mount reports
`WorkspaceMountReady=False(MountObjectNotReady)` and the front proxy answers **503** to the tenant —
not an auth error. Setting `phase` to `Ready` alone, leaving the conditions still saying
`Ready=False/Initializing`, flipped the mount to serving on the first poll. So a written `status.URL`
is NOT sufficient for a user's `kubectl` to reach the apiserver, and a cluster whose phase is stuck is
a cluster nobody can reach even while its apiserver answers.

**The minted bearer crosses the mount, and it is deliberately powerless.** Measured end to end: the
provider mints a `gwmint` ServiceAccount token in the tenant cluster (through the TokenRequest
subresource, under the `socialweb-mint-identity` ValidatingAdmissionPolicy), writes it as a
kubeconfig, and that token authenticates BOTH directly at the apiserver and through the mount —
`auth whoami` answers `system:serviceaccount:users:gwmint` in both cases. Through the mount it is
**Forbidden** for `nodes` and `namespaces`, and `get` on the one ServiceAccount it is scoped to
returns NotFound rather than Forbidden — i.e. authorized for exactly its own name and nothing else.
That is section 7's "the DID to ServiceAccount mapping is the tenancy boundary" observed rather than
asserted: a mint identity has zero cluster read, so the mount is a path a user reaches their cluster
through and not one that hands out the cluster.

**The mint kubeconfig's own `server:` is loopback, not the mount.** It names the apiserver the
provider minted the token against, which is correct for the provider and wrong for a user, who must
reach the cluster through the mount. The gateway re-points it when it renders the user's kubeconfig,
but the file under the mint directory LOOKS user-ready and is not — it works only from the host that
wrote it. Nothing user-facing may read that file directly.

**A client certificate does not traverse the mount.** kcp terminates TLS, so only header credentials
survive: a bearer works through the mount (verified with `auth whoami` returning
`system:serviceaccount:default:default`), and a client-certificate kubeconfig aimed at a mount URL
fails with "the server has asked for the client to provide credentials". The provider's own minted
certificate credential is therefore LOOPBACK-ONLY by construction, and the only credential that can
cross the mount is a bearer — which is exactly what §7's "bearer only through the mount" requires,
now with the measurement behind it.

**The mount's URL comes from the Cluster's status, and that dependency is load-bearing.** A tenant
reaches its cluster through a `Workspace` whose `spec.mount.ref` names the `Cluster`; kcp populates
that workspace's `spec.URL` FROM the `Cluster`'s `status.URL`. Measured against a live kcp: pointing
the mount at a Cluster whose `status.URL` named a dead port made every request through the mount fail
with `an error on the server ("") has prevented the request from succeeding`, and repointing
`status.URL` at a live endpoint made it serve. So `status.URL` is not decorative — it is the only
thing that decides where the mount goes, and a cluster whose status is not written is a cluster the
user cannot reach even while its apiserver is answering.

**The negative rule, which is a design invariant:** never configure a cluster's apiserver for
request-header auth against the mount. The mount path authenticates NOTHING, so an apiserver trusting
a request-header identity is forgeable.

## 7. Identity and auth

- **Bearer only through the mount.** The credential crosses the kcp mount as a bearer header and
  nothing else. No JWKS, no `--authentication-config`.
- **ServiceAccount tokens via TokenRequest** for the machine class.
- **The DID to ServiceAccount mapping** is the tenancy boundary, and it has exactly ONE spelling: a
  DID's ServiceAccount is `did-` plus the lowercased unpadded base32 of the DID
  (`kcpclient.ServiceAccountName`), which is a valid DNS-1123 label because base32 emits only
  `[a-z2-7]` and never a hyphen or a digit outside that range. Three live things depend on that one
  spelling and none of them is a convention: the policy's third validation refuses a target that does
  not match `^did-[a-z2-7]+$`; the mint Role is scoped by `resourceNames` to derived names rather
  than a wildcard, because `create serviceaccounts/token` without `resourceNames` is TOTAL
  impersonation of every ServiceAccount in the namespace; and `TestTheGrantIsScopedByResourceNames`
  reads the deployed file and fails on a `*` or an underived name. The spelling also cannot be
  changed later without a migration, because the deployed grant carries it in its `resourceNames`.
- **The grant is rendered for the cluster's OWNER, and the owner can act in it.** The name in those
  `resourceNames` is not a literal in the deployed file: `deploy/tenant-grant.yaml` carries
  `__SOCIALWEB_OWNER_SERVICE_ACCOUNT__` and the bootstrap substitutes `ServiceAccountName` of the
  `Cluster`'s `spec.account` — the same DID the mount path is derived from — before it parses the
  document. `RenderTenantGrant` REFUSES a document with no placeholder rather than applying it, so a
  grant scoped to a name nobody owns cannot be applied by accident. The file also carries a
  `ClusterRoleBinding` `socialweb-owner` binding that ServiceAccount to `cluster-admin`: it is the
  owner's own cluster, and until this existed a real user's minted token authenticated and was
  authorized for NOTHING, because no `did-*` ServiceAccount had any RBAC. The `gwmint` Role stays
  scoped by `resourceNames` and is NOT widened by this — it still reaches exactly one name, and the
  owner's power comes from its own binding rather than from the mint's.
- **A revocation that only deletes objects does not revoke.** Deleting a ServiceAccount is a ~10 s
  interruption, not a revocation: a stale, name-keyed RoleBinding resumes grants the moment the
  object is recreated. Revocation needs a consultable deny record, and it is per-DID.
- **The token is per cluster INSTANCE**, not per cluster. A new agent cannot join an orphaned server,
  because it does not hold that instance's token. This is what keeps an orphan harmless.

**The tenant credential is minted from the ClientCA.** `cred/admin.kubeconfig` is derived, not stored,
so the provider derives it: it reads `<DataDir>/server/tls/client-ca.{crt,key}` and signs a fresh
ECDSA P-256 client certificate with CN `system:admin`, O `system:masters`. That is the identity k3s's
own admin kubeconfig carries, and it is the arm of the grant's carve-out that writes Secrets —
`deploy/tenant-grant.yaml`'s `socialweb-mint-identity` policy is a TERNARY on
`request.resource.resource`, not a single condition: the `serviceaccounts/token` arm admits only
`system:serviceaccount:users:gwmint`, and the Secrets arm admits only
`request.userInfo.username == 'system:admin' && 'system:masters' in request.userInfo.groups`, or the
External Secrets controller `system:serviceaccount:default:external-secrets`. The distinction is
load-bearing rather than pedantic, because the two arms admit DIFFERENT subjects for different verbs
on purpose: a ServiceAccount token is requested by the tenant's own mint, while writing a Secret in
the tenant namespace is the provider's act -- through its own admin credential for the grant, and
through the External Secrets controller for the vault-backed Secrets it materialises. No TENANT
subject is admitted for either verb, so a credential with any lesser subject cannot apply the grant
at all and cannot request a token either. `system:masters` is the apiserver's
built-in superuser group for client certificates, so
the credential is cluster-admin-equivalent on the tenant apiserver, and it is the **provider's**: the
provider applies the tenant grant, requests ServiceAccount tokens through TokenRequest, reads the
tenant cluster's Nodes and Pods for the scale observation, and deletes the tenant-side Node object on
teardown.

**Nothing k3s generated is read.** The rejected route was the k3s-written admin kubeconfig under the
data dir: its server URL and SANs are unverified against the address a node dials, and
`/etc/rancher/k3s/k3s.yaml` is shared state on a host that runs several clusters.

**Which source mints it follows the SUBSTRATE, and not the mere presence of a seal.** There are two
sources: the cluster's own `ClientCA` on disk, and the cluster's vault, whose PKI engine issues from a
copy of that same CA imported into it. The choice is an explicit input to the seam
(`clusterprovider.CredentialSource`), derived by the caller from where the cluster's nodes are placed.
For `market` the credential is always signed from `client-ca.{crt,key}` on disk and the vault is never
consulted: a market node's machine is not this host, the cluster vault in that path exists for the
guest's custody and wake material rather than for the provider's own credential, and minting from it
on every pass puts a second TLS dependency between the provider and the tenant apiserver for no gain.
For `firecracker` and `qemu` nothing changes: the vault issues when the cluster has left unseal
material beside its data dir, and the ClientCA signs when it has not. A caller that names no source is
refused (`ErrCredentialSourceUnknown`) rather than defaulted, because the default is the thing this
seam exists to remove.

**Minting refuses rather than downgrades.** No ClientCA on disk is a refusal, never a fallback to a
wider credential. A CA key that is not its certificate's key is refused *before* anything is signed,
because that pair still signs and the failure would otherwise surface as an authentication error at
the first apply. An empty CN or O is refused, because it authenticates as the empty username or as an
unprivileged user. A non-https URL is refused, because the URL's host is the name the apiserver's
certificate must carry. The rule holds ACROSS the two sources as well as within each: a cluster whose
ClientCA cannot sign is refused with the reason, and the vault is not tried instead.

**The credential never reaches a user.** It is a certificate credential, and the mint kubeconfig the
gateway reads back carries a token only — `WriteMintKubeconfig` refuses a credential with no bearer
token. The user's path renders an exec credential, and `kubeconfig.AssertClean` refuses any document
carrying `client-certificate-data` or `client-key-data`. That is a test, not a convention: the minted
credential is run through a faithful kubeconfig writer and `AssertClean` must refuse it, while the
document `kubeconfig.Build` hands a device must pass.

**A workspace ServiceAccount does not leave its workspace.** Its token carries
`authentication.kcp.io/scopes: [cluster:<origin>]` and kcp refuses it in every other logical cluster
before RBAC is consulted — measured both directions, with a real TokenRequest token: a provider
token is denied in a tenant workspace even with a matching ClusterRoleBinding there, and a tenant
token is denied in the provider workspace. `--as=` impersonation does NOT reproduce it (it sets the
username and not the scope extra), so an impersonation probe cannot answer the question and an RBAC
probe that uses one is not evidence. And the refusal is not RBAC at all: a token is refused at the
door of a workspace it was not minted in, which no subject string can spell.

The consequence is load-bearing: `internal/kcpclient` addresses root, the account workspace, the home
workspace and the mount from ONE `rest.Config`, so **the runtime cannot be any of the four provider
ServiceAccounts**. The cluster provider holds a credential kcp does not confine to a workspace — the
admin kubeconfig the operator already installs — and the four ServiceAccounts exist for a future
split into four identities per workspace, not for today's single client. That is a deliberate
privileged gateway-side credential: it is never handed to a tenant, and the tenant's own path is the
mount plus a minted, per-device token.

This corrects an earlier claim here that "RBAC already enforces the identity classes exactly". RBAC
cannot enforce them for a runtime that spans workspaces; the SCOPE does, and it enforces them by
refusing the client rather than by shaping it.

**The identity bundle is exactly 13 entries:**

```
tls/server-ca.crt      tls/server-ca.key
tls/client-ca.crt      tls/client-ca.key
tls/request-header-ca.crt   tls/request-header-ca.key
tls/etcd/server-ca.crt tls/etcd/server-ca.key
tls/etcd/peer-ca.crt   tls/etcd/peer-ca.key
tls/service.key        cred/passwd        cred/ipsec.psk
```

`cred/admin.kubeconfig` is NOT one of them: it is DERIVED from the ClientCA, deterministic. Nor is
the instance token, which is per-instance state.

**An earlier list omitted request-header-ca and all four etcd CAs. Pre-seeding that list makes k3s
REGENERATE those and INVALIDATE every kubeconfig and SA token.** The count is load-bearing; the
bundle's own test asserts it against the data.

## 8. Ports

Every per-cluster port is leased from `internal/portallocator`, ADR 0026. Persisted per host.

| purpose | for |
|---|---|
| `postgres` | the cluster's embedded-postgres - if embedded bostgres doesn't work, use real postgres |
| `openbao` | the vault |
| `kine` | kine's listener |
| `k3s-secure` | the apiserver, and the port a node dials it on |
| `k3s-controller-manager`, `k3s-scheduler`, `k3s-cloud-controller-manager` | the k3s control-plane components' secure ports |

**The vault's lease is ONE port, and it was two for as long as nothing read the second.**
`purposeWidth[PurposeOpenBao]` was 2, spelled "api + cluster", while the config the vault is given —
`clusterstack.OpenBaoServerConfig`, section 5 — sets `disable_clustering = true`, which removes the
cluster listener and makes "one vault per cluster, no HA" a property of the file. Both the listener
stanza and `api_addr` take the same `Lease.Port`, no code in the tree reads `Lease.Port + 1`, and with
the cluster listener removed nothing binds the second port, so it was leased, held out of the carve, and
never read. `PurposeK3sSecure` stays 2 and the difference between the two is the point: the k3s pair is
MEASURED (the apiserver binds `*:<https-listen-port>` AND `127.0.0.1:<https-listen-port + 1>`), where
the vault's second port was an assumption about a listener the vault's own config turns off.

**Two purposes in this table's history are retired, and neither was ever a second fact.** `advertise`
was a lease for the port a node dials, until measurement showed the advertised port IS the bound port:
`K3sAPIPorts` answers both questions from the `k3s-secure` lease, and the invariant refuses an
`--advertise-port` that is not the port the apiserver holds. `kubelet` was a lease for the node's
kubelet, until a node stopped being a process in THIS host's kernel and network namespace and the port
became the guest's own. Both are in `retiredPurposes`: leases for them are dropped from an existing
lease file on open, `Allocate` refuses them, and their ports return to the carve. Section 15 holds both
arguments in full, including what the kubelet lease was for.

**The invariant that matters:** an unleased or colliding advertise **port** routes a node's tunnel to
ANOTHER CLUSTER'S APISERVER, and that surfaces as an **x509 name error**. The certificate is not the
fault; the lease is. The advertised port is not chosen and is not separately leased: it IS the port the
`k3s-secure` lease holds, so the two cannot disagree.

**A node binds no port of this host's, so a node owns no lease.** Every port its guest binds, the
kubelet's included, is bound inside the guest's own network namespace. MEASURED on this host with a
guest running, nothing binds 10250, 10248, 10249, 10251 or 10256, the host's k3s runs `--disable-agent`
and so has no kubelet at all, and pasta is started without `-t`/`--tcp-ports`, so no guest port is
published and the host cannot initiate a connection to a guest.

**Only ONE port serves the API, and it is the one a node must dial.** Measured: `k3s server
--disable-agent` binds `*:<https-listen-port>` AND `127.0.0.1:<https-listen-port + 1>` as a PAIR —
which is why `purposeWidth[PurposeK3sSecure] = 2` — and in steady state it binds NOTHING on
`--advertise-port`. `/cacerts` answers 200 on the listen port and 401 on the advertise port for every
credential form, so an agent dialed at the advertise port loops on `failed to get CA certs:
Unauthorized`. The node's `--server`, `status.URL`, the tenant credential, the readiness gate and the
hardening seam all name the listen port; hardening through the advertise port is what kept the
process group from ever becoming ready.

**N control planes per host: proven. N nodes per host: refuted.** Two `--disable-agent` servers ran
concurrently, each with its own kine and store, both serving — `kubectl get ns` against each — with the
port pair above and disjoint control-plane secure ports. A node is the opposite: `127.0.0.1:6444`
collides FATALLY and the loser is served the other cluster's CA; there is one
`/run/k3s/containerd/containerd.sock` (hardcoded — `--data-dir` moves only `root`; measured
cross-contamination, cluster A's containerd served `RunPodSandbox` for cluster B's pods), one
`flannel.1` whose NAME is not redirectable and whose reconfiguration panicked the other agent
(`vxlan_network.go:217`), one `8472/udp` socket, one `/run/flannel/subnet.env`, one IPAM pool, one set
of `KUBE-*` chains and one `/var/lib/kubelet/pod-resources`. All four candidate fixes were refuted.
That list is the measured case for a VM per node.

**The bind probe, corrected by measurement.** The earlier claim here was that the probe coexists with
a wildcard binder because Go sets `SO_REUSEADDR`, and therefore reports a HELD PORT AS FREE. That
does not reproduce on Linux: a LISTEN socket conflicts with a wildcard or a specific bind whatever
the prober's `SO_REUSEADDR` is. The fault is the ADDRESS, not the flag, and it is wider than claimed:

```
holder bound 127.0.0.2:P, LISTENing            -> old probe said FREE   (a wildcard bind is blocked)
holder bound 0.0.0.0:P with SO_REUSEADDR, not yet LISTENing
                                               -> old probe said FREE   (a wildcard bind is blocked)
holder bound [::]:P, ipv6-only, LISTENing      -> old probe said FREE   (a wildcard bind is blocked)
```

The probe now binds the WILDCARD with `SO_REUSEADDR` cleared; both halves are load-bearing, and the
old probe leased `35167` while it was already bound. The measured cost: a port whose only occupant is
a TCP TIME_WAIT socket reads held where a reuse-enabled listener could in fact bind. That over-report
is accepted — it skips a port for the life of the TIME_WAIT, while the under-report hands a cluster a
port it cannot bind. `/proc`'s LISTEN table is deliberately not consulted: `internal/portscan` imports
this package, and it has no row at all for the bound-not-listening holder.

**Unleased per-node ports.** `127.0.0.1:6444` (k3s agent supervisor), `:10248` (healthz) and `:10010`
(containerd) are unleased, and `6444` is host-fixed, so two agents on one host collide on it.

## 9. A node

**The contract**, which is durable and substrate-independent:

- a node is one long-lived command;
- closing it is the teardown, and it is the ONLY teardown - one code path, no leak surface beyond
  "did we close it";
- nothing is ever read back out of a node. We generate the token and set the node name ourselves.
- `--node-name` is set from the `(cluster, node)` pair, never learned by a round trip. This is not
  hygiene: IP reuse lets a dead node shadow a live kubelet and break `logs`/`exec` with a 502.

**The mechanism**, which is substrate-specific. Two, and both are the mechanism OF a substrate, never
alternatives for one cluster:

| | |
|---|---|
| **a VM** | the node is a guest with its own kernel, containerd and network stack. **This design's mechanism**, and the only one that survives measurement |
| **local process** | a `k3s agent` child process, `internal/nodelocal`, behind the same `scale.Nodes` seam. REFUTED as the substrate, three ways, all measured: `k3s agent` is fatal as a non-root user (`agent requires additional privilege if not run with --rootless`); it needs root for `/run/k3s/containerd`, `/var/lib/kubelet/device-plugins`, `127.0.0.1:6444` and the host `KUBE-*` chains, none of which `--data-dir` moves; and postgres is fatal AS root (`initdb: cannot be run as root`), so a host that runs the store cannot run a node |
| **SSH to a market guest** | the node is a VM rented from the COMPUTE MARKET through a `NodeClaim` the clusterprovider writes and the market door fulfils under the cluster owner's own atproto OAuth session. **`internal/nodemarket`**, selected by `SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE=market`. NOT a guest of this host: no subnet lease, no pasta, no VMM, no node image and no `/dev/kvm` is involved. The clusterprovider's whole part in the node is the claim; the door bids, wins, boots the guest from the winning bidder's cloud-init, reverse-forwards the advertised address (`SOCIALWEB_CLUSTERPROVIDER_MARKET_ADVERTISE_ADDRESS`, default `198.18.255.254`) to this host's loopback apiserver and writes the phase back. Confirming is the claim reaching `Running`, not a local process being alive |

**QEMU is the second implementation of the VM mechanism, behind the same seam.** Firecracker and
QEMU are not two mechanisms for one cluster and not alternatives in one process: the composition root
selects ONE from configuration, and a cluster is placed by the substrate its gateway was configured
with. `SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE` names it, `firecracker`, `qemu` and `market` are the
three values that pass `checkPlacement`, and every other name is refused for the same reason one
substrate was ever refused: a placement nobody wrote is one a gateway cannot be caught not honouring.
Both `firecracker` and `qemu` are GUESTS, so `substrate.IsGuest()` is true for both, and
`AdvertiseAddress`, `GuestDialled`, `GuestGateway`, `NodeExpiry` and per-node network allocation apply
to QEMU exactly as they do to Firecracker. `market` is the one value that is NOT a guest:
`IsGuest()` is false for it, so it takes no subnet range and mounts no guest gateway, and it is
therefore the one value whose `checkPlacement` demands no VMM, no agent, no image and no pasta.

The local-process mechanism is now **DELETED, not merely refuted**: `internal/nodelocal` and the
`process` value of `SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE` are gone, so a host can no longer be
configured into it. It was kept as a sibling only long enough for the firecracker substrate to run
the proof, which it now has — deleting it earlier would have left the node path with nothing green,
and this repo's worst failure mode is a control that exists with nothing reaching it. The
measurements that refuted it survive in this repository's history — `git log` on the deleted
`experiments/process-substrate/` carries them, along with the commit messages that explain what each
one established — because deleting code deletes the error messages that recorded why. The tree itself
is gone: a prototype that is not built, vetted or tested by anything, and that had already begun to
lie about its own failures. The `scale.Nodes` seam stays:
two substrates behind it, and the seam is what made the second one cheap.

**A guest kernel is obtainable with no privileges at all, and this was measured.** Firecracker boots
a kernel YOU supply, and the CI kernels cannot run a k3s node: all three fail `k3s check-config` with
`STATUS: 4 (fail)` — `NETFILTER_XT_MATCH_IPVS`, `XT_MATCH_COMMENT`, `XT_MATCH_MULTIPORT`,
`XT_MATCH_STATISTIC`, `IP_VS`, `IP_SET`, `NF_TABLES_INET` and `NFT_MASQ` are all absent, so BOTH
kube-proxy modes are unusable. Building a kernel needs `flex`, `bison` and `libelf`, which are not
installed and need root. The way through is that **the host's own kernel already has every one of
those symbols as modules**: extract the ELF from `/boot/vmlinuz-$(uname -r)` (zstd; the payload
starts at byte offset 21196) and carry the host's own `.ko.xz` files in an initramfs, with `boot_args`
that omit firecracker's default `nomodule`. Inside such a guest, `k3s check-config` returns
**`STATUS: pass`**. No build, no root, no packages — and this is the single biggest de-risking of the
whole substrate.

**A guest kernel's module set is a MEASURED set, and `k3s check-config` cannot make that
measurement.** The image's module list is what the guest agent loads before k3s starts, and it is a
measurement rather than a preference. Measured on this host: with `xt_conntrack` absent from the
list, kubelet inside the guest failed to create the KUBE-FIREWALL chain — the one that drops packets
to 127.0.0.0/8 arriving from a non-loopback interface:

```
"Failed to ensure rule to drop invalid localhost packets in filter table KUBE-FIREWALL chain"
err="error appending rule: exit status 1: Warning: Extension conntrack revision 0 not supported,
missing kernel module? iptables: No chain/target/match by that name."
"Failed to initialize iptables rules; some functionality may be missing." protocol="IPv4"
```

and the same for IPv6 with `ip6table_mangle` absent. **`k3s check-config` returned RC=0 both before
and after.** It reads the kernel CONFIG file staged beside the kernel and reports
`CONFIG_NETFILTER_XT_MATCH_CONNTRACK: enabled (as module)` without ever asking whether that module is
LOADED. It audits the config the kernel was built with, not the kernel the guest is running — so a
node can be Ready, and a green check, with a firewall rule missing. iptables 1.8.11 (legacy) no
longer autoloads xtables extensions, which is why a module has to be NAMED rather than left to the
first rule that needs one. The instrument that names this class of defect is the guest console, and
nothing else.

The list is also bounded by what the kernel was built with, and that bound is not negotiable from
here: k3s tries `modprobe nft-expr-counter` and fails because `CONFIG_NFT_COUNTER` is not set at all,
so there is no module to add; it is harmless only because kube-proxy runs in iptables mode. Three
further check-config complaints (`CONFIG_RT_GROUP_SCHED`, `CONFIG_CRYPTO_GHASH`,
`CONFIG_INET_XFRM_MODE_TRANSPORT`) are compile-time gaps of the same kind.

**The same instrument found three more, and they were not cosmetic.** With `ipt_REJECT`, `ip6t_REJECT`
and `xt_nat` absent, kube-proxy's `iptables-restore` failed on the rules that target them, so the
in-cluster `10.43.0.1:443` had no ClusterIP, and every system pod that talks to the apiserver — the
provisioner and the metrics server — crash-looped on `dial tcp 10.43.0.1:443: connect: connection
refused`. The pods were scheduled and started, so the symptom is two services down rather than a
cluster that will not come up. iptables 1.8.11 (legacy) does not autoload these either.

**The list is complete because it is DERIVED, not because it grew — and the weaker instrument was
hiding the rest.** `iptables` inside this guest does not load an extension on demand, and
`iptables-restore` stops at the FIRST extension it cannot find, so discovering names by running and
reading the console is a treadmill: one name per boot, dozens of boots, and a guest whose firewall
is incomplete for as long as nobody looks. Both sides are enumerable at build time. The supply side
is the kernel's own tree: `modules.dep`, `modules.builtin` and `modules.alias`, which is the
kernel's `<ipt_|ip6t_|xt_><EXTENSION> -> module` table. The demand side is the guest's own console,
because a failed restore dumps its ENTIRE payload — 401 rule lines in one run — and the extensions
in it are the demand.

That derivation found two more, and the way it found them is the important part: `xt_NFLOG` and
`xt_physdev`, absent from the list while `-j NFLOG --nflog-group 100` and
`-m physdev --physdev-is-bridged` sat in kube-router's network policy payload.

**And with them missing, the three system pods reached `1/1` Running, with zero restarts, and stayed
there for 1m43s** — while kube-router's network policy controller aborted its whole sync. So pod
readiness is the WEAKER instrument for this class of defect, and a longer module list is not
evidence either. The acceptance is: pods Ready AND held for a stability window AND the guest's own
console free of a failed `iptables-restore` or an aborted policy sync. The failure is racy, too —
one of two boots hit it with the old list — which is what explains two agents measuring the same
three pods as `1/1` and as `0/1` with restarts and both being right.

The names that cannot be left to a same-name module are the ones to watch, and they are few: the
target `REJECT` is `ipt_REJECT` and `ip6t_REJECT` (there is no `xt_REJECT`); `DNAT` and `SNAT` are
`xt_nat` (there is no `xt_DNAT`); `NFLOG` and `physdev` are `xt_NFLOG` and `xt_physdev`. The builder
now refuses a config that names no provider for an extension the cluster's rules use, and refuses a
name the kernel does not carry, so this is caught at build time rather than one boot at a time. What
that cannot do is prove a name NECESSARY: five names have been proven by a measured failure —
`ipt_REJECT`, `ip6t_REJECT`, `xt_nat`, `xt_NFLOG`, `xt_physdev` — and the remaining ~43 are carried
for modes and matches these boots did not exercise.

**And the same missing module is why a node's own cluster services die, which is the failure that
looked like a network problem.** `iptables-restore` is ALL-OR-NOTHING: when kube-proxy's ruleset names
the first extension the guest has no module for, the entire `KUBE-SERVICES` chain is dropped —
including the DNAT rule for `default/kubernetes`. A packet to `10.43.0.1:443` then leaves by the
default route and is refused, and every kube-system component that talks to the apiserver dies on it:

```
metrics-server            panic: unable to load configmap based request-header-client-ca-file:
                          Get "https://10.43.0.1:443/api/v1/namespaces/kube-system/configmaps/
                          extension-apiserver-authentication": connect: connection refused
local-path-provisioner    level=fatal msg="Error starting daemon: invalid empty flag
                          helper-pod-file ... Get ".../configmaps/local-path-config": refused"
coredns                   0/1, never Ready, same address
```

So a missing module presents as three unrelated services failing on one address, and the cause is
visible only in the guest's console 7.6 s in, before any pod needs it. The node itself is unaffected:
it registers, reaches `Ready`, and runs a pod — which is why this hid behind a green four-step proof.

**Two hypotheses were wrong here and are recorded so they are not re-run.** The first: that the
kubelet's LEASED port breaks these pods, because the ecosystem assumes 10250. Refuted three ways —
every failing and passing boot used the lease (`10251`); two boots identical but for the port both
reach `1/1` and hold; and structurally the apiserver takes the kubelet's address AND port from the
Node's own status (`status.daemonEndpoints.kubeletEndpoint`), with k3s starting its metrics-server
`--kubelet-use-node-status-port`, so 10250 is not assumed. The endpoint that looked like evidence,
`https://10.42.0.2:10250/readyz`, is the kubelet's readiness probe OF THE METRICS-SERVER POD, because
k3s gives that pod `--secure-port=10250` — it points at the victim, not the cause. The second: that
the failure was intermittent, because two agents measured the same three pods as `1/1` and as `0/1`.
The reconciliation is that the console reads differ by how much was read — the captured consoles are
bounded copies (~90-123 KB) and a failing run prints only the last 60-80 lines, so a tail read is
indistinguishable from a clean console. Neither agent's measurement was wrong; one of them was
looking at a truncated file.

**The kubelet port lease is retired.** Its recorded justification is that two k3s AGENTS as local
processes on one host contend for host-global ports (`listen tcp 0.0.0.0:10250: bind: address already
in use`), and the fourteen host-global resources measured alongside it are all properties of a node
that is a process in the host's network namespace — which is the substrate this design deleted. A node
is now a guest with its own kernel and its own netns: measured, nothing on the host binds 10250, 10248,
10249, 10251 or 10256 while a guest runs, the host's k3s runs `--disable-agent` and has no kubelet at
all, and pasta is started without `-t`/`--tcp-ports` so no guest port is published and the host cannot
initiate a connection to a guest. What kept the lease alive after that was that `perNodeNetwork`
derived each guest's address FROM the leased port, making it the only per-node-varying input the
composition root had. The leased CIDR replaced exactly that, which is what made the lease removable,
and it has been removed: `PurposeKubelet` is deleted, `kubelet` is in `retiredPurposes`, and the
guest's agent passes no `--kubelet-arg` at all, so the kubelet binds its own default inside its own
namespace. Nothing is lost by that, because no part of the ecosystem assumes a port: the apiserver
takes the kubelet's address AND port from the Node's own status
(`status.daemonEndpoints.kubeletEndpoint`), and k3s starts its metrics-server with
`--kubelet-use-node-status-port`. MEASURED: two boots identical but for the port (10250 vs 10251) both
reached `1/1` system pods and held. MEASURED ONCE MORE, with the flag removed entirely — one
uninterrupted run against a real `k3s server --disable-agent`: k3s passes NO `--port` argument to the
kubelet at all, the guest registered as a Ready node **12.9 s** after `InstanceStart`, its cluster's
three system pods (`coredns`, `local-path-provisioner`, `metrics-server`) reached **`1/1` with zero
restarts** 44.5 s after instance start and held for **1 m 47 s**, the node reports its own kubelet
endpoint as `{"Port":10250}`, `kubectl top nodes` reads it through that port (`215m` CPU, `169Mi`),
and the guest console is clean on every firewall signature. What that run also needed, and what
nothing enforces: the image had to be REBUILT first. The guest's kubelet argv is decided by the agent
inside the image, and the image's recorded inputs do not include that agent (section 14), so a boot on
the previous image passed `--kubelet-arg port=0` — the removed field read back as Go's zero value —
and the kubelet refused to start with `port (--port) 0 must be between 1 and 65535`. The rule
underneath the lease survives it, restated as invariant 8.

**The guest never reaches a registry, which is stronger than giving it a resolver.** The image builder
pulls the images the cluster's own system components use — with `go-containerregistry`, as Go, no
daemon, no credential — and stages them as docker-save tarballs under `<k3s_data_dir>/agent/images/`,
where k3s's agent imports every tarball BEFORE it starts the kubelet. Measured: the import lines are
in the guest's own log at 27-150 ms each, and the whole console contains zero occurrences of
`ErrImagePull`, `ImagePullBackOff`, `no such host`, `dial tcp` or `failed to resolve reference`. The
system pods reach `Running` in a guest whose `/etc/resolv.conf` is still one byte, which is what makes
the property provable rather than merely likely — they cannot have fetched anything.

The list is explicit in the operator's config rather than derived at build time, and it WAS derived
from the shipped binary before being written down: read out of the manifests `k3s server` extracts,
with `pause` taken from a string inside the binary because no manifest names it. Five images, 61.5 MiB
staged and 62.0 MiB of rootfs, against a 1500 MiB headroom. Three of the resolved digests appear
verbatim as blob names inside upstream's own air-gap tarball for this version, which is independent
confirmation that the same images were resolved. One trap the verification caught: containerd
normalizes seeded names through docker's `ParseDockerRef`, so emitting `index.docker.io/rancher/...`
imports under a name no pod asks for — a silent `ImagePullBackOff`, and a test now pins that for every
docker-hub spelling.

Two things this does not cover, both named rather than implied: the `svclb` image for a LoadBalancer
Service is created by the service controller on demand and cannot be found by reading manifests, so
the first tenant LoadBalancer Service will hang until it is added; and a k3s bump moves both the list
and the digests.

**pasta is the rootless network path, and it does hand out privileges.** Measured: under
`pasta --config-net` the child runs as uid 0 in a fresh user and network namespace with a full
capability set, and `ip tuntap add … mode tap` SUCCEEDS there — while the same command in the host's
own namespace is denied. So a guest gets a tap, a bridge and an address without root, and it reaches
a HOST-LOOPBACK-ONLY listener through the gateway address. That is what lets the apiserver stay off
the network: bind loopback, advertise a host address the apiserver will accept for the in-cluster
`kubernetes` Service endpoint, and add the gateway address as a `--tls-san`.

**A guest joins, and this is now the measured destination rather than the direction of travel.** A
k3s agent inside a firecracker guest registers against the host's real `k3s server --disable-agent`:

```
liveweb-node1   Ready   <none>   0s   v1.36.4+k3s1
INSTANCE_START_TO_READY_S  7.4   10.6   9.5
```

Three runs. The time is measured ON THE HOST from the firecracker `InstanceStart` timestamp to the
moment `kubectl` first reports `Ready`, because a guest's own account of its own elapsed time is not
evidence: an earlier round of this work spent real effort chasing a reported 332-second boot that was
a literal `echo` in an init script, printed 0.29 s after the VM started. The spread is k3s's startup,
not the VM. Inside the guest, kubelet logs `Starting flannel with backend vxlan`, registers the node,
sets the pod CIDR, and the kubelet it started binds the port it is given — a leased `10251` in this
measurement, and its own default since the lease was retired (the kubelet lease paragraph above).

Supporting numbers on the way there: a `k3s server --disable-agent` on the host is ready in
**1.1-1.9 s**; the guest's namespace reaches the apiserver. And **a kernel is validated by restoring
it, never by comparing versions**: 6.18.48 cannot be restored at all on this AMD host (`Bad FPU state
… restore_fpregs_from_fpstate` → panic). That is how 6.1.186 became the pin for the CI-kernel route
discussed below, a route this design then abandoned in favour of the host's own kernel — which is
what actually ships, which is why the guest that joined runs the host's kernel, and which turns out
to matter for snapshot restore, below.

**Boot and control-plane costs, recovered from the retired experiment tree before it went**, and
worth keeping because two of them closed lines of work rather than opening them:

```
kernel boot medians   0.270 / 0.341 / 0.336 s   6.1.141 / 6.1.186 / 6.18.48
kernel sizes          41.9 / 45.0 / 27.8 MB     spread 0.07 s
  -> KERNEL CHOICE BOUGHT NOTHING. The 27.8 MB kernel is not faster than the 45 MB one.
root device           initramfs 0.303 s   ext4 0.763 s   squashfs 0.763 s   (delta +0.460 s)
  uncompressed 1.44 MB cpio ties 0.86 MB gzip cpio
  -> the cost is the virtio-blk DEVICE plus the mount path, NOT decompression, so compressing
     the initramfs harder buys nothing
snapshot              create 168.24 ms, state 31 KB, mem 512 MB; loads 5.81 / 6.16 ms
control plane         cold 26.79 s   PRE-SEEDED 1.28 s   restart 1.23 s
                      SIGTERM -> exit 0.50 s, first listener ~0.11 s, 503 -> 401 gap 26.68 s
  -> the 26.79 s was attributed to k3s GENERATING ITS CAs, and that attribution is REFUTED by a
     control experiment on the process substrate. A rebuild that must author its own CAs pays
     within +/-0.24 s of one that is handed them -- sometimes FASTER -- so there is no ~25 s of
     CA authoring to collect, and k3s's own CA generation is ~0.2 s here. What the cold figure
     actually contains is a fresh DATASTORE: 3.7-5.6 s of it disappears when only the store is
     warm and the bundle is absent. The 1.28 s pre-seeded figure reproduces (1.135-1.139 s); the
     26.79 s cold one does NOT (4.97-6.75 s), so the two were measured under different store
     conditions and comparing them was wrong. The identity bundle's measured value is the
     IDENTITY it preserves -- a CA fingerprint byte-identical across a teardown and rebuild,
     where a control's changed every time -- not a time saving. Without it every rebuild is a new
     cluster under the same name, and every prior kubeconfig and ServiceAccount token stops
     validating silently.
flannel recovery      killing agent B leaves A's pods Running again 75 s later; 6 veths on cni0
                      shared by both clusters' pods
```

**Restoring a snapshot instead of booting is REFUTED as the provisioning primitive, and the numbers
are not close.** Measured on this host against the same image:

```
cold boot          InstanceStart -> Ready   9.079   9.158   10.768 s
snapshot freeze    with the agent running   0.698   0.719     0.731 s
snapshot load      firecracker's own count  3.96 ms   (0.247 s for the whole path, pasta included)
LOAD -> Ready                               9.974 s
```

The load is **2.6 % of the operation**. Everything else is k3s reconnecting and re-reporting itself,
so a 4 ms restore buys a 250 ms head start on a 9.5 s job. Counted honestly from the other end it is
worse still: a node whose VM dies is not noticed by the cluster for **53.9 s**, so a cold boot from
nothing beats waiting for the monitor and then restoring.

**And it cannot make a NEW node at all, which is the part that kills it.** Restore does not re-run
boot-time units, so the guest's own identity adoption never happens: `kcp-agent.service` does not
re-run, `mountNodeDisk` and `adoptIdentity` never run, and no agent start marker appears in a restored
console. A snapshot frozen as `liveweb-node1` — with its recorded node-disk path deliberately
re-pointed at another node's disk before the load — comes back as **`liveweb-node1`**. So restore can
thaw a node the cluster still knows and nothing else. Two consequences follow directly: a control
plane can only ever scale a restored node back up, never out, and **a deleted Node object is never
re-created**, because the kubelet only retries `updateNodeStatus` against a node that is not there and
makes no registration attempt. Since a snapshot is also valid only for the control-plane INSTANCE it
was frozen against (the join token is per instance), the mechanism's scope is one node, one name, one
cluster incarnation.

Two facts worth keeping even though the mechanism is gone. **A per-node disk CAN be attached to a
restored guest**: a drive `PUT` whose backing file exists is accepted and the load that follows
succeeds, so the constraint is not that the drive slot must exist in the snapshot. What is true is
that a drive `PUT` which FAILS leaves the VMM unable to load *any* snapshot (`Loading a microVM
snapshot not allowed after configuring boot-specific resources`), which is a nasty failure shape:
one rejected device poisons every later attempt. And **restorable FPU state is a property of the
kernel, not of the guest**: restoring the host's 7.2.7 kernel panics on the guest's first syscall
return (`exc_general_protection` in `restore_fpregs_from_fpstate`, reached from `switch_fpu_return`),
and `noxsave` removes the panic on this kernel — an earlier note that `noxsave` merely converts the
panic into a hang was measured on 6.18.48 and does not hold on 7.2.7. That leaves two requirements in
direct conflict: k3s needs the host kernel's netfilter symbols, and a restorable guest wants a kernel
whose FPU state this host can actually restore. `noxsave` is an escape hatch, not a resolution, and
its effect on a k3s workload is unmeasured. Firecracker v1.13.1 also carries the pre-1.15.1 kvm-clock
bug: a restored guest's monotonic clock jumps forward by the pause duration.

**Two failures that presented as plumbing, and were one cause: the `pasta` child inherits pasta's own
SELinux domain.** `/usr/bin/pasta` is `pasta_exec_t`, so a child of it runs in `pasta_t` unless the
program it execs is a type whose own policy transitions it away. In `pasta_t`, `ip tuntap add` is
denied `CAP_NET_ADMIN` and `open` on a console file in `/tmp` is denied — and, the detail that made
this expensive, `ifconfig_t` cannot write to the pipe `pasta_t` owns, so the error TEXT of both
failures was silently discarded and arrived as an empty string. `pasta link in namespace: ""` and a
permission error with no message were the same bug. The child is now `/bin/sh -c 'exec ...'`, and
**the reason that works is not the one this document first gave.** Measured: on this host `/bin/sh`
is `system_u:object_r:bin_t:s0`, NOT `shell_exec_t` — `/usr/bin/bash` is the `shell_exec_t` one. What
actually holds is that the gateway itself runs in `unconfined_t`, so exec'ing a `bin_t` file does not
transition at all. The fix is incidental rather than principled: a gateway deployed under a systemd
service domain would get the original failures back, including the silent one.

What survives the domain changing is a POSITIVE PROBE rather than a label. The namespace is treated
as configured only once the child has been observed to find pasta's own upstream link, and nothing is
mutated before that, so a child without the privileges the work needs fails at the observation
instead of half-configuring a VMM. Its power is bounded and worth stating: it refuses a child that
was unshared WITHOUT pasta's configuration, but a child that was never unshared at all sees the
host's own interfaces and would pass — so it establishes "unshared, with pasta's config" rather than
"provably pasta's namespace". A failure that writes nothing on either stream is also now a failure
that names its own emptiness, because the original bug was exactly that: a denial whose account was
denied too.

**pasta also hands its child a PID namespace.** The guest-side code reported its VMM as pid 17 while
the host saw 2188474, so a record naming that pid named nothing. The record now resolves the HOST pid
from its own identity, so the record, the startup sweep and the liveness lookup agree by construction
rather than by three callers happening to do the same thing.

**One writable ext4 serving as both template and instance was a correctness bug, not hygiene.** The
guest's writes — k3s's extracted data dir, `/etc/rancher/node/password`, generated keys — persisted
into the file the next boot read, and the next boot failed `k3s check-config` with
`sha256sum: does not match`. Two VMs on one ext4 would also have corrupted each other. The image is
now a TEMPLATE and every node gets its own clone at `<dataDir>/rootfs.ext4` (`cp --reflink=auto`,
free on this filesystem), removed on teardown. Overlayfs is the alternative; reflink needs no
guest-side mount work and no extra drive.

**A host that died at its first step and a host that is merely slow look identical until the clock
runs out.** The guest's console is read for named fatal markers (`Kernel panic`, `KCP_INIT fatal`,
`KCP_PREFLIGHT fatal`, `KCP_AGENT fatal`, `KCP_AGENT_EXIT rc=`), and confirming a join returns
immediately with the guest's own words instead of waiting out the timeout. This is what turned a
missing k3s binary from a four-minute mystery into an instant, named error. The guest agent writes
the same account to `/dev/console` directly as well as to a file, so a dead or rate-limiting journald
cannot turn a guest's only report of its own failure into silence.

**A host prerequisite that cost real time:** the repository directory carried the SELinux type
`user_tmp_t` instead of `user_home_t`. Under pasta's namespace that makes binaries built in the tree
unexecutable and repo paths unreadable, and it presents as `permission denied` on unrelated-looking
operations. `restorecon -R` on the tree and its parent is the fix, and a host that runs this substrate
should be checked for it.

**What the VM substrate requires, and what it costs.** Firecracker's CI kernels are under a
DATE-based artifact prefix; a version-numbered prefix such as `firecracker-ci/v1.13/` is STALE and
still served, and a year-old kernel from it BOOTS PERFECTLY, which is exactly why the mistake is
invisible. Pin by the newest date prefix.

The current kernels carry `VXLAN=y`, `NF_TABLES=y`, `VETH`/`BRIDGE`/`TUN=y` and `OVERLAY_FS=y`, so
k3s's networking is reachable — with ONE hard exception: `CONFIG_NETFILTER_XT_MATCH_COMMENT` is
UNSET, and kube-proxy's iptables mode appends `-m comment --comment` to essentially every rule it
writes. It is on kubeadm's required-kernel-config list for that reason. That left two routes — build
a kernel, or build `xt_comment.ko` against the 6.18 CI kernel (the only one with `CONFIG_MODULES=y`)
and ship it in the rootfs — and BOTH were abandoned for the cheaper route stated above: boot the
host's own kernel with the host's own modules. What survives from this finding is the rule rather
than the route: **a guest kernel's module set is a MEASURED set**, and the measurement is
`k3s check-config` run inside the guest, plus reading the guest's own console for the iptables
failures that check-config does not catch. `CONFIG_IP_VS` being unset is irrelevant: ipvs mode is
deprecated and being removed.

Firecracker's DEFAULT cmdline passes `nomodule`, so a distro kernel whose virtio drivers are modules
needs both a `boot_args` override and an initramfs — which makes building a kernel the better route,
not the worse one. `VIRTIO_FS` is unset in both kernels, so a virtiofs root is not available.

The guest needs a REAL init: k3s runs the kubelet with `--cgroup-driver=systemd`, so systemd must be
managing cgroups inside the guest. A busybox-init guest proves rootless boot and networking and is not
the shape a node needs.

Networking is rootless either way; what changes is whether the host's apiserver has to leave loopback.
With a userspace gateway such as `pasta` the guest reaches the host's loopback services through the
gateway address, so the listener stays on loopback and only the SAN grows: `--tls-san` carries the
gateway address the guest dials. That is why bind and advertise are now separate values.

The point of the VM is not isolation for its own sake: it is that every one of those host-global
resources is PER GUEST inside it, so N nodes fit on one host where a local process allowed one. The
node contract above is unchanged by the substrate — one long-lived command (the VMM), closing it is
the teardown, nothing is read back out, `--node-name` is set from the `(cluster, node)` pair and the
token still travels as a `0600` file.

**The seam must not give the dial lifecycle ownership.** If `Close` also destroys, then `Wait` and
`Close` become indistinguishable in effect, and the contract above collapses: the node is supposed to
die BECAUSE THE COMMAND EXITED, not because we asked. The refusal was therefore explicit, and it lived in a control that nothing reached: the package that
carried it, `internal/nodetransport`, is DELETED, because a control nothing reaches is
indistinguishable from a control that is not there. The rule it enforced is kept here as a rule,
which is where it always belonged: **the substrate must not destroy the node it dials.** "The
substrate must not destroy this" has to be a value a caller is handed, never a convention a caller
has to remember. A lifecycle-owning dial puts a destroy inside the transport, which is precisely
where nothing asserts it is never called.

Note the control's reach, which is why it is gone: nothing in the tree ever imported it, so that
refusal is recorded in section 15 alongside the package that carried it. A control nothing reaches is indistinguishable from a control
that is not there, which is why it is stated here as a rule rather than trusted as a guard.

The token never rides argv: it goes in a `0600` file named by `--token-file`. An argv value is
readable from `/proc` by every local user, and k3s derives its bootstrap storage key from the join
token, so a leaked token is a leaked cluster.

**For a guest the token must not ride the kernel command line either, and the reason is not obvious.**
The command line reaches a console TWICE: firecracker logs the body of its own `PUT /boot-source`
request, and the kernel prints `Kernel command line:`. A `kcp.token=...` argument is therefore
written to `guest.console` in plaintext, twice, every boot. The per-node secret travels instead on a
per-node read-only virtio-blk disk, written `0600` on the host at provision time and destroyed at
teardown, carrying the join token and the `(cluster, node)` identity. Nothing secret reaches a
console, k3s still takes it from `--token-file`, and the disk is what makes a snapshot CLONE-able:
the image stays generic because the per-node half is injected.

**The join token is also the node's k3s password, written to three paths, and that reuse is recorded rather than latent.**
`adoptIdentity` writes the same `node.Token` to the file k3s is handed by `--token-file`, to
`/etc/rancher/node/password` and to `<data-dir>/agent/node-password`: k3s reads the second as the
node password it presents when it registers, so one value is both the cluster join secret and the
credential the kubelet authenticates with. The v1 ceiling is stated rather than hidden: every node
of a cluster shares one node password, which is harmless at 0-or-1 nodes and is inherited by the
first multi-node change, where a per-node password must be minted instead.

**The image is a directory, not a file.** `vmlinux`, `initramfs.cpio`, `rootfs.ext4` and `image.json`
— and the boot line travels WITH the kernel inside `image.json`, so a kernel and the command line
that suits it cannot be separated by an operator choosing two paths. The builder refuses to write a
2 GB image that is missing k3s, the agent, the guest document, the units or the kernel config, which
fails in seconds instead of after a boot.

**The record's identity is supplied by the substrate, and it is not a local-process idea.**
`internal/noderecord` matches a token SUBSET against `/proc/<pid>/cmdline` after confirming the pid's
start time, and a record whose identity does not match is NOT OURS and is never signalled — a pid
reuse must stay a refusal, not a kill. A local agent is `{"agent", nodeName}`; a VMM is
`{"firecracker", "--api-sock", <sock>}`, and the socket path is per node, so one node's record can
never match another's VMM. An empty identity matches nothing rather than everything, which is the
only safe reading of "every token present".

**The guest runs with SELinux off**, `selinux=0` on its command line and `SELINUX=disabled` in the
root filesystem where the base image carries a config. A node's guest boots the host's OWN kernel,
which is built with SELinux, while its minimal root filesystem has no policy to enforce, so leaving
it on is at best noise and at worst an unlabelled denial that surfaces as a permission error
somewhere unrelated to its cause — which is exactly the shape of the two `pasta_t` failures above.
The security boundary for a node is the microVM, not SELinux inside a guest that has no policy. This
is guest-only; the host's policy is untouched.

`k3s server` is deliberately NOT a node: it runs gateway-side with `--disable-agent`. Only the worker
scales to zero.

**A Node's cloud identity is the cloud controller manager's, and it runs on the host.** A guest joins
as a Kubernetes Node with no cloud identity: an empty `spec.providerID` and no labels for an
autoscaler to group by. The controller stamps each Node it observes with
`spec.providerID = socialweb://<cluster>/<node>` and the label
`node.socialweb.computer/cluster=<cluster>`, and deletes a Node whose instance record is absent -
the guest is gone. It reads the guest set from `<DataDir>/nodes.json`, which the provider writes
(atomic temp-plus-rename) on every provision, teardown and reap, and once at startup as `[]`.

Two things about that snapshot are load-bearing and neither is obvious. An UNREADABLE snapshot is an
error and patches nothing, because invariant 14 applies and deleting a Node out from under its own
running work is the cost of getting it wrong. But an ABSENT snapshot and an empty array are the same
answer to the reader - `(known=false, err=nil)`, the delete path - so writing `[]` protects nothing
on its own; what protects a live Node is that the snapshot LISTS it. The file is infrastructure and
not a cache, and the ordinary path is what keeps the absent case out of reach: the provider writes it
before the controller can be asked anything.

The identity is SCOPED, never the superuser, and the provider grants it `get`, `list`, `watch`,
`patch`, `update` and `delete` on `nodes` once k3s is up. That grant is not decoration: a scoped
identity with no binding is authenticated-and-denied, so the informer never syncs, the readiness gate
times out, and the failure reads as a controller that will not start rather than as a missing
binding.

### `k3s server` argv



```
--disable-agent                        the keystone
--disable=traefik                      leaves the manifest controller free otherwise
--data-dir <dir>
--datastore-endpoint <dsn>             REQUIRED; empty is a silent new cluster
--egress-selector-mode cluster
--advertise-address <addr>             what a node dials
--bind-address <loopback addr>         NOT the advertise value. k3s defaults it to 0.0.0.0, which is
                                       EVERY tenant's apiserver on every interface of the host.
                                       Bind is loopback; advertise is an address the GUEST can
                                       reach, and the two being separate is the whole point: a node
                                       that is a guest dials the pasta gateway, never the loopback.
                                       Making them one value is correct only for a node on the
                                       host, which is the mechanism this design refuted
--tls-san <the address a node dials>   repeated; carries the gateway address, so the certificate
                                       covers what the node actually connects to
--advertise-port <the apiserver lease's port>
--https-listen-port <the same port>    ONE lease answers both. A separate advertise lease existed
                                       for a forward that this cluster does not have, and it is
                                       retired: dropped from an old lease file on open, refused on
                                       allocate, its ports returned to the carve. Measured with the
                                       two split: the EndpointSlice reads <gateway>:<a port nothing
                                       binds> -- 63014, connection refused, while the listen port
                                       answers 401 -- coredns sits 0/1 for five minutes,
                                       local-path-provisioner and metrics-server exit 2 into
                                       CrashLoopBackOff, and kubectl logs answers
                                       `proxy error ... code 502`. With one lease: three system
                                       pods 1/1 with zero restarts, and kubectl logs arrives through
                                       the apiserver's own tunnel
--token-file <0600 path>
--write-kubeconfig <DataDir>/server/cred/admin.kubeconfig
--kube-apiserver-arg=audit-policy-file / audit-log-path / -maxsize / -maxage / -maxbackup
--kube-apiserver-arg=service-account-issuer, api-audiences
--kube-controller-manager-arg=secure-port, --kube-scheduler-arg=secure-port,
--kube-cloud-controller-manager-arg=secure-port
```

A direct `--audit-policy-file` on the server is SKIPPED by k3s with only a warning, which leaves the
cluster SILENTLY UNAUDITED and starting normally. The audit flags must ride `--kube-apiserver-arg`.
An unarmed kcp is the failure mode nobody sees from a green suite.

**The address a node DIALS belongs to the substrate, and it is not the same address for both.** A
node that is a local process reaches an address this host carries. A node that is a guest reaches
NONE of them: it sits behind a tap in its own network namespace, and its only path to the host is the
gateway its default route names — which is why a guest reaches the host's loopback through that
gateway and cannot reach the host's LAN address at all. So the guest's dial target is built from the
substrate's gateway rather than from an operator-chosen host address; k3s advertises that same
gateway for this substrate; and the gateway is in `--tls-san`, because the guest dials it and a
certificate that does not cover what the node connects to reads as an x509 error rather than as a
missing SAN.

The host-carried-address check is therefore narrowed to the substrates where it is true rather than
deleted, and what replaces it for a guest is not another address property but POSITIVE EVIDENCE:
`AssertAgentDialledOurPort`, run against the node's own account of what it dialled, because a node
joining is not evidence that it joined THIS cluster. That control had been written and unreached
since before the substrate existed.

**Every guest gets its own address, taken from the range its cluster leases.** The shared constant was
this host's own LAN address, handed to every node — one node works and two collide into two Node
objects with one InternalIP, with flannel on each treating the other's pod CIDR as its own. A range is
compared against the host's own networks before it is leased, so no guest is ever handed this host's
address; the gateway is the range's address 1 and the node is `1+ordinal`, and `Lease.Host` refuses a
number outside the range rather than returning one.

### The QEMU substrate

`internal/nodeqemu` is a second substrate behind the same `nodeHost` seam, selected by
`SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE=qemu`. It boots the SAME image as firecracker — the kernel,
the initramfs and the rootfs `internal/nodefirecracker.LoadImage` resolves from the builder's
configuration — because a node image is a set of portable files, not a firecracker artifact. The
guest is started with a `qemu-system-x86_64` command line:

```
-name <node name>                    the per-node token the record is matched by (see below)
-m <memMiB>  -smp <vcpu>             the machine size, from the image manifest
-machine q35,accel=kvm:tcg           kvm where the host has it, and a plain fallback rather than a
                                     refusal where it does not: the host contract checks for kvm, and
                                     a boot that finds none is a boot that is merely slow
-display none  -serial stdio  -monitor none
-kernel <vmlinux>  -initrd <initramfs>  -append "<manifest boot args>"
-drive file=<rootfs>,if=none,id=rootfs        -device virtio-blk-pci,drive=rootfs
-drive file=<node-disk>,if=none,id=nodedata,readonly=on -device virtio-blk-pci,drive=nodedata
-netdev tap,id=net0,ifname=<tap>,script=no,downscript=no
-device virtio-net-pci,netdev=net0,mac=<guest mac>
```

The rootfs clone is writable and the per-node identity disk is read-only, exactly as under
firecracker, so the guest still finds its token on the disk by label rather than on any command line.

**The network is the SAME mechanism, not a QEMU mode.** pasta gives the namespace child a link
carrying one of the host's addresses; `nodevm.WireNamespace` creates the tap and bridges it onto
pasta's own link, and refuses to configure the VMM until the namespace has been observed to carry
that link. Both substrates start pasta the same way — `nodevm.PastaArgv` — and both exec the same
`kcpagent namespace <params.json>` inside it; the params file's `kind` field decides which VMM the
agent runs. There is no `-netdev user`, no host-forwarding rule and no QEMU-only address: the guest
dial address, the gateway, the tap, the bridge and the per-node address all come from the same range
`internal/subnetallocator` leases to the cluster, so a QEMU guest is addressed exactly as a
firecracker guest and reaches the host's apiserver only through the leased gateway.

**The record's identity is the node name on the command line.** `internal/noderecord` matches a
token SUBSET against `/proc/<pid>/cmdline` after confirming the pid's start time. Firecracker's
identity is its API socket; QEMU has no API socket, so its identity is `{"-name", <node name>}` —
the node name is already per cluster and per node, qemu is started with it, and a record whose
identity does not match is NOT OURS and is never signalled. A gateway that restarts reaps a QEMU
guest the previous one booted by reading the same record and matching the same `-name`, which is the
whole of the "reap by identity" contract.

**What is refused, and why each refusal is a value that cannot be in force.** `qemu` demands the
QEMU binary, the node agent and the image store and configuration, exactly as `firecracker` demands
its own binary; `checkPlacement` refuses a qemu placement missing any of them, and it refuses a
firecracker-only input under qemu (and a qemu-only input under firecracker) because a VMM binary for
the substrate that was NOT selected is never executed, and an operator who set one and saw no
complaint would believe it was carrying it. `--advertise-address` is refused under qemu for the same
reason it is refused under firecracker: a guest reaches none of this host's addresses, so the value
cannot be in force.

**The shared half lives in `internal/nodevm`, and neither substrate imports the other.** The types
that are true of both mechanisms (`Image`, `Guest`, `Network`, `NodeDocument`, `NamespaceParams`),
the namespace plumbing that wires pasta's link to the guest's tap, the per-node identity disk, the
rootfs clone, the console's fatal-marker watch and the `/proc` identity scan are one package. `cmd`
is the only place that knows both substrates: it loads one image and builds one `nodeSubstrate`,
and `nodeSubstrateFor` chooses the implementation. A cross-substrate import is a coupling this
avoids rather than a convenience it takes.

**A node image is built for one VMM, and refuses to boot under the other.** The builder's
configuration carries `vmm` (`firecracker` or `qemu`, absent meaning firecracker), and the VMM is a
recorded input: it is in `BuildInputs` and in `BuildConfig`, so two images of the same inputs for
different VMMs have different fingerprints and never collide in the store. The image's manifest also
carries `vmm`, so the image is self-describing, and a substrate refuses an image built for the other
one — in `checkPlacement`, where the builder's own configuration is held to the selected substrate,
and again in `nodeSubstrateFor`, where the loaded manifest is held to it. That refusal is the point:
both VMMs boot the same kernel and hand the guest the same files, so a cross-substrate boot is not
caught by the kernel, comes up, joins and reaches Running, and the only place the mismatch is written
down is this field. What genuinely differs by VMM is the virtio TRANSPORT the guest's block device
arrives on — firecracker hands it over virtio-mmio, qemu over virtio-pci — and the builder derives
which transports the image must be able to load from the real module tree rather than asserting a
rule: on kernel 7.2.7-200.fc44.x86_64 `virtio_pci` and `virtio_blk` are BUILT IN (present in
`modules.builtin`, absent from `kernel/drivers/`), while `virtio_mmio` is a loadable module the
firecracker configuration already stages. So a qemu image needs no transport module at all here and a
firecracker image needs the one it already names; the artifact sets are otherwise identical. The
builder refuses a VMM whose transport the named tree carries neither built in nor loadable, and
refuses a VMM whose transport is a module the initramfs does not name.

**Status: the QEMU argv, identity, placement and per-VMM image are exercised by unit tests, and the
four-step proof runs under QEMU on this host.** `internal/nodeqemu` boots a real guest only when
`SOCIALWEB_NODEQEMU_IMAGE`, `SOCIALWEB_NODEQEMU_AGENT` and a qemu binary are supplied along with
pasta and `/dev/kvm`, and skips when they are not; a unit test boots a stand-in executable instead
and asserts the command line and the published pid, so no test needs real QEMU.

**The four-step proof was run under both substrates on 2026-09-24, from scratch, each one
uninterrupted run, nothing deleted by hand.** The QEMU run used the qemu image at
`kcp-image/out/sha256-3f9b63b082a3fbf7b92a49160c8e2697ed2b726dad8ca8048ef4171c9998e654` (built from
`kcp-image/nodeimage-qemu.json`, `vmm=qemu`), an agent built from the same tree, and
`/dev/kvm` present, so the guest was booted with `-machine q35,accel=kvm -cpu host`. All four
transitions hold: the control plane reached Ready with zero nodes (STEP 1); `kubectl get nodes` was
empty and the cluster stayed at zero across 10 passes 2 s apart over 20 s (STEP 2); a tenant workload
applied at zero nodes gave `Provision, nodes=1`, the qemu guest booted and its k3s joined as a Ready
node, and the tenant pod ran on `liveweb-node1` (STEP 3); deleting only the tenant workload gave
`Teardown` after the hold, `kubectl get nodes` was empty again, and the control plane was still Ready
and still listed its namespaces (STEP 4). The firecracker run was re-measured the same day against a
freshly built firecracker image and the same agent, so the two numbers below share one method and one
image.

| measurement | firecracker | qemu |
|---|---|---|
| `INSTANCE_START_TO_READY_S`, host-timed from the VMM's instance record to the Node's Ready condition, resolution 250 ms | **5.460** | **7.256** |
| wall time of the whole proof | 98.79 s | 114.39 s |
| historical baseline (2026-09-23, older image, firecracker's `InstanceStart` timestamp) | 9.749 / 9.609 | — |

The qemu guest is ~1.8 s slower to Ready than firecracker's on this host. The two start clocks are
the same kind of host timestamp but not the same instant: firecracker's `StartNanos` is written when
its API's `InstanceStart` succeeds, qemu's when the VMM process is started, so qemu's interval
includes the process spawn and device setup that firecracker's does not — the real gap in guest boot
is therefore somewhat smaller than the table's difference.

One operational condition the first firecracker run exposed, and which is pre-existing rather than
qemu-specific: the node's data directory must be short enough that `<dataDir>/fc.sock` fits the unix
socket path limit (108 bytes), or firecracker's socket cannot be dialled and the guest never starts.
The proof's default `TMPDIR` satisfies it; a very long temporary or state path does not.

One prerequisite the qemu live boot exposed earlier: the host-side agent binary must be built from
the same tree as the gateway, because the namespace parameters carry `kind` and `vmm` rather than
firecracker's fields, and an older prebuilt agent refuses them. The guest-level measurements recorded
above — the module set, the iptables failures, the Ready timing — were taken under firecracker and
are not re-derived here; the qemu run above is the full-flow proof, not a re-measurement of those.

### The host platform

The gateway is one binary per OS, and the substrate, the accelerator, the network, the process
identity and the disk tooling are the host's, not the cluster's. `internal/hostos` is the single
seam: capability predicates (`Name`, `Firecracker`, `QEMU`, `NetworkMode`, `Accel`) and portable
primitives, with `_linux.go` and `_windows.go` implementations. Nothing else in the tree reaches
for `syscall.Flock`, `syscall.Kill`, `/proc/<pid>/stat`, `Pdeathsig` or `Setpgid`; the packages that
used to are routed through the seam, and the Linux implementation is the previous behaviour.

| | Linux | Windows |
|---|---|---|
| substrates | firecracker and qemu | **qemu only**. Firecracker is a KVM guest and exists only on Linux, so `checkPlacement` and `nodeSubstrateFor` refuse the name on a Windows host, naming the host and why, rather than counting a node that was never booted |
| accelerator | `kvm` when `/dev/kvm` exists, with `-cpu host`; `tcg` otherwise | `whpx` with a `tcg` fallback (`-machine q35,accel=whpx:tcg`); no `-cpu host`, because the guest is not the host's CPU under WHPX and the flag is untestable here |
| network | pasta: a fresh user and network namespace, a tap bridged onto pasta's own link; the guest dials the first usable address of the cluster's leased range | qemu user-mode (slirp): no namespace, no pasta, no tap and no bridge; the guest is `10.0.2.15/24`, its gateway is `10.0.2.2`, and the guest dials `10.0.2.2`, which slirp maps to the host's loopback where the apiserver already binds. The per-node subnet lease is not taken; `nodevm.UserNetwork` supplies the fixed network |
| process identity | `/proc/<pid>/stat` start time plus `/proc/<pid>/cmdline`, so a reused pid is NOT OURS and is never signalled | `OpenProcess` + `GetProcessTimes` creation time plus the command line via the Windows management interface, decomposed by `CommandLineToArgv`; a reused pid is refused the same way |
| liveness and stop | `kill(pid, 0)`, `SIGTERM`, `SIGKILL` | `GetExitCodeProcess`, `TerminateProcess` |
| process groups | `Setpgid`; `Pdeathsig` on the VMM | `CREATE_NEW_PROCESS_GROUP`; no `Pdeathsig` equivalent, so the VMM is not signalled by the parent's death |
| file locking | `flock` | `LockFileEx`/`UnlockFileEx` |
| local port range | `/proc/sys/net/ipv4/ip_local_port_range` | `netsh int ipv4 show dynamicport tcp`, falling back to the documented 49152-65535 when the command or its localized output cannot be read |
| rootfs clone | pure-Go copy, on both | pure-Go copy, on both |
| rootfs filesystem | `mkfs.ext4` from the distribution | `mkfs.ext4` must be provided by the operator (an e2fsprogs toolchain on PATH); a missing one is refused with the platform named |
| node image build | this host's own `/boot` kernel and `/lib/modules` tree | requires the kernel and module tree handed in, because there is no `/boot` or `/lib/modules` of this host's kernel; the builder and its checks report those inputs NOT APPLICABLE on Windows |

**What is refused on Windows, and where.** Firecracker is refused at `checkPlacement` and again at
`nodeSubstrateFor`. A missing `mkfs.ext4` is refused where a node disk is built. The guest half of
`socialweb-nodeagent` (`init`, `agent`) is a Linux build by construction — it mounts the node disk
and switches root inside the guest — so a non-Linux build of that binary refuses those verbs and
carries only the host side. The node image's kernel and module inputs are reported NOT APPLICABLE by
`check-host` on a platform that has neither.

**What was observed, and what was not.** On 2026-09-24 the four-step proof was run on this Linux host
as the capability matrix: `TestTheFourStepProofRunsThroughTheCompositionRoot` ran both substrates in
one process, and both passed in 210.86 s -- firecracker `INSTANCE_START_TO_READY_S=5.423` and qemu
`INSTANCE_START_TO_READY_S=6.915` (host-timed from each VMM's instance record to the Node's Ready
condition, 250 ms resolution), each through all four transitions. The second substrate's run exposed
a test-harness defect rather than a substrate one: `liveports.TakeForProcess` opened a slot lock and
did not retain the handle, so a second subtest in the same process could take the first's slot and
the two runs collided on ports. The handle is now retained for the process, and the runs use
separate slots. A second, deeper defect turned a failed placement into an unbounded hang:
`processrunner.Process.Wait` waited for the goroutines copying the child's stdout and stderr to
reach EOF, not only for the direct child to exit, and a node's VMM is a descendant of pasta that
inherits those pipes. Signalling pasta alone left the guest holding the write end, and the proof
blocked for 37 minutes in `sync.Once.Do` inside `Wait` -- past its own context, which `Wait` does
not observe. `WaitDelay` now bounds the drain to five seconds after the child exits, and a reaped
process is not an error. MEASURED after both fixes: the matrix ran clean twice, both substrates in
one process -- 210.86 s (firecracker 5.423 s, qemu 6.915 s) and 205.33 s (firecracker 5.166 s, qemu
13.489 s). Everything in this document that is measured was measured on Linux, and the Windows
column above is **design and cross-compilation only**: no Windows binary
was executed on this Linux host, so no Windows boot, no WHPX boot, no user-mode networking path and
no Windows process-identity read has been observed to work. `GOOS=windows GOARCH=amd64 go build
./...` and `go vet ./...` are clean, which proves the tree compiles and type-checks for Windows, and
nothing more. The runtime paths that carry the most risk are the ones no compiler can check: the
CIM/PowerShell command-line read (it shells out per lookup, and a localized or denied environment
breaks it), `TerminateProcess` standing in for a graceful stop, `LockFileEx`'s byte-range lock
against `flock`'s whole-file semantics, WHPX actually being available on a given Windows host, and
slirp's `10.0.2.2` reaching the apiserver's loopback listener. Each is a place a Windows host must
be measured before this document may claim it works.

## 10. The scale decision

```
observe  ->  decide  ->  perform
```

`internal/scale` is a pure `Decide(Observation, State, now, Options) -> Decision`. It asks whether
ANYTHING is running, not whether a specific pod is. **The narrow predicate cannot scale to zero:** a
predicate that can only fire upward cannot express it. So the intent test is deliberately coarse, and
a list failure is an ERROR, never an empty observation - an unreadable cluster is not an idle one,
and acting on that reading tears a node down under its own work.

`internal/scaledriver` performs the decision and owns the one thing `Decide` cannot: the state a
decision carries BETWEEN passes. `State.Nodes` is deliberately NOT remembered; it is re-read from the
Node CRs, because those CRs are the idempotency record and a node that died must be seen to be gone
rather than trusted from a variable. What is remembered is `IdleSince` - **without it there is no
hold at all**, because `Decide` would see an idle cluster for the first time on every pass and never
reach it.

**The state advances only on success.** A driver that recorded a node as up because a call was
attempted leaves a cluster with intent and no node, permanently.

**Both halves of the observation come from ONE call.** The objects decide whether there is intent and
the count decides whether a node exists; read at different moments they can disagree - a pod counted
before its node's CR landed is a cluster that provisions twice, and the reverse tears a node down
under its own work.

**Node phase semantics, and where their boundary is.** `Pending` is
  written before the command exists and `Draining` on every teardown path, and `Running` is written
  only by `ConfirmJoined`, which the composition root calls after the tenant cluster's own node list
  reports the node. So `Running` means k3s registered it, not merely that a command is up. The scale
  decision does not read the phase at all — it counts the tenant cluster's Nodes — so a node stuck in
  `Pending` past the join timeout is a warning and not a wedge: the next pass re-observes and
  confirms it.


The count is the tenant apiserver's own Nodes, which is what `kubectl get nodes` reads, so the number
the decision acts on is the number an operator can check. That is the *dead* direction that matters:
a node that died without its CR being updated would be counted alive by the CR route, which is the
direction that stops a cluster ever reaching zero.

**The tenant's Node object outlives the agent, so the teardown must delete it.** A k3s agent does not
remove its own Node object when it stops — kubelet does that only when a cloud provider is configured,
and none is. So a teardown that stops the process and skips the delete leaves the count above zero
with no intent, and `Decide` calls teardown again on every pass for ever: the cluster is never
observed at zero nodes, which is the objective. The delete belongs to the teardown, after the guest
is stopped, tolerating NotFound as success; a provision deletes nothing. It cannot live in
`nodelocal`, which has no tenant client, so it lives in the
composition-level `scale.Nodes` that wraps the host — and a nil tenant client is an explicit refusal,
never a skipped cleanup, because a skipped cleanup and a completed one look identical from a green
suite.

`SOCIALWEB_CLUSTERPROVIDER_SCALE_HOLD` is the down-direction grace. It is a parameter rather than a
constant because the number has not been derived by measurement.

**The decision carries a target total, and the boolean that reaches zero is untouched.** The
narrow-predicate argument above is not weakened by counting: `Intent` stays a boolean, and a
SEPARATE `Demand(o)` counts the work as a pod count, so an error in the count cannot make zero
unreachable - it can only ask for the wrong number of nodes. `Target(o, opts)` is
`ceil(Demand / PodsPerNode)` clamped into `[1, MaxNodes]` when there is intent, and `0` when there
is not, which is the only place the ratio is applied. `Decision.Nodes` is therefore a target total
rather than a delta, and the driver reconciles the cluster's actual count - re-read from the Node
CRs, never remembered - to it.

**`MaxNodes` defaults to 10, and a ceiling below one node is REFUSED rather than clamped.** Named
defaults are `DefaultMaxNodes = 10` and `DefaultPodsPerNode = 8`, resolved in `Options` the same way
`hold()` resolves the grace, and settable per cluster as
`SOCIALWEB_CLUSTERPROVIDER_SCALE_MAX_NODES` and `SOCIALWEB_CLUSTERPROVIDER_SCALE_PODS_PER_NODE`. The
refusal is on `n < 1`, not on `n < DefaultMaxNodes`: a ceiling under one is not a smaller cluster, it
is a configuration whose only consistent reading is "never place the work", while a ceiling of three
is a smaller cluster and is taken. The default was 1 until 2026-10-07, when a five-job GitLab run
measured what one node means for a runner with `concurrent = 5` (section 15); `scripts/deploy.sh`
passes 10 and one pod per node, so a cluster serving this runner asks for the nodes the jobs need.

**Scale-in is all-or-nothing, and that limit is stated rather than latent.** While there is work
the target only grows, to the cap; when the work stops the cluster goes to zero, exactly as it
always did. An intermediate count is not reachable, because scaling three nodes to two needs an
answer to "which node, and does its work move", and no instrument here measures that yet. The
branch that would do it reads `None` with a reason naming this paragraph, so a future scale-in has
one place to change rather than a silent gap. The ceiling also interacts with the cluster's own
`/24`: every node takes an address from the range `subnetallocator` leased, so a ceiling the range
cannot address is a boot failure at the third node rather than a scheduling one.

The reading for this is a table case: the same observation that decided `Provision, Nodes: 1`
before now decides `Provision, Nodes: <target>` with `MaxNodes` raised, and every existing case at
`MaxNodes == 1` is byte-for-byte what it was. What is NOT yet read is a live cluster holding more
than one node at once - `NEXT.yaml` AUTO-1 stays open for that, and `docs/plans/0002` names the
instrument: three concurrent pods, three joined nodes, and a return to zero.

## 11. Teardown, and the gate

Teardown is gated, because a teardown that races a running job is a lost write, and after rotation
the objects are gone.

**`status.nodeExpiry` decides whether the gate can compare:**

```
absent / unknown     -> REFUSE. A gate that cannot compare must not proceed.
explicit 0           -> PROCEED. There is no expiry to compare against.
non-zero reachable   -> refuse if the gate would outlive it
```

**Conflating absent with `0` makes a deliberately-unlimited cluster undeletable.**

`0` means UNLIMITED. The trade: it removes the only automatic bound on an ORPHAN. Containment then
rests on token-per-instance, the door's session accounting, and our own teardown path - **there is no
timer backstop**. On the local-process mechanism there is no door and no session accounting, so
containment is the teardown path, plus the node record below.

**A producer reports; it never guesses.** The producer surface cannot express an estimate: it returns
a value *and whether it has one*, so "nothing to report" is a bit rather than a sentinel, and a
source that forgets to report errs toward refusing rather than toward proceeding. `0` is the report of
a substrate with NO DOOR and therefore no session cap at all — the local-process mechanism — and it is
a fact about that substrate, not a guess about a door it does not have. That is why absent and `0` are
both reachable and mean opposite things. A guessed deadline that is late reproduces exactly the silent
loss the gate exists to prevent, and a guess is indistinguishable from a report in the schema, so it
would remove the refusal the gate is. The report is written **only when the object carries no value**:
the settled shape of the `Cluster` value is a roll-up over the node CRs, and a second writer that
revises a report is the one direction that loses data. A stale earlier value can only refuse a
teardown; it can never permit one that outlives the truth.

**A gateway restart with live children: reap, never adopt.** Adoption is refused because an adopted
process has two owners, and a node that dies under one of them looks alive to the other. Both seams
enforce it with a record rather than a convention, and neither can mistake a stranger for its own:

```
process group   a group is found by its LEASED PORTS. portscan reads /proc for the processes
                LISTENING on leased ports, attributes one to a cluster only when its command line
                contains every token of a component that cluster's own stack spawns, and reports
                every unattributed holder as a Drop. Survivors is keyed by (cluster, port), so a
                handle can never be filed under a cluster whose argv it never matched, and an empty
                stack set is ErrNoAttribution rather than an empty observation
node            a node is found by its own record. Provision writes <node data dir>/node.json
                (0600) naming the pid, the process start time from /proc/<pid>/stat field 22,
                and the node name; liveness requires BOTH that the start time matches and that
                /proc/<pid>/cmdline names the agent subcommand and this node name
```

A pid-reuse mismatch is reported as NOT OURS and is never signalled. The 0600 mode is not hygiene: the
record names a pid this gateway will signal, and a record another user can write is a record that aims
our reap at a pid of their choosing.

The expected argv is read off the steps `StepsFor` returns — the SAME derivation the spawn path uses,
never a second one, so a stack the running path would not spawn is not one the reap may kill. The
survivors are recorded on the `LocalHost` itself, so the reap and the lifecycle that follows share one
object, and a host that reports live handles it cannot forget is refused: a handle that outlives its
own kill becomes a kill target again the moment the kernel recycles that pid.

The order inside a restart is fixed: reap the recorded survivors, then the group, then bump
`status.instance`, then spawn. The bump is the durable name of the token a group was started with, so
a pass that names the new token before it has taken the group holding the old one is describing a
group that is still running. The bring-up's init timeout parks AFTER the reap, so a parked cluster
cannot leave the owner of its own ports running.

Two cases are handled by name, not by a fallthrough: a cluster whose CR is gone — deleting, OFF or
halted — with a group still running is reaped by the startup pass and logged with the reason it is not
being brought up; a cluster with a running CR and no survivor is brought up from nothing. There is no
third case, because the only alternative to refusing is to spawn over a process that may still be
writing to the store.

**Reap does not reach the store.** `embedded-postgres` is a library child of the gateway, so a restart
leaves a postmaster holding its leased port and `StartPostgres` ADOPTS a serving one — `Drop` reports
it and nothing kills it, by design. The purposes the process group binds are `PurposesFor`'s, and no
purpose in that list is a node's, because a node binds no port of this host's.

**One Host per cluster, for the life of the process.** A node actuator built fresh on each call cannot
scale to zero: `Provision` in one pass and `Teardown` in a later pass act on different objects, so the
teardown finds no handle, stops nothing, and the node keeps running. The composition root memoises one
host per cluster and hands back the same one thereafter, under a mutex because passes may overlap.
Memoisation also holds the cluster's `k3s-secure` lease for the process lifetime, which is what keeps
`status.URL` meaningful after OFF: a port re-leased to another cluster is a silent misroute on this
cluster's next ON.

**A shutdown reaps; it does not kill.** A process group is spawned with `context.WithoutCancel`, so a
gateway's SIGTERM does not `exec`-kill k3s, kine and postgres — which would leave the store needing
crash recovery and bypass the reverse-order teardown and the tenant-side Node delete entirely. The CLI
reaps explicitly on the way out, with a fresh context; what it does not finish is reaped by the next
start, which is the same rule as above.

**Every cluster, not the one this process was told to join.** `SOCIALWEB_CLUSTERPROVIDER_JOIN` names
a single cluster by its bare name and keeps working exactly as it did. `SOCIALWEB_CLUSTERPROVIDER_JOIN=*`
is the mode the k8s door needs, because that door lets any atproto user create a cluster and NOTHING
materialises one unless a process joins it: the provider lists `registry.Refs` — which already walks
root, the accounts, the homes and the clusters — and reconciles EVERY cluster each pass, in SERIES and
one at a time. Serial rather than parallel is the v1 answer and it is stated rather than implied: the
pass is bounded by the interval, the interval is 2 s, and the passes this process runs per cluster are
the same ones the single-cluster loop runs, so what a wider tree costs is latency and not correctness.
A failure in one cluster is caught, logged with the cluster's name, home and error, and the pass moves
on to the next: one cluster that cannot come up must not hold back the others, and an all-clusters pass
that returns on the first error is a tree where every cluster behind the broken one is frozen. The
startup reap and the shutdown reap cover the same set. Startup already walked every `Cluster` CR and
every lease to decide which group to adopt, reap or bring up — that walk was always tree-wide, and only
the JOIN-scoped state dir and the single `Host.Reap` on the way out were not. Shutdown now asks the
`LocalHost` which groups it holds (`Clusters()`) and reaps each, so `*` gets the same reverse-order
teardown, the same node-host reap and the same store stop that a single join gets, and the node hosts
are reaped per cluster rather than only for the joined one.

**One process per host still holds ONE apiserver per store (invariant 1).** Every cluster in the pass
gets its own state dir, its own port leases, its own postgres, its own process group and its own
`Host` entry, and the `Host` is memoised by the same key the state dir is named by, so a pass in one
cluster and a teardown in a later pass act on the same object. Two clusters named `ci` by two
different accounts are two entries in every one of those maps.

**A market claim does not stall the pass.** `tenantNodes.Provision` writes the `NodeClaim` and returns.
It does NOT poll the tenant cluster for the node first, and the distinction is the whole reason the
all-clusters mode is usable: a market machine is absent for minutes, the join wait is 30 s, and one new
claim would otherwise block every other cluster in the tree for the whole timeout, once per cluster,
serially. Joining is confirmed on a LATER pass, by `nodeHosts.confirm`, which runs before that
cluster's scale pass: a node whose tenant Node object has appeared is confirmed through the same
`ConfirmJoined` the guest substrates call inline, and a node whose claim reads `Failed` is torn down
there — the claim released and the tenant Node deleted — so the failure is reported and the cluster may
ask the market again rather than counting a machine it will never get. The guest substrates still
confirm inline, unchanged.

## 12. Code layout

```
api/v1alpha1/          wire types only. No logic, no I/O
internal/${concept}/   one package per concern; the interface declared where consumed
internal/atprotoauth/  verifies an atproto service-auth token for one XRPC method: signature, audience, method, admitted DID methods and a 5-minute lifetime cap
internal/execplugin/   the client-go exec credential plugin: mint, on-disk cache, refuse an empty or already-expired token
internal/nodeprovider/ derives a node's DNS-1123 name from the (cluster label, node id) pair
internal/nodevm/       the substrate-neutral half of a node: its image, guest, network and namespace parameters, the pasta link wiring, the identity disk, the rootfs clone and the /proc identity scan
internal/nodefirecracker/ places a node as a firecracker guest
internal/nodeqemu/     places a node as a qemu guest, behind the same seam
internal/nodemarket/   places a node as a claim on the COMPUTE MARKET: it writes a NodeClaim into the cluster's home workspace and returns, reads the claim's status for the join wait and its fatal message, and closes the node by deleting the claim and waiting for the door to destroy the machine
internal/hostos/       the one platform seam: capability predicates and the portable primitives (locking, process identity, liveness, termination, command line, port range, SysProcAttr) with Linux and Windows implementations
internal/plcresolver/  resolves a did:plc or did:web to its key IDs for the revocation sweep; NXDOMAIN is an outage, not an absence
internal/plcsweep/     the periodic pass that lists DIDs, resolves them and revokes those whose keys are gone
internal/sweepkcp/     the kcp side of that sweep: build the account worklist from workspaces and AtprotoIdentity records, write the deny, plan the fan-out
cmd/${binary}/         the CLI layer; nothing else builds a binary
deploy/                install scripts, manifests, the audit policy
```

Dependencies flow one way: `api` <- `internal` <- `cmd`. A new substrate or transport is a NEW
PACKAGE, not a branch inside an existing one.

**A cluster's key is derived once, from its home workspace.** `kcpclient.ClusterKey` is the only
spelling: it takes a home workspace path `root:<accountHash>:<name>` — `HomePath` builds the one the
door's `Ensure` creates — and returns `<accountHash>-<name>`, which is a DNS-1123 subdomain. Every
place a cluster is identified INSIDE a provider uses it and nothing else: the state dir
(`pdr-<key>`), the node data dir (`<key>/nodes/<node>`), the `Cluster` field of every port and subnet
lease, the node name `nodeprovider.NodeName` derives, the `nodes.json` instance snapshot, the
`scaledriver` state map, the tenant-credential mint file, the `NodeClaim` name and the process-group
attribution in the startup sweep. Both binaries reach it through `internal/kcpclient`: the
clusterprovider from a `Ref` (`Ref.Key()`), the gateway from the `(did, cluster)` pair a mint binding
carries (`ClusterKey(HomePath(did, cluster))`), so the file the clusterprovider writes and the file
the gateway reads are named by one function. The bare name is still what a `Cluster` CR, a `Node`
CR's `spec.cluster` and a `NodeClaim`'s `spec.cluster` carry, because those live inside one home
workspace and are unique there; what the key keeps apart is everything OUTSIDE it.

`cmd/socialweb-clusterprovider/migrate.go` is why an existing single-cluster deployment keeps working.
Before the first pass, and only when the CR tree names a given bare name exactly ONCE, it renames
`<state dir>/pdr-<name>` to `pdr-<key>`, `<state dir>/<name>` to `<key>` (the node records, renamed
with their directory), the mint credential `<mint dir>/<name>` to `<key>`, and re-keys that cluster's
port and subnet leases in place, preserving their ports and ranges. A name two accounts share is left
alone and reported: which home it belongs to is not knowable from the name, and renaming it onto one
of them would hand that cluster the other's data. It is a one-way move — a downgrade finds the keyed
names and starts from an empty state dir — and the cost of the node names changing with the key is
recorded in section 15.

Binaries: `socialweb-clusterprovider`, `socialweb-gateway`, `socialweb-credential`,
`socialweb-nodeimage`, `socialweb-nodeagent` and `socialweb-computer-kcp-check-host`.
`socialweb-nodeimage` and `socialweb-nodeagent` are the node side of the seam:
`socialweb-nodeimage` builds a node image and is a TOOL, never a controller, because provisioning
must not be coupled to a boot plus a bootstrap; `socialweb-nodeagent` runs INSIDE the guest and is
one binary with one verb per stage of a guest's life (`namespace`, `init`, `preflight`, `agent`),
which is what lets a single build serve both sides of the namespace boundary. That tool is also a
BUILD STAGE rather than a step anyone schedules, and the last binary is the host contract itself: it
is what the host and the control-plane container are both measured against, and its answer is what
separates "this host cannot run this" from "this container must be handed this". The substrates are
`internal/nodefirecracker` and `internal/nodeqemu`, sharing the substrate-neutral half in
`internal/nodevm`; the record they reap by is `internal/noderecord`, and the composition
root SELECTS a substrate from configuration rather than branching inside one.

**A node image is named by its inputs, and nothing asks whether it is stale.** The build fingerprints
everything it read — the VMM it is built for, the module list, the initramfs module list, the seeded
image references, and the
kernel, its config file, the base image and the k3s binary by path and by digest, plus the
configuration's own path — and publishes at `<out_dir>/<fingerprint>/` by renaming a staging
directory into that name only once it is whole. So a build killed by a crash, a full disk or a signal
leaves a name nothing asks for, which matters because `Validate` is presence checks only: a
half-written directory used to validate and be consumable. A run asks whether the name its inputs
give EXISTS, which is a fact rather than a comparison — an unchanged configuration is left alone with
no build, no write and no pull, and a changed one asks for a name that does not exist.

**The runtime derives that name too, and is told neither it nor the directory.** The gateway that
places a node is configured with the store and the builder's own configuration —
`SOCIALWEB_CLUSTERPROVIDER_IMAGE_OUT_DIR` and `SOCIALWEB_CLUSTERPROVIDER_IMAGE_CONFIG` — never with a
directory. It hashes the configuration's inputs exactly as the builder did, joins the fingerprint onto
the store, and then holds what it finds at that name to three things: that the manifest's own record
hashes to the name, that the manifest's operative fields and recorded digests match the configuration
(the same `CheckInputs` the substrate already ran), and that the kernel, the initramfs and the rootfs
are there. A directory name an operator computed by hand was the one input to a node's boot that no
build ever wrote, and a stale one is a directory that exists, validates and boots — so a node came up
from an old image with nothing saying so, which is the fingerprint's own failure one layer up, at the
configuration boundary. What is configurable about a node's boot is now the store and the inputs, and
nothing else.

Removal is its
own mode (`-gc`), never a side effect of building: it keeps the name the configuration resolves to,
every `-keep` name, and the newest `-retain` images, reaps a staging directory only when the pid in
its name is gone, and leaves anything not named like its own store. Keep-N rather than a byte cap,
because every image here is one build of one configuration and a count is what an operator can check
by looking.

**The build is a STAGE of the control plane's container build, not a tool someone remembers to run.**
This is what answers "what builds the node image, and when": the same multi-stage build that produces
the control-plane image produces the node image in an earlier stage, so the artefact and the thing that
needs it are built together and neither can be the stale one. The ordering stops being a property of a
procedure and becomes a property of the build graph — a stage whose inputs did not change is not
re-run, and a stage whose inputs changed cannot be skipped, which is the same question `Fingerprint`
answers by hand. The two are not redundant and neither may be deleted: the build system holds the
ORDER, and the fingerprint is what the RUNTIME uses when it asks whether the files lying on a given
host are the ones this build produced — one fact with two readers, the same shape as the builder's
`VerifyModules` and the guest's own module check. What the stage bakes is the **kernel, the rootfs, the
containerd/runc/k3s-agent binaries, the module tree and the seeded image set**: plain portable files,
with no snapshot anywhere in it, because snapshot restore is refuted as the provisioning primitive
(section 9) and a node boots cold from its own clone of the rootfs. So the baked artefact is coupled
neither to the building host's CPU nor to a firecracker version, and reaching the host is files landing
at paths rather than a restore. Those paths are the container's edge, and the binary below is what
states them: `socialweb-computer-kcp-check-host` runs the host contract in both places, so the same
checks that say "this host can run the host stack" also say what a container is missing and what it
must be handed.

**Every seeded image is pinned as `<tag>@<sha256:...>`, and the tag is not decoration.** A tag is not
an identity: a registry can be pushed to at any time, and when it is, a tag-only entry is unchanged,
so the name is unchanged, and the store serves the image the tag *used to* mean — a node runs a
previous release's images under the current name, and nothing in the cluster says so, because a
cluster whose images are all one release old is as internally consistent as one that is current.
Recording the digest makes it an input, so a re-pushed tag produces a second image instead of
silently serving the first. The tag must stay in the entry because containerd matches a pod's request
against the tag in its store, and a digest with no tag seeds an image no pod ever asks for — and in a
guest with no resolver that falls through to a pull that waits rather than fails. The builder refuses
a bare tag and hands back the exact entry to paste, resolving it to a digest with two manifest GETs
and no layer bytes, before any pull, so a bad entry costs a refusal rather than 2.3 GB.

**The name follows the file, not the spelling.** The configuration path and every recorded input are
symlink-resolved before hashing, on both sides of the substrate's comparison, because one file
reached two ways is one file and a store that follows the spelling holds two copies of one 2.3 GB
image. A path that cannot be resolved is refused rather than recorded as spelled — an unresolvable
input is the one case where a stale image and a current one look identical. An input that is merely
ABSENT is not a resolution failure: it still records its canonical spelling and fails later where
"cannot tell" already lives.

That input is covered now, and the shape of the coverage is the point: the record weighs the module
tree the build copies into the image — every file's path and contents, and every symlink's target — and
not only the names read out of it, so pointing `module_source` at another host's tree of the same
kernel version moves the image's name. It was the second of two inputs the record did not mention, and
section 15 records what closed both and what held the family shut.

`internal/versions/versions.json` pins component versions. It has no production importer: its
consumer is its own test.

### The two node images, and the self-contained live rig

**The container build produces one node image per VMM.** `deploy/control-plane-container/Dockerfile`
has a `nodeimage` stage (firecracker) and a `nodeimage-qemu` stage (qemu), each parameterized by its
own configuration: the firecracker stage reads `nodeimage.json`, the qemu stage
`nodeimage-qemu.json`. Because the VMM is a recorded input, the two images have different
fingerprints and coexist in the store; the `extract` stage copies both stages' `/dist` into one root,
so the `computer` image carries both images under `/var/lib/socialweb/images` and both runtime
configurations at `/home/johnandersen777/kcp-image/`. `build.sh`'s `nodeimage` and `nodeimage-qemu`
targets mean exactly the stage they name, `nodeimages` builds the pair through `extract`, and
`computer` builds all of it because `extract` depends on both. Before this the qemu image existed only
as a host-built artefact; the container build is now the thing that produces it, which is the same
statement the firecracker half already made. MEASURED 2026-09-25: `build.sh computer` built the
firecracker image at `sha256-6f0163f5b34f4b7c793709cdbb90c74a9df700466f9d8ed6c9dedb2569319e3e` and the
qemu image at `sha256-e356da745efe1dab4b6c233045f33b491cc7cfe0851fdaa5856501991a565afd` in the same
run, 122 s wall.

**The live gate has a self-contained, privileged, ephemeral CI-only rig, and nothing but the rig
takes the relaxed flags.** `deploy/test-rig/run.sh` builds the `computer` image, starts it through the
product's own `container-entrypoint.sh`, waits for the cluster provider's own `"phase":"Ready"` line,
and then runs `SOCIALWEB_REQUIRE_LIVE=1 go test ./...` inside it through `deploy/test-rig/inside.sh`.
The rig derives the discoverable inputs instead of hand-exporting them: `cmd/socialweb-rig-inputs`
reuses `internal/nodefirecracker.LoadImage` and the builder's own configuration parsing to resolve the
firecracker and qemu image directories, the agent, the store and the two configurations, and it picks
the tenant image out of the seeded set; `internal/hostos` is what reports the platform. The kcp admin
kubeconfig, the mint directory and the default cluster name come from the running product. The inputs
the product cannot derive are named before the full gate fails, each with the test it gates: a tenant
cluster's admin kubeconfig, DID, registry workspace and namespace, and the granted ServiceAccount. An
operator can run a named subset for a partial proof; the default is the whole gate and a skip is a
failure.

**Why the relaxation is confined to the rig, and what was measured.** A guest needs `/dev/kvm`,
`/dev/net/tun` and a user and network namespace to build. Docker's default seccomp profile denies the
`unshare` that the namespace wiring performs, so the rig's `docker run` line carries
`--device=/dev/kvm --device=/dev/net/tun --security-opt seccomp=unconfined --security-opt
apparmor=unconfined` and NO added capability: the tap and bridge are created INSIDE the namespace
pasta hands the agent, where the process is root-mapped and holds its own capability set, so
`NET_ADMIN` and `SYS_ADMIN` are not needed. MEASURED 2026-09-25: with exactly that minimal set and no
capability, `TestALiveQEMUGuestBootsAndIsReaped` passed in 0.93 s and
`TestLiveTheGuestsGatewayIsAnsweredOnItsOwnLink` (firecracker) passed in 2.47 s, each with the product
stack Ready in ~30 s. A privileged container can escape to the host, so the flags live only on the
rig's command line: the shipped image, the shipped `container-entrypoint.sh` and
`deploy/control-plane-container/validate.sh` are unchanged. The strict control-plane proof keeps
running as non-root with no device, no capability and no host network, documented where it always was
(section 9's container build and the check-host contract); MEASURED 2026-09-25,
`validate.sh` against this image still printed `PROOF: --- PASS:
TestTheControlPlaneReachesReadyAtZeroNodes (8.08s)`. The rig is a test harness, not a deployment path.

**The irreducible inputs.** A node image and a live boot cannot be made from the tree alone. The guest
boots this host's own kernel, so `/boot/vmlinuz-$(uname -r)`, `/boot/config-$(uname -r)` and
`/lib/modules/$(uname -r)` are read as they are, and the base userspace is
`artifacts/img/ubuntu-24.04.squashfs`; `build.sh` refuses to run without any of them rather than
inventing one. A live boot adds `/dev/kvm`. The rig derives everything else, and `build.sh`'s refusal
is what keeps a missing kernel, module tree or base image from becoming an image that silently boots
the wrong userspace.

## 13. Invariants

A change must not break any of these. Each exists because something failed.

1. **One apiserver per store.** kine's revision cannot tolerate a second writer.
2. **`--disable-agent`.** Without it "zero nodes" is not a state.
3. **The token never rides argv.** k3s derives its bootstrap key from it; `/proc` is world-readable.
4. **The identity bundle is exactly 13 entries.** A short list makes k3s regenerate CAs and
   invalidate every credential.
5. **`--datastore-endpoint` is required and non-empty.** Empty is a silent new cluster.
6. **Audit flags ride `--kube-apiserver-arg`.** Direct flags are skipped with a warning, leaving the
   cluster silently unaudited.
7. **The advertise port comes from a lease.** A colliding one points a node at another cluster's
   apiserver and reads as an x509 error.
8. **A node's ports are its own, and no lease of the process group covers one.** The kubelet's is
   bound inside the guest's own network namespace, under its own kernel, and no port it binds is
   published on this host. So a node holds no lease to give back: a node's teardown releases nothing,
   and the cluster's own leases — its `k3s-secure` port and the range its guests are addressed in —
   outlive it, because `status.URL` stays live after OFF. This replaces "the kubelet lease is released
   on the NODE's stop, and is not in `DefaultPurposes`", whose purpose is retired: there is nothing
   left to release and nothing left to keep out of a list.
9. **Absent `nodeExpiry` refuses teardown; explicit `0` proceeds.**
10. **Nothing is read back out of a node.** The token and the name are ours.
11. **Never request-header auth against the mount.** The mount authenticates nothing.
12. **A revocation needs a consultable deny record**, not just a deleted object.
13. **Secrets never ride argv or `user_data`.** `user_data` is readable from inside the guest.
14. **A list failure is an error, never an empty observation.**
15. **The state advances only on success.**
16. **No comments in code.** Reasoning belongs in this document; `test/nocomments_test.go`'s
    `TestNoCommentsAndASCII` fails on any comment that is not a `//go:` directive or a `ponytail:`
    group, and on any byte above 0x7F.
17. **A gate must be able to fail.** A check whose failure and success look alike is not a check, and
    a probe that cannot fail proves nothing.
18. **The substrate seam does not own the lifecycle.** Closing a node must not be the same act as
    destroying it, or the teardown semantics stop being observable.
19. **A child's output is drained by the runner, never through a pipe a caller may forget.** A pipe
    whose reader never reads blocks the child once the kernel buffer fills, and the symptom is a
    readiness timeout over a healthy process. Keep a bounded, evicting tail, never an open stream.
20. **The provider's tenant credential is minted, never copied.** It is a client certificate signed
    by that cluster's ClientCA and valid only against that cluster. The k3s-written admin kubeconfig
    is never read: it is shared state on a multi-cluster host, and its server name is unverified
    against the address a node dials.
21. **A process group is not killed by its spawn context.** Teardown is the explicit reap, in reverse
    order, and it is what stops the guest and deletes the tenant-side Node object. A group killed by
    the gateway's own SIGTERM bypasses both and leaves a store needing crash recovery.
22. **A workspace ServiceAccount does not leave its workspace.** Its token's scope confines it to the
    logical cluster it was minted in and kcp refuses it everywhere else before RBAC is consulted, so a
    ClusterRoleBinding naming it elsewhere grants nothing and no subject string changes that. Any
    grant whose effect is expected in another workspace is mis-designed, not mis-spelled — which is
    why the runtime holds a credential kcp does not confine.
23. **A spawn failure says why.** The reconciler carried the op and dropped the error, so a process
    group that never came up read as "the cluster is Initializing" and the readiness gate's own
    reason — and the component log under the cluster's data dir — were the only account of it.
24. **A snapshot is a template, never an instance.** Every node boots its own clone of the rootfs.
    One writable image serving as both carries a guest's writes into the next boot, and it presents
    as a corrupted image rather than as shared state; two live VMs on one file corrupt each other.
25. **A boot command line is a console.** Anything on it is written to the guest's console at least
    twice — firecracker logs the body of its own boot-source request, and the kernel prints
    `Kernel command line:` — so no secret may ride it. The per-node secret travels on a per-node
    read-only disk that exists for one node and is destroyed with it.
26. **The record's identity is supplied by the substrate, and an empty identity matches nothing.**
    Matching every token present, read literally, would match every process on the host. A pid whose
    identity does not match is NOT OURS: a reuse stays a refusal and never becomes a kill.
27. **A guest's own account of its own elapsed time is not evidence.** Time a guest by the host's
    clock. A reported 332-second boot was a literal `echo` in an init script, printed 0.29 s after
    the VM started, and it sent this work after a 30x non-issue.
28. **A failure already visible in a guest's console is not waited for.** Named fatal markers end the
    wait immediately, with the guest's own words. A timeout that runs to its end over a host that
    died at its first step reports the timeout instead of the cause, and the two are indistinguishable
    until the clock runs out.
29. **A market node's secrets cross only the SSH session's stdin.** The join token, the per-node
    password and the cluster's server CA reach a market guest as the door writes them to the standard
    input of the session it runs the join program on. They never ride the claim: a `NodeClaim` is a
    cluster-scoped object in the owner's home workspace, readable by anything that can read that
    workspace, and it carries the PATHS the door reads them from instead. They also never ride argv,
    `user_data` or a boot command line, for the reasons invariants 13 and 25 already give.
30. **A cluster is its home workspace, not the name inside it.** Two accounts may each name a cluster
    `ci`; they are two clusters, with two state dirs, two sets of port and subnet leases, two sets of
    node names, two mint credentials and two process groups. Anything that identifies a cluster
    OUTSIDE its home — a state dir, a lease, a node name, a snapshot, a mint file, a process group —
    carries the key `kcpclient.ClusterKey(home)` = `<accountHash>-<name>` and never the bare name. A
    pass over one cluster must never key anything by a name two workspaces can share, because the
    failure mode is not a collision error: it is one cluster reading another's leases and state dir
    and reporting them as its own.
31. **A claim does not wait for the machine.** Provisioning a market node writes the `NodeClaim` and
    returns; the join is confirmed on a later pass. A blocking wait inside a pass that serves every
    cluster in the tree stops being a local delay and becomes a global one, and the thing being waited
    for is minutes away by construction.

32. **A node is counted by the cluster that has it, never by the claim that asked for it.** The count a
    scale decision reads is the tenant cluster's own node list, plus - under a substrate whose
    `Provision` returns before the machine exists - the claims this process holds whose nodes that list
    does not name. A claim's phase is not a count: the door writes `Running` when the
    node's agent reaches the apiserver through the forward, and the Node object it is registering
    appears in the cluster's list only after that connection is served, so a phase-keyed count has a
    gap with a duration in it and every claim that crosses the gap together is counted nowhere at all
    (section 15).

## 14. Open architectural gaps

These are the things the architecture does not yet answer, and this list is the work. What is settled - a
decision taken, a defect closed, a measurement recorded - is in section 15 instead, and the two are
separated deliberately: a work list padded with history reads as complete, which is how a bullet that was
already closed, or one whose subject the design has since deleted, survives re-reading.

On 2026-09-24 this section listed eleven open items and the batch closed every one of them. Ten went to
section 15 with their before-and-after readings; the eleventh, REL-7, closed by an experiment whose
answer split a narrower window out of it, and that window was PASTA-1, now closed in section 15 with the
rest. On 2026-09-25 a privileged rig run and the vault and ESO work opened five more. One of the five,
CHK-1, was refuted by measurement before any code changed -- the check reads HOLDS on the host and in
the product image, and the MISSING line came from a build stage that needs no qemu -- so four were
listed below, and a claim is not an item until it has been run. VAULT-3 has since closed and moved to
section 15, leaving three: the vault's own store line was the one the design already answered, so it
was implemented rather than left open. VAULT-2 has since closed too: both vaults now speak TLS, and the
root vault's listener authenticates its clients by certificate. VAULT-1 closed with it, having found
that its own premise named a file `kcp` does not write. RIG-6 was opened and closed in the same window,
by a rig run that tested a tree moving under it. On 2026-10-05 five more were opened by the GitLab and
cloud-controller-manager work: CCM-1 and AUTO-1 were opened and CLOSED inside the same window and are
in section 15 with their readings, while ESO-2 remains and FLAKE-1 joins it. GL-1 and GL-2 have since
closed and are in section 15 too. Each was
written from the code rather than from the request that produced it, and each names its instrument.
On 2026-10-06 MARKET-2 was opened and CLOSED inside the same window, by the k8s door's tenant
credential work, and is in section 15 with its readings.
**The live backlog is `NEXT.yaml`, which this section mirrors:
an item is written here when it is opened and moves to section 15, with its readings, when it is
closed.** The practice that produced the batch is the one worth keeping: the artefact was run and each
guard was watched to fail, rather than the code read against this document alone.

An entry belongs here when the design does not answer a question the code already asks, when a reading
the architecture depends on has no instrument, or when a settled decision turns out to have been
recorded only in a comment. Every entry closed so far was found by reading the code against this
document, or by running it, rather than by reading the document. What remains at the end, after the
open list, are two stated LIMITS rather than open work: each marks where a behaviour that passes stops
being true, which is a fact about the design rather than a question left in it, and each sits here
because it bounds a mechanism the rest of this document leans on.

- **Whether the node half may ever run inside the control-plane container was answered by reading where
  the refusal lives, and the answer is a STATED LIMIT rather than an open question.** The allocator's
  refusal to lease a range overlapping the host's networks is a property of the NETWORK NAMESPACE the
  process runs in — the host enumerates 15 networks, the container 3 (`127.0.0.0/8`, `172.17.0.0/16`,
  `::1`) — so a containerized allocator would lease a colliding range without refusing. What bounds it
  is that the allocator and the node substrate are ONE PROCESS behind one config value:
  `subnetallocator` is built once at `cmd/socialweb-clusterprovider/main.go:107` and both of its reach
  sites — `guestGateway` (`main.go:36`, installed only `if substrate.IsGuest()`) and the node path
  (`nodes.go:453`, inside `tenantNodes.Provision`) — sit behind `substrate.IsGuest()`, which is true
  for BOTH guest substrates (`Name == SubstrateFirecracker || Name == SubstrateQEMU`). Either value's
  own validation demands its own VMM binary, the node
  agent, the image out dir and the image config (`config.go`), i.e. exactly the list the container
  declines as MUST BE PASSED IN. So a container that can REACH the refusal is a container doing the node
  half, and a control plane that places no nodes never allocates a range at all — the zero-node run's
  ops end at `[allocate-ports spawn]`, with no gateway step. Two things are recorded with it rather than
  left latent: the limit is CONDITIONAL, costing nothing only because this host's LAN and bridges sit
  outside `10.100.0.0/16`, so a host network or docker network carrying part of the pool would let a
  containerized allocator lease a colliding range and the collision lands on the HOST, because pasta
  builds the guest's namespace from the host's network; and the seam to state it already exists, since
  `subnetallocator.Config.HostNetworks` is injectable, so a containerized deployment can be handed the
  host's enumeration without touching the package.
- **Two concurrent proofs in two containers are not protected from each other, and no production path
  depends on that.** `liveports.LockDir()` is `/var/tmp/socialweb-liveports` inside each container's own
  filesystem, so a slot one run claims is invisible to a run in another container and both would take
  the same ports. The bound is the same shape as the one above: `internal/liveports` has NO production
  user — it is imported only by `*_test.go` files and by the host-contract binary's probe — so the
  container is a place two proofs collide only when the suite runs there, which is what
  `deploy/control-plane-container/validate.sh` does. Two runs inside ONE container see each other,
  because they share one `/var/tmp`. Two containers must share that path or be serialized, and nothing
  outside the test suite turns on which.
## 15. Settled decisions, and the defects they closed

Not gaps. Each entry was open once, and each says what settled it and what the settlement was — and,
where it matters, that it was settled AFTER the entry was written. That timing is kept because it is the
case nobody re-reads: a gap still listed as open reads as honest incompleteness, which is why a bullet
can outlive the work that closed it, and why one whose subject has been deleted can sit in a list of open
questions for hours without anyone stopping on it. The measurements stay with the entries rather than
being tidied away, because a number without its condition is how the 332-second boot outlived its own
refutation.

- **A cluster's kubeconfig mount child is built from the Cluster CR's `status.URL`, and it is created
  or repaired on every Ensure. CLOSED, and it closed a mount that served kcp itself.** Before,
  `kcpclient.Ensure` POSTed the Cluster CR and then POSTed the mount child at `MountPath` back to
  back, both without reading anything the CR reports. kcp's `workspaceSpecUpdater`
  (`kcp/pkg/reconciler/tenancy/workspacemounts/workspacemounts_reconcile_updater.go`) copies the
  MOUNTED OBJECT's `status.URL` into the workspace's `spec.URL`, and returns
  `reconcileStatusStopAndRequeue` with `unable to read .status.URL` when the object has none; a mount
  workspace's own `status.URL` is empty by design. The CR's `status.URL` is written later, by the
  clusterprovider's `Joined` (`internal/clusterprovider/provider.go`: `o.Cluster.Status.URL = at.URL`),
  so a child created in that gap inherited nothing. The requeue is rate limited by the controller
  (`c.queue.AddRateLimited(key)`), so the healing pass recedes rather than arriving promptly, and in
  production it did not arrive at all in the observation window. kcp then served the mount ITSELF:
  `kcp-admin` was accepted, `kubectl get nodes` returned the placeholder Node the provider wrote, and
  every object on the real tenant apiserver was invisible. Downstream, the SPA/runner kubeconfig
  showed an empty cluster and the clusterprovider re-claimed a node from the market every 2 s because
  it never saw the join. MEASURED in production on `root@socialweb.computer` (ssh port 1997):
  `kubectl --kubeconfig=/var/lib/socialweb/kcp/admin.kubeconfig
  --server=https://127.0.0.1:6443/clusters/root:<account>:<cluster> get workspace clusters -o
  jsonpath={.spec.URL}` was empty on the original child; after deleting and recreating that child by
  hand the same read gave `https://127.0.0.1:20003`, and traffic then reached the tenant apiserver,
  which began asking for credentials -- the tenant k3s refusing kcp's admin token, the correct
  refusal. After, `Ensure` calls `ensureMountChild` (`internal/kcpclient/kcpclient.go`), which builds
  the child from the URL and never from nothing: `createCluster` now reports whether it created the
  CR; if it did and the CR reports no `status.URL`, a bounded wait (`clusterURLTimeout`, 30 s, the
  budget `workspaceReadyTimeout` already spends in the same function) re-reads the CR until a URL
  appears; when it gives up, the child is NOT created and `Ensure` returns nil, leaving the job to a
  later `Ensure`. If the CR already existed its URL is read once and nothing is waited for, so the
  budget is paid only by the `Ensure` that itself created the CR, and a later one that finds the CR
  still without a URL costs a single GET. Either way a missing child is CREATED with `spec.URL` set,
  and an existing child whose `spec.URL` is empty is PATCHed with the CR's current `status.URL`; a
  child that already carries one is left alone. THE ORDERING DECISION: a cluster whose node has not
  started legitimately has no `status.URL` -- a market join is minutes away by construction, section
  13 invariant 31 -- so the give-up is a DEFERRAL and not a refusal, which is safe precisely because
  `Ensure` runs again for a live cluster (the door's `listClusters` and `getKubeconfig` both reach it
  through `gateway.Server.create`), so a hard refusal would refuse every first login whose node has
  not joined yet. Instruments, all in `internal/kcpclient/kcpclient_test.go`
  unless named: `TestEachEnsureLeavesTheMountChildAddressingTheTenantApiserver`, four subtests that
  pin the repair (an existing child with an empty `spec.URL` is PATCHed from the CR's `status.URL` and
  is NOT re-created), the create (a child built after the CR reports a URL carries it),
  idempotence (across two Ensures the child is POSTed exactly once), and the deferral (a CR reporting
  no URL leaves no child, and the CR reporting one on the next Ensure produces it); all four subtests
  and `TestEnsureCreatesTheTree` were watched to FAIL when `ensureMountChild` was replaced by the old
  unconditional create, so the readings are the change and not the ambient result. NOT VERIFIED
  against production: the deployment was inspected read-only, and this is a source change the owner
  deploys. The two live-gated mount proofs (`internal/kcpclient/mount_child_live_test.go`,
  `internal/clusterprovider/live_mount_test.go`) call `Ensure` once while the CR's `status.URL` is
  still empty, so they now need a second `Ensure` after the CR reports one; they were not run, and
  neither was `deploy/check.sh --local`. `NEXT.yaml` MOUNT-1.

- **A market cluster's tenant credential is signed from its own ClientCA, and the choice of source
  belongs to the substrate. CLOSED, and it closed the k8s door.** Before, `MintTenantCredentialForDataDir`
  chose its source from `ReadVaultSeal(dataDir)` alone: a port and a root token meant the vault, and
  nothing asked where the cluster's nodes ran. A market cluster therefore minted its tenant credential
  through its own custody vault over TLS on every clusterprovider pass, and the tenant never
  materialised. MEASURED in production, container `socialweb-k8s` on `root@socialweb.computer`, on
  every 2 s pass: `cluster provider: the pass failed ... no tenant credential for
  test-cluster-alice-0001: the tenant credential could not be signed: issuing the tenant credential
  from the vault at https://127.0.0.1:20001: openbao: transport failure: POST
  /v1/pki/issue/tenant-superuser: tls: failed to verify certificate: x509: certificate signed by
  unknown authority (possibly because of "x509: ECDSA verification failure" while trying to verify
  candidate authority certificate "socialweb-vault-tls-ca")`. The scale driver observes the tenant
  through this credential, so with the pass failing no tenant was ever observed, a pending pod wrote
  no NodeClaim, and no node was rented. The material the credential needed was on disk the whole time:
  `server/tls/{client-ca.crt,client-ca.key,server-ca.crt}` exist for that cluster, and the provider
  wrote them. After, `CredentialSource` is an explicit argument to `MintTenantCredentialForCluster`
  and `MintTenantCredentialForDataDir`, `cmd/socialweb-clusterprovider/main.go` derives it from the
  substrate, and an unnamed source is refused rather than defaulted. `market` signs from the ClientCA
  and never opens the vault; `firecracker` and `qemu` are unchanged and still reach for the vault when
  a seal exists. The refusal-not-fallback rule is unchanged on both paths. Instruments, all in
  `internal/clusterprovider/tenantcredential_test.go` unless named: `TestTheCredentialSourceFollowsTheSubstrate`
  (`cmd/socialweb-clusterprovider/market_test.go`) reads the derivation for all three substrates;
  `TestTheMarketSourceSignsFromTheClusterCAAndNeverReachesForTheSeal` seeds a ClientCA beside a seal
  whose port nothing answers at, and it was watched to FAIL with the production error shape above
  (`the tenant credential could not be signed: issuing the tenant credential from the vault at
  https://127.0.0.1:<port>: openbao: transport failure: POST /v1/pki/issue/tenant-superuser: ...
  connect: connection refused`) when the market source was made to fall through to the vault, so the
  reading is the change and not the ambient result; `TestTheMarketSourceRefusesWithNoClientCAWhateverTheSealSays`,
  which refuses a ClientCA-less data dir with the seal present and again with it removed and requires
  the two refusals to be the same one, so the seal is shown to change nothing; and
  `TestTheCredentialSourceMustBeNamed`. The guest half is `TestTheVaultIssuedTenantCredentialChainsToTheImportedClientCA`,
  unchanged in what it proves and now naming `CredentialSourceVaultIfSealed`. NOT VERIFIED against the
  production deployment: the container was inspected read-only, and this is a source change the owner
  deploys. `NEXT.yaml` MARKET-2.

- **The live gate now runs end to end in the rig, and it derives the tenant it runs against. CLOSED,
  and the derivation is what closed it.** `deploy/test-rig/inside.sh` used to refuse before the gate
  because eight inputs "the product cannot derive" were unset; that premise was wrong. The container
  provisions the tenant those inputs describe, so `cmd/socialweb-rig-inputs` now reads them out of the
  running product: it lists the account workspaces and Cluster CRs through the kcp admin kubeconfig at
  `SOCIALWEB_KCP_KUBECONFIG` and takes the account DID and the cluster name from the CR, the tenant
  admin kubeconfig from `clusterprovider.ClusterDataDir(stateDir, cluster)/server/cred/admin.kubeconfig`,
  the registry workspace from `SOCIALWEB_GATEWAY_DEVKEY_REGISTRY` and the registry namespace the gateway
  itself uses (`default`), and the granted `did-*` ServiceAccount from the tenant grant's token rule.
  Nothing is fabricated: a value whose source is absent is left unset and the preflight names it. The
  rig also removes its container and volume on exit now, including on failure and interrupt, with
  `KCP_RIG_KEEP=1` to keep them for debugging; before this it leaked `socialweb-rig` for eleven hours.
  Running it exposed four real defects, each fixed at the root rather than in the test:
  (1) `container-entrypoint.sh` named the account workspace and the home independently of the DID, so
  the cluster it created at `root:socialweb:live` was unreachable at the product's canonical path
  `AccountHash(did):cluster` and carried no deny record -- `kcpclient.HomePath`, the gateway's whole
  read path and the live proofs all use that path. It now names the account workspace `AccountHash(did)`
  and the home the cluster, writes the account's `AtprotoIdentity`, and defaults the DID to
  `did:web:example.com`, the DID the shipped tenant grant scopes, so the account's ServiceAccount and
  the grant's `resourceNames` are one name.
  (2) the gateway's PLC sweep revoked the very account the deployment had just provisioned -- a `did:web`
  whose document 404s reads "the account is gone" -- so the bootstrapped record is marked
  `socialweb.computer/sweep: exempt` and `sweepkcp`'s worklist skips it. Pinned by
  `TestASweepExemptIdentityIsNotSwept`, which failed before the skip existed.
  (3) the tenant grant named a `did-*` ServiceAccount nothing created, so the mint proof's TokenRequest
  404'd; `clusterprovider.Bootstrap` now creates the ServiceAccounts the grant's token rule names,
  pinned by `TestBootstrapEnsuresTheServiceAccountsTheGrantNames`.
  (4) `portallocator`'s cross-process helper was also a top-level `Test` that skipped when it was not the
  child, and a skip is not a pass, so the gate failed on it; the child body moved into `TestMain`, which
  prints no skip.
  And one live proof was unrunnable as written: `TestTheNotFoundDiscriminatorAgainstALiveApiserver`
  drove the tenant through the kcp mount with the kcp admin kubeconfig, which the mount does not
  authenticate (the mount forwards a BEARER header to the tenant and nothing else), and it hardcoded a
  `did`/`cluster` no run provisioned. It now takes the deployment's derived DID and cluster and issues a
  tenant bearer through a short-lived probe ServiceAccount and token Secret, then deletes the
  DID-derived SA twice so the 404 discriminator is exercised for real. MEASURED 2026-09-25 on this host,
  16 cores, `deploy/test-rig/run.sh`: the product stack reached Ready in 30 s; the gate command
  `SOCIALWEB_REQUIRE_LIVE=1 go test ./... -count=1 -v` inside the rig printed **736 PASS, 0 FAIL, 0 SKIP**
  and `PROOF: the live gate passed with no skips`. Per test: the four-step proof PASS in 234.18 s
  (firecracker 116.41 s, `INSTANCE_START_TO_READY_S=5.452`; qemu 117.77 s, `=7.424`), the subnet proof
  PASS 5.531 s (alpha) and 5.220 s (beta), the live firecracker join PASS 16.03 s, the audit PASS
  32.67 s, the gateway-answered-on-its-own-link PASS 2.68 s, the system-pods PASS 116.54 s, the live
  qemu boot-and-reap PASS 2.10 s, the mount-shutdown PASS 32.03 s, and every vault and ESO test PASS.
  The four derived-tenant proofs PASS: the renewal path 0.08 s, the mint credential 0.01 s, the live
  bootstrap 0.02 s, the mount child 0.03 s. `deploy/check.sh --full` on the HOST still cannot pass and
  this is not a defect: the live artefacts (the kcp admin kubeconfig, the tenant kubeconfig and the
  node images) live in the container's state volume, so the rig is the gate and `check.sh` is the
  host-loop.

- **The advertise address is an ALLOCATED value, and this entry was wrong twice. WRONG, not open.** It
  asked whether the substrate's advertise address is "a tap/bridge address created once by the operator
  or an existing host address", and what it costs in exposure. There is no such substrate to choose a
  value for: `firecracker` is the only name `nodeSubstrateFor` builds and every other name is refused,
  and `--advertise-address` is REFUSED outright for a guest rather than accepted and ignored, because it
  cannot be in force. The value is the first usable address of the range the cluster holds, allocated
  from `internal/subnetallocator` — no flag, environment variable or config file can pick a cluster's
  range, because a range two operators can set differently is the same collision in a tidier place. Its
  second claim is refuted by the code too: `--bind-address` does NOT equal the advertise address. It is
  pinned to loopback, and the readiness gate dials the address the component was told to LISTEN on, so
  a guest-reachable advertise address is not a guest-reachable listener and the exposure question is
  moot. That is what the entry was reaching for when it recorded the rule; the rule is now the guest's
  own and INVERTED, because a guest reaches NONE of this host's addresses, so `AssertGuestDialAddress`
  refuses an address this host carries. What survives from the entry is its measurement: the deleted VM
  substrate owned a constant, `192.168.5.2`, which this host does not carry and which nothing answers,
  and the store DSN built from it never connected. The one measurement still live is the apiserver
  rejecting a loopback in-cluster `kubernetes` Service endpoint — `subsets[0].addresses[0].ip: Invalid
  value: "127.0.0.1": may not be in the loopback range` — and the leased gateway is the address that
  satisfies it.
- **`nodeExpiry` has a producer at BOTH ends now. CLOSED — and it was closed after this entry was
  written, which is why it is here rather than in section 14.** All three pieces the entry called
  unwritten were written later the same evening it was written: `Registry.WriteNode` now carries a whole
  `v1alpha1.NodeStatus` rather than a phase, so the per-node report has a channel; the node recorder
  writes `Node.status.nodeExpiry` from the substrate's own report, and a substrate with no door reports
  an explicit `0`, so a local cluster is deletable; and the `Cluster` value IS the roll-up — the minimum
  over live nodes with `0` counting as infinity, `Draining` nodes skipped, the empty set as `0`, one
  live node absent as absent — performed by `NodeExpiryRollup`, wired as `Options.NodeExpirySource` and
  re-derived every pass rather than only when a node changes. The entry's last sentence is the part that
  is still true: the door's own report was already parsed (`internal/sessionreport`), so what remained
  was the two writes — and `sessionreport`, that parser, was reached by nothing, which is why it is
  DELETED rather than wired (this section, the unwired controls). The door stays out of scope (section
  16), so its report surface has no parser and needs none.
- **The identity bundle is seeded and harvested at `<DataDir>/server`, and the fingerprint survives a
  rebuild — measured.** `identitybundle.Harvest`/`Seed` are called from
  `clusterprovider.HarvestBundle`/`SeedBundle`, both rooted at `<DataDir>/server` — the same root
  `clusterstack.TLSDir` puts `tls/` under — so a seed lands where k3s looks. `Seed` runs from the
  Identity guard, which rides the step BEFORE k3s and only when the datastore already holds a
  bootstrap row; `Harvest` runs after k3s's Ready gate. `bundle_test.go` pins the root itself: seed,
  then assert every entry sits at `clusterstack.TLSDir(dataDir)`.

  This bullet replaces one claiming the bundle "has no persistence", which was not merely stale but
  **self-contradicting**: the same file already listed `Harvest`/`Seed` as wired, and the claim was
  added in the very commit that added `bundle.go` and both callers. A gap list can be wrong in the
  CLOSING direction, and that one was — which is why this section is now re-read against the code
  rather than trusted.

  Measured on the process substrate, one uninterrupted test, three runs: cold (fresh store and fresh
  dir) **4.97-6.98 s**, pre-seeded (warm store, `server/` wiped, bundle seeded) **1.134-1.139 s**, and
  a CONTROL rebuild with the bundle removed, which must author its own CAs, **1.128-1.378 s**. The CA
  fingerprint was byte-identical across a teardown and rebuild in every run; the control's was not.
  The bundle's value is the identity it preserves, not a time saving — see the timing block in
  section 9 for the attribution this refutes.
- **`status.URL` is written while `Initializing`, and that is LOAD-BEARING, not sloppy.** The URL is
  deterministic from the lease, so the first pass publishes it with the intent; the provider's next
  pass PROBES that URL to decide `Reachable`, and a URL withheld until the apiserver answered would
  mean nothing to probe and a cluster that never leaves `Initializing`. So the two fields must be read
  together and neither is redundant: **`status.URL` says where it will be, `status.phase` says whether
  it is there.** The hazard is real but belongs to the mount, not to this writer: kcp's mount gate
  reads the phase (above), so a loosened gate would expose an unprobed port — which is the argument
  for keeping the gate rather than for withholding the URL.
- **`Joined` remains test-only.** Deliberately: with the group live the second pass's `on()` path
  reaches `claimReady` through the decider, so this substrate needs no external join to answer. The
  external-join path is reachable only from `provider_test.go`.
- **A guest JOINS, so time-to-usable-node is measured: 7.4, 10.6 and 9.5 s.** A k3s agent inside a
  firecracker guest registers as a Ready node against the host's real `k3s server --disable-agent`,
  host-timed from `InstanceStart` (section 9). The two blockers this entry used to attribute to "the
  prototype harness's own" were NOT harness bugs, and the correction matters: one was an environment
  fact (`pasta`'s child inherits the `pasta_t` domain, which denies CAP_NET_ADMIN and silently
  discards the error text through a pipe it cannot write) and one was an architecture fact (one
  writable rootfs serving as both template and instance, which persisted a guest's writes into the
  next boot). Both are fixed, and both would have recurred in production because neither was in the
  harness. **The composition-root join is now established too**, and it is a test rather than a
  scripted run: `cmd/socialweb-clusterprovider/live_fourstep_test.go` holds
  `TestTheFourStepProofRunsThroughTheCompositionRoot`, which lives in the package that owns the
  composition root, takes the substrate as a VALUE rather than reading it from the environment, and
  derives all four transitions from scratch. It skips until seven documented variables name the store
  the builder published into, the builder's own configuration that names the image in that store, a
  firecracker binary, the guest agent, k3s, kubectl and an image the guest already carries
  — the last one because the guest reaches no registry, so the tenant image cannot be derived from
  anything the test can see, and requiring it is honest where guessing would produce a test that
  fails for a reason it does not state. **It has now run, and it passes**: `PASS` in 99.48 s, with
  `INSTANCE_START_TO_READY_S=9.749` host-timed from firecracker's `InstanceStart` to the Node's
  `Ready` condition, and the tenant pod pulling NOTHING (`Container image
  "rancher/mirrored-pause:3.10.2" already present on machine`), which is the sealed-image property
  holding inside the objective's own proof rather than only in the substrate test.

  **It found two bugs in itself on first execution, and both are the same lesson.** `kubectl get
  nodes --no-headers` reports an empty list on STDERR, so a harness that combines the streams counts
  `No resources found` as one node — which meant the "empty cluster" step could never pass, and the
  first run died on it after 362 s. And step 4 had been logging "the control plane still answers" on
  the strength of a reconcile returning no error, which establishes nothing about the phase or the
  cluster's own objects; it now requires the same `Ready` phase step 1 reached AND reads the
  namespaces back. A guard that cannot fail the way it claims to is the failure this whole document
  keeps rediscovering, and it was found only by running the thing.

  One thing the guard deliberately does NOT depend on: the k3s add-on pods sit at `0/1` with
  restarts while the guest console is CLEAN on every firewall signature, so that is not the module
  list. `Intent` stamps those pods from their add-on ReplicaSet, so they never hold the node — which
  is exactly the objective, and step 4 proves it. **Pod readiness and the objective are different
  questions, and a green guard is not a working cluster DNS.** The same join IS now measured through
  the composition root, by the four-step proof below, with a tenant `Cluster` CR, real leases and a
  minted tenant client; the sentence that used to stand here said the two had never been measured
  together, and it had already been overtaken by the run this entry records.
- **The 332 s boot was fabricated, and the real number is 0.300 s — and this entry's evidence is in
  HISTORY, not in the tree.** The claim is right and its citation does not open: `run/initrd/init` was
  never committed to this repository at any point, and the artifact that measured it was deleted by the
  commit that retired `experiments/`, once `internal/` had come to implement it. What that artifact
  recorded, and what is recorded here instead of a path nobody can open: `init` line 10 is a literal
  `echo Boot took 332.33 seconds` with no clock read and no computation, printed 0.29 s after
  `InstanceStart`, so it could not have been a measurement. An Ubuntu guest with systemd reaches a login
  prompt in **1.401 s**, host-timed from firecracker's own `InstanceStart` rather than from the guest's
  own clock — the same rule invariant 27 states — and that number survives only in the same deleted
  file. Boot was never the problem, which is worth stating because a fabricated measurement sent this
  work after a 30x non-issue.
- **The resolver gap is CLOSED and verified, and the entry that recorded it argues with itself.**
  Measured in the image template: `/etc/resolv.conf` is EMPTY — one byte, a newline — so the guest has
  no nameservers, which is exactly what `lookup registry-1.docker.io: Try again` is, and every pod
  sandbox fails because the `pause` image cannot be pulled. The reason it hid so well is that
  registering with the apiserver needs no DNS, so the node reaches `Ready` and the four-step proof's
  third step looks much closer than it is. `pasta --config-net` configures DNS in the NAMESPACE and
  cannot reach a file inside the guest's rootfs, and pasta proxies DNS at the gateway, so the fix is
  for the guest's agent to write its own resolver pointing at the gateway address it is already given.
  That write is implemented, on the production guest path rather than behind a flag: the agent's
  `configureResolver` writes `/etc/resolv.conf` from the gateway, and it is called unconditionally as
  the guest's agent starts. It is verified rather than assumed, in the four-step proof below: the guest
  console carries `KCP_AGENT_RESOLV=nameserver 192.168.0.1`, and a busybox image was pulled and started
  on that node, which is impossible without a working resolver.

  This entry used to say, in one sentence, that nothing in `cmd/socialweb-nodeagent`,
  `cmd/socialweb-nodeimage` or `internal/nodefirecracker` configures a resolver at all — and then, three
  sentences later, that the write was implemented and unverified. The premise was never updated when the
  conclusion was, so the same entry claimed the gap was absent and present at once, and the "unverified"
  half outlived the run that verified it. Both halves are recorded here because that shape is worth
  recognising: a premise kept while its conclusion moves is how a document ends up arguing with itself,
  and the next reader cannot tell which sentence is the current one. What is still true of the original
  disjunction is that the image builder does not write a resolver: the template ships the empty file on
  purpose and the guest's agent replaces it at boot.
- **The reconciler's readiness gate dialled an address nothing was listening on, and this is the
  failure shape that hides best.** Measured: the gate dialled the ADVERTISED address while the
  apiserver binds loopback, and from this host the advertised address is refused. So the gate could
  never pass: every pass blocked in `Spawn → gate → waitK3s` for the gate timeout (~91 s) and then
  reaped and respawned the process group it had just started, on a loop. That presents as "the
  cluster is Initializing" for ever, which reads as a slow component rather than as a wrong address.
  Fixed by dialling the BIND address. The general lesson: a readiness probe must dial the address the
  component was told to LISTEN on, never the one it was told to ADVERTISE, and the two are different
  values here by design.
- **The four-step proof is COMPLETE, measured through the composition root.** Against real kcp, with
  the substrate chosen by configuration (`SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE=firecracker`), in one
  uninterrupted run:

  1. the control plane reaches `Ready` at its leased apiserver port with zero nodes, and
     `TestTheControlPlaneReachesReadyAtZeroNodes` drives that against real leased ports, real kine, a
     real per-cluster postgres and a real `k3s server --disable-agent`, in under three seconds;
  2. `get nodes` is empty;
  3. a node joins ON DEMAND — the scale decision made it from the tenant cluster's own intent
     (`action=Provision, nodes=1, "intent and no node"`), never from an operator; the guest
     registered as `liveweb-node1 Ready` with `INTERNAL-IP 192.168.0.10`, kernel
     `7.2.7-200.fc44.x86_64`, `containerd://2.3.4-k3s1.36`; and a pod ran on it
     (`docker.io/library/busybox:1.36`, pulled inside the guest). **`INSTANCE_START_TO_READY_S=9.609`**,
     host-timed from firecracker's own `InstanceStart` in `instance.json` to kubelet's node-Ready
     transition;
  4. it returns to zero: the decision goes `Teardown` past the hold and then `None`, `get nodes` is
     empty, the guest and its data dir are gone, and the control plane STILL ANSWERS — `/readyz` ok,
     namespaces still listed.

  The resolver gap is closed and verified rather than assumed: the guest console carries
  `KCP_AGENT_RESOLV=nameserver 192.168.0.1`, and the busybox image was pulled and started on that
  node, which is impossible without a working resolver.

  What skips by DEFAULT is `TestTheFourStepProofRunsThroughTheCompositionRoot`, and it was not weakened
  into a pass: it skips until seven documented variables name the store the builder published into, the
  builder's own configuration that names the image in that store, a firecracker binary, the guest
  agent, k3s, kubectl and an image the guest already carries. This entry used to attribute that to a
  test named `TestANodeJoinsAndTheClusterReturnsToZero`, which does not exist — it was deleted, with the
  file that held it, six hours and 109 commits BEFORE this entry was written, and its name survived only
  in this document. The description was of the right test; only the name was stale, which is the harder
  kind to notice, because a reader who greps for it finds nothing and concludes they are looking at the
  wrong branch.
- **Intent is the tenant's workload, not anything running — and getting this wrong made the objective
  unreachable.** `scale.Intent` counted every pod `Pending`, `Running` or `Unknown`, and a k3s cluster
  always holds its own coredns, metrics-server and local-path-provisioner: `Pending` at zero nodes,
  `Running` at one, so intent was true in BOTH directions. An empty cluster therefore provisioned a
  node it had no reason to want and never released it, which presents as a scale bug rather than as an
  intent bug. The baseline was measured rather than assumed, on k3s v1.36.4+k3s1 with nothing applied
  through kcp: namespaces `default`, `kube-node-lease`, `kube-public`, `kube-system`, `users`; three
  Deployments (coredns, local-path-provisioner, metrics-server) each `replicas=1`, their three
  ReplicaSets, their three `Running` pods, and no StatefulSets, DaemonSets or Jobs at all.

  The discriminator is measured too, and the measurement is what makes it correct: k3s marks what its
  own manifest controller applies with `objectset.rio.cattle.io/owner-gvk =
  "k3s.cattle.io/v1, Kind=Addon"`, and **that marker holds on the Deployments and the ReplicaSets but
  NOT on the pods**, which carry only their pod-template labels — so a marker-only rule would have
  missed exactly the objects `Intent` reads. The rule resolves it through the owner chain instead: a
  pod whose `ownerReferences` name a marker-carrying ReplicaSet is the control plane's own. There is
  deliberately no list of add-on names and no list of namespaces: a list of names goes stale silently
  the first time k3s ships a fourth add-on, and a list of namespaces would stop a tenant who applies
  into `kube-system` from ever holding a node.

  The converse property was proven in the same run, because a filter that excludes too much turns
  "nodes scale to zero" into "nodes are never provisioned" and reads as success at zero: a tenant pod
  that cannot be scheduled IS intent and holds the node. Measured, one uninterrupted run, nothing
  deleted by hand: an empty cluster sits at zero with the add-ons `Pending`; a tenant pod gives
  `Provision`, the node comes up and the pod runs; an unschedulable tenant pod gives "intent, and a
  node is already counted"; and deleting ONLY the tenant workload gives "nothing running, holding the
  node", then `Teardown, nodes=0, nothing running for 3m0.002514861s, past the 3m0s hold`, with those
  three Deployments still deployed and untouched, after which `get nodes` is empty, `/readyz` is ok,
  and the guest and its data dir are gone.
- **The local-process node is REFUTED as the substrate; the node is a VM.** Three processes, three
  measured answers, and they cannot be reconciled on one host:

```
k3s server --disable-agent   must read and write /etc/rancher/node/password, which is NOT under
                             --data-dir and no flag moves it. Needed MODE, not content: the file's
                             value is never checked, because no node registers through this server
                             and so the k3s-<node> Secret that would disagree with it is never
                             created (measured: kube-system holds only k3s-serving, get nodes is
                             empty). Runs happily as a plain user once that file is theirs
k3s agent                    "Error: agent requires additional privilege if not run with
                             --rootless: not running as root" -- fatal, measured from the node's
                             own agent.log. /run/k3s/containerd, /var/lib/kubelet and 6444 are
                             root-level host resources it does not relocate via --data-dir
postgres                     "initdb: error: cannot be run as root" -- fatal
```

  A host that runs the store therefore CANNOT run a node, and the node cannot share a host with
  another node either — so no ordering of privileges fixes it. The control plane needs no privilege
  beyond being able to read and write ONE file, which is a one-time ownership decision by the
  operator rather than a per-run requirement, and with the node inside a VM nothing on this host
  runs as root at all. `--rootless` is the cheaper alternative and is unmeasured: it moves the
  kubelet's paths into a user namespace, which is exactly where flannel's VXLAN and the containerd
  socket would have to live too.
- **The vault is not in the default component set, and that is now a DECISION rather than a gap.** The
  config is still rendered and `bao server` still runs for a stack that names `ComponentOpenBao`, but
  `PurposeOpenBao` is no longer in `materialiser.DefaultPurposes`, and the startup check asks
  `PurposesFor(stack)` rather than the global list — it had been answering "is this purpose bound"
  from a list that says nothing about the stack in use. The reason is not that the vault is
  unfinished but that **nothing consumes it**: measured, a fresh vault from a rendered config answers
  `GET /v1/sys/health` **501** `{"initialized":false,"sealed":true}`, so the readiness gate
  (200/429/473) refuses; and there is no secrets flow to finish, since nothing enables a KV mount,
  nothing writes a bundle into a vault, and ESO's `SecretStore` path is hardcoded to `secret`. The
  identity bundle's real home today is the file tree at `<DataDir>/server/tls`, which persists and
  pre-seeds on its own.

  What blocked it was the unseal-key policy, and the reason to stop rather than choose one is that
  **every available answer defeats the purpose**: auto-unsealing with a key the gateway holds makes
  the vault only as strong as the gateway, and Shamir shares written beside the data they protect
  achieve the same thing. A component that starts, fails its own readiness gate and provides nothing
  is worse than absent — it occupies a lease and it parks a cluster in `Unavailable` for a reason
  unrelated to the cluster. It comes back when something needs a secret, and the unseal policy is
  decided then, against a real consumer.

  Worth recording so the hazard is not overstated later: this was **latent, not live**. The
  composition root already built `Stack{ComponentKine, ComponentK3s}`, so no production cluster was
  parked in `Unavailable` by the vault. What remained was the declarations and a library default that
  contradicted the decision, which is a trap for whoever configures a stack next.
- **`deploy/install-provider.sh` fails at its last gate, and the gate it fails at is not the one this
  entry named. PARTLY WRONG.** The script does not hold that gate: its last line runs
  `apply-permission-inventory.sh`, `set -e` propagates the status, and that script ends `exit $fail` —
  so the install exits 0 when every expectation it measures holds, and "exits 1" is a statement about
  one run rather than about the script. `IdentityValid=True` is a PRECONDITION the script waits for,
  not something it asserts or prints: two `until` loops poll for it and the only mention of the string
  is the timeout message that exits 1 if it never arrives. And the failing gate's own closing text
  contradicts the reason this entry gives for the failure. What it says a failure means is one of the
  credentials the DESIGN CHOSE — the runtime's kcp credential or its per-tenant minted certificate —
  "and never that a workspace ServiceAccount needs a wider binding". So the entry describes the finding
  the gate exists to prevent rather than the reason the gate fails. That finding is real and is in
  section 7: a workspace ServiceAccount token is confined to its origin logical cluster and refused
  elsewhere before RBAC is consulted, so no subject string lets one serve the runtime. It is a gate
  doing its job, not a broken install, and it means an install is not "green" until the credential
  question is answered in the deploy, not just in this document.
- **Multi-node is out of scope for v1.** The underlay verdict makes single-node the shape, and
  `scale.Decide` assumes it harder than this entry said: `Provision` returns a literal `Nodes: 1`, a
  cluster with intent and any node already counted gets `None`, teardown is all-or-nothing to zero, and
  the `scale.Nodes` seam itself has no count — an actuator can only be asked to BE the node.
  `nodeID` is the package constant `"node1"`, so every cluster gets exactly one node, named
  `<cluster>-node1`, and the per-node machinery that would generalise (the ordinal, the per-node address
  off the cluster's range) is only ever called with ordinal 1.
- **An image did not record the guest agent it carried, and closing that closed the family it belonged
  to. CLOSED, and measured twice: once by the failure, once by the fix.** The record weighed the
  kernel, its config, the base image, the k3s binary, the module NAMES and the seeded digests — and not
  the agent, which is the binary that decides the guest's kubelet command line, its resolver, its
  identity and its interface. So an agent change could not move an image's name, and the consequence
  was not theoretical: the builder answered `up to date, nothing built, nothing pulled` and was RIGHT
  about every input it tracked, the image that booted then ran the previous agent against a new node
  disk, that agent read a removed field as Go's zero value, passed `--kubelet-arg port=0`, and the
  kubelet refused to start outright (`port (--port) 0 must be between 1 and 65535`), so the guest never
  joined. Nothing in that sequence named the agent, and the first instinct is to debug the kubelet: a
  refusal that cannot name the thing that is actually stale is the failure mode, not a missing check.

  The record now carries the agent's path and **the digest of the binary the build just produced** —
  the binary and not its source, because the flags and the toolchain are inputs to that binary and no
  digest of a source tree covers either. It carries the module tree's path and a digest over every file
  in it, path and contents and symlink targets, because the build copies that tree into the image whole
  (section 12). And it carries the configuration values that were free to change under a name that did
  not move: the boot arguments, the guest's k3s data directory, the rootfs label and its headroom, the
  agent's package and its build flags. Those are recorded as VALUES and not as a digest of the
  configuration file, because the file is canonical in places the bytes are not — a module list
  reordered is the same module list, and a digest of the bytes would rebuild 2.3 GB for a change
  nothing made.

  What closes the CLASS rather than these instances is what matters more: a test holds the builder's
  read set and the record's field set equal in both directions, so a configuration field the build
  reads and the record does not mention fails the suite, and a record field nothing reads fails it too.
  It was checked the way a gate has to be — by removing the agent's declaration and watching the test
  fail — and it is the shape section 12's fingerprint test already had, applied to the enumeration as
  well as to the mutation. VERIFIED through the ordinary path rather than a hand-repointed one: the
  same command over the same configuration that answered `up to date` answered `building` and produced
  a different name, the normally-built image records the digest of the agent beside it, and a guest
  booted from it reached `1/1` system pods with zero restarts, reported `{"Port":10250}` for its own
  kubelet, served a proxied `/healthz`, and held.

- **The unwired controls were settled ONE AT A TIME, and the settlement was not the same for all five.
  Two were deleted, on this repo's own rule that a control nothing reaches is indistinguishable from
  absent, and the explanation of why each existed is kept here rather than in the commit: a deletion that
  takes its reasoning with it leaves the next reader unable to tell residue from a missing feature.**
  `processrunner.Reach` and `processrunner.Identity` are DELETED, and they were not a missing wire —
  every production call site passed `Identity` (`clusterprovider/steps.go`, the pasta line in
  `nodefirecracker/node.go`, and the materialiser's own test helper), so the mapper mapped nothing
  anywhere. It was VM residue: the substrate this design abandoned reassigned a guest's ports on every
  boot, so a caller that could not ask "what port is this guest's 6443 on this time" could not connect —
  and a guest's ports are its own network namespace now, so there is nothing to map. Deleted with it: the
  type, the identity function, the `Spec` field, the `Process` field, the accessor, and
  `TestReachDefaultsToIdentityAndIsOverridable`, whose own failure text ("an off-host substrate reassigns
  on every boot, so a caller that cannot override Reach cannot connect") is the sentence that dates it.
  `internal/nodetransport` is DELETED: it is an unwired REFUSAL (`ErrSecondSession`, "a node is one
  connection; a second session is a second VM"; `ErrSubstrateDestroy`, "a node is not destroyed by the
  substrate, or the VM's disappearance would prove the harness instead of the provider") whose only
  importer was itself, and the arm it was written for is the property the substrate does not have — with
  the node in a VM, teardown is the guest's own. The RULE that refusal encoded — the seam must not give
  the dial lifecycle ownership, so `Wait` and `Close` stay distinguishable, and one node is one
  connection — is prose in section 9 and stays there; what is deleted is the code that asserted it, which
  nothing called. `internal/sessionreport` goes with it, and not because
  it was unused: it parses the market's SSH front-door report, the deadline line, which is the only
  honest source of `nodeExpiry`, and its only non-test importer was `nodetransport`. Section 16 puts the
  door out of scope and says its report surface is not, so a parser of that surface has no importer and
  cannot get one without bringing the door back in scope. What is NOT lost with it is the invariant its
  test guarded: "a node whose deadline is UNKNOWN must not clear the teardown gate" is enforced live by
  the materialiser ("status.nodeExpiry is unknown and the apiserver has answered; refusing to tear down
  against a gate that cannot compare") and measured by `TestTeardownRefusesWhenTheGateCannotCompare` —
  the deleted test was over the parser, not over the gate. Two were KEPT, each with a caller worth naming
  rather than an assumption that one exists. `internal/versions/versions.json`'s reader is
  `internal/versions/versions_test.go`, which is not a self-test: it is where the DEPLOY config's guards
  live — both audit policies are metadata-only, the deployed `kcp-audit-policy.yaml` matches its copy,
  the `kcp` entry pins `--feature-gates=WorkspaceMounts=true` (ALPHA and default-off, so with it off
  every mount silently stops being served), the `APIResourceSchema` covers every field of the Go type,
  and the workspace type binds an export that exists — NINE test functions in one file, and deleting the
  manifest deletes all nine. `internal/devicekey` is the only implementation of a declared production
  seam, `kcpclient.DeviceKeyGranter`, and its caller is the LIVE proof
  `TestTheRenewalPathAgainstRealInfrastructure` in `internal/tenantclient/live_renewal_test.go`, gated on
  six `SOCIALWEB_LIVE_*` variables — the only place ADR 0020's Grant → Resolve → Issue has ever run
  against real infrastructure. Deleting it would delete the seam's only implementation and the proof of
  that path together. **That ruling stands, and the rest of the fact about the seam is now settled: the seam is WIRED, and
  the endpoint became what the surface already said it was.** The gateway was constructed without a
  granter — `cmd/socialweb-gateway/main.go` passed `Options{Host: ..., RestConfig: ...}` and no
  `DeviceKeys` — so in production `kcpclient.Client.deviceKeys` was nil and `Client.Kubeconfig` took its
  guard branch and returned `ErrNoDeviceKey`. That method is not internal plumbing: section 6's XRPC
  surface serves it as `getKubeconfig`, "the credential the user runs kubectl with", from `xrpcserver`
  through `gateway.GetKubeconfig`. So a real gateway had an endpoint that was routed, documented and
  unit-tested, and refused every call it would ever receive. The shape of the failure is why it read as
  a note rather than an alarm: it fails CLOSED, with a message that says what is missing, so it was
  never a hole in the boundary — it was an absence that reads as a presence, which is this document's
  recurring defect.
  Wiring beat removing the endpoint because the seam was already complete and already proven.
  `internal/devicekey` generates the did:key, writes the binding row, signs the credential and renders
  the kubeconfig, and the store `buildDeps` already constructs — `internal/tenantclient.Client` — IS
  the `Binder`, because its `Record(ctx, tenantclient.Row) error` is the interface's one method, word
  for word. What was missing was one argument and four policy values. Removing the endpoint would have
  deleted a working seam to make the document agree with the wiring, and ADR 0020's Grant → Resolve →
  Issue would have lost its only non-test counterpart.
  The four policy values are configuration with NO defaults, which is what `devicekey.New` exists to
  enforce: `SOCIALWEB_GATEWAY_DEVICE_KEY_AUDIENCE`, `SOCIALWEB_GATEWAY_DEVICE_KEY_TTL`,
  `SOCIALWEB_GATEWAY_DEVICE_KEY_EXEC_COMMAND` and `SOCIALWEB_GATEWAY_DEVICE_KEY_EXEC_ENV_NAME`. A
  device credential's audience, lifetime, helper path and environment variable name are choices an
  operator takes, so a missing one is a refusal at `load()` rather than a lifetime nobody chose. The
  audience MUST equal the first of `SOCIALWEB_GATEWAY_AUDIENCES`, and a mismatch is refused rather than
  reconciled, because the mint verifies the credential against that audience and the divergence would
  otherwise surface as every handed-out kubeconfig minting Unauthorized. The consequence is deliberate
  and is the same gate `ErrNoMintStore` already is: a gateway configured without these four variables
  refuses to start.
  **A routed verb is not a working verb, and the suite now says so twice.** `TestEveryVerbIsBound` asks
  only that each declared verb is not 404, which a verb that is routed and permanently non-functional
  passes — the same shape as `k3s check-config` returning 0 over a missing module, which is why it
  passed over this endpoint for as long as this entry sat open.
  `TestBuildDepsWiresTheDeviceKeyGranterFromTheRealImplementation` calls the REAL `buildDeps` and fails
  on `ErrNoDeviceKey`, naming the guard; `TestEveryVerbTheLexiconsDeclareIsAnswerableWithTheRealDeps`
  drives every verb the lexicons declare past AUTHENTICATION with a real service-auth token and requires
  its ANSWER, not its presence, and derives its table from the lexicon files so a verb added to the
  surface cannot escape it. Against the un-wired tree the first reports the guard by name and the second
  reports `getKubeconfig answered 500`, while `TestEveryVerbIsBound` still PASSES — and that last
  measurement is the one that makes the pair worth having, because it is the difference between a test
  that could fail and a test that never did.
- **The identity bundle's home is the FILE TREE at `<DataDir>/server`, and section 5's component 7 said
  otherwise because a document outlived the code it described.** The bundle is the 13-entry
  `tls/`+`cred/` tree under `<DataDir>/server` — the same root `clusterstack.TLSDir` puts `tls/` under —
  seeded and harvested there by `clusterprovider.HarvestBundle`/`SeedBundle`, with a CA fingerprint
  measured byte-identical across a teardown and a rebuild (section 7, and the timing block in section 9).
  OpenBao was never its home: `PurposeOpenBao` is not in `materialiser.DefaultPurposes`, a fresh vault
  from the rendered config answers `GET /v1/sys/health` **501** `{"initialized":false,"sealed":true}` so
  the readiness gate (200/429/473) refuses it, nothing enables a KV mount, nothing writes a bundle into a
  vault, and ESO's `SecretStore` path is hardcoded to `secret`. So the ESO/KV-mount flow is not
  unfinished business: it is UNNECESSARY until a secret exists that the file tree cannot hold, and the
  unseal policy is decided then, against a real consumer. Component 7 now says what the code does.
  **SUPERSEDED 2026-09-25: the file tree is still where k3s reads the bundle, and the vault is now its
  authoritative store.** That is not a reversal of the measurement above but of its conclusion, and the
  mechanism is the one the paragraph describes as unnecessary becoming necessary. The vault is in the
  production stack, it is initialised and unsealed, it holds all 13 entries at `secret/data/k3s/identity`,
  and the wake reads them from it: `SeedBundleForWake` (`internal/clusterprovider/bundle.go:72`) tries
  `SeedBundleFromVault` FIRST, materialises `<DataDir>/server/{tls,cred}` from the vault, and only falls
  back to the file tree when the vault holds nothing (a first bring-up, before the harvest has written
  one). The fallback is deliberately narrow: when the vault's unseal material EXISTS but is unreadable,
  the code REFUSES rather than falling back, because a fallback there would let k3s author NEW CAs under
  the same name and silently invalidate every credential issued before the rebuild
  (`internal/clusterprovider/vault.go:632`). Two tests pin the direction:
  `TestTheWakeMaterialisesTheIdentityBundleFromTheVaultNotTheFileTree` and
  `TestTheWakeFallsBackToTheFileTreeWhenTheVaultIsEmpty`, both PASS. What the flip does NOT change is
  durability: the vault's storage and its unseal material sit on the same state volume as the
  materialised tree, so the vault buys authority -- one store of record, custody, and a wake-time
  restore that cannot silently regenerate CAs -- and not off-host backup. Losing the volume still loses
  all three together.
- **The vault's lease is width 1, and the second port was never read.** `purposeWidth[PurposeOpenBao]`
  was 2, spelled "api + cluster", while the config the vault is given sets `disable_clustering = true`,
  which removes the cluster listener and makes "one vault per cluster, no HA" a property of the file.
  Both the listener stanza and `api_addr` take the same `Lease.Port`; no code reads `Lease.Port + 1`; and
  with the cluster listener gone nothing binds it. A leased port that is never bound is not harmless: it
  is a port carved out of the per-host range no later cluster can use, and a reader of the lease file has
  to work out which of the two ports is real. Section 8 records the measurement that separates this from
  `PurposeK3sSecure`, which stays 2 because the apiserver's pair is MEASURED as two bound listeners. The
  fixtures that had pinned 2 — portallocator's `TestJ`, portscan's fold fixture, and the clusterprovider
  step leases — pin 1 now.
- **A reconcile pass is accepted as unbounded at v1, with a named trigger: a decision with a condition,
  not an open question.** Measured: ONE PASS TOOK 6.7 s AGAINST A 2 s TICK, because the tenant bootstrap
  runs synchronously inside the pass, so the tick is a floor rather than a period. At 0-or-1 node that is
  accepted: one cluster, one node, and the pass is not on a tenant's critical path. It is still a
  scheduling decision nobody has made at N, and the trigger that reopens it is precise — a pass whose
  duration can exceed the tick BY DESIGN, which is what happens once a cluster can hold more than one
  node. `scale.Decide` returns a literal `Nodes: 1`, `nodeID` is the package constant `"node1"`, and
  multi-node is out of scope for v1 (section 10), so the condition cannot be met today and the decision
  holds until it can.
- **A restored guest is a TEMPLATE, and no resume-triggered re-identification is worth having: the
  mechanism cannot scale out, and the cold boot is faster than the cluster noticing the node died.**
  Snapshot restore is refuted as a provisioning primitive, by measurement rather than by taste:
  restore-to-Ready **9.974 s** against cold **9.079 / 9.158 / 10.768 s**, so a restore buys nothing in
  time; and a restored guest cannot make a NEW node, because restore does not re-run boot-time units, so
  the identity adoption the guest's own agent performs — reading the per-node read-only virtio-blk disk
  this design settled on, in place of firecracker's MMDS, which appears in no `.go` file — never runs, and
  a snapshot frozen as one node comes back as that node even when its recorded disk path is re-pointed at
  another node's disk. What stayed open was whether ANY resume-triggered re-identification is worth
  having, and the answer is no: the mechanism cannot scale out, so re-identification would serve a node
  the cluster ALREADY knows, and a cold boot (9.079-10.768 s) is faster than the cluster notices the node
  died. One part of the old entry stays UNVERIFIED rather than resolved: on 6.1.186, the pinned-kernel
  route this design abandoned in favour of the host's own kernel, the guest does NOT log `crng reseeded
  due to virtual machine fork`, so clones may share RNG state — unmeasured on the kernel that actually
  ships, and a reason to keep validating it.
- **The GATEWAY's verdict on a normally-built image is measured now, not pinned only by unit tests.**
  The image record carries the agent's digest, and the normally-built image at
  `/home/johnandersen777/kcp-image/out/sha256-bbee5b4ab2a616e43bebf3212ebe50ac956e800e372e083c4488fb0f761b28a8`
  records `agentBinaryDigest sha256:cabe50a056285cefcc53aadd7795531ddc823d7227d8fce96f635b978327882d` beside
  the module tree's digest. The run that booted that image before exercised the SUBSTRATE directly, which
  does not call `CheckInputs`, so the composition root's own verdict on a normally-built image was a
  unit-test fact. It is a measurement now: the four-step proof ran against THAT image, env-gated, with the
  substrate named by configuration, and **PASSed in 97.65 s** — the step that derives a node from the
  image is what reaches `nodeSubstrateFor → manifest.CheckInputs()`, and a guest booted from it, or no
  node would have joined. All four transitions held in one uninterrupted run: (1) the control plane
  reached `Ready` at its leased apiserver port **https://127.0.0.1:62702** with zero nodes; (2) `get
  nodes` was empty, and stayed empty across passes; (3) a node joined ON DEMAND — `Provision, nodes=1`,
  "intent and no node" — as `liveweb-node1 Ready <none> ... v1.36.4+k3s1`, with the tenant workload
  running on it; and (4) the cluster returned to zero with the control plane STILL `Ready` at the same
  URL and still listing `default`, `kube-node-lease`, `kube-public`, `kube-system` and `users`. The run
  prints the four transitions and no host-timed instance-start-to-ready, and its ports come from
  `liveports.FourStepProof` in `internal/liveports` (flock-held slot) rather than from a remembered
  number — the package exists because a port block picked from memory collided, and the fingerprint in
  that image's path is 71 characters, so the image is named by its digest and never by a glob.
- **`shut` means the tenant's apiserver was NOT REACHED, not "not 2xx", and the gate now holds against a
  serving cluster where it used to reap.** The defect and the fix are one substitution: `shut` was
  `o.PathStatus != 0 && (o.PathStatus < 200 || o.PathStatus >= 300)`, which a 401 satisfies, and the
  provider's probe through the mount can never get a 2xx because the tenant apiserver refuses `/version`
  to callers it cannot authenticate — so the gate read a SERVING cluster as shut. It is now
  `o.PathStatus != 0 && !reached(o.PathStatus)`, where `reached` is 2xx, 401 or 403. The code table is
  stated with its basis rather than asserted: **0 HOLD** (transport failure to kcp, so there is no answer
  about the mount at all); **2xx HOLD** (the mount served); **401 HOLD, MEASURED** — and it is a reach
  rather than a guess because the BODIES prove it: the direct apiserver GET and the through-the-mount GET
  return the identical k8s Status JSON, while kcp's own refusal (503) and the proxy's (502) carry
  different or empty bodies; **403 HOLD by judgement, not by measurement** — kcp's localproxy returns
  only 404/429/5xx before forwarding and no path was found where kcp answers 403 on a mount itself, so
  this is the code treated generously ON PURPOSE, because if it ever occurs the gate holds longer and
  that is the safe direction; **404 SHUT, MEASURED** (kcp answers 404 for a workspace path it cannot
  resolve, and nothing there proves a backend); **502 SHUT, MEASURED** (the proxy with no backend, the
  dead direction); **503 SHUT, MEASURED** (kcp's own `Not available.` before forwarding); and any other
  non-zero code SHUT, failing closed on the unproven. The error direction is chosen rather than
  inherited: ambiguity resolves to reached-and-hold, because "stuck and billing beats silent loss" is
  settled and a reap on a guess is not. `TestOnlyAnUnreachedPathIsShut` pins the predicate behaviourally
  at the materialiser, both directions. The before-and-after is the reading that matters, both taken from
  the same live instrument over the same cluster: BEFORE the fix the gate read
  `phase=Draining ops=[tenant-path-shut reap release-ports release-subnet remove-finalizer] PathStatus=401`
  48 ms after the delete, with the group up and serving; AFTER it read
  `phase=Draining ops=[shut-tenant-path await-tenant-path-shut] PathStatus=401` at 12 ms with no finalizer
  released and nothing reaped, and the tenant apiserver was STILL ANSWERING when that pass finished. The
  mount turned 503 at 218 ms, the reap came on a LATER pass at 260 ms reading 503, and the group died
  last. That order is what section 11 always claimed and only a fake had ever produced.
- **The Cluster finalizer is installed by the create path and released by the pass that reaps, and
  installing it exposed a second defect that would have made things worse.** `FinalizerCluster` was
  declared and RELEASED (`materialiser.go:368` sets `RemoveFinalizer`) but never ADDED, so
  `DeletionTimestamp` was unreachable and `Draining` was written by no production path. What the fix had
  to confront is that `RemoveFinalizer` had NO CONSUMER either: setting the field on a Result that
  nothing reads would have left every deleted Cluster terminating for ever, with `Refs` still listing it
  and every later pass reaping a dead cluster — a leak traded for a leak. So the fix is three parts, not
  one: `Clusters.RemoveFinalizer` on the interface, `Registry.RemoveFinalizer` on the implementation, and
  the call in `Provider.reconcile`. Six fake implementers gained the method, enumerated by the compiler
  rather than by search. `Registry.RemoveFinalizer` removes only THIS repository's finalizer and guards
  the write with a JSON Patch `test` operation on the list it read, so a concurrent writer produces a
  refused patch rather than a clobbered list. One bounded gap is noted rather than papered over:
  `kcpclient.Ensure` returns early on `AlreadyExists`, so a CR created before this change keeps no
  finalizer — there is no deployment to migrate, which is why it is a note and not a convergence write.
  And the measurement now supplies nothing production does not: the mount proof asserts the CR ARRIVES
  from `kcpclient.Ensure` already carrying the finalizer and fails if it does not, with a message saying
  a self-supplied finalizer would keep passing over the broken path — then measures the CR being deleted
  at 261 ms once the reaping pass releases it.
- **A `Draining` Cluster CR shuts the mount BEFORE its process group dies, and the reading came from a
  real kcp started inside a test.** This is the question the whole of section 11's gate was built around,
  and it is settled by measurement rather than by inspection:
  `TestTheMountShutsBeforeTheTenantProcessGroupDies` starts kcp as the CURRENT uid in its own temp root
  directory — `--feature-gates=WorkspaceMounts=true`, with a temp `kine` on a sqlite file behind
  `--etcd-servers` — deliberately not the root-owned `/var/.kcp`, and deliberately not an embedded etcd,
  because etcd is host-fixed on 2379/2380 and no flag moves it. It applies `deploy/`'s APIResourceSchemas,
  APIExports and WorkspaceTypes into `root:socialweb-provider`, builds the tenant tree with
  `kcpclient.Ensure`, and behind it runs the real reconciler, the real `Registry`, real leased ports,
  real postgres and kine and a real `k3s server --disable-agent`. Three answers are distinguishable at
  that path and the CONTROL establishes all three, which is what makes the third a reading rather than a
  default: a serving cluster answers **401** with the tenant apiserver's own Status body; after the group
  is SIGTERMed the mount answers **502** with an empty body, which is section 14's long-recorded dead
  direction reproduced from the real mount; and a `Draining` CR answers **503 `Not available.`**, kcp's
  own refusal, with the backend never contacted. Writing `Draining` into the CR while the group was up and
  its apiserver answering moved the mount off 401 in **206 ms**, and writing `Ready` back brought it to 401
  again — so the PHASE is what shut it. The phase is read as a string by a `switch` whose only case is
  `Ready`, so the mechanism is phase-agnostic above it: `Unavailable` and `Initializing` measure 503 too,
  and `Connecting`/`Unknown` cannot be stored at all because this repository's own APIResourceSchema
  enumerates four phases. The reconciler is therefore correct here, and the reason this took so long to
  get is stated plainly in section 14: a fake registry was answering the only question anyone asked it.
  Two things about the instrument are worth keeping: the whole test is **27 s**, needs no root and no
  `/dev/kvm`, and carries **no env gate** — it skips only when `kcp`, `kine`, `k3s` or `postgres` is
  absent, with a skip message that says a skip here is this question staying open, because an env-gated
  test that has never run is worth nothing and this repository has been bitten by exactly that.
- **The control plane runs as a non-root container, and the zero-node proof passes INSIDE it.** This is
  the measurement the container exists for. `TestTheControlPlaneReachesReadyAtZeroNodes` — the real
  reconciler against real leased ports, real kine, a real per-cluster postgres and a real
  `k3s server --disable-agent` — ran in the image and PASSED, in 9.50 s (and 7.67 s on an earlier
  image) against a ~6 s host baseline, with STEP 1 reaching `Ready` at `https://127.0.0.1:31202` and
  STEP 2 reading a live control plane with zero nodes. Two runs, both PASS. The image takes no
  permissive flag: not `--privileged`, not `--security-opt`, not `--cap-add`, not `--device`, not
  `--network host`, no userns remap. It runs `USER 1000:1000`, deliberately the host user's uid so the
  mounts are readable and the lock directory lines up, and it is built from the host's own family so
  host-built binaries run. Run against itself, `check-host` answers 9 HOLDS, 1 MISSING and 10 MUST BE
  PASSED IN and exits non-zero — which is the honest reading of a control-plane-only image and exactly
  what the third category was designed to say: the kernel, the module tree, `CLONE_NEWUSER`,
  `/dev/kvm`, the firecracker binary and the image store are the container's EDGE, and `pasta` is the
  one thing MISSING, because egress for the node half is not this image's business.
  **The failure it caught first is worth more than the pass.** The first in-container run failed with
  k3s logging `Sending HTTP/1.1 400 response: node password not set` for ever while `/readyz` stayed
  503 and the cluster sat in `Initializing`. The cause was an EMPTY `/etc/rancher/node/password`: it
  opens read-write, so every check that opens it says yes, and the server needs it non-empty. So the
  requirement is that the file be NON-EMPTY and not merely writable, the image now writes 32 random
  bytes into it, and `check-host` reports an empty file as `MISSING` with that k3s line as its
  evidence. It is the same shape as everything else here — a thing that opens clean and is wrong — and
  it was found by running the proof in the new environment rather than by reasoning about it.
  **Its validation script runs twice in a row, and the host-contract step it runs is falsifiable.**
  `deploy/control-plane-container/validate.sh` names its two containers, and `--rm` removes a container
  only on a NORMAL exit: a run ended by a timeout, a Ctrl-C or a supervisor being stopped leaves
  `kcp-zeronode` behind holding the name, and the next run dies with `Conflict. The container name
  "/kcp-zeronode" is already in use` — a validation that fails for a reason it does not state. That was
  observed rather than anticipated: one run held the name and blocked the next. The fix is a
  `docker rm -f <name>` before each `docker run`, guarded so a name that is not there cannot abort under
  `set -e`. That is chosen over unique per-run names and over dropping `--name` because it is the only
  one correct UNDER A KILL: a suffixed name still leaks one stale container per kill and a pid- or
  `$RANDOM`-derived suffix can collide, since pids recycle and a container from a previous boot still
  holds the name; dropping the name leaves an ever-growing pile of anonymous stopped containers and
  gives up the one label a human uses to find a run that is still alive; and `rm -f`, unlike either,
  also reaps a still-RUNNING orphan whose owner is gone, freeing the ports it holds rather than leaving
  them for the next run to trip over. The second step is gated now on a POSITIVE assertion rather than
  an exit-code allowlist, because `check-host` is DESIGNED to exit non-zero here — the image is the
  control-plane half, so it reports something like `9 HOLDS, 1 MISSING and 10 MUST BE PASSED IN` — and
  the `|| true` that allowed for that swallowed every OTHER ending too: a crash, a missing binary, an
  OOM kill, all printing identically while the step gated nothing. The script captures the output,
  requires the summary line `N checks: A HOLDS, B MISSING, C MUST BE PASSED IN`, fails with that
  expectation named when it is absent, and fails when the total is not the sum of the three categories.
  **`check-host`'s third exit code is what makes this load-bearing**: it exits 2 when it cannot read the
  host well enough to check anything, printing `MISSING host.facts` and NO summary line — so a `{0,1}`
  allowlist would have caught that by luck and a `{0,1,2}` allowlist would have passed it silently. The
  line is the instrument, and its absence is the failure.
- **Only what is inside the build context is ordered by the build graph, and the artifact leaves the
  build as files.** The build system hashes what it is given, so the stage COPIES the operator's
  configuration into itself at the path the configuration names rather than mounting it: a copy is
  content-addressed, so its bytes enter the cache key and a change to it re-runs the stage instead of
  answering a cache hit over the previous image. Everything the configuration reads that cannot move
  into the context — this host's `/boot`, its `/lib/modules/<release>`, the base image and the artifact
  directory — is mounted read-only at the exact paths it spells, which is what makes the container's
  build compute the same inputs as the host's. Two measurements forced this shape rather than being
  chosen: a named build context is mounted READ-ONLY (`Read-only file system` on a write into one), so
  no stage can write the store through a mount; and `/boot` cannot even BE a context (`error from
  sender: open /boot/efi: permission denied`, mode 0700), so its two files are staged. The artifact
  therefore leaves through an `extract` stage and `--output type=local,dest=<out_dir>`, landing
  `<out_dir>/<fingerprint>/{vmlinux,initramfs.cpio,rootfs.ext4,image.json}` — exactly the paths
  `LoadImage` derives from the configuration, so the runtime's lookup is the same check it always was.
  The cost is stated rather than hidden: the artifact is written twice more than one build needs, and
  the builder must hold both at once.
  **The record covers the configuration's path and every value it transcribes**, which is
  `modules`, `initramfs_modules`, `images`, every file digest, the whole module-tree digest,
  `boot_args`, `k3s_data_dir`, `rootfs_label`, `rootfs_headroom_mib`, `agent_package` and
  `agent_go_flags` — and not `firecracker`, `stage_dir`, `out_dir` or `image_cache_dir`, none of which
  is image content. Two consequences follow, and both are stated rather than discovered. The same bytes
  at another path is another name, because the path is an input. And **the agent binary carries Go's
  VCS stamp, so a commit that changes no agent source still moves every image's name**: one agent
  source at three repository states gave `cabe50a0` (40781b3+dirty), `790f97a2` (e88b2f3+dirty) and
  `c9300ed9` with `-buildvcs=false` (reproducible twice, and byte-for-byte what the container's own
  stage produced). Both of the first two were `+dirty`, meaning editing any file in the repository — a
  document, a test — renamed every node image and cost a 2.2 GiB rebuild for no change in image
  content. The name must be a function of what determines image content, and a repository dirty flag is
  not one, so `-buildvcs=false` is set in `agent_go_flags`. Traceability is not lost by it:
  `agentBinaryDigest` identifies the source by the content that produced it, which is a stronger
  identity than a revision label.
- **The node image was the one input to a boot that nothing built, and the runtime derives it now.**
  `SOCIALWEB_CLUSTERPROVIDER_IMAGE` named a LITERAL directory and the substrate then ran
  `CheckInputs` over it, so an operator had to compute a fingerprint by hand and paste it into
  configuration — and every way that goes wrong is silent, because a stale fingerprint is a directory
  that exists and validates, so a node boots from an old image and nothing says so. The old guard was
  narrower than the defect: `checkPlacement` did stat the three image files at config load, so a wholly
  wrong path was caught, and what passed silently was a STALE name. `CheckInputs` could not close it,
  because it holds the image to the CONFIGURATION and never to the name the configuration's own inputs
  give. The name is derived now from the same inputs the build fingerprints, and `Fingerprint` stays
  load-bearing as the runtime's answer to "does this name exist" while `CheckInputs` stays the answer
  to "is what is at it the build of the configuration in front of me" — one fact with two readers, which
  is why neither was deleted. A configuration whose inputs moved no longer names a stale image at all:
  it names a directory no build has written, the substrate refuses and says which command writes it, and
  the image built before the change keeps its own name in the store as the rollback it is. The three
  checks the runtime now performs are the manifest's own record hashing to the name — which the runtime
  did not have before and which is the piece that closes the class — then `CheckInputs`, then the
  kernel, initramfs and rootfs being present. The digesting that costs is paid once, at startup, in
  `nodeSubstrateFor` rather than in `load()`, which still refuses an unset value with no default. One
  honest cost is recorded rather than hidden: the runtime digests 2.3 GB twice, once in `Fingerprint`
  and once through `CheckInputs` → `CompareImageToConfig`, and with the name check in place the second
  pass is provably redundant for the file, tree and list dimensions and load-bearing only for the
  manifest's operative fields. One requirement moved with it and operators must know: the four-step
  proof now needs the builder's CONFIGURATION, not merely a built image directory, because an image is
  only valid relative to the configuration whose inputs name it.
- **The node image is built by the control plane's own container build, and the host contract is one
  binary that runs in both places.** Two decisions, taken together because each is what makes the other
  checkable. The first answers a question this document had left implicit — what builds the node image
  and when — and the answer is that it is a stage of the same multi-stage build that produces the
  control-plane image, so the build graph orders it instead of an operator's memory doing so (section
  12). The second is the instrument: `socialweb-computer-kcp-check-host` is a single binary that runs
  the whole host contract, and it is the reason the first decision can be validated rather than
  asserted. Each check reports one of three things — HOLDS, MISSING, or MUST BE PASSED IN — and that
  third category is why one binary serves both environments: on a host the last two collapse into "this
  host cannot run this", and inside a container MISSING means the image is wrong while MUST BE PASSED IN
  means the container is correctly declaring its edge. The checks are drawn only from failures this
  design has already paid for: the guest kernel's measured module set, the xtables extension set that
  makes `iptables-restore` all-or-nothing, `/dev/kvm`, `CLONE_NEWUSER` and pasta, the env-provided
  binaries, free space for a 2.3 GB image, `postgres` as non-root, and the SELinux label whose absence
  presents as an unrelated permission error. What the binary does NOT do is define any of those facts a
  second time, and it does not do it in more places than first written here: the module check is
  `internal/kernelmodules` (the builder's own check, moved out of `cmd/socialweb-nodeimage` so the
  builder and this binary share one definition rather than restating it — the builder's `modules.go` is
  now a wrapper), the pool and the host-network enumeration are `internal/subnetallocator`'s
  `DefaultPool`/`BlockAt`/`HostNetworks` using the same `Overlaps` the allocator's own admission makes,
  the image store is read through `internal/nodefirecracker.LoadImage` — the provisioner's own
  config→fingerprint path, so the check and the boot cannot disagree about which image a configuration
  names — the postgres probe is `internal/clusterprovider`'s own `StartPostgres`, and the lock directory
  is `internal/liveports`'s `LockDir`. This is the reason the count here is worth stating at all: a
  second definition of one fact is how the two drift, and the fingerprint's missing agent was exactly
  that drift. The container that follows is the control-plane half only —
  kcp with its own root directory, kine, a per-cluster postgres and `k3s server --disable-agent` — run
  non-root, because `postgres` refuses root and because "nothing here needs root" is a claim the
  container should prove by construction rather than restate. It takes no permissive flags: not
  `--privileged`, not a wholesale seccomp disable, not the host root mounted in. If a capability turns
  out to be needed, it is granted narrowly and what the grant's width costs is stated. Its validation is
  the strongest reading available without a node: `TestTheControlPlaneReachesReadyAtZeroNodes` drives
  the real reconciler against real leased ports, a real kine, a real per-cluster postgres and a real
  `k3s server --disable-agent` and passes in about 6 s on the host, so running it INSIDE the container
  makes portability a measurement — a pass there is the result, and a failure there is the finding.
  This is also what section 14's last gap was waiting for: the one thing the fakes cannot answer, a live
  kcp serving the mount under `--feature-gates=WorkspaceMounts=true` with an `APIExport` and a tenant
  `APIBinding`, is a thing the repo can now stand up rather than a thing it stands up nowhere.

- **DEV-2.** The development tools were built with go1.23.7 and could not type-check this go1.26.4
  module: gopls, staticcheck and govulncheck all reported errors that do not exist and missed those
  that do, so the repo had no working static analysis and every editor diagnostic was untrustworthy.
  This is the real cause behind the recorded `stale_gopls` trap, whose rule ("the COMPILER is the
  authority") worked around the symptom rather than the tool build. All three were reinstalled with the
  module's toolchain. The before-and-after is one reading: `staticcheck ./...` printed 10 `(compile)`
  lines, each saying the tool was built with go1.23.7 and analysed nothing, and prints 0 now. The
  instrument is `staticcheck ./... 2>&1 | grep -c '(compile)'` returning 0 together with
  `go version -m $(which gopls)` naming go1.26.x.
- **DEP-1.** go.mod pinned `go 1.26.4` while the standard-library fixes that landed in `go 1.26.6` were
  absent, leaving six reachable standard-library vulnerabilities. The pin moved to `1.26.6`. The
  before-and-after: `govulncheck ./...` reported 6 reachable findings and reports 0 now, with 3
  remaining in required modules that are never called. The instrument is `govulncheck ./...` exiting 0
  with no reachable finding.
- **SEC-5.** `apply-permission-inventory.sh` compared the certificate's and the key's public key by
  hashing whatever openssl printed, so when openssl failed it hashed an empty string on both sides, a
  junk certificate and a junk key compared EQUAL, and the "does not parse" branches were unreachable.
  The public key is now captured only on a successful parse. Before and after, three fixtures: a
  junk/junk pair, a valid/junk pair and a mismatched valid pair each `note_fail` and exit 1, where
  before the junk pair passed; a matching pair exits 0. The instrument is those fixtures, which fail if
  the empty-input hash returns.
- **REL-3.** `deploy/control-plane-container/validate.sh` treated the last `go test` exit code as the
  verdict, and `go test` exits 0 on a `--- SKIP` and on `[no tests to run]`, so the validation could
  pass over a proof that never ran. The script now requires a `--- PASS: $TEST` line and refuses both
  skip forms. Three fixtures exercise it, the whole script was driven with stub `go`/`docker`, and a
  real image run gave rc=0 with a 7.20 s PASS. The instrument is a fixture that skips the named test and
  must make the script exit non-zero.
- **REL-2.** `shut` was `PathStatus != 0 && !reached`, so 300, 429, 500 and 504 reaped a serving
  cluster while only 404, 502 and 503 are measured shut; the section 15 text that said "any other
  non-zero code SHUT" was the contradiction being fixed. `shut` is now an allowlist of 404/502/503, and
  300/429/500/504 HOLD with no reap. The new `TestUnmeasuredCodesAreNotShut` failed before and passes
  after, the live mount proof still PASSes (26 s), and the now-dead `reached` helper was deleted. The
  instrument is that test, plus the live mount proof reading `Draining ops=[shut-tenant-path
  await-tenant-path-shut]` with the apiserver still answering.
- **REL-1.** `Probe` returned `(status, true)` for ANY answer, so a control plane whose `/readyz`
  answered 503 was read Reachable and stayed Ready. `reachable` is now `apiserverIsAnswering(code)`
  (200/401). The new `probe_test.go` pins 200/401 reachable and 503/500 not, failed before and passes
  after, and the clusterprovider and materialiser suites PASS. The instrument is `probe_test.go`.
- **SEC-1.** Service-auth tokens of any lifetime were accepted, and a 30-day token worked, so a stolen
  token stayed valid with no bound and no replay record. `MaxServiceAuthLifetime` (5 minutes) with
  `ErrLifetimeTooLong` now refuses one over the cap and accepts one exactly at it; the xrpcserver and
  gateway test mints were over the cap and moved to the constant. A `ponytail:` comment records that a
  validated token stays replayable until its short exp, with a jti set keyed by exp as the upgrade path.
  The instrument is the lifetime check's own tests, which must reject beyond the cap and accept at it.
- **LINT-1.** With staticcheck finally working, it read 16 findings: two tests whose assertion could
  never fail, a dead production constant, dead fields, and `time.Now().Sub`. All 16 were cleared, and
  the vacuous seam checks became compile-time interface assertions plus value checks. `staticcheck ./...`
  is now empty and exits 0. The instrument is `staticcheck ./...` printing nothing and exiting 0.
- **SEC-2.** The gateway served plaintext HTTP on whatever address it was given, including a public one.
  `load()` now refuses a non-loopback host: `0.0.0.0:8080` and `:8080` are refused, `127.0.0.1:8080`
  and `[::1]:8080` accepted. The instrument is a `load()` test over those four addresses, with a
  `ponytail:` comment naming the terminator ceiling.
- **COR-1.** `buildDeps` indexed `cfg.Audiences[0]` before the guard that checks the slice is non-empty,
  so the guard could never fire and nil audiences panicked instead of returning the written error. The
  length guard moved above the call. Nil audiences now returns the written error, pinned by
  `TestBuildDepsRefusesNilAudiencesWithoutPanicking`. The instrument is that test.
- **DEV-3.** The gateway's env file was undocumented and the handoff's settings count was short by
  seven. `deploy/socialweb-gateway.env.example` now lists all 16 `load()` settings, and
  `TestTheEnvExampleLoads` reads the constants out of `config.go` and fails when one is missing, naming
  it. The instrument is that test, which fails when a setting is deleted from the example.
- **SEC-4.** `install-kcp.sh` installed a kcp binary trusted on transport alone. `KCP_SHA256` is now
  pinned to the real sha256 of the v0.33.0 release tarball and checked with `sha256sum -c` before
  extraction, so a corrupt tarball exits 1 before install; there is no upstream signed manifest, so the
  pin was verified by direct download and hash. The instrument is the digest check itself, over a
  deliberately corrupted tarball.
- **REL-5.** The kcp unit used `After=network.target` with no `Wants=` and no `RestartSec`, so at boot
  kcp died on "no default routes found" and gave up after five fast restarts. The unit now uses
  `network-online.target` with `Wants=` and `RestartSec=5s`, installed through `install-kcp.sh`. Boot
  confirmation is operator-side and was not run here. The instrument is a reboot whose `systemctl
  status kcp` shows the unit started after the network came online.
- **REL-6.** The three live proofs used fake registries that answered 200/unconditionally from
  `Spec.Running`, so they could not detect the mount defect they exist to find. All three now answer
  401 live, 503 Draining and 502 reaped from host liveness and `DeletionTimestamp`, and none returns
  200; the live mount proof PASSes. Two honest limits are recorded: the 503 branch is unexercised and
  `subnetRegistry.MountStatus` is never invoked. The instrument is the three fakes answering from
  liveness rather than from `Spec.Running`.
- **DEP-2.** The container base was `fedora:44`, a bare tag, so the base could move under the same name.
  It is pinned to
  `fedora:44@sha256:2cdfedd312a3c92e0d9932ee18d9d2dd80b00fa0bb4ebdaa86736195b5eaa974`, the manifest
  index over four architectures, confirmed identical across buildx and skopeo reads. The instrument is a
  build that resolves the digest rather than the tag.
- **DEV-1.** The no-comments and no-non-ASCII rule had no instrument, so it read as held while broken:
  two `//nolint:gosec` comments violated it. `test/nocomments_test.go` now holds
  `TestNoCommentsAndASCII`, which walks `api`, `internal`, `cmd` and `test` with `go/parser` and fails
  on any comment that is not a `//go:` directive or in a `ponytail:` comment group, and on any byte
  above 0x7F. The two comments were removed. The test was watched failing on a scratch `// x` comment
  (`internal/scratch_comment.go:3:1`) and on a 0xE2 byte (offsets 26, 27 and 28), then passing.
  Invariant 16 now carries the test name. The instrument is that test.
- **REL-4.** "`go test ./...` all green" hid fourteen live tests that SKIP, the four-step proof among
  them, because the only reading anyone reports is the exit code, which is 0 on a SKIP.
  `internal/livegate` now holds `Require` and `Short`: `SOCIALWEB_REQUIRE_LIVE=1` turns every live skip
  into a `t.Fatalf` carrying the same message the skip would have. The before-and-after is two
  readings: `SOCIALWEB_REQUIRE_LIVE=1 go test ./cmd/socialweb-clusterprovider` exits 1 naming
  `TestTheFourStepProofRunsThroughTheCompositionRoot`, where it used to exit 0, and plain `go test
  ./...` still exits 0. The helper is imported only by `_test.go` files, so no production code can
  reach it. The instrument is that env-gated failure.
- **ITER-1.** `-short` did nothing: no test read it, so every edit either ran the live proofs or ran
  nothing, and `internal/clusterprovider` alone measured 71.15 s. `livegate.Short` now skips the long
  tests under `-short`, and `SOCIALWEB_REQUIRE_LIVE=1` overrides that skip into a failure, so the fast
  loop cannot be mistaken for the gate. The reading is `go test -short ./internal/clusterprovider`:
  71.15 s before, 2.28 s wall after, with `SOCIALWEB_REQUIRE_LIVE=1 go test -short` still failing. The
  instrument is `-short` over the long suite together with that override.
- **SEC-3.** Both units ran as root on the L0 host: the internet-facing gateway held uid 0 only to read
  `/var/.kcp/admin.kubeconfig`, and kcp needed no privilege at all. The gateway unit is now
  `DynamicUser=yes` with `LoadCredential=kcp.kubeconfig:/var/.kcp/admin.kubeconfig` and
  `Environment=SOCIALWEB_KCP_KUBECONFIG=%d/kcp.kubeconfig`; the kcp unit runs `User=kcp` with
  `/var/.kcp` owned by that user. `grep '^User=root'` over both files prints nothing, and
  `systemd-analyze verify` adds no new warnings over the pre-change units (its two gateway warnings
  pre-existed and are out of scope). One finding is kept because it changes what an operator must do:
  settings from an `EnvironmentFile=` OVERRIDE the unit's `Environment=`, so an installed env file that
  sets `SOCIALWEB_KCP_KUBECONFIG` would replace the credential path at run time.
  `deploy/socialweb-gateway.env.example` therefore does NOT set that key, and `TestTheEnvExampleLoads`
  now FAILS if the key is added back. The migration hazard is recorded rather than left latent: a host
  that ran kcp as root has root-owned 0600 files under `/var/.kcp` that block the new `kcp` user. The
  installed-unit reading is operator-side and was not run here. The instrument is the two greps, the
  verify comparison, and the test that fails when the key returns.
- **REL-7.** The `pasta` namespace holder is never recorded, so this entry asked whether a hard provider
  kill leaves it behind. Measured with real pasta and no code change, the answer is no: pasta SURVIVES
  a `kill -9` of the provider, but it lives only as long as its child, the agent, which exits when the
  VMM exits, and the VMM IS recorded and reaped, so the sweep tears the chain down and no netns holder
  persists. Before and after were the experiment itself: after the provider kill all three of pasta,
  the agent and the VMM were alive; after the VMM was killed all three were gone and `pgrep -a pasta`
  was empty, reproduced twice. The residual pre-record window became PASTA-1 in section 14. The
  instrument is that kill sequence with its output recorded.
- **SEC-6.** The guest writes the cluster join token as its k3s node password (`GuestTokenPath`,
  `/etc/rancher/node/password`, `<data-dir>/agent/node-password`), so one value carries two roles and
  every node of a cluster shares one node password. Recorded rather than latent, in section 9 with the
  v1 ceiling: harmless at 0-or-1 nodes, inherited by the first multi-node change, where a per-node
  password must be minted instead. The instrument is `grep -n 'node-password' docs/ARCHITECTURE.md`
  printing that section 9 line.
- **DEV-8.** This section claimed to hold no open gaps while the live backlog held eleven, which is the
  list-outliving-its-work defect in reverse: a list emptier than the work. It now carries the open list
  and names `NEXT.yaml` as the live backlog, and the instrument is `sed -n '/^## 14\./,/^## 15\./p'
  docs/ARCHITECTURE.md | grep -c 'PASTA-1'` printing at least one.
- **DEV-4.** Section 12 never named six live internal packages, and with no comments in code their
  purpose was stated nowhere: `atprotoauth`, `execplugin`, `nodeprovider`, `plcresolver`, `plcsweep`
  and `sweepkcp`. Each now has a line in section 12 stating what it does. The instrument is `for p in
  $(go list ./internal/...); do grep -q "$(basename $p)" docs/ARCHITECTURE.md || echo $p; done`
  printing nothing, where before it printed six lines.
- **DEV-6.** Four backlog entries described work already done (the memory file's salvage entry, the
  image-consumer wiring, the `nodeExpiry` producer and STEP 2 hammering), as did the code references to
  `internal/nodetransport` and `processrunner.Reach`, both gone. Each was struck in the memory file and
  in `handoff.yaml` with its evidence line; `handoff.yaml` had gone missing from the tree and was
  restored from this session's record. The instrument is `grep -n 'Record the salvage list'` over the
  memory file printing nothing under its "Open" heading, where before it printed one line.
- **DEV-7.** A 144-file `https:` kubectl cache tree had returned and `.gitignore`'s `/https:*/` hid it,
  turning a visible defect into an invisible one. The tree was deleted and the ignore removed, so a
  recurrence shows in `git status`. The producer is still unidentified and is recorded as unverified:
  no tracked Go or shell file sets a cache dir or runs kubectl with its home set to a server URL
  relative to the repo. The instrument is `test ! -e 'https:'` after a full `go test ./...` and one run
  of each deploy script.
- **DEV-5.** `README.md` pointed at a `LICENSE` file that did not exist, so the public-domain grant it
  claimed was not in the tree. The Unlicense text is now at `LICENSE`. The instrument is
  `test -f LICENSE && grep -q 'unlicense.org' LICENSE`, which before failed at `test -f`.
- **SEC-3 (follow-up).** `deploy/socialweb-gateway.env.example` was created by DEV-3 with all 16
  `load()` settings, and it carries the kubeconfig-key guard above: it does NOT set
  `SOCIALWEB_KCP_KUBECONFIG`, so an installed env file cannot override the gateway unit's credential
  path, and `TestTheEnvExampleLoads` fails if the key is added back.
- **PASTA-1.** A provider killed between `processrunner.Start` (`internal/nodefirecracker/node.go:98`)
  and `recordLocked` (line 112) left no `node.json`, so a fresh provider's sweep read `ErrNotExist` and
  reaped nothing, and the firecracker VMM plus its pasta netns holder survived. The fix was chosen by a
  measurement rather than a guess: real pasta was run and both `kill -9` and `kill -TERM` of pasta LEFT
  its child alive, while killing pasta's child made pasta exit, so recording pasta would not have closed
  the window; the process whose death cascades is pasta's child, the namespace child. The namespace
  child now writes its own single-process record (pid, start time and `NamespaceIdentity`, which is
  `["namespace", <dataDir>/namespace.json]`) as its first instruction, before it wires the namespace or
  starts a VMM, so the record exists even if the provider dies before doing anything; the provider's
  existing `recordLocked` still overwrites it with the VMM record on the normal path. `LivenessOf` and
  `Reap` now verify the recorded pid against EITHER the VMM identity or the namespace identity, still by
  pid, start time and cmdline, so reap-never-adopt is unchanged: a record matching no identity, carrying
  no identity, or with a mismatched start time is still left untouched. A stand-in test drove
  `Host.Provision` into the window (pasta stand-in forks, the child records itself, no VMM), then
  asserted a record existed, that it verified as `LivenessOurs`, and that `noderecord.Reap` killed the
  recorded pid. Before, `node_test.go:442: pasta started and its namespace child is running, but no
  record names that child: ... node.json: no such file or directory`, and the new tests are
  `TestReapKillsARecordedNamespaceChildOnlyUnderItsOwnIdentity` and
  `TestTheSweepReapsANamespaceChildAPreviousGatewayLeft`; after, PASS. The existing not-ours tests still
  pass and still assert nothing is signalled. One remainder stays uncovered and is stated: between pasta
  forking its child and the child reaching its first instruction there is still no record, but no VMM
  can exist yet and the child records itself regardless of the provider's death, so only a transient
  pre-exec shell can be orphaned. The instrument is the stand-in test that drives `Host.Provision` into
  that window and the sweep test that reaps a namespace child a previous gateway left.

- **The deployment is ONE container. The host runs a container runtime and nothing else.** Until this
  decision the repo shipped two substrates at once: the control-plane half was built and proven as a
  non-root container, while the DEPLOYED shape was host systemd units (`deploy/install-kcp.sh` for kcp,
  `deploy/socialweb-gateway.service` for the gateway), a manually started `socialweb-clusterprovider`,
  and each cluster's process group as children on the host. That split is what produced SEC-3 and REL-5
  -- two hardened host units -- and a list of operator steps (install the unit, `chown /var/.kcp`, point
  an env file at a credential path, reboot) that exist only because the process runs on the host. The
  deployment scenario is now ONE image and ONE `docker run`. The image starts kcp, the gateway, the
  credential plugin and the clusterprovider; the clusterprovider starts each cluster's postgres, kine
  and `k3s server --disable-agent` INSIDE the same container; the firecracker guest is the only thing
  outside a process boundary. The grant the node half needs is MEASURED and stated rather than
  described: the four-step proof passes inside the container with `--device /dev/kvm` (the VMM),
  `--device /dev/net/tun` (the guest's tap), `--security-opt seccomp=unconfined` (Docker's default
  profile blocks `CLONE_NEWUSER`) and `--cap-add NET_ADMIN,SYS_ADMIN` (the namespace wiring pasta and
  the tap need). That is wider than "one narrow grant" and is recorded as what it costs: no
  `--privileged`, no `--network host`, no host root mounted in. With those four, `check-host` answers
  `19 checks: 19 HOLDS, 0 MISSING, 0 MUST BE PASSED IN` inside the image. A narrower set is an open
  question to be measured, not assumed. The host systemd units are retired rather than maintained.
  The acceptance is the four-step proof running inside that container -- PASS, 99.12 s, with the node
  joining and being reaped -- because a deployment that cannot run the proof is not the deployment,
  and an operator step that only exists because of where a process runs is a step this removes rather
  than documents. The reachability gap is closed the same way: the gateway serves plaintext and binds
  container loopback, so the container terminates TLS itself -- a terminator on the container's
  published port forwarding to the loopback gateway, using an operator certificate when one is
  supplied and a self-signed one whose SHA-256 fingerprint is logged otherwise. The plaintext hop
  never leaves the container, SEC-2's refusal is untouched, and `docker restart` on the same volume
  now comes up Ready rather than refusing (the provider adopts its store before deriving the argv
  that names the DSN, and a recycled pid no longer looks like a live postmaster). Which grants the
  node half needs, the terminator's ceiling (`socat`, single-backend, no client certificates) and any
  narrower grant set are stated as measurements to make, not as assumptions.
- **Consequence of the one-container shape, stated rather than discovered.** The container's network
  namespace is its own, so the subnet allocator sees fewer networks than the host does and its
  host-network overlap refusal is narrower than the host's (section 14's first limit); the allocator's
  `HostNetworks` seam is where a real collision would be handed in. `liveports.LockDir()` is likewise
  per-container (section 14's second limit). Neither has a production path that depends on it. The node
  image is built inside the image, so the host needs no `/lib/modules`, no kernel and no firecracker
  binary at run time; the build still reads this host's kernel and module tree, which is what makes the
  guest's module set measured rather than assumed.

- **The two-level vault layout is DECLARED in Go, and the unseal policy is DECIDED rather than
  deferred. This entry states where each secret lives, what unseal costs, and the one conflict it
  does not resolve.** The layout is `internal/vaultlayout`, plain Go values later code turns into
  API calls, and `vaultlayout.Validate` FAILS on a policy referencing a path no mount provides, a
  consumer with no role, a role referencing a missing policy, a PKI role whose max TTL exceeds its
  mount's, or a secret class assigned to NEITHER vault or to BOTH. The test drives every one of
  those to a failure, so the check is known to be able to fail.

  **ROOT vault - one per deployment, in the container beside kcp, on the state volume.** It holds
  the cross-deployment credentials and nothing per-cluster: the gateway's kcp admin kubeconfig for
  kcp's own API (`<KCP_ROOT>/admin.kubeconfig`, token-only, `shard-admin`, which never rotates -
  ADR 0020), kept at `secret/data/socialweb/kcp/admin`; kcp's adopted mechanism-B material - the
  `client-ca` certificate kcp is handed as `--client-ca-file` (ADR 0027) and the `.admin-token-store`
  hash of the `shard-admin` token, at `secret/data/socialweb/kcp/client-ca` and
  `secret/data/socialweb/kcp/admin-token-store`; the central rustfs S3 credentials the per-cluster
  `kine` store rides (section 4), at `secret/data/socialweb/store/rustfs`; the deployment's ATProto
  device-key audience and registry binding (`SOCIALWEB_GATEWAY_DEVICE_KEY_AUDIENCE`,
  `SOCIALWEB_GATEWAY_DEVKEY_REGISTRY`), at `secret/data/socialweb/devicekey`; and the container TLS
  terminator's operator certificate and key (`SOCIALWEB_CONTAINER_TLS_{CERT,KEY}`), at
  `secret/data/socialweb/container/tls`. The root vault also carries the PKI mount `pki/`, whose
  role `kcp-client` issues the gateway's scoped client certificate under the `socialweb.local`
  domain, client-auth only, key usage digital signature plus key encipherment, `max_ttl` 720 h
  against the mount's 8760 h. The CA private key is generated inside that mount and never readable;
  only its certificate is handed out. Root vault unseal material lives OUTSIDE the vault,
  operator-held.

  **CLUSTER vault - one per ON cluster, started in that cluster's process group beside the
  embedded-postgres ADR 0025 sequences.** It holds per-cluster identity custody and nothing
  cross-deployment, so its KV paths are cluster-relative: the 13-entry identity bundle at rest at
  `secret/data/k3s/identity`, the exact `identitybundle.Paths` list section 7 measures; the instance
  join token at `secret/data/k3s/instance-token`; the postgres role and password (`postgres.pw`,
  section 5) at `secret/data/k3s/postgres`; and the per-cluster SSH key at `secret/data/k3s/ssh`,
  kept only while the substrate that dials a market guest over SSH is in use. The tenant credential
  is not here: it is derived from the bundle's `client-ca.{crt,key}` at mint time, never stored
  (section 7, ADR 0023).

  **The unseal policy, decided.** Both vaults auto-unseal from a key file on the state volume that
  holds their data, written `0600` and read by the process that starts the vault. Stated plainly
  rather than buried: this is a real weakening. An attacker who reads the state volume gets the
  ciphertext AND the unseal key in one read, so the vault's encryption is integrity at rest against
  a stolen disk, not confidentiality against a host that is already compromised; on the cluster
  vault the volume is the cluster's own data directory, which the gateway process already holds.
  What makes it acceptable at v1 is that the volume is the deployment's own state, the container is
  the trust boundary, and the alternative - operator-held Shamir shares - would make every cold
  start a manual step, which for a cluster vault that comes up per ON cluster is an operator page
  per cluster. What would change it: the day a vault secret's compromise outlives the host it sits
  on - a root secret a tenant can reach, a CA key used to forge rather than to restore - the root
  vault moves to Shamir shares held off-host or to a KMS/HSM seal, and the cluster vault follows
  only if its bundle's loss stops being recoverable by re-provisioning the cluster. `ponytail:`
  auto-unseal from a file on the same volume makes the vault exactly as strong as the volume; move
  the unseal file to a separate trust domain when a vault secret's loss exceeds the cost of an
  operator-held unseal.

  **The conflict, stated not resolved.** ADRs 0020, 0023 and 0025 decided the identity bundle's
  durable home is OpenBao: ADR 0023 records the harvest into `secret/data/k3s/<cluster>/identity`,
  and ADR 0025 puts the `sha256` bundle verify between `Vault.Ready` and `k3s start`. Sections 5,
  7 and the entry below decided the opposite, and the CODE does today: the bundle is the file tree
  at `<DataDir>/server/tls` plus `cred/`, seeded and harvested by `identitybundle.Harvest` and
  `identitybundle.Seed`, because k3s reads `client-ca.key` and its siblings from disk at RUNTIME -
  `kube-controller-manager`'s `cluster-signing-*-client-key-file` and k3s's own cert handlers
  (ADR 0020) - and a cold start with the key absent OVERWRITES `client-ca.crt` with a fresh,
  self-signed CA. OpenBao can therefore be the bundle's AT-REST CUSTODY and its wake-time source,
  never its runtime location. "OpenBao secures everything" means every secret has a vault home and
  a least-privilege reader; it does not mean k3s reads its identity from a vault, and it does not
  mean the file tree is going away.

- **The two-level vault is real, not a launcher.** OpenBao was in this repository as a launcher that
  was never switched on: config rendered, argv, a lease, bring-up order and a readiness gate, and
  nothing that initialised it, unsealed it, enabled an engine, wrote a policy or role, or consumed a
  secret. The deployment image did not even carry the `bao` binary. Both levels now run and hold
  secrets, and the measurements are these, all taken in the one container.
  - **Root vault**, one per deployment, a child of the entrypoint on the fixed loopback port 8200
    (`internal/rootvault`), storage on the state volume. It is initialised 5-of-3, unsealed, and
    carries `secret/` as kv-v2 and `pki/`; the policies and AppRoles are the ones
    `internal/vaultlayout` declares, and the gateway's cross-workspace kcp admin kubeconfig is stored
    at `secret/data/socialweb/kcp/admin`. `cmd/socialweb-vault apply-root` does this after kcp is
    ready and before the gateway starts, and fails the container if it cannot. The unseal material is
    `$STATE_DIR/openbao/root-unseal.json`, mode 0600. Least privilege was measured rather than
    asserted: a token minted through the gateway AppRole reads its own secret and is refused 403 on
    another path. Re-running `apply-root` succeeds and leaves the PKI root unchanged. Its listener is
    MUTUAL TLS, not plaintext (VAULT-2 below): the entrypoint's `socialweb-vault prepare-root` step
    generates the deployment CA, the server certificate and the client certificate under
    `$STATE_DIR/openbao/tls/` before `bao server` starts, and the listener stanza carries
    `tls_require_and_verify_client_cert = true` with `tls_client_ca_file` pointed at that CA, so a
    client with no certificate, or one from another CA, is refused in the handshake.
  - **Cluster vault**, one per ON cluster, in the per-cluster process group on its leased port (20001
    in the run below, beside postgres 20000 and k3s 20003). It is initialised and unsealed, carries
    `secret/` and `pki/`, the cluster policies `identity-bundle`, `instance-token`, `postgres` and
    `ssh`, the AppRoles `cluster-data`, `cluster-identity` and `cluster-node`, and it holds all 13
    identity-bundle entries at `secret/data/k3s/identity`. Its PKI engine IMPORTS the cluster's own
    `client-ca` so issued certificates chain to the CA the tenant apiserver already trusts, and the
    tenant client credential is now issued from the vault: `TestTheControlPlaneReachesReadyAtZeroNodes`
    passes with a vault-issued credential, and a test proves the leaf chains to the imported CA. The
    unseal material is `<cluster data dir>/openbao/unseal.json`, mode 0600.
  - **What is honestly not yet true, stated rather than implied.** The unseal material lives on the
    same volume as the ciphertext, so the vault is integrity-at-rest against a stolen disk and not
    confidentiality against a host that can read the state volume; k3s still reads `client-ca.key` and
    its siblings from the file tree at `<DataDir>/server`, so the vault is durable CUSTODY and a
    wake-time source, never the runtime location -- the ADR-versus-architecture conflict recorded
    above is exactly this; the vault's store is now the postgres backend on the cluster's own
    embedded-postgres, database `openbao`, and `procgroup`'s refusal to start OpenBao without that
    store is a dependency met on every path -- a deployment whose kine store is external still starts
    the postgres for the vault, so "no local postgres" no longer means "no vault"; three root secret
    classes (`kcp-admin-token-store`, `kcp-client-ca`, `central-store-credentials`) have no producer in
    the container yet and are logged with named reasons rather than faked; and the layout's `ecdsa`
    key type is translated to OpenBao's `ec` at the boundary because the PKI role endpoint rejects the
    former. `tenant-superuser` is an extra PKI role beyond the layout, added because the declared
    `kcp-client` role structurally cannot issue the `system:admin` common name.
  - **Terraform was not used.** Section 5 states the rule: everything is a controller-runtime
    reconcile loop or a deploy config, and no new language. The vault configuration is Go, driven by
    `internal/vaultlayout`'s declarative layout, applied by `cmd/socialweb-vault` and by the
    clusterprovider's vault step.
- **`apply-root` presents a client certificate to the root vault, the transport the cluster vault
  already had. CLOSED.** Before, the root-vault config rendered `api_addr = "http://127.0.0.1:8200"`
  and `tls_disable = true`, and `cmd/socialweb-vault` reached it with `openbao.New(addr, token)` over
  plaintext, so the cross-deployment credentials -- the kcp admin kubeconfig and the device-key
  binding -- rode a transport any local process could dial. After, `internal/rootvault.ServerConfig`
  renders `api_addr = "https://127.0.0.1:8200"` and one listener with `tls_disable = false`,
  `tls_cert_file = "$STATE_DIR/openbao/tls/server.crt"`, `tls_key_file =
  "$STATE_DIR/openbao/tls/server.key"`, `tls_client_ca_file = "$STATE_DIR/openbao/tls/ca.crt"` and
  `tls_require_and_verify_client_cert = true`. The deployment's own CA -- not any cluster's -- signs
  BOTH the server certificate, whose SANs cover `127.0.0.1` and the container's hostname, and the
  single client certificate `cmd/socialweb-vault` presents. `vaultcert.EnsureRootVaultTLS` reuses the
  per-cluster CA and signing code rather than a second crypto implementation, writes `ca.crt`,
  `server.crt`, `server.key`, `client.crt` and `client.key` at mode 0600 under a 0700 `tls/`
  directory, and reuses them across restarts, regenerating only when a certificate is inside the
  renew window or a required SAN is gone. The entrypoint's `prepare-root` step generates the material
  and the config before `bao server` starts; `apply-root` loads the client certificate and the CA from
  the state volume. `internal/openbao` gained `WithClientCertificate(tls.Certificate)` as the client
  half of the trust `WithCACertPool` already carried, and the one `apply-root` client and the layout
  API's own HTTP client present it. The root token remains the AUTHORISATION layer for the admin
  operations; the certificate is transport, not authority, and was not weakened to make mTLS easier.
  The readings, all on this host with bao v2.6.2: **(a)** the rendered config carries TLS on and no
  `tls_disable = true`; **(b)** a client presenting NO certificate is refused in the handshake
  (`remote error: tls: certificate required`); **(c)** a client certificate from a DIFFERENT CA is
  refused (`remote error: tls: unknown certificate authority`); **(d)** `apply-root` completes
  presenting the generated certificate and a second run leaves the `pki/ca/pem` bytes unchanged;
  **(e)** two `EnsureRootVaultTLS` calls return the same CA and the same server certificate, the
  server leaf chains to that CA for `serverAuth` and the client leaf for `clientAuth`, and the SANs
  cover `127.0.0.1` and the test hostname (the control that removes a covered host flips the reading
  and regenerates). `SOCIALWEB_LOCAL=1 go test ./cmd/socialweb-vault -run
  'TestTheRootVaultRefusesClientsWithNoOrAnotherCertificate|TestApplyRootPresentsTheGeneratedClientCertificateAndIsIdempotent'`
  PASS 0.11 s and 0.18 s; `deploy/check.sh --local` 0 fail, 13 artifact-gated skips; the end-to-end
  gate `deploy/test-rig/run.sh` exits 0 on the committed tree with **761 PASS, 0 FAIL, 0 SKIP** and
  `PROOF: the live gate passed with no skips`, which is the proof the mutual-TLS path runs in the
  product image: the entrypoint's `prepare-root` and `apply-root` both completed before the gateway
  started, and the two live tests pass 20 times over under `-count=20` in that image.
- **The dev loop was measured, and it was rebuilt around the measured cost.** Across 32 subagent
  logs the loop spent its time in four places: 72 poll loops containing `sleep`, each blocking a
  turn for 300-400 s; 94 `docker logs`/`exec`/`inspect` calls, most of them inside those loops; 49
  reads of this document, then 2331 lines; 8 failed edits; 3 subagent calls that died on the
  nesting depth limit; and 2 sessions that ended with a build still running. Four changes followed.
  The loop is fast by default and the gate can fail: `deploy/check.sh --fast` (about 9 s end to
  end; a bare `go test -count=1 ./...` is 5.6 s, where it was 73.8 s before), `--local` for the
  proofs this host carries (about 75 s, and it NAMES each of the 14 artifact-gated tests it
  skipped), and `--full`, which fails on any skip of a live input and therefore only passes where
  the artifacts are present -- on this host it exits non-zero naming the missing firecracker and
  kubeconfig inputs. An earlier revision of this change made the artifact-gated `Require` skip
  even under the full gate, which is the REL-4 defect again: a function that cannot fail is not a
  gate. `Require` now fails under `--full`, `RequireLocal` fails under `--local` when an input the
  host is assumed to carry is absent, and the skip it can take is named rather than silent. A readiness helper replaces the sleep loops:
  `deploy/wait-ready.sh <container> [timeout-seconds]` polls `docker logs` at 1 s under a hard
  timeout (default 60), returns 0 on this provider's own readiness line
  (`"msg":"cluster provider: status"` with `"phase":"Ready"`), and prints the last 40 log lines and
  exits non-zero on timeout or container exit. Measured on this host: a container whose stack
  reached Ready returned 0 in 30 s (30.03 s wall); a container that never emitted the line returned
  1 in 3.1 s with its last log lines, where the loop it replaces would have blocked for 300-400 s
  and still not failed; and a container that exited first was caught immediately on the same
  non-zero path. A working-rules file, `AGENTS.md` at the repo root, names the one small file that
  answers each question -- the objective, the layout, the invariants, the decisions, `NEXT.yaml`,
  the clusterprovider and gateway environment, how to run the container -- so a subagent reads one
  file instead of ten large ones and instead of this document end to end, and states the loop
  rules, the edit discipline, the no-work-in-flight rule and the standing constraints. The node
  image build is now layered so it is not paid on every container build: the `nodeimage` stage
  COPYs only the inputs the guest agent build reads (`go.mod`, `go.sum`, `cmd/socialweb-nodeagent`,
  `api`, `internal`) instead of the whole repo, so the 48-module check and the 4 GiB image build
  stay cached across unrelated edits. Measured: a `build.sh computer` that re-ran the nodeimage
  stage took 62 s; the next one took 13 s with `[nodeimage] RUN` and `[extract] COPY` reported
  CACHED; editing `cmd/socialweb-gateway/main.go` took 49 s and left the nodeimage stage CACHED
  while only `gobuild` re-ran. The ceiling is stated where it belongs: if the node agent ever
  imports a new top-level package outside `api`/`internal`/`cmd`, the agent build fails loudly and
  that directory must join the COPY list.
- **The per-node mTLS work broke the deployment it was meant to secure, and the fix is one
  listener, not two.** The first shape gave the cluster vault a second listener on the cluster's
  gateway address so the guest could reach it. Binding that address needs root or
  `net.ipv4.ip_nonlocal_bind`, so `bao` exited, the loopback listener died with it, and the
  container sat at `phase: Initializing` for ever -- measured on the documented USER-1000 run as
  `openbao never became ready: ... dial tcp 127.0.0.1:20001: connect: connection refused`. The
  same shape also failed the guest for a different reason: pasta forwards guest traffic to the
  host LOOPBACK, where the plaintext admin listener sat, so the guest got `server gave HTTP
  response to HTTPS client`. One listener on `127.0.0.1:<leased port>` with TLS ON fixes both:
  the certificate carries SANs for `127.0.0.1` and the gateway address, the admin client speaks
  HTTPS and trusts the vault CA (`internal/openbao.WithCACertPool`), and the guest still dials
  the gateway address because pasta lands it on loopback. Measured after: the container reaches
  `phase: Ready` in 30 s as `uid=1000(kcp)` with no `--user 0`, no sysctl and no host route
  (`deploy/wait-ready.sh` exited 0 in 29.99 s), and a real firecracker guest read its own secret
  through CERT auth -- `KCP_AGENT_VAULT node=socialweb-node1 path=secret/data/k3s/node/socialweb-node1
  state=read`. The lesson is the batch's own: the change that added a listener for a guest was
  never run as a guest or as USER 1000 until it had already broken both.
- **External Secrets Operator is wired to the cluster vault, with two costs stated.** The
  `ClusterSecretStore` and `ExternalSecret` are applied to the tenant apiserver by the
  clusterprovider after `Bootstrap` (`internal/eso`), reading `deploy/eso/*.yaml` from the image
  and pointing at `https://<gateway>:<port>` with a client certificate minted from the cluster
  vault. Two things had to be true and are recorded rather than hidden. Upstream ESO's two
  validating webhooks default to `failurePolicy: Fail`, and their pods are Pending while the
  cluster has zero nodes, so the apiserver rejected the `ClusterSecretStore` with `no endpoints
  available`; the deployed bundle sets `failurePolicy: Ignore`, whose cost is that CRD validation
  is best-effort while the controller is down. And the ESO identity's policy had to gain
  `auth/token/lookup-self` (read) and `auth/token/revoke-self` (update), because the provider
  validates its token before reading a secret and the CERT role disables the default policy;
  without them the store reported `InvalidProviderConfig` with a 403. The ESO identity is still
  denied a node's secret path and the identity bundle, and a node's identity is still denied
  `lookup-self`. The controller runs at one node and is Pending at zero; end-to-end
  materialisation of `Secret/users/openbao-demo` is now MEASURED. Two things stood between the
  accepted objects and a materialised Secret, and both are closed. First, nothing authored the value
  the ExternalSecret reads: `vaultcert.WriteESODemoSecret` existed but was called by no production
  path, so `secret/data/k3s/eso-demo` was empty and the ExternalSecret had nothing to copy. The
  clusterprovider's tenant add-ons now write it (`{"demo": "openbao:<cluster>"}`) before minting the
  ESO certificate. Second, the tenant grant's `socialweb-mint-identity` ValidatingAdmissionPolicy
  admitted only `system:admin` to create Secrets in `users`, so the External Secrets controller
  (`system:serviceaccount:default:external-secrets`) was refused and the ExternalSecret read
  `Ready=False reason=SecretSyncedError`, message `secrets "openbao-demo" is forbidden:
  ValidatingAdmissionPolicy 'socialweb-mint-identity' ... denied request`. The policy's Secrets arm
  now also admits that one controller ServiceAccount: it is the provider's materialiser, not a tenant
  credential, and no tenant subject was added for either verb. The arm names the ESO install namespace
  `default`, which is where `deploy/eso/install.sh` puts the controller; installing ESO in another
  namespace needs this arm and the message updated to match. Measured with a container built from
  this tree and a fresh volume, with no hand apply of any object: `socialweb-node1` Ready,
  `deployment/external-secrets` 1/1, the store `Ready=True reason=Valid` ("store validated"), the
  ExternalSecret `Ready=True reason=SecretSynced` ("secret synced"), `Secret/users/openbao-demo`
  (ON A FIRECRACKER CLUSTER: the store's URL is the cluster's subnet gateway, which that substrate
  binds and routes and `market` does not -- see ESO-3 in section 14, measured failing on production)
  `data.demo` = base64("openbao:socialweb"), and `demo` = `openbao:socialweb` at
  `secret/data/k3s/eso-demo` in the cluster vault -- the Secret equals the vault value. The node was
  booted by the scale driver rather than a test harness: ESO's Deployment is not addon-owned, so its
  Pending pods read as intent and the next node-scale pass provisioned a firecracker guest that joined
  as `socialweb-node1`, which is where the controller then scheduled. Instrument correction: the
  acceptance test names a store condition of type `Valid`, which ESO v2.11 does not emit -- it uses
  `Ready` with `reason=Valid` -- so `...conditions[?(@.type=="Valid")].status` reads empty even when
  the store is valid; the same jsonpath with `@.type=="Ready"` returns `True`.
  Applying 25 CRDs needs `--server-side --force-conflicts`, because two of ESO's CRDs exceed the
  262144-byte client-side annotation limit.
- **The tenant add-ons applied an External Secrets store to a cluster no operator was installed
  into.** The pass applies the `ClusterSecretStore` and the `ExternalSecret` through `internal/eso`,
  and ESO's own install lives in `deploy/eso/install.sh` -- a script the image carries and NOTHING
  ran: no Go path exec'd it, the container entrypoint has no ESO step, and
  `SOCIALWEB_CLUSTERPROVIDER_ESO_MANIFEST_DIR` names only the directory the two YAML files are read
  from. On a cluster the provider brings up, the apiserver therefore served no
  `external-secrets.io/v1` and answered the store apply with `the server could not find the requested
  resource`: measured in production on a market cluster as `clusterprovider: installing the tenant
  add-ons for test-cluster-alice-0001 after its bootstrap: eso: applying the ClusterSecretStore
  openbao: the server could not find the requested resource`, once every 2 s, with `kubectl get crd`
  against that tenant apiserver answering `No resources found`. The pass now reads the store resource
  itself (`eso.Installed`: one `List` of `clustersecretstores` with `Limit: 1`), and a 404 from it
  means the operator is absent, so the pass runs the shipped installer against the tenant apiserver
  with a kubeconfig rendered from the provider's own tenant credential
  (`clusterprovider.Kubeconfig`) and re-reads the resource BEFORE applying the store; a resource that
  answers means the installer does not run again, so a steady cluster pays one `List` per pass. The
  probe is deliberately a RESOURCE read and not a discovery read: the same request is the one the
  production failure is made of, so its classification is the measured one, and the alternative was
  measured to be wrong on this deployment -- kcp answers every `/apis*` path with
  `forbidden: User "kcp-admin" cannot get path "/apis/external-secrets.io/v1": access denied` for a
  tenant credential, so a `ServerResourcesForGroupVersion` probe would have turned a 404 into a 403
  and failed the pass for a second reason. A cluster the installer cannot reach now fails with a
  message naming the group and the installer's own output rather than with an unknown kind. Two costs
  are stated rather than hidden: the installer fetches the pinned upstream bundle (v2.11.0) from its
  release URL at tenant bring-up, so a provider host with no route to github.com fails the pass with
  curl's error naming the host, and `ESO_BUNDLE` is the escape hatch that points it at a local copy;
  and `bash`, `curl` and `kubectl` are inputs of this step, all three carried by the image today.
  Watched to fail: with the install moved after the apply,
  `TestTheOperatorIsInstalledBeforeTheStoreIsApplied` recorded
  `secrets,clustersecretstores,externalsecrets,install` against the wanted
  `install,secrets,clustersecretstores,externalsecrets`, and `TestAFailedInstallLeavesTheStoreUnapplied`
  showed the store going out over a cluster with no operator. `NEXT.yaml` ESO-3.
- **The clusterprovider's test package did not build, so the loop's own gate could not run.** Commit
  c57d870 added the `CredentialSource` parameter to `MintTenantCredentialForDataDir` and left two
  calls in `internal/clusterprovider/vault_tls_growth_test.go` at three arguments, so `go vet` and
  `go test ./...` refused the package on `main` as well as on this branch, and what a reader would
  have seen is a compile error rather than a test result. Both calls name
  `CredentialSourceVaultIfSealed`, which is the path that test exercises: it mints against a running
  vault after the tenant add-on pass has asked the vault's certificate to cover one more host.

- **The container build now produces the qemu node image too, and the live gate runs inside a
  privileged, ephemeral rig. SETTLED 2026-09-25.** Before: the `nodeimage` stage built one image from
  one configuration, so the qemu image was a host-built artefact a fresh container did not have; and
  `SOCIALWEB_REQUIRE_LIVE=1 go test ./...` had no self-contained way to run, because the live inputs
  are paths -- the two images, the two VMM binaries, the agent -- that only a host holding the
  artefacts could name. After: a second stage, `nodeimage-qemu`, builds the qemu image from
  `nodeimage-qemu.json`; `extract` copies both stages' `/dist` into one root; the `computer` image
  carries both images and both runtime configurations. `cmd/socialweb-rig-inputs` derives the inputs by
  reusing `internal/nodefirecracker.LoadImage` and the builder's own configuration parsing rather than
  restating the fingerprint, and `deploy/test-rig/run.sh` starts the image through the product's own
  `container-entrypoint.sh` and runs the gate inside it. MEASURED 2026-09-25: one `build.sh computer`
  produced the firecracker image
  `sha256-6f0163f5b34f4b7c793709cdbb90c74a9df700466f9d8ed6c9dedb2569319e3e` and the qemu image
  `sha256-e356da745efe1dab4b6c233045f33b491cc7cfe0851fdaa5856501991a565afd` in 122 s wall, and the
  runtime configurations resolved to the same two names inside the container, which is the cross-check
  that the build-time and runtime configurations fingerprint alike. The product stack was Ready in 33 s;
  the qemu guest boot `TestALiveQEMUGuestBootsAndIsReaped` passed in 0.93 s and the firecracker guest
  boot `TestLiveTheGuestsGatewayIsAnsweredOnItsOwnLink` passed in 2.47 s. The security boundary is
  unchanged and stated where it was: the relaxed flags (`--device=/dev/kvm --device=/dev/net/tun
  --security-opt seccomp=unconfined --security-opt apparmor=unconfined`, with no added capability)
  exist only on the rig's own command line, and the strict `validate.sh` proof was re-run against the
  new image as non-root with no device, no capability and no host network, printing
  `PROOF: --- PASS: TestTheControlPlaneReachesReadyAtZeroNodes (8.08s)`. NOT VERIFIED: the whole
  `SOCIALWEB_REQUIRE_LIVE=1 go test ./...` gate was not run to a passing verdict, because a tenant
  cluster and its identity inputs are the operator's; the rig names the eight underivable variables and
  the tests they gate, and runs a named subset on request.

- **Stopping a process that had already exited returned its non-zero exit status. CLOSED
  2026-09-25.** `processrunner.Process.Stop` signalled the child, then waited, and swallowed a
  non-zero exit only when the signal was DELIVERED. When the child had already exited, `Signal`
  returned `os.ErrProcessDone` and `Stop` returned `Wait`'s raw `*exec.ExitError`, so stopping a
  node whose process was already gone failed with `exit status 1`. Before: in the container rig,
  `TestALiveQEMUGuestBootsAndIsReaped` failed on teardown with `nodeqemu: stopping the process
  hosting node liveweb-node1's guest: exit status 1` in two consecutive runs (0.90 s and 2.40 s),
  including with `-p 1`, while every `internal/nodefirecracker` live test passed. The cause is the
  ORDER inside `nodeqemu.Teardown`: it reaps the guest VMM by identity, the guest's death takes
  pasta with it, and only then is `Stop` called on pasta, which has already exited. The host path
  passed the same code because pasta was still alive when it was signalled, so the
  `*exec.ExitError` branch swallowed the same non-zero status. After: the `os.ErrProcessDone`
  branch falls through to that same wait-and-swallow path, so a process that has already exited is
  stopped successfully and only a process that refuses to die returns an error. MEASURED
  2026-09-25: the unit test `TestStopSucceedsWhenTheChildAlreadyExitedNonZero` (a child that exits
  7, waited for, then `Stop` must return nil) failed before with `Stop: exit status 7` and passes
  after; in the rig `--- PASS: TestALiveQEMUGuestBootsAndIsReaped (0.91s)` and the combined
  `SOCIALWEB_REQUIRE_LIVE=1 go test ./internal/nodeqemu ./internal/nodefirecracker -count=1 -p 1`
  passed both packages with no `--- FAIL`; on the host
  `TestTheFourStepProofRunsThroughTheCompositionRoot` passed both substrates in 218.68 s
  (firecracker 99.76 s, qemu 118.92 s). The rig entry above recorded the qemu boot as passing in
  0.93 s before this fix: where it passed, pasta had not yet exited when it was signalled, which is
  the timing dependence the fix removes.

- **The change that made the loop fast broke the container's own validation, and the gate could not say
  why.** Reducing the default test run to the fast loop made `RequireLocal` skip unless
  `SOCIALWEB_LOCAL`, `SOCIALWEB_LIVE` or `SOCIALWEB_REQUIRE_LIVE` is set. `deploy/control-plane-container/validate.sh`
  drives `TestTheControlPlaneReachesReadyAtZeroNodes` with none of them, so the test SKIPPED and the
  script failed every run -- the instrumentation that proves the container was disabled by the change
  that was meant to speed it up. It was found not by running it but by the qemu agent reading the
  parent tree against its own. Fixed by passing `-e SOCIALWEB_LOCAL=1` into that container run:
  `validate.sh` now prints `PROOF: --- PASS: TestTheControlPlaneReachesReadyAtZeroNodes (8.39s)` and
  exits 0, still showing the cluster vault holding all 13 identity-bundle entries. The second half of
  the same defect was that `check.sh --full` printed only the `--- FAIL` lines, so an operator saw a
  test name and not the input it wanted: it now prints each failing test's own message, which names the
  missing variable, as `--full` does above on this host. The instrument now says what it needs.

- **CHK-1 was refuted before it was worked, and the refutation is the entry.** A build log printed
  `MISSING host.qemu`, and an item was opened saying `check-host` misreports a host that runs qemu
  guests. Running the check instead of reading the line settled it: on this host `check-host -config
  nodeimage.json` answers `21 checks: 21 HOLDS, 0 MISSING`, and inside the product image the line reads
  `HOLDS host.qemu` with the binary present (installed at Dockerfile:145-146 and symlinked into
  `/usr/local/bin`). The MISSING line came from the nodeimage BUILD stage, which installs only `tools`
  and needs no qemu, and `-build` is explicit that only the checks a build reads decide its exit
  status (`main.go:71-72`), so the stage's 10 MISSING are runtime facts it does not need. No code
  changed. The lesson is the batch's oldest one applied to a claim rather than to code: the item was a
  sentence greener than the fact, and an item is not real until the artefact has been run.

- **The vault's store was `file` while the design, the guard and the deployment all said the
  per-cluster postgres.** Section 6 states the vault's store is its own embedded-postgres and section
  9 recorded the file backend as being on an expiry, but `OpenBaoServerConfig` rendered `storage
  "file"` under `<data dir>/openbao/data`. The guard in `procgroup` refused OpenBao unless the local
  postgres was up -- true of the design, false of the config as rendered -- and `PurposesFor` and
  `openStore` leased and started no postgres when `SOCIALWEB_CLUSTERPROVIDER_STORE_ENDPOINT` named an
  external store, so such a deployment silently started no vault at all. BEFORE: the rendered config
  carried `storage "file" { path = "<data dir>/openbao/data" }` and a fresh vault's health body read
  `Storage Type file`; a stack with an external store spawned no postgres and its vault step never
  came up. AFTER: the config carries `storage "postgresql" { connection_url = ... }` and the health
  body reads `Storage Type postgresql`; the vault's database is `openbao` while kine's is `k3s`, so
  `TestTheClusterVaultKeepsItsDataAcrossARestartOnPostgres` finds `openbao_kv_store` in `openbao` and
  `to_regclass('openbao_kv_store')::text = 'absent'` in `k3s`; the same test starts a vault, unseals
  it, writes a KV value, restarts the vault on the same store and reads the value back;
  `TestAnExternalKineStoreStillStartsThePostgresTheVaultNeeds` shows an external kine store leaving
  kine on the operator's endpoint while the per-cluster postgres still starts for the vault;
  `procgroup` still refuses a vault whose store is not up, and its message now names the store; and
  `TestAVaultPointedAtAnotherClustersStoreDoesNotAuthenticate` shows a vault holding one cluster's
  unseal material cannot open another cluster's store. Instrument: `deploy/check.sh --local` on this
  host runs the vault tests against real postgres and bao (13 artifact-gated tests named as skipped,
  none of them these), and the live gate runs them with `SOCIALWEB_REQUIRE_LIVE=1`.

  The backend name and its key were read off the bundled `bao` v2.6.2 rather than remembered:
  `storage "postgres"` answers `unknown storage type postgres`, and `storage "postgresql"` with a DSN
  starts and its health body reports `Storage Type postgresql`. The decoupling chosen for an external
  store is to KEEP running the per-cluster postgres for the vault: section 6 makes that postgres the
  vault's store, so a deployment whose kine rides central S3 still owes the vault its own store, and
  silently starting no vault was the one outcome wrong under either reading. `Stack.NeedsLocalPostgres`
  is true when kine writes locally OR the Stack contains OpenBao, and only a stack with neither skips
  the postgres; a nil `VaultStoreDSN` for a vaulted stack is refused by name (`ErrNoVaultStore`).

  MIGRATION, both paths stated. The clean path is `bao operator migrate` from the old `storage "file"`
  config to the new postgres one, which preserves the root token and every secret; it needs the old
  config, the new one and a stopped vault. The path IMPLEMENTED is the cheaper fallback, which needs
  no code: the postgres store comes up uninitialised, `EnsureVault` sees `initialized:false` and
  re-initialises, writing new unseal material and a new root token, and the next guarded pass
  re-harvests the 13-entry identity bundle that still sits in the file tree at `<DataDir>/server`.
  Its consequence, stated rather than improvised: the vault is RE-AUTHORISED (new root token, new
  unseal keys) and the CAs are UNCHANGED, because the bundle comes from the file tree, not from the
  vault; the old file store is left on disk at `<data dir>/openbao/data` and is no longer read.

- **The privileged gate could run against a tree that moved under it, and that is how it lied about the
  vault store.** `deploy/test-rig/run.sh` mounts the repo read-only at `/src` and runs `go test ./...`
  from it, so the tree it tests is the tree on disk, changing while it runs. The vault agent launched
  the rig before committing; the run reported
  `FAIL: TestAnExternalKineStoreStillStartsThePostgresTheVaultNeeds` with `postgres on port 20000
  exited with exit status 1` -- 20000 being the port the live deployment holds in that same container --
  against a version of the test that never became a commit. The committed test leases
  `liveports.PostgresWindows` (31300-31399), which cannot hand out 20000, and it PASSes in 1.73s; the
  re-run against the committed tree read `1026 PASS, 0 FAIL, 0 SKIP`. The guard is now structural rather
  than a habit: the rig refuses a dirty tree unless `KCP_RIG_ALLOW_DIRTY=1`, records HEAD and the
  uncommitted-path count before the run, and re-reads both afterwards, failing with
  `the result describes a tree that is not any commit` if either moved. Watched failing first: one
  appended newline to `README.md` made it refuse before any build. The lesson is not "run the artefact"
  but the harder version of it -- run the artefact at a revision, because a reading that names no commit
  can be trusted wrongly in either direction, and here it produced both a false red and a commit made on
  a red gate.

- **VAULT-1's premise was half wrong, and running `kcp` is what showed it.** The item said to produce
  "kcp's client CA and its admin token store from the kcp root directory". A real `kcp start` writes
  `admin.kubeconfig  .admin-token-store  apiserver.crt  apiserver.key  sa.key  audit.log`: the
  shard-admin token store is real, at `<KCP_ROOT>/.admin-token-store`, and there is NO client-CA file --
  `--client-ca-file=<missing>` fails because kcp generates none. A producer reading
  `$KCP_ROOT/client-ca.crt`, which is what this item would have been implemented as, would never have
  fired and the class would have kept reading as a gap. The real source is the root vault's own PKI CA
  at `pki/ca/pem`, the CA the `kcp-client` role issues the gateway's certificate from
  (`cmd/socialweb-vault/sources.go:48`). The token store is written as the hex key `token-hash`.
  `central-store-credentials` is required only when `SOCIALWEB_CLUSTERPROVIDER_STORE_ENDPOINT` names an
  external store and is otherwise reported NOT-APPLICABLE on a distinct line, never as the silent
  "not written"; a partial set of the four variables is a hard error, because a half-configured store
  is not a store. Readings: `apply-root` logs 0 `root secret not written`; `client-ca` reads back
  byte-identical to `pki/ca/pem`; `admin-token-store` equals `xxd -p .admin-token-store`; deleting that
  file exits 1 naming the path. The correction is the entry: an item is a claim about where a fact
  lives, and this one named a file that `kcp` does not write.

- **The vault's tenant PKI role pins the organization, so the only identity the vault can issue is
  the superuser's.** `internal/clusterprovider/tenantcredential.go` had one path to a tenant
  credential and it produced `system:admin` / `system:masters` - the reason being that the
  vault-backed branch issues through the PKI role named by `VaultPKIRoleTenant`, and that role
  pins `organization: system:masters` on every certificate it signs, while the non-vault branch
  hardcoded the same pair. A CI runner or a controller that took that credential would hold
  cluster-admin on the tenant, which is the "wider credential" `ErrNoClientCA`'s own text refuses
  to reach for, and it makes any RBAC written against the runner decorative. The vault path
  cannot be widened either: the PKI role is `pki/roles/tenant-superuser` (written by
  `writeTenantPKIRole` in `internal/clusterprovider/vault.go`, named by `VaultPKIRoleTenant`) and
  its role config pins `organization: system:masters`, so every certificate it issues carries that
  group regardless of what the caller asks for. Closed by adding a second, disk-only entry point -
  `MintTenantCredentialForClusterAs(ctx, stateDir, cluster, server, commonName, organizations,
  validFor)` - which signs with the cluster's ClientCA directly and takes no vault path, so an
  arbitrary CommonName and Organizations are honoured. The superuser entry points are unchanged;
  the two are now distinguishable by name rather than by argument. Readings: a mint with
  `COMMON_NAME=gitlab-runner` and `ORGANIZATIONS=socialweb:runners,system:authenticated` yields a
  certificate whose parsed subject carries exactly those, with a single verified chain to the
  signing ClientCA; the same test asserts the CommonName is not `system:admin`. A second reading,
  taken because the CLI parsed a `VALID_FOR` it never used - a knob that looked like it worked and
  did not - is the certificate's own `NotAfter` at `VALID_FOR=1h30m`: `delta=1h29m59s`, read off
  the written `tls.crt` rather than off the configuration. The fact is recorded here rather than
  left in the code as a comment, per the rule that a decision recorded in a comment is deleted the
  next time comments are swept.

- **A real GitLab pipeline runs a job on a tenant cluster. CLOSED 2026-10-05.** Before: every proof
  in this tree stopped at the Kubernetes API. The GitLab Runner's own API calls were never made by
  a runner, so "GitLab CI works here" rested on a vendored document and on a Pod shape a test
  applied by hand. After: `TestAGitLabPipelineRunsOnATenantCluster`
  (`cmd/socialweb-clusterprovider/live_gitlab_pipeline_test.go`) stands up a tenant cluster at zero
  nodes, mints a tenant credential from that cluster's own ClientCA, writes the runner's
  `config.toml` with `executor = "kubernetes"` and the credential as `cert_file`/`key_file`/
  `ca_file`, starts the real `gitlab-runner` binary against a real GitLab CE, commits a
  `.gitlab-ci.yml` through the API and triggers a pipeline. MEASURED 2026-10-05: PASS in 34.12 s.
  The job pod's own `Pending` state drove the scale decision (`intent and no node`), a node joined,
  GitLab's runner logged `Waiting for pod ... to be running on the node liveweb-node1`, two job
  pods were observed carrying `spec.nodeName = liveweb-node1`, and the pipeline reported
  `success`. Three things about this are worth keeping. First, the runner had to be told the
  apiserver's LOOPBACK address with a client certificate, not the kcp mount with a bearer: the
  mount is a reverse proxy and the exec/attach paths CI needs are not guaranteed to survive it,
  which is the same fact `NEXT.yaml` GL-2 records from the other side. Second, the harness drives
  the scale decision only when a test asks it to, so a pipeline that merely runs while the harness
  waits leaves its pod Pending for ever - the first attempt failed exactly that way and the fix is
  in the test, not in the product. Third, a job Pod runs INSIDE a guest, so `CI_REPOSITORY_URL`
  naming the GitLab host's loopback resolves to the guest's own loopback and the clone fails with
  `exit code 128` and a `script_failure` that reads like a broken runner; the proof sets
  `GIT_STRATEGY: none`, and the three ways out are recorded in `docs/gitlab/03-jobs.md`.
  NOT closed by this: the CLI half of GL-1 (GL-2 has since closed - see section 15).

- **The cloud controller manager now runs, and it is IN the process group. CLOSED 2026-10-05.** A
  controller that nothing starts is a design and not an installation, and that was the state:
  `internal/cloudprovider` and `cmd/socialweb-cloud-controller-manager` existed, were unit-tested,
  and nothing ran them. Closed by making the controller the fifth member of a tenant cluster's own
  process group. `procgroup.ComponentCloudControllerManager` sits after `k3s server` in both
  `DefaultOrder` and `StackOrder`, so `Reap` stops it first; it is present by default in the
  composition root. It is PORTLESS - it dials the apiserver and listens on nothing - so
  `portallocator` gained no purpose and no lease. Its credential is minted scoped, from the cluster's
  own ClientCA, into `<DataDir>/cloud-controller-manager.kubeconfig` (invariant 20 forbids the k3s
  admin kubeconfig); its cluster, credential and snapshot paths ride `Spec.Env` and never argv
  (invariants 3 and 13); and its readiness gate is its own `phase=Ready` log line, taken after the
  Node informer syncs - it binds no port, so that line is the only signal, and it is a positive
  marker that can fail (invariant 17). MEASURED 2026-10-05:
  `TestTheFourStepProofRunsThroughTheCompositionRoot` PASSES in 110.45 s with the fifth component,
  and its STEP 3 asserts `kubectl get node liveweb-node1 -o jsonpath={.spec.providerID}` reads
  `socialweb://liveweb/liveweb-node1` with the label `node.socialweb.computer/cluster=liveweb`.
  TWO DESIGN CLAIMS DID NOT SURVIVE CONTACT WITH THE CODE, and both are recorded because each would
  have shipped as a plausible sentence. First, RBAC was not a follow-on: a scoped identity with no
  binding is authenticated-and-denied, so the informer never syncs and the gate times out. The
  provider now creates the ClusterRole and ClusterRoleBinding for the controller's group once k3s is
  up, in the same change that introduced the identity. Second, the snapshot's absent-versus-empty
  distinction does not exist in the reader: `internal/cloudprovider/fileinstances.go` answers
  `(known=false, err=nil)` for an absent file AND for an empty array, so both are the delete path.
  Writing `[]` protects nothing; what protects a live Node is that the snapshot LISTS it. The sharp
  edge is stated rather than smoothed over - the snapshot file is load-bearing infrastructure and not
  a cache, and a snapshot that reads as absent is read as "every guest is gone". A snapshot that
  exists and cannot be parsed IS an error and patches nothing (invariant 14); the absent case is not,
  and that asymmetry is the risk this entry leaves behind. Split out rather than folded in:
  the stated limit in `NEXT.yaml` for a portless process-group member, which cannot be found by a
  record-based sweep and is therefore not swept.

- **The scale decision could not express more than one node. CLOSED 2026-10-05.** Before: `Decide`
  returned `Provision, Nodes: 1` for any intent, so one pending pod and fifty pending pods decided
  identically, and a second concurrent CI job waited rather than being placed. After: `Demand(o)` is
  a separate pod-work COUNT, `Options.MaxNodes` (default 1) and `Options.PodsPerNode` (default 8)
  bound `Target` at `ceil(Demand / PodsPerNode)` clamped to `[1, MaxNodes]`, and `Decision.Nodes` is
  that target total. `scaledriver` calls `Provision` once per missing node until the re-read count
  reaches the target, and each node gets a distinct id (`node1`, `node2`, ...) whose ordinal is read
  back from the tenant cluster's own Node list rather than remembered. `Intent` stays a boolean and
  `Demand > 0` agrees with `Intent` on every fixture, so the zero direction is untouched. MEASURED
  2026-10-05: `TestThreeGitLabJobPodsScaleToThreeNodes` (`live_scalemax_test.go`) PASSES in 95.65 s
  with `SOCIALWEB_CLUSTERPROVIDER_SCALE_MAX_NODES=3` and `..._SCALE_PODS_PER_NODE=1` - the decision
  reads `Provision, Nodes: 3` with reason `intent and no node`, `kubectl get nodes` reports three,
  three GitLab Runner job pods reach `Succeeded`, and past the 1m hold all three are reaped with the
  control plane still `Ready`. A second, independent run of the same test reported 90.01 s; the two
  runs used different leased apiserver ports (62102 and 62402), which is the same point the four-step
  reading makes - the port is not a constant. Unit readings: `Target` is 0 at no demand, 1 at one
  pod with
  `PodsPerNode == 8`, 3 at seventeen pods, and 4 at a hundred pods with `MaxNodes == 4`, while the
  default ceiling pins seventeen and a hundred pods to 1; at the default ceiling every existing
  `Decide` case reproduces its previous action and count and the two reachable reasons are
  byte-identical to the old strings; a failed scale-out stops the loop and leaves `State.Nodes` at 0;
  the demand/`Intent` agreement case covers 20 fixtures. The two prior live proofs were re-run after
  the harness change and still pass: the GitLab job pod in 84.93 s and the four-step in 100.53 s with
  `INSTANCE_START_TO_READY_S=5.420`. WHAT THIS DOES NOT SHOW, stated because the numbers read
  stronger than they are: the test asserts the CLUSTER reached three nodes, not that three guests
  each hosted work - the scheduler placed all three pods on `liveweb-node1`, which the design permits,
  so the evidence for three simultaneous guests is three `Provision` calls plus `kubectl get nodes`
  listing three, together with an independent observation of two firecracker processes during the run.
  No negative control was run either: that the test would fail with `MaxNodes` unset is argued from
  `Target`'s ceiling rather than measured. Partial scale-in is deliberately not implemented (plan
  0002 section 4.4), so the target only grows while intent exists.

- **The live backlog was declared in four places and did not exist.** `AGENTS.md:18`,
  `README.md:262`, `handoff.yaml:155` and section 14 of this document all name `NEXT.yaml` as the
  live backlog - section 14 calls itself its mirror and carries the syntax to read it - and
  `ls NEXT.yaml` answers `No such file or directory`. The file is not gitignored; it is absent.
  This is the reverse of the defect this document keeps recording: not a list that outlived its
  work, but a work list that never had a file, so the five references pointed at a page no operator
  could read while the open items themselves lived only in section 14. Closed by writing the file
  rather than by striking the references, because section 14 already held the items and the
  references were the correct design: `NEXT.yaml` now carries ESO-2 and the four items this
  iteration opened, each with the instrument that would close it. The reading is the two commands
  that disagree: the four references, and `ls` on the path they name. The class is worth keeping in
  view because it is the same one a green check is - a pointer with no referent reads as presence.

- **The door's verbs: createCluster, a chosen kubeconfig lifetime, the public front, and an owner
  who can act.** Four things stood between a real user and a usable cluster, and each was a gap
  between two parts that were individually finished. First, `createCluster`
  (`com.publicdomainrelay.temp.cluster.createCluster`) did not exist, so the k8s door reached a live
  cluster only by calling `getKubeconfig` for a kubeconfig it would throw away and then
  `controlPlaneUp`, in that order, and had to know the order. It is now one verb: it walks
  `getKubeconfig`'s create-on-read path (`revocation.CreateOnRead` around `Store.Ensure`, so the deny
  is consulted before anything is created) and then the desired-state flip `controlPlaneUp`
  performs, and answers the phase. It declares the same six errors as `controlPlaneUp`, including the
  retryable `ClusterInitializing` with its `Retry-After`. `getKubeconfig` KEEPS its create-on-read:
  that is what makes a first login work. Readings: the answerability check in `cmd/socialweb-gateway`
  now drives seven verbs, `internal/xrpcserver` drives `createCluster` past authentication and
  validates its output against the lexicon, and `internal/gateway` asserts one `Ensure` and a
  `Running` desired state, a second call still idempotent, and a revoked identity refused with zero
  creates. Second, a device credential's lifetime was whatever the deployment chose and a caller
  could not ask for anything else. `getKubeconfig` now takes an optional integer `ttlSeconds`, signed
  by `internal/devicekey` with the requested TTL when one is given and with
  `SOCIALWEB_GATEWAY_DEVICE_KEY_TTL` when none is. `SOCIALWEB_GATEWAY_DEVICE_KEY_MAX_TTL` is the
  ceiling, and when it is unset it EQUALS the TTL, so the default answer to "may a caller extend its
  own credential" is no. A value below 60s or above the ceiling is `InvalidRequest` and is REFUSED
  rather than clamped, because a caller that asked for a lifetime and silently got a different one
  has been told something false about the credential in its hand; the check runs BEFORE the create
  path, so a refused request writes nothing. A gateway with no ceiling configured refuses any
  explicit lifetime rather than granting an unbounded one. Readings: tables in `internal/gateway`
  (absent, floor, ceiling, one below, one above, zero, negative, no ceiling at all) and in
  `internal/xrpcserver` (the same window driven through the real lexicons and the real transport).
  Third, the kubeconfig a user held pointed at the address the GATEWAY uses.
  `SOCIALWEB_GATEWAY_KCP_PUBLIC_HOST` now names the public kcp front in the rendered `server:`
  (`<public host>/clusters/<MountPath>`), and `SOCIALWEB_GATEWAY_PUBLIC_MINT_URL` adds
  `SOCIALWEB_MINT_URL=<value>` to the exec block's env, which `cmd/socialweb-credential` reads and
  which nothing had ever set. The gateway's OWN kcp client keeps `SOCIALWEB_KCP_HOST`, so the
  loopback address and the user-facing address are now two settings instead of one that could not be
  both; with either public setting unset the rendered document is byte-for-byte what it was. Both are
  validated as absolute https URLs at startup, because client-go silently drops the exec credential
  plugin for a server that is not https, and `kubeconfig.AssertClean` still passes on the document
  that carries the mint URL. Readings: `internal/kubeconfig` asserts the env entry is present when
  configured and absent when not; `internal/kcpclient` asserts the granter is handed the public host
  with the trailing slash trimmed and the requested TTL, and that no request reaches kcp from a verb
  that only derives a path; `internal/devicekey` asserts a requested 90s really is what gets signed.
  Fourth, and the one that made the rest useless: the tenant grant scoped its mint Role to ONE
  hardcoded ServiceAccount, `did-mruwiotxmvrduzlymfwxa3dffzrw63i`, and no `did-*` ServiceAccount had
  any RBAC at all, so a real user's minted token authenticated and could do nothing.
  `deploy/tenant-grant.yaml` is now a TEMPLATE: it carries `__SOCIALWEB_OWNER_SERVICE_ACCOUNT__`
  where the derived name goes, and `Bootstrap` renders it with `ServiceAccountName` of the `Cluster`'s
  `spec.account` — the owner, and the same DID the mount path is derived from —
  via `kcpclient.RenderTenantGrant`, which refuses a document with no placeholder. The grant grew a
  `ClusterRoleBinding` `socialweb-owner` binding that ServiceAccount to `cluster-admin`, because it is
  the owner's own cluster and the credential has to be able to create namespaces, exec into pods and
  drive a GitLab runner. The `gwmint` Role is NOT widened: it is still scoped by `resourceNames` to
  exactly one derived name, and the owner's power comes from its own binding rather than from the
  mint's, so the tenancy boundary section 7 states is unchanged. Readings: `internal/tenantclient`
  renders the deployed file and still fails on a `*` or an underived `resourceName`;
  `internal/clusterprovider` asserts every `resourceNames` entry and the binding's subject are the
  owner's derived name, that the binding's `roleRef` is `ClusterRole cluster-admin`, that rendering
  for a different owner produces different names, and that a second `ensureGrantServiceAccounts`
  pass creates nothing new; `cmd/socialweb-rig-inputs` renders before it derives
  `SOCIALWEB_LIVE_GRANTED_SA`. The re-apply of the objects themselves is a server-side apply and is
  not exercised here: the client-go dynamic FAKE cannot apply a patch it did not first create, so a
  test of it would be a test of the fake. WHAT THIS DOES
  NOT SHOW, stated because the unit readings are strong and the live ones do not exist here: no run
  in this tree has yet created a cluster through `createCluster`, followed a kubeconfig whose server
  is a public front, or measured that a real user's minted token can `create namespaces` against a
  real tenant cluster through that binding. The instrument is the container-mode stack plus the k8s
  door: `createCluster` once, a `kubectl` through the public host, and a job whose script needs
  `pods/exec`. Until that runs, the owner's power is asserted from the document and the API, not
  measured.

- **A node can be rented from the market: NodeClaim and the market substrate.** Before this, a node
  was either a guest THIS host booted (firecracker, qemu) or nothing, and section 2 declared the
  market out of scope at the `ComputeProvider` seam. A tenant cluster's nodes are now rented as THE
  CLUSTER'S OWNER through that owner's single atproto OAuth session, held in a separate Deno process
  (the k8s door) that this repo does not build. The hand-off is a new CRD and one new substrate.

  The CRD is `NodeClaim`, `socialweb.computer/v1alpha1`, cluster-scoped in its workspace, written by
  the clusterprovider into the CLUSTER'S OWN HOME workspace - the logical cluster `Ref.Home` the
  registry already derives - and listed by the door across `clusters/*` through the `clusters`
  APIExport's virtual workspace, which is why the resource is in `deploy/cluster-apiexport.yaml`,
  schema `v1alpha1-1.nodeclaims.socialweb.computer`, applied by `deploy/install-provider.sh`. Its
  spec is `cluster`, `nodeName`, `ownerDid`, `apiserverPort`, `advertiseAddress`, `tokenFile`,
  `nodePasswordFile`, `serverCAFile`, `denyBidderDids`; its status is `phase`
  (`Pending|Bidding|Provisioning|Joining|Running|Failed|Released`), `contract`
  (`vmUri|rfpUri|acceptUri|receiptUri|receiptCid|submitEventRef`), `fqdn` and `message`. The Go side
  writes spec and metadata and NEVER status: `setPhase` on the Go side, the door's `replaceStatus` on
  the other, and `kcpclient.WriteNodeClaimTo` patches an existing claim's spec only when it differs,
  so a live claim is not rewritten on every pass.

  The substrate is `internal/nodemarket`, selected by `SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE=market`.
  `Provision` mints-or-reads the per-node password at `nodevm.NodePasswordPath(DataDir)`, records
  `Pending` on the existing Node CR, writes the claim and RETURNS - it does not wait for the market,
  because a claim takes minutes and the scale pass runs every 2 s for every cluster. `ConfirmJoined`
  reads the claim and requires `Running`: the door writes that only once the node's own agent reached
  its apiserver, so a tenant cluster reporting the node before then is a node something else
  registered. `Fatal` returns the claim's `status.message` while the phase is `Failed`, so the join
  wait ends with the door's own words instead of a timeout (invariant 28). `Teardown` and `Reap` delete
  the claim and then WAIT, bounded by
  `SOCIALWEB_CLUSTERPROVIDER_MARKET_RELEASE_TIMEOUT` (default 2m), for it to disappear, because the
  door removes its finalizer only after the market VM is deleted: closing is not destroying
  (invariant 18), and a claim still there when the wait runs out is a machine still being paid for and
  is reported as a failed release rather than a completed one. `NodeExpiry` is the explicit `0` that
  means UNLIMITED, not nil: no door session cap applies to this substrate, because the door holds the
  owner's session for the node's whole life, and an ABSENT expiry refuses teardown for ever
  (invariant 9).

  Three smaller decisions came with it, each of which had to be made rather than assumed. **(a)** The
  address a market node dials is `SOCIALWEB_CLUSTERPROVIDER_MARKET_ADVERTISE_ADDRESS`, default
  `198.18.255.254`: not loopback, because k3s refuses a loopback address for the in-cluster
  `kubernetes` Service endpoint, and NOT an address this host carries, because the guest puts it on
  its OWN loopback and the door reverse-forwards it to the apiserver, which stays bound to
  `127.0.0.1`. That is a third case in `portallocator`'s advertise invariant
  (`AssertMarketAdvertiseInvariant`), because neither existing case fits: the local-process case
  REQUIRES a host-carried address and the guest case requires one of the cluster's own subnet range.
  The same address goes into `--tls-san`, for the reason the guest gateway does: the certificate has
  to cover the address the node's kubelet actually connects to, or the join reads as an x509 name
  error rather than as a missing SAN.
  `SOCIALWEB_CLUSTERPROVIDER_ADVERTISE_ADDRESS` is refused under this substrate, and so are the VMM
  binaries, `NODE_AGENT`, the image settings and `NETWORK=pasta`: none of them can be in force here,
  and an operator who set one and saw no complaint would believe it was carrying it. **(b)** The node
  count a scale pass OBSERVES now includes every node this clusterprovider HOLDS whose own cluster
  does not yet list it - because a market node is absent from its tenant cluster's node list for
  minutes while the pass runs every 2 s, and without that the decision reads zero nodes and asks
  again on every tick. `internal/scale.Decide` is unchanged; the counting happens in the observe
  closure `cmd/socialweb-clusterprovider/main.go` hands `scaledriver`. This was first written as
  "held hosts whose claim is not yet `Running`", keyed on the claim's phase, and that predicate was
  itself the defect closed as MARKET-4: the door writes `Running` when the node's agent reaches the
  apiserver through the forward, which is a moment BEFORE the Node object appears in the cluster's
  node list, so a claim in that gap was in no list at all. The predicate is now the tenant cluster's
  own node list, and it is the only list that can be. **(c)** The startup sweep
  (`sweepNodeRecords`) skips the walk under this substrate with a logged decision, because a market
  node's machine is not a process on this host and no identity can be derived for it; what that leaves
  open is the sweep MARKET-1 has since closed (section 15).

  The instrument is `go test ./...`. New: `internal/nodemarket` (the claim's fields and paths
  survive `Provision`; a second `Provision` leaves one claim; `ConfirmJoined` refuses anything short
  of `Running` and carries the door's message for `Failed`; a teardown the door never releases is a FAILURE and one it does is not;
  a failed read during a release is not a claim that is gone), `internal/kcpclient` (the claim is
  created in the home workspace with the door's exact field names, is not rewritten when its spec
  matches, is patched when it does not and never through `/status`, and an absent claim decodes to
  nil), `api/v1alpha1` (the wire field names and the seven phase literals), `internal/portallocator`
  (the market advertise invariant, and that it rejects loopback, the unspecified address and every
  address this host carries), `internal/clusterstack` (a stack cannot carry both dials), and
  `cmd/socialweb-clusterprovider` (the market substrate builds with no VMM, agent, image, pasta or
  `/dev/kvm`; two scale passes over a `Pending` claim ask for exactly ONE node; a claim is counted
  until its own cluster's node list names it and not one moment earlier; a second pass over claims
  that have all reached `Running` with none of their nodes listed rents nothing; the teardown issues
  exactly one claim delete; the
  sweep is a no-op; and the market join needs nothing but its own advertise address).
  `internal/scale`, the four-step proof and the live GitLab tests are untouched and still use
  firecracker.

- **One provider serves every cluster, and a cluster is named by its home.** The k8s door writes
  `root:<accountHash>:<name>` plus a `Cluster` CR plus the `:clusters` mount for every cluster any
  atproto user creates, and NOTHING materialised one unless a process was started with
  `SOCIALWEB_CLUSTERPROVIDER_JOIN` naming it. Worse, a cluster was looked up by its bare name, so two
  accounts that each named a cluster `ci` shared ONE state dir, ONE set of port leases, ONE set of
  node names, ONE `nodes.json` and ONE `scaledriver` state map — a collision that reports as one
  cluster reading another's state rather than as an error. The key is now
  `kcpclient.ClusterKey(home)` = `<accountHash>-<name>` (invariant 30), used for the state dir, the
  port and subnet leases, the node names, the snapshot, the scale driver key, the mint credential, the
  `NodeClaim` name and the startup sweep's process-group attribution (section 12). `JOIN=*` reconciles
  every `Cluster` in the tree each pass, serially, with one failure logged and skipped rather than
  returned (invariant 23 says why it is logged at all); startup already walked the whole tree and now
  the shutdown reap does too, by asking the `LocalHost` which groups it holds. An existing
  single-cluster deployment is migrated by rename at startup, and only where the bare name is
  unambiguous (section 12). Two costs are recorded rather than hidden. **(a)** The node names change
  with the key, so a cluster that already had nodes under `<name>-node1` sees `<key>-node1` after the
  migration and its `nextOrdinal` no longer recognises the old name. The tenant Node object is not
  renamed, because renaming it is a write into a running cluster's apiserver and the shell that
  carried the node outlives the rename; the old node is reaped by the ordinary zero-state path and
  re-provisioned under the new name, which is the same work an OFF/ON cycle does. **(b)** A
  `NodeClaim` written before the migration is named for the bare cluster and is not renamed, so it is
  released and re-claimed once.
  Two smaller defects closed with it. `tenantNodes.Provision` no longer polls the tenant cluster for
  a market node before returning: a market machine is absent for minutes and the poll is 30 s, so a
  single new claim blocked every other cluster in the tree for the whole timeout, once per cluster,
  serially. The market substrate now returns once the claim is written and the snapshot updated, and a
  later pass confirms the join or fails the node on `Fatal` (invariant 31, section 11); the guest
  substrates are unchanged. And the market advertise default moved from `100.127.255.254` to
  `198.18.255.254`: the old value is inside `100.64.0.0/10`, which is Tailscale's CGNAT space, so a
  tailnet peer can hold exactly that address and the guest's loopback forward collides with the
  tailnet. `198.18.0.0/15` is RFC 2544 benchmarking space, never routed on the internet, and
  `AssertMarketAdvertiseAddress` still refuses loopback, the unspecified address and every address this
  host carries.

  The instrument is `go test ./...` plus `deploy/check.sh --fast`. New:
  `cmd/socialweb-clusterprovider` (two accounts naming a cluster `ci` get distinct state dirs, leases,
  node names and mint paths; an all-clusters pass over three CRs, two ON and one OFF, brings up
  exactly the two and reaps the OFF one; a cluster whose CR cannot be read does not hold back the
  others; a market `Provision` returns in well under a second with no tenant Node present and still
  writes exactly one claim; a later pass confirms a node the tenant cluster reports and tears down one
  whose claim reads `Failed`; a legacy state dir, node dir, mint credential and port and subnet leases
  are migrated to the key; a bare name two accounts share is left where it is). `internal/portallocator`
  and `internal/subnetallocator` gained `Rekey`, `internal/materialiser` gained `LocalHost.Clusters`,
  and the four-step proof and the live tests are untouched.

- **The cluster vault's certificate can be asked to cover one more host without any client losing the
  CA it trusts. CLOSED, and the CA key written beside the CA is what closed it.**
  `vaultcert.EnsureVaultTLSCertificate` minted a brand new CA and leaf whenever the hosts it was asked
  for were not already on the leaf, then wrote `ca.crt` over whatever was there, and it never wrote the
  CA key at all - so a CA could not be reused and every host-set change forced a new anchor. Two
  callers ask a market cluster's vault for different hosts: bring-up asks for
  `SOCIALWEB_MARKET_ADVERTISE_ADDRESS` and loopback (`internal/clusterprovider/stack.go`), and the
  tenant add-on pass asks for the `10.100.0.0/16` gateway `internal/subnetallocator` leases and
  loopback (`cmd/socialweb-clusterprovider/eso.go`). So the second call replaced `ca.crt` while the
  vault the first call had configured was still listening and serving the leaf it loaded at start,
  which OpenBao does not re-read. Every later client built its pool from the new `ca.crt` and verified
  it against the old leaf. Reproduced against a real `bao`: the mint failed with `x509: certificate
  signed by unknown authority (possibly because of "x509: ECDSA verification failure" while trying to
  verify candidate authority certificate "socialweb-vault-tls-ca")`, and it stayed failed for the life
  of the process because the trust anchor had moved under a listener that never re-read it. The CA key
  is now written 0600 beside `ca.crt`, the CA is minted only when there is none, and a caller that
  needs one more host re-signs the leaf under the SAME CA to cover the union of the hosts the leaf
  already names and the hosts asked for. Readings, on one run of the reproduction: the CA's SHA-256
  first eight bytes were `d6e56d5aea1e43d7` before the tenant add-on pass and `d8491e1f7319d3b2` after
  it, and the mint failed; with the change the CA stayed `829b657ffabfe2a0` across the same pass and
  the mint succeeded against the same running vault. Pinned by
  `TestTheVaultTLSCAOutlivesTheHostsItsCertificateCovers`,
  `TestTheVaultTLSCertificateCarriesEveryHostItHasBeenAskedFor` and
  `TestTheVaultTLSCAKeyIsWrittenBesideTheCAItSignsWith` in `internal/vaultcert`, all three of which
  fail against the old writer, and by `TestTheVaultStillMintsAfterTheTenantAddOnPassAsksForAnotherHost` in
  `internal/clusterprovider`, which starts a real `bao` and reproduces the production message verbatim.
  A cluster whose `ca.crt` predates the CA key is kept rather than re-anchored - a leaf that is still
  usable is returned as it is, because replacing the anchor breaks every client and every node
  certificate already issued - and the host such a cluster cannot cover is section 14's VAULT-TLS-1.
  The cluster vault and the root vault deliberately differ here, and the difference is the listener:
  `EnsureRootVaultTLS` REPLACES its whole material, CA included, when the host set no longer matches
  (`TestRootVaultTLSRotatesWhenAHostItMustCoverIsGone` pins it), and that is sound because every root
  vault client re-reads `LoadRootVaultTLS` before it dials; a cluster vault's clients do not, and the
  `bao` serving the leaf reads it once. So the cluster vault grows the leaf under a stable CA and the
  root vault rotates both. Neither is the other's bug.

- **The tenant add-ons pass awaited an operator rollout a cluster with zero nodes can never produce,
  so it stalled 180 s every pass.** Before: the provider ran `deploy/eso/install.sh`, whose last act
  was `kubectl -n default rollout status deployment/external-secrets --timeout=180s`. That
  deployment's pods cannot be scheduled until a node exists, and the node is created by the
  node-scale pass, which runs AFTER the reconcile pass in the same loop
  (`cmd/socialweb-clusterprovider/wiring.go`: `p.pass(ctx, ref, key)` then
  `p.scale(ctx, key, ref.Name, true)`), so the wait outlives the very pass that would satisfy it.
  It is a stall rather than a deadlock - the wait sits inside `if ! ...; then`, so the script exits
  0 with a warning - but a cluster at zero nodes cannot bring itself up promptly and every pass pays
  it. Measured in production at zero nodes: `docker exec socialweb-k8s ps -eo pid,etime,args` showed
  `bash /opt/socialweb/deploy/eso/install.sh` at 01:35 beside
  `kubectl -n default rollout status deployment/external-secrets --timeout=180s` at 01:32. After:
  `install.sh` honors `ESO_SKIP_ROLLOUT_WAIT` (default 0, so a human running the installer still
  gets the rollout diagnostic) and `cmd/socialweb-clusterprovider/eso.go` sets it to 1, so the
  install returns as soon as the bundle is applied and CRD establishment - which needs no node - is
  the only thing that must happen before the store is applied. Nothing else in the pass moves: the
  gate is still the probe that already exists, `eso.Installed` reading one `ClusterSecretStore` with
  `Limit: 1`, and `InstallOperator` still fails the pass by name with `ErrNoOperator`, naming
  `external-secrets.io/v1`, when the group does not serve after the install, so an operator whose
  CRDs never come up is refused rather than passed silently. The change is a flag on the repo's own
  installer rather than the bundle applied from Go: `install.sh` is a file of this repo (added in
  `init`), while the pinned upstream bundle is the v2.11.0 `external-secrets.yaml` it fetches at
  runtime and applies unchanged with its webhooks rewritten to `failurePolicy: Ignore`; that bundle
  is untouched. Reapplying the manifests from Go would have put the version pin and the webhook
  rewrite in a second place and left the shipped installer without a caller, which is the shape
  ESO-3 closed. Watched to fail: with the provider's `ESO_SKIP_ROLLOUT_WAIT=1` removed,
  `TestTheInstallDoesNotWaitForTheOperatorToRoll` recorded
  `-n default rollout status deployment/external-secrets --timeout=180s` against the wanted
  `apply --server-side --force-conflicts -f -`, and
  `TestAnInstallThatLeavesTheOperatorUnservedFailsThePassByName` pins the refusal by group name.
  `NEXT.yaml` ESO-4.

- **A market cluster's scale pass re-asked the market for one node instead of naming the nodes its
  target needed, every 2 s, for as long as a claim was in flight. CLOSED, and the ordinal that names
  a node is what closed it.** `tenantNodes.nextOrdinal` named the next node from the TENANT cluster's
  `Nodes()` list alone (`cmd/socialweb-clusterprovider/nodes.go`). That list holds only nodes whose
  k3s agent has already joined, and a market `Provision` writes its `NodeClaim` and returns before the
  machine exists (invariant 31, section 11), so every provision inside one pass read the same stale
  list and resolved to the SAME ordinal: `scaledriver` asked for `target - observed` nodes and
  `inFlight` correctly counted the claims already pending, but the loop wrote the one claim again for
  each node it still wanted and never advanced past it. The count was right and the naming was wrong.
  MEASURED in production on `root@socialweb.computer` (ssh port 1997), with
  `SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE=market`, `SCALE_PODS_PER_NODE=1` and a target of 6:
  `docker logs socialweb-k8s` carried four `nodemarket: node claimed from the market` lines for
  `...-node2` per 2 s pass (240 in two minutes) beside one
  `scaledriver: node count changed action=1 nodes=6 reason="intent, and 2 nodes are counted for a
  target of 6"`; the cluster's home workspace `root:2wjeqtgx:test-cluster-alice-0001` held two claims,
  `node1` `Running` and `node2` `Provisioning`, and the tenant apiserver reported one node, `node1`.
  So `observed` was 2 (one joined plus one in flight) and the four provisions that aimed at 6 all
  landed on `node2`. The `429` the door logs (`node_claim_list_failed ... nodeclaims: 429`) is largely
  an EFFECT and not the cause: the door and the clusterprovider both authenticate with
  `/var/lib/socialweb/kcp/admin.kubeconfig`, so kcp's per-user rate limit is shared, and the
  provider's churn spent the budget the door's own list needed. After: `nextOrdinal` seeds its
  `present` set with the ordinals this `tenantNodes` already holds (`heldNodesLocked`, reading each
  name's ordinal the same way the tenant branch already does), so a pass at `observed=2`/`target=6`
  names `node3..node6` and the next pass counts 6 and asks for nothing. The door's own side was
  originally left as a separate change in the sibling Deno tree; it has since been made, and it was
  the same defect seen from the other end of the shared credential. The door's poll
  (`socialweb-computer-k8s/lib/socialweb-computer-k8s-node-market/mod.ts:330`,
  `setInterval(tick, opts.pollIntervalMs ?? 2_000)`) called `NodeClaimStore.list`, which re-walked
  every account and home and then LISTed nodeclaims per home on EVERY tick - two tenancy queries per
  account per 2 s - and its `request` had no `429` handling at all, so the rate limit it earned was
  paid again on the next tick and the one after that. Both halves are now in the store, where every
  caller gets them: the account-and-home walk is cached for 30 s (`homesTtlMs`, 0 to disable), and a
  `429` parks READS until the `Retry-After` it named has elapsed, capped at a minute, five seconds
  when the header is absent or unparseable. A read arriving inside the window fails immediately
  without reaching kcp; writes are deliberately not parked, because a claim's own status write is
  not the traffic that earned the limit and holding it back delays the node the door is bringing up.
  The poll's default interval moves 2 s to 5 s. The instruments are three tests in
  `socialweb-computer-k8s/test/nodeclaim_kcp_test.ts`: the walk happening once per ttl rather than
  once per poll, the walk happening AGAIN past its ttl, and a `429` holding reads back while writes
  still go through. The first and third were watched to fail with the cache and the park removed; the
  second cannot fail against the uncached code by construction, so it was watched against a cache
  that never expires, and the pair pins the cache from both sides.
  The instrument for the count itself is `TestOneScalePassClaimsEveryNodeTheTargetNeeds` in
  `cmd/socialweb-clusterprovider`:
  three running pods and one pass, watched to fail against the old `nextOrdinal` with `one pass that
  needs three nodes left 1 claims, want three`, and green with the change, where three distinct claims
  exist and a second pass writes nothing. `NEXT.yaml` MARKET-3.

- **A market cluster rented 15 machines for a target of 10, because a claim that had reached
  `Running` was counted in no list at all until its k3s agent registered. CLOSED, and the count is
  now the tenant cluster's own node list.** The count a pass observed was `tenant Nodes +
  inFlight`, and `inFlight` was keyed on the CLAIM's phase: `nodemarket.Host.InFlight` returned
  `claim.Status.Phase != Running`. That predicate has a hole with a duration in it. The door writes
  `Running` from `onForwarded`
  (`socialweb-computer-k8s/lib/socialweb-computer-k8s-node-market/mod.ts:214`, "the node's agent
  reached its apiserver"), which fires when the guest's k3s agent completes its first forwarded TCP
  connection through the door to the apiserver - and the Node object it is registering appears in
  the CLUSTER's node list only after that connection is served. Between those two moments the node
  exists and is paid for, is absent from the tenant node list because k3s has not registered it yet,
  and has stopped being counted as in flight because its claim says `Running`. It is in neither half
  of the sum, so the sum reads one node too few for as long as the gap lasts. One node too few would
  be harmless. What makes it a defect is that claims cross that gap TOGETHER: a batch of guests
  booted by one pass reaches `Running` within seconds of each other, so the whole batch leaves the
  count at once, the pass reads a count far below the target that is really out there, and it rents
  the difference again.
  MEASURED in production on `root@socialweb.computer` (ssh port 1997) on 2026-10-06, five GitLab
  jobs admitted at once with `SCALE_MAX_NODES=10` and `SCALE_PODS_PER_NODE=1`: the node count read
  `1 -> 5 -> 6 -> 7 -> 8 -> 15` and settled on `scaledriver: no change action=None nodes=15 ...` -
  15 Ready nodes named `node3` to `node17` for a ceiling of 10 - while all five jobs went `success`
  on five distinct nodes and both bidders carried the 18 VMs with no resource refusal. The claims
  that had already reached `Running` with none of their Nodes in the tenant list were the whole of
  the gap. The fix removes the phase from the count entirely: `tenantNodes.inFlight` lists the
  tenant cluster's Nodes once per pass and counts every node it HOLDS whose name is not in that
  list, so the sum is `tenant Nodes + held claims the tenant does not name`, which cannot
  double-count and cannot lose a moment. That second half is gated on `Substrate.deferredJoin()`,
  which is the market substrate alone, so a guest substrate's count is exactly what it was: a
  firecracker or qemu `Provision` does not return until the tenant cluster reports the node, so a
  host such a substrate still holds and the tenant does not name is a join that ran out its timeout
  rather than a machine on its way, and counting it would hold the decision off a node that is
  never coming. `internal/nodemarket.Host.InFlight` and the
  `inFlightReporter` seam are deleted rather than corrected, because the shop that could answer the
  question moved: a host knows its claim's phase, and only the cluster knows what it has registered.
  A read that fails counts every held node, the conservative direction, and says so in a warning;
  the previous behaviour on a failed read was the same. `tenantNodes.client`'s error text lost its
  teardown-specific wording, since the count now calls it too, and `Teardown` carries that wording
  itself.
  The instrument is `go test ./cmd/socialweb-clusterprovider/ -count=1`. Two tests, both watched to
  fail against the old predicate by restoring it in place and running them:
  `TestAMarketNodeIsCountedUntilItsOwnClusterListsIt` records `a claim that reached Running while
  its cluster still did not list the node counted 0 nodes in flight, want 1`, and
  `TestASecondPassRentsNothingWhileTheFirstNodesAreStillJoining` - the regression itself - records
  `a second pass over three claims that had all reached Running and whose nodes the cluster did not
  list yet left 6 claims, want three`, the doubling, on three claims instead of the production five.
  Both are green with the change. `NEXT.yaml` MARKET-4.

- **A node claimed from the market was torn down two seconds later, because a node name hands its
  last claim's words to the next claim written under it. PARTLY CLOSED - the naming rule is fixed for
  every node but the first, and the first is MARKET-5.** `kcpclient.WriteNodeClaimTo` never touches
  status: it reads the claim, and when the SPEC already matches it returns without a write at all. The
  door writes the terminal status of a claim it can no longer serve (`adoptNever`, section 15's market
  entries), and nothing clears it, so a name whose claim ended carries that phase and that message
  into whatever is claimed under it next. The provider then reads its own fresh claim as a failure:
  `confirmHeld` sees `Failed`, `guestFatal` returns the door's words, and the node is released.
  MEASURED in production on 2026-10-06 during the five-job run: `nodemarket: node claimed from the
  market` for `-node7` at 06:18:59, `nodemarket: releasing the node` for it at 06:19:01, and the
  reason the provider reported was `the door restarted and released this node's VM; a new node will be
  requested` - a sentence the door had written for a claim of an earlier generation. Four nodes went
  that way in one run: `node1` (claimed 06:18:38, released 06:18:41), `node2`, `node7` and `node11`.
  No machine was rented for any of them - the door acts on a claim that is `Pending` or has a
  deletion timestamp, and a `Failed` one is neither - so the cost is a spent claim cycle and minutes
  of slower convergence, not a leaked VM.
  `tenantNodes.nextOrdinal` now seeds its `present` set from the claims already in the cluster's home
  workspace whose phase has ENDED, so such a name is retired. A claim that has NOT ended is
  deliberately left alone, and that is the half worth stating: re-claiming a live name is a no-op on
  the spec, which is how a provider that restarted adopts a node the door is still bringing up instead
  of abandoning it. `nodemarket.Claims` gained `ListNodeClaims` for this; `kcpclient` already had the
  call. The instrument is `TestANodeNameWhoseClaimHasEndedIsNotClaimedAgain` in
  `cmd/socialweb-clusterprovider`: a `Failed` claim seeded at `node2`, two provisions, and the second
  must name `node3`. Watched to fail with the seeding removed. `NEXT.yaml` MARKET-5 covers what is
  left - the FIRST node of a cluster, whose name is fixed at construction and never reconsidered.

- **A torn-down node kept being held, so the count passed the ceiling and the cluster could not rent
  another machine. CLOSED.** `tenantNodes.teardownHeld` deleted the node's claim, deleted the
  cluster's `Node` object and dropped the name from the live set, and left the host in `extra`. That
  was invisible while the count was keyed on a claim's PHASE, because a deleted claim reads as nil and
  a nil claim was not counted; the moment the count became "the tenant cluster's node list plus the
  names this process holds" (MARKET-4, above), a hold that outlives both its claim and its `Node`
  became a node counted for ever. It is the same shape as the defect it came from: the count and the
  thing counting it disagreed about what a held node is.
  MEASURED in production on 2026-10-06, at the end of the five-job run, by counting the log rather
  than reading the count: 11 `node claimed from the market` lines, 7 `releasing the node` lines, so 4
  live claims - and the tenant cluster reporting exactly those four Nodes, `node3` to `node6`, while
  the scale decision read `nodes=11` on 131 consecutive passes. That is not a wrong number in a log.
  11 is above the ceiling of 10, so `scale.Decide` returned the branch that reads "more nodes are
  counted than the target needs", which is the branch that does NOT provision: the cluster was holding
  four nodes and could not rent a fifth, and no pass would ever have corrected it on its own. The
  instrument is `TestANodeThatIsTornDownStopsBeingCounted` in `cmd/socialweb-clusterprovider`: a node
  is provisioned, its claim is failed, the cluster is confirmed, and the count must fall back to zero.
  Watched to fail with `forgetHeld` removed: "a node that was torn down is still counted as 1 nodes in
  flight". `forgetHeld` drops the primary host when the name is its own and removes the ordinal from
  `extra`, and `teardownHeld` calls it once the teardown has returned.
  AFTER, on the same cluster: the decision reads `intent, and 4 nodes are counted for a target of 6`
  against a `kubectl get nodes` that lists exactly four, and the pass provisions the two the target
  asks for. The wedge is the thing to watch for because it is silent - an inflated count and a correct
  count both stop at the same line, and only the cluster's own node list says which one this is.

- **A cluster's first node was claimed under a name whose claim had ended, and released two seconds
  later. CLOSED.** `nodeHosts.forCluster` built the primary host as `node1` and `Provision` took
  ordinal 1 straight to `hostFor(1)`, the host already in hand, never consulting the free-ordinal rule
  that the NEXT node is named by. `kcpclient.WriteNodeClaimTo` reads the claim and, when the spec
  already matches, returns without a write - so a name whose claim had ended carried its `Failed`
  phase and the door's old sentence into the claim written under it, and `confirmHeld` read it as a
  failure. MEASURED in production on 2026-10-06, after a deploy restarted the door: `-node1` claimed
  at 06:18:38 and released at 06:18:41 with `the door restarted and released this node's VM; a new
  node will be requested`, a sentence the door had written for an earlier generation's claim.
  `Provision` now asks whether the primary host's name is spent before it uses that host, reads the
  claim at that name (a by-name GET, not a list), and when its phase has ENDED it drops the host
  (`forgetHeldLocked`) and lets the ordinary free-ordinal rule name the first node instead. This is
  the same shape as the two counts above and it closed the same way: the thing that names a node is
  the claim that already exists at the name, not the host this process happened to build.
  The instrument is `TestTheFirstNodeOfAClusterIsNotClaimedUnderASpentName` in
  `cmd/socialweb-clusterprovider`: a `Failed` claim seeded at `node1`, one provision, and it must name
  `node2` and leave the spent claim alone. Watched to fail with the check removed. `forgetHeld` was
  split into a locked and an unlocked form in the process - the first draft called the locking one
  from `Provision`, which already held the mutex, and hung the test until the timeout named it.

- **The door's node-session test let ssh2 generate the key it then asked ssh2 to parse, and failed
  about one run in four. CLOSED, and BOTH of the causes proposed before the real one were wrong.**
  `test/node_session_test.ts`'s `fakeGuest` did `const hostKey = utils.generateKeyPairSync("ed25519")`
  and then `new Server({ hostKeys: [hostKey.private] })`, which fails with `error: Cannot parse
  privateKey: Malformed OpenSSH private key` from `ssh2@1.17.0/lib/server.js:249` whenever the
  generator's output is one its own parser rejects. MEASURED 2026-10-06/07, and the two refutations are
  the part worth keeping:
  the FIRST cause proposed was an ssh2 key-generator defect under load, which is right in substance but
  was written before any control; the SECOND was this host's `/tmp`, a 24 GiB tmpfs at 91% with 19 GiB
  of stale scratch, on the evidence of five green runs with `TMPDIR` moved off it - and that control
  changed two things at once. The refutation is direct: five more consecutive runs were green with
  `TMPDIR` LEFT ON the 91%-full tmpfs, and a 60-iteration probe of exactly that path
  (`generateKeyPairSync` + `Deno.makeTempFile` + write + read + `new Server`) never failed once. What
  the rate actually tracks is CONCURRENCY: 0 failures in 60 sequential generations, 1 in 240 across
  four concurrent probe processes, and 1 of 4 concurrent `deno task test` suites against 0 of 5
  sequential ones. Production does not use that path at all - the requester shells out to the real
  `ssh-keygen` into a per-VM temp dir (`atproto-market/lib/requester-xrpc/mod.ts:749`) - so this was
  ever only the test's own key.
  The fix is a fixed test keypair for the fake guest, which removes the generator and the randomness
  together. Closing reading: SIX concurrent `deno task test` suites, 65 passed each; the same six-way
  run before the change failed one suite. `NEXT.yaml` FLAKE-2.

- **A Node whose guest the door reaped stayed Ready and took pods, and the door already had the word
  that says so. CLOSED.** The door's restart releases the claims it holds and destroys their machines,
  and it wrote `Failed` for that - the same phase it writes for a claim whose machine it could NOT
  confirm released. The provider acts on `Failed` only for the nodes IT holds, and a provider that has
  just started holds nothing, so the previous generation's `Node` objects stayed in the tenant cluster
  reading `Ready` and took pods. MEASURED on 2026-10-06, forty seconds after a deploy restarted the
  door: pipeline 74's four runner pods were scheduled onto `...-node6`, sat `Pending` for 58 s, and the
  runner gave up with `Job failed (system failure): prepare environment: waiting for pod running: pods
  "runner-w93ufg0qn-project-1-concurrent-3-nuixd3cp" not found` - the pod gone when the `Node` finally
  was. Four of five jobs failed that way, where a pipeline 25 minutes earlier with no restart in front
  of it had all five running at once on five distinct nodes. The deletion came from the cloud
  controller manager, not from anything that noticed the guest was gone: a corpse is a `Node` absent
  from the instance snapshot, and a snapshot is rewritten only when the provider next provisions or
  tears down.
  The obvious reconcile was recorded as UNSAFE first, and that entry is the reason the fix is shaped
  the way it is: `adoptNever` wrote `Failed` both for a claim it reaped and for one whose machine it
  could not confirm released, and the second may still have a running machine on its way to joining, so
  a reconciler built on the phase alone would delete `Node`s for machines still being paid for. The
  door's vocabulary already had the distinction - `Released` is what it writes in the normal teardown
  path, for a node whose VM it deleted - so `adoptNever` now writes `Released` when its own reap
  returned true and `Failed` when it did not, and no schema changed at all.
  Two halves follow from that. `nodemarket.Host.Fatal` becomes `Terminal` and returns the claim's own
  words for ANY ended phase, so a claim the door released is given up on rather than left counted (the
  guest hosts implement `Terminal` as the fatal-console marker they always had). `nodeHosts.reconcile`
  runs each pass before `confirm`: it lists the claims in the cluster's home workspace, and for every
  one reading `Released` it deletes the tenant `Node` of that name, because only `Released` says the
  machine is gone. It does not need the cluster's node host, which is what a just-restarted provider
  does not have.
  The instruments are three tests, each watched to fail with its half removed. In the door,
  "a live claim this process never started is reaped, never adopted" now asserts `Released` where it
  asserted `Failed`, and "a claim the door could not confirm released is Failed, not Released" holds
  the other branch. In `cmd/socialweb-clusterprovider`,
  `TestANodeWhoseClaimTheDoorConfirmedReleasedIsRemoved` seeds a `Released` claim and a `Running` claim
  with a tenant `Node` each, and asserts the first Node is gone and the second is not - watched to fail
  with the reconcile made a no-op: "the Node acct-liveweb-node1 is still in the tenant cluster after
  the door confirmed its machine gone".
  The reconcile's OWN first version was a defect, and in production within five minutes: it deleted by
  NAME, and a claim the door released stays in the workspace for ever as its own record, so the names
  to act on are not the Nodes still there. MEASURED on 2026-10-07: 325 `removed a Node the door has
  taken the machine away for` lines in five minutes - one write per pass per lingering released claim,
  on Nodes that had gone the first time - beside a `429` on the credential the door and the provider
  share. It now lists the tenant `Node`s once, intersects, acts only on names in both, and logs only a
  removal that happened. `TestAReleasedClaimWhoseNodeIsAlreadyGoneCostsNoDelete` runs it twice over a
  released claim with no `Node` and asserts the fake client saw no delete action; watched to fail with
  the intersection removed. `NEXT.yaml` MARKET-6.

- **A GitLab Runner off the kcp host reaches a tenant apiserver, exec and all. CLOSED, and it was
  closed by the runs rather than by a change.** The apiserver binds loopback and the only off-host
  route is the kcp mount, a reverse proxy that carries bearers; whether the exec and attach paths a CI
  runner needs survive that proxy was recorded as unmeasured (GL-2), with the note that making it work
  means publishing an authenticated front and widening a trust boundary the gateway and the entrypoint
  keep at loopback on purpose.
  MEASURED on 2026-10-06, from this workstation - a different host from the one serving kcp - with the
  kubeconfig the SPA downloads (`/etc/gitlab-runner/tenant-ci/kubeconfig.yaml`, server
  `https://kcp.socialweb.computer/clusters/root:2wjeqtgx:test-cluster-alice-0001:clusters`):
  `kubectl logs` returned a running container's own log lines, and `kubectl exec` returned the
  CONTAINER's answer - `error executing command in container: failed to exec in container: failed to
  start exec "82d36cf2...": OCI runtime exec failed: exec failed: unable to start container process:
  exec: "sh": executable file not found in $PATH` - which names the container runtime's own failure to
  find a shell in a distroless image, and therefore says the request reached containerd rather than
  stopping at a proxy. The front is named: Caddy on `socialweb.computer` terminates TLS for
  `kcp.socialweb.computer` and reverse-proxies `https://127.0.0.1:6443` with `versions 1.1` and
  `flush_interval -1`, which is what carries the upgraded exec connection; that front is reachable
  anonymously and answers `403 forbidden: User "system:anonymous" cannot get path "/version"`, so what
  it exposes to a caller without a credential is a refusal and nothing else.
  The runner itself is the second reading and the one the instrument asked for: the GitLab runner
  registered against project 1 uses the `kubernetes` executor, which runs a job's script with
  `pods/exec` in the build container, and it has completed FIVE jobs at once on five distinct market
  nodes with every job green (pipeline 75, `scale-check-1..5`, 198-202 s each), plus the same five
  green in pipeline 71 before it. A path that could not traverse the mount could not run a job.
  `NEXT.yaml` GL-2.

- **The minting CLI had never been run against a real cluster's data dir, and could not have been:
  it was not in the image. CLOSED.** `cmd/socialweb-tenantcredential` builds, is unit-tested, and the
  container image built eight other commands and not this one - so the tool the section-14 bullet was
  about existed and was undeployable, and its instrument ("a run of the minting command against a real
  cluster data dir") could not be carried out anywhere the state dir is. The section-14 bullet's own
  premise had also gone stale: it said no command writes a minted credential anywhere an operator can
  use, and the command had existed since the entry below.
  Built and copied into both final stages, beside `socialweb-vault`, and then run for real on
  `root@socialweb.computer` on 2026-10-07 as uid `kcp` inside the container - the uid that can read
  `<StateDir>/pdr-<key>/server/tls/client-ca.key`, mode 0600, which is why this cannot be done from
  outside:
  `SOCIALWEB_TENANTCREDENTIAL_STATE_DIR=/var/lib/socialweb/clusterprovider`,
  `..._CLUSTER=2wjeqtgx-test-cluster-alice-0001`, `..._SERVER=https://127.0.0.1:20003`,
  `..._COMMON_NAME=cli-close-gl-1`, `..._ORGANIZATIONS=system:masters`,
  `..._OUT_DIR=/tmp/tenantcred`. It printed
  `validUntil=2036-10-04T07:13:11Z ca=/tmp/tenantcred/ca.crt cert=/tmp/tenantcred/tls.crt
  key=/tmp/tenantcred/tls.key`, and the three files came out mode 0600 owned by `kcp`.
  `kubectl` with exactly those three files against the tenant apiserver listed the cluster's own Nodes
  (`node28` to `node32`, all Ready), and `kubectl auth whoami` reported `Username cli-close-gl-1` and
  `Groups [system:masters system:authenticated]` - the apiserver naming back the identity the CLI
  claimed to mint, which is the reading the instrument asked for. A second run refused:
  `these files already exist and this tool will not overwrite a credential ... that silent replacement
  is the failure invariant 20 exists to prevent`. The credential was removed from the container's
  `/tmp` afterwards. `NEXT.yaml` GL-1.

- **A host a caller adds to the vault's certificate is not served until the vault restarts. CLOSED as
  a STATED LIMIT, with a refusal where a URL would otherwise be handed out.**
  `vaultcert.EnsureVaultTLSCertificate` re-signs the leaf under the cluster's existing CA when a caller
  asks for one more host, so no client loses the anchor it trusts (the entry above). What it cannot do
  is make the RUNNING vault serve the new leaf, and the reason is where the handle lives: OpenBao reads
  `tls_cert_file` once at start; the vault step's own gate (`internal/clusterprovider/steps.go`) runs
  the vault a whole pass before the tenant add-on pass and the node disk stage exist; and those two are
  closures in `cmd/socialweb-clusterprovider` (`eso.go`, `nodes.go`) built without the materialiser's
  process group, whose `LocalHost` is the only thing holding the vault's `processrunner.Process`. On a
  market cluster the two host sets differ by construction: bring-up asks for
  `SOCIALWEB_MARKET_ADVERTISE_ADDRESS` and loopback (`internal/clusterprovider/stack.go`), the tenant
  add-on pass asks for the `10.100.0.0/16` gateway `internal/subnetallocator` leases and loopback
  (`cmd/socialweb-clusterprovider/eso.go`). The reading, against a real OpenBao v2.6.2 on 2026-10-07:
  the vault started covering `[198.18.255.254 127.0.0.1]`, the tenant add-on pass re-signed the leaf to
  cover `[198.18.255.254 127.0.0.1 10.100.0.1]` on disk, the vault went on serving
  `[198.18.255.254 127.0.0.1]`, and a client dialing it as `10.100.0.1` failed with `tls: failed to
  verify certificate: x509: certificate is valid for 198.18.255.254, 127.0.0.1, not 10.100.0.1`. The
  window is from the re-sign until the vault process next starts; the leaf on disk already carries every
  host a caller has asked for, so a restart - or a cluster teardown and bring-up - closes it, and
  nothing asks for a NEW host after that. The next-best behaviour is taken: the tenant add-on pass
  REFUSES to hand the ExternalSecret a URL the running listener cannot present
  (`vaultcert.RequireServedVaultTLS`, called from `cmd/socialweb-clusterprovider/eso.go`), failing that
  pass with the reason rather than writing a Secret whose client is guaranteed to fail with a name
  mismatch. A signal-based repair was refused with it - it would need a handle the re-sign site does
  not have, and a reload whose completion nothing observes would let a reload that did not happen and
  one that did look identical, the defect class `ErrNoGuardSeam` exists to prevent - and so was moving
  the gateway into the FIRST mint, whose host source is the allocator itself: `subnetallocator.Allocate`
  walks the pool by occupancy, so the gateway is not derivable without it, and leasing a cluster's range
  on the vault bring-up path would change what the zero-node run allocates (section 14). Pinned by
  `TestARunningVaultDoesNotServeAHostItsLeafGainedAfterItStarted` in `internal/clusterprovider`, which
  takes the reading and fails without the refusal. `NEXT.yaml` VAULT-TLS-1, now a limit.

- **The legacy-key migration ran in a different process from the one that reads what it renames.
  CLOSED.** The clusterprovider writes a mint credential as `<MINT_DIR>/<accountHash>-<name>` and the
  gateway reads that name, so the two agree from the clusterprovider's first pass onward. What did not
  agree was the ORDER: the entrypoint started the gateway first, and the rename of an existing
  `<MINT_DIR>/<name>` was the clusterprovider's own startup migration, so a mint request arriving in
  between named a file that was about to be renamed and was refused. It failed closed - an error naming
  the cluster and the key, never another cluster's credential - so the cost was a window of seconds and
  a refusal that reads to an operator as a broken deployment.
  The clusterprovider gained a `migrate` subcommand that opens the same inputs the gateway needs
  (`buildInputs`, extracted from `buildDeps`) and runs `migrateLegacyKeys`, and the entrypoint runs it
  BEFORE starting the gateway. A migration that fails does not stop the deployment: the gateway is
  started anyway and the clusterprovider runs the same migration itself, because a gateway that never
  starts is worse than a mint refused in the seconds before that one does. The ordering is what the fix
  IS, and it lives in a shell file, so it is pinned by a test that reads
  `deploy/container-entrypoint.sh` and asserts the `migrate` invocation appears before the gateway is
  started - `TestTheLegacyKeyMigrationRunsBeforeAnythingCanReadAMintCredential`, watched to fail with
  the step removed: "the container entrypoint never runs the legacy-key migration as its own step".
  `NEXT.yaml` MINT-1.

- **A node record did not say which substrate wrote it, so a sweep could only reap its own. CLOSED,
  and it closed with the reading the item asked for.** `noderecord.Record` held a pid, a start time
  and an identity, and the identity is the ARGV the process was started with - which only the substrate
  that started it knows how to read. `nodeIdentityFor` switched on the CONFIGURED substrate, and
  `sweepNodeRecords` returned early under `market`, so a state dir a firecracker or qemu gateway left
  behind was never walked and its guests stayed up.
  The record now carries `Substrate`, `noderecord.Write` refuses an empty one, and the four writers set
  it: `internal/nodefirecracker` and `internal/nodeqemu` from a name constant each, and
  `internal/nodevm` from `NamespaceParams.Kind`, which is the substrate the namespace child was started
  for and was already in the file it reads. The sweep resolves the identity pair PER RECORD from
  `rec.Substrate` rather than once from its own configuration, and a record naming a substrate with no
  identity function is a refusal naming that substrate, never a skip - guessing reads a live guest as
  gone and a gone one as live.
  Readings: `TestTheMarketSweepReapsWhatAnotherSubstrateLeftBehind` starts a stand-in process, records
  it under firecracker, sweeps it with a gateway configured for `market`, and asserts the process is
  gone and the record cleared - the item's own closing reading, watched to fail with the market
  early-return restored (`waitGone` timed out). `TestASweepStopsOnARecordWhoseSubstrateItCannotRead`
  holds the other side. `TestTheSweepRefusesASubstrateWithNoIdentity` asserted the up-front refusal
  that this replaces and was rewritten to the per-record one rather than deleted. `NEXT.yaml` MARKET-1.

- **The tenant store was written with a vault address nothing could reach, so the demo
  ExternalSecret had never synced on a market cluster. CLOSED.** The store's vault server is
  `https://<the cluster's subnet gateway>:<vault port>` (`cmd/socialweb-clusterprovider/eso.go`). On
  a guest substrate that address is inside the cluster's own range -- the guests dial it and the host
  binds it -- and the demo was measured syncing there, which is where section 15's claim about
  `Secret/users/openbao-demo` comes from. On `market` it is neither bound nor routed.
  MEASURED on production on 2026-10-07, one cluster on `SUBSTRATE=market`: the store read
  `https://10.100.0.1:20001`; `grep -c 0A640001 /proc/net/tcp` inside the container was `0`, so
  nothing on the host binds that address; the only listener on 20001 was `0100007F:4E21` -- loopback
  -- and it is the cluster's own `bao server`; the store read `Ready=False unable to create client`
  and the ExternalSecret `Ready=False reason=SecretSyncedError`, `could not get secret data from
  provider`.
  The entry above about the served leaf does NOT catch this, and the distinction is the point: that
  check asks whether the running vault can PRESENT the host, and the leaf does carry 10.100.0.1 --
  what it cannot do is answer, because there is no listener there and no route to one.
  Closed by the third of the item's own instruments: the store is not written where nothing can reach
  it. `tenantAddOns` installs the operator on every substrate and returns before the store, the demo
  value and the ESO certificate when `tenantStoreIsReachableFrom` is false, with a logged decision
  naming this entry. `TestTheTenantStoreIsNotWrittenWhereNothingCanReachIt` pins the decision for all
  three substrates, watched to fail with it forced true. Not writing is not enough on its own, and
  that was measured too: an upgraded cluster kept the store an older version had written, because
  nothing removed it, so `eso.Remove` takes back what `eso.Apply` wrote -- by the names in the same
  manifest directory -- and the pass calls it on that path.
  `TestRemoveTakesBackTheObjectsThisInstallerWrote` seeds the objects the manifests name and asserts
  none survive; watched to fail with `Remove` made a no-op: "1 clustersecretstores objects survived
  the removal".
  One imperfection is left in it deliberately rather than fixed in a hurry: the removal is ATTEMPTED
  on every pass, because nothing remembers that the cluster's store is already gone. Each attempt is
  three deletes that come back NotFound, so it is cheap and it is bounded by the 60 s timeout -- but
  it is the same shape as the unguarded installer that stalled the provider the same night, and the
  fix (a per-cluster flag in the closure that owns the pass, so the removal is attempted once per
  process and a restart retries it) is a change to a path that runs inside every pass. It was not
  made at that hour because it would have shipped unverified, which is the mistake being recorded.
  Removing it took three passes to get right, and each correction came from production rather than
  from reading. FIRST: not writing is not enough, because an upgraded cluster keeps what an older
  version wrote. SECOND, and this is the one worth reading twice: the ExternalSecret went and then sat
  TERMINATING on ESO's own finalizer
  (`externalsecrets.external-secrets.io/externalsecret-cleanup`), which the controller removes after
  cleaning up the Secret it owns -- and that controller runs at ONE node, so on a cluster at rest,
  which is every market cluster that is not working, it is Pending and removes nothing. The object
  would have sat there indefinitely. The removal now deletes the ExternalSecret FIRST, so a running
  controller has something to work against, and then releases the finalizer itself, because the target
  Secret is already gone and waiting for a controller that cannot schedule is not a cleanup.
  The first version of that test passed against a no-op release, and it was the FAKE that was wrong:
  it deleted an object with finalizers outright, where an apiserver marks it Terminating and waits.
  With the fake made to behave like an apiserver,
  `TestRemoveReleasesAnExternalSecretTheControllerCannot` fails as it should when the release is
  removed.
  The first version of THAT change stalled production, and the way it read is the lesson. The
  store-less path called the installer function directly, which bypasses `eso.InstallOperator`'s "only
  if the group is not served" guard, so `bash install.sh` ran against the tenant apiserver on every
  pass; the pass is synchronous, so the node-scale pass behind it stopped with it. MEASURED minutes
  after it shipped: the last line the clusterprovider logged was `installing the External Secrets
  Operator into the tenant cluster`, nothing followed it, and the process showed ZERO cpu time 51
  seconds in -- blocked rather than working. It now calls `InstallOperator`, which is what the guest
  path has always reached through `applyTenantAddOns`, and the removal carries a 60 s timeout so the
  worst case is a pass that fails loudly rather than a provider that stops. NO instrument covers this
  one: the stall was found by reading the log and the process's cpu time, and what would have caught
  it is a test that counts the installer's invocations per pass, which does not exist. What is given up is stated rather than
  implied: a market cluster now has the operator and no store, and a tenant who wants one configures
  one they can reach. The other two instruments remain open as options -- reaching the vault through
  the door the node's SSH session uses, or publishing the vault on the provider host behind the
  client certificate that is already minted per cluster -- and both are trust decisions rather than
  bugs, which is why neither was taken here. They were scoped rather than left as words, and the
  scoping is the decision material:
  THROUGH THE DOOR. The door already opens exactly this bridge for the apiserver -- one ssh2
  `forwardIn` per session, pumped back to the provider host
  (`socialweb-computer-k8s/lib/socialweb-computer-k8s-node-market/guest.ts:198`, `:223-235`) -- so a
  second forward for the vault port carries the client certificate unchanged, because the forward is
  a raw TCP pipe and not a TLS terminator; the vault's own `auth/cert` stays the thing that decides,
  and a credential for one cluster cannot open another's vault because the role trusts only that
  cluster's imported CA. Two companions are required and neither is optional: the advertised address
  is a `/32` on the GUEST's own loopback, so a pod's CNI cannot reach where the forward would land,
  and the vault's leaf would have to carry the advertise address or the store's server-name check
  fails. And the forward lives and dies with the join session, which is reaped and renewed (MARKET-6),
  so vault reads would fail whenever a session drops -- the apiserver path lives with that; the vault
  never has.
  PUBLISHED ON THE PROVIDER HOST. This is the highest-blast-radius option: it puts a vault listener on
  the internet and makes the edge the first gate, where a miswritten site exposes every vault to
  anonymous TLS and one leaked ESO client certificate -- it lives in a tenant-namespace Secret --
  reads from anywhere. It also has an unanswered question that has to be settled before it can be
  called achievable at all: the vault authenticates by CLIENT CERTIFICATE IN THE HANDSHAKE, and an
  HTTP-terminating Caddy does not forward that certificate upstream, so as stated the vault would
  never see it. Settling that means L4/SNI passthrough to a per-cluster port, or replacing cert auth
  with a token.
  A THIRD OPTION, cheaper and honest, which the item did not name: under `market` keep the OpenBao
  store off and write instead a small, DIFFERENTLY-NAMED store using a provider that lives inside the
  tenant cluster (a `kubernetes` provider reading a ConfigMap). That proves operator installed ->
  store `Ready` -> `SecretSynced` end to end inside the cluster, with no provider-host transport and
  no new exposure -- it proves less, in that it does not exercise OpenBao on market, and it has to say
  so by name. The manifest set is already a directory chosen by config, so this is one YAML and one
  branch beside the log. Leaving the demo out entirely under market is the same shape and already
  half-built; it is the honest minimum that proves nothing.

- **The tenant grant admitted the External Secrets controller by the literal namespace `default`, and
  the installer could not have put it anywhere else. CLOSED, and the second half was found by taking
  the reading.** The grant's CEL policy admitted exactly
  `system:serviceaccount:default:external-secrets`, so an install anywhere else was denied and the
  denial read as a broken ExternalSecret rather than as a grant that does not name it.
  MEASURED BEFORE, on production: the policy named `default`;
  `kubectl create secret --as=system:serviceaccount:external-secrets:external-secrets -n users` was
  `forbidden`, while the same create as `system:serviceaccount:default:external-secrets` was
  `secret/... created (server dry run)`.
  The grant is a template, not a file applied verbatim, so the namespace became a placeholder
  `RenderTenantGrant` substitutes, from one source that also sets `ESO_NAMESPACE` for the installer, and
  the default is `default` on every path so an unchanged deployment renders byte-identically. MEASURED
  AFTER: the policy names `external-secrets`, and the same create as
  `system:serviceaccount:external-secrets:external-secrets` is `created (server dry run)`.
  Taking that reading is what showed the fix was only half of one. The upstream ESO bundle names
  `default` in SIXTEEN places -- the metadata of every namespaced object, the namespace of each
  ClusterRoleBinding's subject, and the webhook's service namespace -- and defines no Namespace object
  of its own, so `ESO_NAMESPACE` only ever decided which namespace the ROLLOUT WAIT looked in. With the
  grant naming `external-secrets` and the controller still installed in `default`, the grant admitted a
  controller that was not installed and denied the one that was. The installer now creates the
  namespace and rewrites those sixteen references; measured after: the controller's three pods are in
  `external-secrets`, its bindings' subjects name it, and `default` -- which the grant no longer names
  and which STILL has the RBAC -- is refused, which is the pair that isolates the policy from RBAC.
  What this does NOT close is the ExternalSecret's own sync: that needs a store a tenant can reach,
  which is ESO-3, and the two are separate. `NEXT.yaml` ESO-2.

- **FLAKE-3: the OpenBao live gate lends itself a port by listening and closing, so a concurrent run can
  take it.** `internal/openbao/openbao_test.go`'s `freePort` listens on `127.0.0.1:0`, reads the port,
  CLOSES the listener, and writes a vault config on that number with `tls_disable = true`. Between the
  close and the vault binding it, the number is free for anyone -- including a second `go test` of this
  repo running at the same time. MEASURED on 2026-10-07, with two other test runs in flight:
  `TestClientAgainstRealBao` failed with `the vault answered before it was ready, which this gate did
  not expect: openbao: GET /v1/sys/health -> HTTP 400: Client sent an HTTP request to an HTTPS server`
  -- the gate's plain HTTP probe reached somebody else's TLS listener on the port it had just given
  back. Run alone three times, the package is green each time. This is the same class as the rule the
  TypeScript side already carries for exactly this pattern; it was written there and not here.
  `NEXT.yaml` FLAKE-3.

## 16. Vocabulary

| | |
|---|---|
| **gateway** | the host-side process set: for us, kcp plus the per-cluster process groups |
| **mount** | the kcp workspace mount by which a tenant reaches its cluster's apiserver |
| **guest** | the VM a node IS: its own kernel, containerd and network stack. A node rented from the market is one too, but it is not THIS host's guest: no tap, no pasta and no address out of this host's subnet leases |
| **node** | a worker. One long-lived command. Never the apiserver |
| **instance** | one cluster's incarnation. The token is per instance |
| **ON / OFF** | whether a cluster's process group is up |
| **zero state** | control plane up, zero nodes, workloads Pending |
| **key** | the name a cluster has INSIDE a provider: `kcpclient.ClusterKey(home)` = `<accountHash>-<name>`, derived from its home workspace path. Unique across accounts; the bare name is not |
| **seam** | the interface where a substrate plugs in: `ComputeProvider`, `scale.Nodes`, `processrunner` |
| **the door** | the market's front door, in the sibling Deno process: it holds each owner's atproto OAuth session, fulfils the `NodeClaim`s this repo writes, and is the only writer of a claim's status. Its own build is out of scope here |
