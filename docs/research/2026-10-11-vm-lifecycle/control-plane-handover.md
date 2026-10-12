# Control-plane handover: durable ownership and zero-downtime k3s swaps

Research + design, 2026-10-11. Input to plans 0006 (M3-CLUSTER-SCOPES, M3-SERVE-GATEWAY-KCP,
M5-CUTOVER) and 0007 (M6-GATEWAY-REPOPULATE). Nothing here is decided until the user answers
section 8.4. Answers user-decisions.md line 17 ("Cutover / control-plane continuity: NOT decided").

User requirement (2026-10-11): the gateway tracks durably which clusters own which market-rented VMs,
so after any restart/upgrade the nodes re-join their control planes; ideally zero downtime for
clusters by bringing up a new k3s control-plane process and shutting down the old one.

Already decided (not reopened, refined only where marked): gateway + cluster groups run as transient
services in a lingering user systemd manager; each cluster's postgres is a Postgres object with its
own unit; the cluster group (kine, k3s, bao, dumbpipe, ESO, CCM) is its own unit so a core upgrade
does not restart control planes; market node loss = Node NotReady 90 s -> released and replaced.

Evidence tags: **[M]** measured on hat-8 for this document (stand-in processes only, section 2.1);
**[S]** upstream source or docs, URL in Sources; **[C]** this repo, file:line; **[U]** unverified,
must be measured before it is relied on.

---------------------------------------------------------------------------------------------------

## 0. Answer in one screen

1. **k3s HA on a shared external datastore is a working blue/green mechanism today** [M]. A second
   `k3s server --disable-agent` with the same token file, pointed at its own kine on the same
   postgres, reconciled the bootstrap data from the datastore, came up with byte-identical CAs and
   service-account key, served the FIRST server's admin client certificate, and was answering in
   2.2 s. With a 100-line L4 front holding the stable port, switching the front and SIGTERMing the
   old server gave **0 failed `/readyz` polls of 515 (200 ms cadence), 0 failed node-lease
   heartbeats, and the (fake) Node stayed Ready** through a 16 s controller-manager leader handover.
2. **The agent load-balancer problem in the brief does not exist here**, by construction: every k3s
   of a market cluster advertises the guest-local link address (`--advertise-address` = the address
   the guest's own `socialweb-apiserver-link` binds on `lo`) and the same advertise port. The only
   address an agent can learn from the `kubernetes` EndpointSlice is the one it already dials. The
   invariant to keep is "every slot advertises the SAME address AND the SAME port" [M, C].
3. **The link is not swapped for a k3s swap.** A per-cluster L4 front takes over the `k3s-secure`
   lease port (the port dumbpipe targets, the guest's `apiserverPort`, `status.URL`, every
   kubeconfig). k3s slots move to new leased ports behind it. Guest tickets, node secrets, kcp
   mounts and kubeconfigs never change. dumbpipe is re-started only for its own upgrades, and iroh's
   relay already promotes the newest connection of a duplicated endpoint id [S].
4. **Hazards found that the plans do not name:** (a) the k3s `kube-system/k3s` leader lease uses the
   OS hostname as identity, so two servers on one host BOTH lead k3s's own controllers during the
   overlap [M, S]; (b) the market kube-scheduler runs `leaderElect: false` because "one k3s server
   per cluster: nothing to elect" [C] - false the moment two slots overlap; (c) k3s exits 0.1 s
   after SIGTERM [M], so in-flight exec/attach/log streams (the GitLab runner's job stream) break on
   any swap - zero API downtime is not zero stream disruption; (d) users reach a tenant cluster
   THROUGH the kcp mount [C], so a kcp restart is a tenant API outage even when every k3s stays up.
5. **Durable ownership:** NodeClaim (`status.contract` + `status.grant`) plus the cluster vault is
   enough for Joining/Running nodes; Bidding/Provisioning are lost only because the per-contract
   secrets-server keypair is in memory [C]. Add a per-claim ingress key in OpenBao and a served-state
   on the claim; add a host-local per-cluster control-plane record (active slot, versions, ports,
   units) that adoption reads without kcp.
6. **Recommendation:** blue/green slots behind a front for every recurring k3s/kine/supervisor
   upgrade (new items M3-APISERVER-FRONT, M3-CP-SLOTS); members as units under a per-cluster slice
   instead of one supervisor unit; swap a cluster when idle if possible, else blue/green; for the
   ONE-TIME container->host cutover, a **guarded cold restart** (measured on the market bed first,
   NODE-LOSS clock rebased to "path up") rather than a host-side takeover of a live container -
   cost/risk in section 8.3.

---------------------------------------------------------------------------------------------------

## 1. Current topology (what exists on main today)

### 1.1 The path from a guest's kubelet to its tenant apiserver

```
guest (bidder host, cloud-init user_data)                       gateway (container today)
-----------------------------------------                       -------------------------------------
kubelet / meshd / k3s agent
  -> 127.0.0.1:6444  k3s agent client LB (supervisor+apiserver)
  -> ADVERTISE_ADDRESS:APISERVER_PORT  (address put on guest lo)
       socialweb-apiserver-link.service
       dumbpipe connect-tcp --addr ADV:PORT <stable ticket>  ==iroh/QUIC==>  dumbpipe listen-tcp
                                                                           IROH_SECRET=<vault seed>
                                                                           --host 127.0.0.1:<k3s-secure>
                                                                           --ipv4-addr 0.0.0.0:<link UDP>
                                                                             -> k3s server --disable-agent
                                                                                --https-listen-port <k3s-secure>
                                                                                --advertise-port   <k3s-secure>
                                                                                --advertise-address ADV
                                                                                -> kine -> postgres
```

- Guest side [C] `socialweb-computer-k8s/lib/cloud-init-plugin-k3s-join/mod.ts`:
  apiserver link script `:366-370` (`exec dumbpipe connect-tcp --addr "${ADVERTISE_ADDRESS}:${APISERVER_PORT}" "${APISERVER_TICKET}"`),
  its unit `:555-561` (`Restart=always`, `RestartSec=5`, `StartLimitIntervalSec=0`, `:525`);
  the agent `:585` (`k3s agent --server https://${ADVERTISE_ADDRESS}:${APISERVER_PORT} --token-file ...`);
  node-secrets writes ADVERTISE_ADDRESS/APISERVER_PORT/APISERVER_TICKET from the KV entry `:313-342`
  and refuses non-IPv4 / non-digit / non-[a-z0-9] values `:44-45`; the OpenBao link `:354-357`;
  meshd `:112-125` (`--kubeconfig /var/lib/rancher/k3s/agent/kubelet.kubeconfig`, i.e. it rides the
  agent LB, and its own iroh secret in `StateDirectory=socialweb/meshd`).
- Gateway side [C]: `internal/clusterlink/clusterlink.go:185-191` (`Argv`: `listen-tcp --host
  127.0.0.1:<target> --ipv4-addr 0.0.0.0:<linkPort>`); `internal/clusterprovider/links.go:104-131`
  (targets = k3s secure port and vault port, appended after every stack member), `:154-173` (seed read
  from the cluster vault in `Prepare`, passed only as `IROH_SECRET` env), `:181-211` (gate opens when
  the printed ticket names the vault key).
- k3s argv [C] `internal/clusterstack/clusterstack.go:281-317`; ports `internal/clusterprovider/stack.go:271-278`
  (`advertisePort = listenPort`, one `k3s-secure` lease answers both) and the gate
  `internal/clusterprovider/steps.go:164-174` (`AssertMarketAdvertiseInvariant`, `/readyz`, then
  the first kube-system Deployment). `k3s-secure` is width 2 (listen + listen+1)
  `internal/portallocator/portallocator.go:75-89`. ARCHITECTURE section 8 records why advertise and
  listen were made one port: split, the EndpointSlice named a port nothing on the guest bound.
- kine argv `clusterstack.go:319-329` (`--endpoint <postgres DSN> --listen-address 127.0.0.1:<kine>`);
  k3s dials it as an etcd endpoint (`--datastore-endpoint http://127.0.0.1:<kine>`,
  `stack.go:37,157`), so k3s runs in its "external etcd" mode against kine.

### 1.2 Durable vs per-process

| Item | Where | Durable? |
|---|---|---|
| Link iroh key (apiserver, openbao) | cluster vault KV `socialweb-links/<service>` (`clusterlink.go:113-146`, CAS-0 first write, never regenerated) | yes - survives every restart while the vault data lives |
| Published ticket | `ClusterLink.status.ticket` = `StableTicket(key)`: id only, no relay, no addrs (`clusterlink.go:163-167`) | yes - function of the key alone |
| Printed ticket (relay URL + direct addrs) | dumbpipe stdout | per process, NOT published |
| Link UDP port | lease `apiserver-link`/`openbao-link` in leases.json | yes, but the published ticket does not carry it (discovery resolves addrs) |
| Node join secret | cluster vault `socialweb-nodes/data/<node>`: token `K10<sha256 CA>::server:<tok>`, nodePassword, serverCA, apiserverTicket, advertiseAddress, apiserverPort (`internal/clusterprovider/nodesecrets.go:77-84`) | yes; read once by the guest's oneshot node-secrets |
| k3s token | `<dataDir>/token` (`stack.go:33-35`), passed `--token-file` | yes - and it is the PBKDF2 passphrase of the bootstrap data in the datastore [S] |
| k3s CA, SA key, bootstrap | encrypted in kine/postgres + `<dataDir>/server/tls` | yes [S, M] |
| Ports | leases.json (`SOCIALWEB_CLUSTERPROVIDER_LEASE_PATH`, `deploy/container-entrypoint.sh:46`) | yes |
| Advertise address | gateway constant (`SOCIALWEB_CLUSTERPROVIDER_ADVERTISE_ADDRESS`) | yes |
| Postgres data + password | `<dataDir>/postgres`, `postgres.pw` (`postgres.go:22-40`); adopted if already serving (`postgres.go:116-122`) | yes |
| Vault storage | postgres (`internal/clusterstack/openbao.go:24-35`, `disable_clustering = true`, no `ha_enabled`) | yes; seal via the provider's seal file |
| Door secrets server | fresh Secp256k1 keypair + ingress subdomain PER CONTRACT, in memory (`atproto-market/lib/guest-capability-secrets/mod.ts:50-75`); grant installed in memory at onContract | **no** - this is why Bidding/Provisioning claims die with the door |
| Provider in-memory | mints, readySatisfied (`provider.go:129-136`), loss clock fallback "first seen" when lastTransitionTime is zero | no |
| apiserver TCP conns, agent tunnels, watches | processes | no |

### 1.3 ClusterLinks and meshd

`ClusterLink <clusterKey>-apiserver|openbao` (spec cluster/service/targetPort, status phase,
endpointId, did, ticket) is written Listening by a reporter goroutine with retries
(`links.go:213-240`); on listener exit it writes Failed, or Pending "process group was stopped" when
the group was reaped. A link that dies after bring-up is NOT restarted until the next bring-up
(ARCHITECTURE section 5 "The cluster links", stated limits). meshd is per node, guest-side, iroh-keyed
in the guest's own state dir, and reaches the apiserver through the agent LB, so it inherits the same
path as the kubelet and needs nothing from a control-plane swap.

### 1.4 How ownership is recorded

- NodeClaim (kcp, cluster's home workspace) `api/v1alpha1/types_nodeclaim.go:60-80,186-205`:
  `status.contract` {vmUri, rfpUri, acceptUri, receiptUri, receiptCid, submitEventRef, phase,
  vmName, requesterDid, network, released}, `status.grant` {issuer, subject, audience, role},
  `status.phase` Pending|Bidding|Provisioning|Joining|Running|Failed|Released. Name =
  `<cluster>-node<N>` = tenant Node name. Plan 0005 M2-CLAIM-RECEIPT: "the claim is the durable
  record a restarted door resumes from".
- Market records (requester's PDS: rfp, accept, vm.delete; winner's repo: receipt). The claim
  mirrors their URIs; the records are evidence, the claim is the index.
- Tenant `Node` (kine/postgres of the cluster) + `kube-node-lease/<node>` - the liveness truth.

### 1.5 Restart behaviour today

- **Provider** (plan 0005 M3-NODE-LOSS provider half, acd218f; ARCHITECTURE section 9 "A restarted
  provider never reaps; it adopts"): shutdown leaves market nodes; startup adopts every live claim
  (`internal/nodemarket/adopt.go:15-23`) before the cluster's CCM starts; a Failed claim no process
  holds is deleted so the door releases it (b2624c6). Loss rule: Ready False/Unknown with
  `now - lastTransitionTime >= 90 s` (`cmd/socialweb-clusterprovider/nodes.go:194-223`), never-Ready
  nodes get a 2 m join grace (`config.go:51`).
- **Provider vs a running group**: an apiserver answering at `status.URL` with no group held is
  "Foreign ... left alone" (`provider.go:164-168`); the restart reap scans leased ports and argv
  (`cmd/socialweb-clusterprovider/startup.go:95-200`), adopts the store (`EnsureStore`), reaps
  unowned survivors, refuses unattributable holders.
- **Door** (`socialweb-computer-k8s/lib/socialweb-computer-k8s-node-market/mod.ts`): Joining/Running
  claims are adopted by not touching them (`pass()` `:497-519` acts only on Pending, deletion, and
  `ORPHANED_PHASES = {Bidding, Provisioning}` `:90`); orphans are marked Failed with "the secrets
  server its guest was told to use went with the old door" (`:487-495`) and released on deletion
  from the receipt on the claim. `stop()` releases nothing (`:533-539`).

### 1.6 What dies on a container restart today

Everything gateway-side: `container-entrypoint.sh:283-341` supervises rootvault, kcp, gateway, socat
TLS front, clusterprovider, door, and exits 1 when any child exits (`:328-340`); every process-group
member is spawned with `Pdeathsig: SIGKILL` from a locked thread (`internal/processrunner/processrunner.go:121`,
`internal/hostos/sysproc_linux.go:14-15`, plan 0005 M6-LIMITS) so k3s, kine, bao, ESO, CCM and both
dumbpipe links die with the provider; per-cluster postgres has no Pdeathsig (adopted if serving) but
dies with the container. What survives: guests and their `Restart=always` link/agent units, market
records, kcp's root and every cluster's postgres/vault data on the volume, leases.json. Cold start
after restart: postgres, bao + unseal, kine, k3s, controllers, CCM, ESO, links - measured 22.68 s to
Ready at zero nodes with ESO in the group (versions.json `external-secrets.measured`). Note: the dev
script `deploy/run-container.sh:11-19` publishes ports with `-p`; prod (per the user) runs
`--network host`, which section 5c relies on.

---------------------------------------------------------------------------------------------------

## 2. k3s HA with an external datastore as the blue/green mechanism

### 2.1 The measurement [M]

Stand-ins only, in the scratchpad: PostgreSQL 18.6 (`initdb --auth=trust`, 127.0.0.1:35432, no unix
socket), two kine v0.17.1-dev (35001, 35002) on the SAME database, k3s v1.36.4+k3s1 x2, a Python
asyncio L4 front (127.0.0.1:35443, backend read from a file per new connection). No agent, no guest,
no container. Server flags copied from `ServerArgv` (market form):
`--disable-agent --disable=traefik --egress-selector-mode cluster --advertise-address 10.77.0.1
--advertise-port 35443 --bind-address 127.0.0.1 --https-listen-port <slot> --token-file <same file>
--tls-san 10.77.0.1 --flannel-backend=none --cluster-cidr=10.42.0.0/16`, per-slot controller ports.

| Reading | Result |
|---|---|
| blue cold start to first kube-system Deployment | ~1-2 s (zero nodes, empty DB) |
| green (second server, own data dir, own kine, same token, same PG) serving with BLUE's admin client cert | 2.2 s after start |
| green log | `Reconciling bootstrap data between datastore and disk` |
| sha256 server-ca.crt / client-ca.crt / request-header-ca.crt / service.key / server/token, blue vs green | identical, all five |
| `kubernetes` EndpointSlice, blue only / both / after blue stopped | `10.77.0.1:35443` throughout (the advertise pair; k3s passes `--advertise-port` to its apiserver) - no removal event seen |
| kube-apiserver binds | `127.0.0.1:<slot>`, `[::1]:<slot>`, `127.0.0.1:<slot+1>` |
| fake Node `fake1` (Ready=True patched) + Lease renewed every 2 s through the front | Node Ready the whole run, `lastTransitionTime` unchanged |
| switch front to green, wait 5 s, SIGTERM blue | blue exited in **0.10 s** |
| `/readyz` (authenticated) through the front every 200 ms across the swap | **515 polls, 0 non-200** |
| heartbeats through the front | **0 failures** |
| `kubectl get node -w` through the front | exited 0.04 s after the SIGTERM (connection closed; a reflector would re-watch) |
| leases after the swap | kube-controller-manager acquired by green 16.4 s after SIGTERM (15 s lease, no release on cancel); kube-scheduler 17.9 s; k3s-cloud-controller-manager 18.1 s; `leaseTransitions: 1` each |
| `kube-system/k3s` lease | holder `hat-8` (the hostname) for BOTH servers; green logged `Successfully acquired lease lock=kube-system/k3s` while blue held it; a third start with `--node-name slot-blue` did the same - identity is the hostname, not the node name |
| restart in place (blue, warm data dir, same kine) | serving in 1.7 s |
| kine x2 on one PG | no errors in either log; schema `name text COLLATE "C"`, unique `(name, prev_revision)` |
| k3s process title | rewritten to `k3s server` (argv not visible in /proc cmdline) |

All processes were stopped and the data dirs deleted; scripts and logs remain in
`scratchpad/handover/` (front.py, start.sh, heartbeat.sh, poll.sh, logs/).

### 2.2 What the second server must share, and what it must not

Must share: the token (same `--token-file`; bootstrap data is encrypted with it [S k3s token]);
`--datastore-endpoint` reaching the same database (through its own kine or the same kine);
cluster-wide flags k3s requires equal on all servers: `--cluster-cidr`, `--service-cidr`,
`--cluster-dns`, `--cluster-domain`, every `--disable`, `--disable-helm-controller`,
`--disable-kube-proxy`, `--disable-network-policy`, `--secrets-encryption` [S k3s HA]; for us also
`--flannel-backend=none`, `--egress-selector-mode cluster`, the audit flags, `service-account-issuer`
and `api-audiences` (tokens minted by one slot must verify on the other), `--advertise-address` and
`--advertise-port` (section 2.5), `--tls-san` (the advertise address).

Must NOT share: `--data-dir` (server/tls, server/cred, kubeconfigs and the dynamic listener cache are
per server); `--https-listen-port` and its +1; the three controller secure ports; the kine listen
port when slots carry their own kine. The admin kubeconfig k3s writes names the slot's listen port,
so every consumer (provider admin, CCM, ESO, the kcp mount via `status.URL`) must name the FRONT port
instead; client certificates are interchangeable because the client CA is shared [M].

CA reuse: automatic. A fresh data dir pulls CA, SA key and request-header CA from the datastore; disk
newer than datastore is reconciled by k3s at start [M log line, S].

Node password: k3s keeps node passwords as `kube-system/<node>.node-password.k3s` Secrets in the
datastore, so both slots validate the same guests. The server-side `/etc/rancher/node/password` is
read/written by every `k3s server` (versions.json k3s invariant) and its value is never checked for a
`--disable-agent` server, so two slots sharing the file is harmless [C versions.json].

### 2.3 Leader election during the overlap

- kube-controller-manager, kube-scheduler, k3s-cloud-controller-manager: client-go leases with
  unique identities (`hat-8_<uuid>`); handover took the full 15 s lease duration after SIGTERM [M]
  (kcm default `--leader-elect-lease-duration 15s` [S kcm]). During those ~16 s no controller loop
  runs: no NotReady marking, no scheduling, no GC. Acceptable; shorter leases are a tuning option.
- **k3s's own leader-elected controllers** (wrangler `leader.RunOrDie` with `id := os.Hostname()`,
  45 s lease [S wrangler leader.go:22,42]; k3s registers its apiserver controllers there [S k3s
  server.go]): two slots on one host share the identity, so both run them for the whole overlap [M].
  With equal versions this is duplicate idempotent applies (addon manifests, helm controller). With a
  k3s VERSION change, blue re-applies the old bundled addon manifests while green applies the new ones
  until blue stops. Mitigation: keep the overlap to "green ready -> switch -> stop blue" (seconds);
  later option: run each slot with a private UTS namespace/hostname [U for a user-manager unit].
- **kube-scheduler**: the market scheduler config sets `leaderElect: false`
  (`internal/clusterstack/scheduler.go:51-52`, commit 1cbf945: "One k3s server per cluster: nothing
  to elect", made to avoid a 15 s wait on the previous incarnation's lease). With two slots both
  schedulers are active once a node exists: binds race (the loser gets a conflict) and preemption can
  evict twice. Fix: `leaderElect: true` with a short lease (e.g. leaseDuration 4s, renewDeadline 3s,
  retryPeriod 1s): worst-case first-pod delay drops from 15 s to ~4 s instead of 0 [U measure].
  k3s starts the scheduler only when a node exists (no `kube-scheduler` lease appeared at zero nodes,
  it appeared after the fake Node) [M].
- ESO and the socialweb CCM run `--enable-leader-election=false` / no election
  (`clusterstack.go:243`, versions.json): they are single members of the group and must stay single;
  they are NOT duplicated per slot - they dial the front and ride the swap.
- kube-apiserver identity lease is named from the hashed hostname [S k8s leases], so both slots
  share `apiserver-<hash>` [M]; nothing in this design reads it.

### 2.4 Version skew for rolling server upgrades

- k3s: "upgrade server nodes first one at a time, then any agent nodes" [S k3s manual upgrade].
- Kubernetes: HA kube-apiservers within one minor of each other; kcm/scheduler/ccm not newer than
  the apiserver they talk to; kubelet not newer than the apiserver and up to three minors older
  [S skew policy]. In a k3s slot each kcm/scheduler talks only to its own apiserver over loopback, so
  the only cross-version contact is shared storage and the overlap window.
- Guests never upgrade in place (nodes are disposable; a new node takes the door's current k3s
  pin), so the rule for the door is: control plane minor >= guest minor, and <= guest minor + 3.
- One minor at a time; patch releases freely. Rollback to the previous MINOR after green has written
  is not supported by Kubernetes or k3s; rollback is to the previous patch of the same minor only.
  A minor bump therefore needs a postgres snapshot (pg_dump of `k3s` + `openbao`) taken right before
  the switch.
- Fields: a newer apiserver may write fields an older one prunes on a later update through it; the
  overlap is seconds, which bounds this.

### 2.5 The agent's client-side load balancer

k3s agents seed their LB with `--server`, then sync the address list from the `kubernetes`
EndpointSlice and keep the `--server` address as the default server [S k3s architecture, k3s
loadbalancer/servers.go, tunnel.go `watchEndpointSlices`]. The brief's worry - endpoints are
gateway-local addresses a guest cannot reach - does not apply: `--advertise-address` IS the address
the guest's link binds on its own `lo`, and `--advertise-port` IS the port that link listens on
(the market invariant, `steps.go:168`). Every slot must advertise the same pair, so the EndpointSlice
reads one address that equals `--server` [M: `10.77.0.1:35443` with two servers]. With the front,
advertise-port becomes the FRONT port (= the existing `k3s-secure` number) and the slot's listen port
is never advertised; the experiment proves k3s honours a split advertise port in the EndpointSlice.
`--disable-apiserver-lb` (experimental) exists on the agent [S k3s cli agent] and is not needed; do
not add it - the LB's reconnect behaviour is what moves agents from blue to green.

Endpoint removal on shutdown: the lease reconciler keys master leases by IP and calls
`RemoveEndpoints` on graceful stop [S k8s lease.go]; both slots share the IP, so a graceful blue could
blank the Endpoints until green's next reconcile. Not observed (blue's 0.1 s exit) [M]; the bed test
must still assert the slice never empties [U].

### 2.6 Kubelets, watches and NotReady across a swap

- Watches: the front closes the client side when the backend closes; client-go reflectors re-list and
  re-watch through the front to green. Resource versions are kine row ids in the shared table, so a
  resumed watch is valid or gets 410 and re-lists.
- Heartbeats: the kubelet renews `kube-node-lease/<node>` (every 10 s by default) [S k8s leases/nodes];
  the swap gap is the front switch plus one reconnect.
- NotReady timing: kcm `--node-monitor-grace-period` default 50 s [S kcm]. The node lifecycle
  controller stamps a node it has not seen before with `probeTimestamp = now`
  [S node_lifecycle_controller.go "Creating timestamp entry for newly observed Node"]; a new kcm
  leader (green, or a cold-started cluster) therefore gives every node a fresh 50 s before it can mark
  it Unknown. NODE-LOSS then needs 90 s more of NotReady. A total control-plane outage costs a node
  only if its path does not recover within ~50 s after the control plane returns AND stays down 90 s
  more.
- Agent tunnel (`egress-selector-mode cluster`): apiserver -> kubelet (logs, exec, attach,
  port-forward) rides the agent's websocket tunnel to the server it connected to. Between "front
  switched" and "agent reconnected to green", green has no tunnel for that node and exec/logs fail;
  when blue stops, every exec/attach stream through it ends. GitLab's kubernetes executor drives jobs
  through exec/attach [U: measure whether an in-flight job fails on a stream reset]. Consequence:
  swap when idle (zero nodes or no running runner pods) where possible; otherwise switch and stop
  blue immediately so tunnels move within one reconnect backoff [U: measure the backoff on the bed].

---------------------------------------------------------------------------------------------------

## 3. kine

- **Several kine instances on one database are the normal k3s HA shape** (every k3s server embeds
  one against the shared SQL datastore [S k3s HA]). Concurrency safety is in the schema: unique
  `(name, prev_revision)` makes a racing second create/update of the same revision fail like an etcd
  compare [S kine pgsql.go; M schema]. Revisions are the postgres sequence; concurrent writers leave
  temporary gaps, which the poll loop waits out and then fills with a gap record
  [S kine sqllog sql.go "MODREVISION GAP" / `Fill`]. Each kine's watchers see the other's writes by
  polling the table, so cross-slot watch latency is the poll interval, not zero.
- **Compaction leader**: standalone kine's own compaction is off by default ("so that compact may be
  managed by the apiserver") [M `kine --help`]; kube-apiserver compacts every 5 min and coordinates
  across apiservers with a CAS on `compact_rev_key` ("CAS fail will not incur any error")
  [S k8s etcd3/compact.go]. No compaction leader to manage.
- **Upgrades / schema**: on start kine runs `CREATE ... IF NOT EXISTS` and `schemaMigrations`
  (`ALTER TABLE kine ALTER COLUMN id ... BIGINT`, `ALTER ... name ... COLLATE "C"`) when needed
  [S kine pgsql.go; M "Database tables and indexes are up to date"]. An ALTER takes an ACCESS
  EXCLUSIVE lock, so a NEW kine starting beside an OLD one can stall the old one's writes for the
  migration. Rule: a kine bump is its own green slot, and its release notes are read for schema
  changes; a bump that migrates gets a bed test with old+new kine on one database first. kine has no
  downgrade path once it migrated.
- Pairing: one kine per slot (kine@a with k3s@a). It keeps the kine version a property of the slot,
  lets a slot die without taking the other's store connection, and costs one leased port per slot.

---------------------------------------------------------------------------------------------------

## 4. The gateway-side link swap

**Do not re-point dumbpipe for a k3s swap.** dumbpipe `listen-tcp` dials `--host` per accepted iroh
connection [S dumbpipe main.rs `TcpStream::connect` in `listen_tcp`], so whatever holds the target
port decides the backend per connection. A per-cluster L4 front on the target port makes the switch
deterministic for every client at once: guests (via dumbpipe), the kcp mount (`status.URL`), the
provider's admin client, CCM and ESO.

Front design (decided by Claude, user may veto):
- Binds `BindAddress:<k3s-secure lease port>` - the port k3s binds today. Nothing upstream changes:
  dumbpipe `--host`, the guest's `apiserverPort` in every node secret, `status.URL`, kubeconfigs and
  the advertise port all keep their number. Existing guests need no rewrite.
- Backend chosen per NEW connection from a state file or control socket; existing connections stay
  on their backend until it closes them (that is the drain). Pure TCP passthrough: TLS stays end to
  end with k3s (serving SANs already cover 127.0.0.1 and the advertise address).
- Its own unit (`socialweb-c-<key>-front`), tiny, version-pinned, restarted almost never. Its
  listening socket is kept in systemd's fd store (`FDSTORE=1`, `FileDescriptorStoreMax`) so a front
  restart has no refused-connection window, only resets of open connections [U: user-manager fd
  store on the target systemd].
- Health: the front never fails over by itself; the reconciler switches it. (Auto-failover would
  hide a dead green.)

**The iroh key stays put.** It lives in the cluster vault and is read in `Prepare`
(`links.go:154-173`); any new dumbpipe process gets the same `IROH_SECRET`, so guest tickets stay valid
(measured by the repo: `TestARealDumbpipeLinkCarriesTCPThroughItsPublishedTicketWhichARestartLeavesByteIdentical`).

**Swapping dumbpipe itself** (dumbpipe upgrade, cutover): iroh-relay keeps one active and N inactive
connections per endpoint id; a newer connection with the same id becomes active and the older ones get
`SameEndpointIdConnected` and are promoted back if the active one leaves
[S iroh-relay server/clients.rs]. So "start new dumbpipe with the same key, then stop the old one"
moves relayed dials to the new process while open QUIC connections finish on the old. Unverified for
our pin: dumbpipe v0.39.0's iroh version must already carry this relay behaviour, n0's public relays
must run it, and discovery (signed address record published per process) is last-writer-wins
[S iroh docs] [U all three: bed test]. The second process cannot bind the same UDP link port, so it
takes the other slot's link lease (or a random port; the published ticket carries no address).

Alternatives considered: (a) restart dumbpipe pointing at green - every client's connection drops at
once and gateway-local clients still need re-pointing; (b) SO_REUSEPORT on the k3s port - k3s does not
set it; (c) systemd-socket-proxyd - one fixed backend per process, a switch is a restart; (d) HAProxy
or Envoy - a new asset with a runtime API for one backend flip; the Go front is ~150 lines and is
socialweb code that never touches a VM (invariant holds).

---------------------------------------------------------------------------------------------------

## 5. Procedures

Notation: slots `a`/`b` per cluster, each = `kine@<slot>` + `k3s@<slot>` units with their own leases;
`front`, `bao`, `link-apiserver`, `link-openbao`, `ccm`, `eso`, `esowh` are single units; all under
`socialweb-cluster-<key>.slice`, after `socialweb-pg-<key>.service` (A-x-ordering in
plan-0006-0007-linux-exploration.md). A per-cluster record `<dataDir>/controlplane.json` holds
{activeSlot, slots{a,b: version, kineVersion, ports, unit, invocationID}, front{port, backend}}.

### (a) k3s/kine version upgrade of one cluster (blue = active slot)

1. Preconditions: target k3s minor in {active, active+1}; target >= every guest minor and <= guest
   minor + 3; the cluster is Ready; `kubernetes` EndpointSlice = (ADV, front port). Prefer an idle
   moment: defer up to a bound while runner pods are Running (2.6). Check: provider reads the slice and
   the claim list.
2. `pg_dump` k3s + openbao databases to `<dataDir>/backups/<ts>` if the minor changes. Check: dump
   exit 0, size > 0.
3. Lease green ports (k3s pair, 3 controller ports, kine) if not held. Check: allocator bind probe.
4. Start `kine@green` (target kine) on the same DSN. Check: dial, log "tables and indexes are up to
   date", no lock wait > 2 s on the active kine.
5. Start `k3s@green` with identical cluster flags, same token file, same advertise pair, own data dir.
   Check: green CA hash == active CA hash; `/readyz` 200 on the green port with the admin cert; first
   kube-system Deployment listed (the existing `waitK3sControllers` gate) through the green port.
6. Switch the front backend to green; persist `controlplane.json` (activeSlot=green) BEFORE the switch
   is acknowledged. Check: a request through the front lands on green (green's audit log or
   `apiserver_request_total`).
7. Immediately SIGTERM `k3s@blue` (k3s exits in ~0.1 s); stop `kine@blue`. Check: front's blue
   connection count -> 0; EndpointSlice still (ADV, front port); every held market Node Ready (no
   transition); every claim unchanged.
8. Watch for 60 s (> node-monitor-grace): kcm/scheduler/ccm leases held by green (~16 s); agent
   tunnels re-established (an exec into one pod per node succeeds); ClusterLinks still Listening.
9. Keep blue's data dir and units' ExecStart (old version) for one rollback window; rollback = same
   procedure in reverse, only within the same minor.

### (b) cluster-group supervisor / core binary upgrade

With members as units under a slice there is no resident supervisor: a core upgrade (A-x-upgrade
step 3) leaves every unit running and the new core adopts by unit name + `controlplane.json` +
InvocationID (Verdict machinery, Unknown retries, mismatch stops). Checks: every expected unit active,
front backend == activeSlot, `/readyz` through the front, ClusterLinks Listening; no unit restarted
(InvocationIDs unchanged). Members whose binary ships inside the core (CCM is our Go binary; the front)
roll explicitly: CCM = stop/start (no election, ~seconds without Node reconciliation, harmless); front
= restart with the fd store (open connections reset once, clients reconnect).

If the decided single-unit group with a resident supervisor is kept instead: a supervisor upgrade
restarts all members (cold restart of the cluster, (d) timings) unless it hands its children over,
which Pdeathsig forbids. That is the main argument for 8.1's member-units refinement.

### (c) the one-time container -> host cutover

Recommended: **guarded cold restart** (8.3). Steps:
1. Bed rehearsal (once): `internal/marketbed` with a market node joined through runComputeContract;
   stop the control-plane container; start the host core on a copy of its state; record
   time-to-Ready per cluster, time until the node's Lease renews, Ready transitions, exec success.
2. Ship NODE-LOSS rebase (M3-NODE-LOSS-CLOCK) and the front + slot layout in the host core FIRST, so
   the host start is the last cold start ever.
3. Prod: pick a window when the most clusters are at zero nodes (ACC-ZERO says idle clusters sit at
   0). `docker stop` (no rm). Copy the volume to /var/lib/socialweb (uid move, linger first; existing
   M5-CUTOVER script). Start the user unit.
4. Checks as M5-CUTOVER done_when plus: per cluster, `/readyz` through the front, every held Node
   Ready with `lastTransitionTime` before the window or Ready again within 50 s, no claim deleted, no
   vm.delete sent. Rollback unchanged (stop unit, `docker start` on the untouched volume).

The zero-downtime takeover the brief describes is feasible on `--network host` and is written out so
its cost is visible (8.3 rejects it for a one-time event):
1. Root step: copy token, `postgres.pw`, vault seal file, vault TLS, audit/scheduler configs from the
   volume to the host state dir (the volume is root-owned; the gateway user cannot read it).
2. Freeze the container provider without killing its group: `kill -STOP <socialweb-clusterprovider>`
   (Pdeathsig fires only on death; a stopped provider reconciles nothing, so it neither respawns nor
   reaps). The entrypoint's `kill -0` liveness loop still passes. Check: no provider log lines.
3. Per cluster: host `kine@b` + `k3s@b` on new leases against the container's postgres
   (127.0.0.1:<pg lease>). Check as (a)5.
4. Kill the container's k3s (0.1 s) and bind the host front on the `k3s-secure` port in the same
   breath (front started earlier, retrying bind). The container's dumbpipe keeps dialing
   127.0.0.1:<k3s-secure> and now reaches green. Check: (a)7.
5. Host `link-apiserver` and `link-openbao` with the same keys (read from the container's vault on
   loopback with the root token), new UDP ports; then kill the container's links (4. relay handover).
   Check: a guest-side dial (the bed) through the published ticket reaches the host process.
6. Vault: stop the container's bao, start the host bao on the same storage and port (no HA lock, so
   never two at once), unseal. Check `/v1/sys/health`, a node-secret read.
7. Postgres: add `host replication postgres 127.0.0.1/32 scram-sha-256` to the container pg_hba and
   reload; `pg_basebackup -R -X stream` into the host Postgres object's data dir on a new port; wait
   for `pg_stat_replication` replay lag 0; stop `kine@b`; `pg_promote()`; start `kine@b` with the new
   DSN (1-2 s write outage; kube-apiserver retries). Same for the vault DSN (bao restart). Preconditions:
   same PG major, same arch, compatible build; the openbao table's text keys use the database default
   collation (initdb ran with no `--locale`, `postgres.go:279-285`), so the host build's collation must
   match or `REINDEX` the openbao table after promotion; kine's `name` is `COLLATE "C"` and is safe.
8. Stop kcp/gateway/door in the container, copy the kcp root and door state, `docker stop`; start the
   host core; it adopts the units. kcp/door downtime remains (section 6).
Cost: a cutover tool that drives all of the above per cluster + a rehearsal on the bed + failure
handling for a half-moved cluster. Risk: the frozen provider, a half-moved cluster on any failure,
a root-owned copy step mid-flight.

### (d) crash of a cluster group member

- `k3s@active` dies: systemd `Restart=on-failure` restarts it in place (warm data dir: 1.7 s [M]); the
  front refuses nothing - it just fails dials until the port answers. Faster: the reconciler starts
  the standby slot and switches (2.2 s [M]) if the active slot fails twice. NotReady risk: none below
  ~50 s (2.6).
- `kine@active` dies: restart in place; k3s retries its etcd client.
- postgres dies: `Restart=on-failure` on the pg unit; kine reconnects; group untouched (Wants, not
  Requires). Measure bao's behaviour on a storage reconnect [U, already listed in the exploration].
- bao dies: restart + unseal by the reconciler; joined nodes do not need bao; new joins and ESO wait.
- a link dies: restart with the same key (fix the "stated limit: a link that dies is not restarted",
  ARCHITECTURE section 5); nodes' dials fail for the restart time only.
- front dies: restart from the fd store; all open apiserver connections reset once.
- NODE-LOSS: rebase the loss clock to "path up" (8.2, M3-NODE-LOSS-CLOCK) so our own outage is never
  billed to a tenant's VM.

### (e) postgres minor and major upgrade

- Minor: storage format never changes within a major; replace binaries and restart [S PG upgrading].
  As a unit restart: fast shutdown + start, seconds; kine and bao reconnect. Checks: `SELECT 1`,
  `/readyz` through the front, no Ready transitions.
- Major: three options [S PG upgrading, pg_upgrade]. (1) `pg_upgrade --link`: minutes of downtime,
  no extra space, no way back once the new cluster starts. (2) Logical replication into a new
  Postgres object of the new major, then a 1-2 s write freeze: stop kine slots, wait for catch-up,
  `setval('kine_id_seq', max(id))` on the new primary (sequences are not replicated by logical
  replication before PG 19 [U verify for the target major]), switch DSNs, start kine; same for
  openbao. (3) For small databases, dump/restore under the write freeze. Recommend (2) for zero
  downtime, (3) when the database is under ~100 MB [U measure prod sizes].

---------------------------------------------------------------------------------------------------

## 6. kcp/door downtime: what stops and what keeps working

| Stops while kcp + gateway + door restart | Keeps working |
|---|---|
| Tenant `kubectl` / CI runners through the kcp mount (users reach clusters through the mount; ARCHITECTURE section 7 "The mount requires status.phase == Ready", "The mint kubeconfig's own server: is loopback, not the mount") | every tenant apiserver on its own port |
| `getKubeconfig` / exec-credential refresh via the gateway (cached tokens keep working until expiry) | joined nodes, their pods, kubelet heartbeats, meshd, CoreDNS |
| door API + SPA, new RFPs, bidding, accept, the per-contract secrets servers (Bidding/Provisioning claims die today) | both cluster links, cluster vault, ESO sync, CCM (on its last instance snapshot) |
| provider: scale decisions, NODE-LOSS evaluation, claim adoption, vault sync (if the provider is in-process with kcp per M3-SERVE-GATEWAY-KCP) | the tenant's own node lifecycle controller (it keeps recording real NotReady transitions) |

NODE-LOSS does not need to pause or rebase across a kcp/door/provider restart: the clock is the tenant
Node's `lastTransitionTime`, written by the tenant's kcm, which keeps running; a node that died during
the restart is genuinely lost and the next provider pass releases it from the claim. The door only
releases on claim deletion, from the receipt on the claim. Rebase IS needed when OUR side of the path
was down (cluster cold start, link restart, front restart, cutover): count NotReady only from
`max(lastTransitionTime, pathUpSince)`, where pathUpSince = when the provider last saw the cluster's
apiserver answer through the front AND both ClusterLinks Listening (in memory; provider start counts as
path-up for clusters it brings up). Cheap, and it removes the cutover's real exposure.

Keeping tenant API access up across a core upgrade needs kcp out of the core process (exploration Q1
recommends out-of-process kcp in its own unit). Even then a kcp upgrade is a mount outage; kcp HA is
out of scope here.

---------------------------------------------------------------------------------------------------

## 7. Durable ownership state

Is `NodeClaim.status.contract` + market records enough? For Joining and Running nodes, yes: the claim
names the cluster key (spec), the VM (vmUri/vmName), the contract (rfp/accept/receipt/submitEventRef),
the workload grant, and its name is the tenant Node's name; the cluster vault holds the join material;
the tenant datastore holds the Node. The provider and door re-derive everything from those today.

Gaps:
1. **Bidding/Provisioning resume.** The secrets server's keypair and grant are per process
   (`guest-capability-secrets/mod.ts:50-75`). Add: a per-claim ingress key in OpenBao (the cluster's
   vault, KV `socialweb-door/claims/<claim>/secrets-ingress`, CAS-0 like the link keys); the door
   re-creates the ingress from it on restart, so the URL in the guest's user_data still resolves; the
   authorizer grant is rebuilt from `status.grant`; the bundle is rebuilt (ticket, CA, claim.json are
   deterministic). Claim fields: `status.secrets {ingressDid, keyRef, url, servedAt}`.
2. **The accept-before-status edge** (ARCHITECTURE section 9: "an accept written just before a crash
   ... is a VM no claim names"). Add a write-ahead `status.contract.acceptPending {bidUri}` before the
   accept record is written; a restarted door looks for an accept naming that RFP in the requester's
   repo and completes or releases. This removes most of M6-GATEWAY-REPOPULATE's record scan.
3. **Control-plane runtime record** (host-local, read before kcp is up): `<dataDir>/controlplane.json`
   (section 5) plus unit names; mirrored to `Cluster.status.controlPlane` for visibility only.
   Adoption reads units + this file; never kcp.
4. Schema cost: `APIResourceSchema.spec` is immutable, so new NodeClaim status fields are a schema
   revision + export re-point (versions.json `our-crds`); batch 1 and 2 into one revision.

Proposed ownership model (decided by Claude, user may veto):
- **Owner of a VM = exactly one NodeClaim.** Claim name = tenant Node name = guest vmName. The claim is
  written before the RFP (Bidding) and carries every reference needed to release the VM without
  memory. The market records are evidence; the claim is the index.
- **Owner of a control plane = the host**: units + `controlplane.json` + leases.json; the Cluster CR
  is desired state and status mirror.
- **Join material = the cluster vault**, keyed by claim name; link keys never regenerated.
- **Liveness = the tenant Node**, judged with the rebased loss clock.
- Restart = adopt, never reap: door adopts Joining/Running and resumes Bidding/Provisioning from
  gap 1; provider adopts claims and units; anything with a receipt and no claim (gap 2 not yet
  closed) goes through M6-GATEWAY-REPOPULATE's Orphaned rule.

---------------------------------------------------------------------------------------------------

## 8. Recommendation

### 8.1 Design

1. Per cluster: `socialweb-cluster-<key>.slice` with member units (front, kine@a|b, k3s@a|b, bao,
   link-apiserver, link-openbao, ccm, eso, esowh), ordered after `socialweb-pg-<key>.service`.
   Refines the decided "cluster group is its own unit": the group is still one systemd object (the
   slice; stop = stop the slice) but each member has its own lifetime, so blue/green needs no resident
   supervisor and a core upgrade touches nothing. Pdeathsig is no longer needed inside the group
   (units own lifetime); plan 0005 M6-LIMITS' guarantee becomes "every member is a unit of the
   cluster's slice".
2. Front on the `k3s-secure` port; slots on new leases; `AssertMarketAdvertiseInvariant` rewritten:
   advertise port == front port, advertise address identical across slots, slot listen ports never
   advertised.
3. Scheduler `leaderElect: true` with a short lease; kcm/ccm defaults kept.
4. Swap policy: idle-first (0 nodes or no running runner pods, bounded deferral), else blue/green with
   switch-then-stop. Minor bumps one at a time with a pg_dump first.
5. NODE-LOSS clock rebased to path-up.
6. Door: per-claim ingress key in OpenBao + `status.secrets`; write-ahead accept.

### 8.2 Plan items (new / changed)

```yaml
# plan 0006
- id: M3-CLUSTER-UNITS            # replaces M3-CLUSTER-SCOPES (rename already proposed in the exploration)
  title: A cluster is a slice of member units; a core upgrade restarts none of them
  depends_on: [M2-HOST-SCOPES, M2-POSTGRES]
  decision: >-
    Decided by Claude, user may veto (Q-H1): each cluster is socialweb-cluster-<key>.slice holding
    one transient service per member (front, kine@<slot>, k3s@<slot>, bao, link-apiserver,
    link-openbao, ccm, eso, esowh), After/Wants socialweb-pg-<key>.service. The reconcile loop is the
    supervisor (ARCHITECTURE section 5 already says so); adoption = unit name + InvocationID +
    <dataDir>/controlplane.json. Pdeathsig inside the group is retired; M6-LIMITS is restated as
    "every member is a unit of its cluster's slice".
  test_first: >-
    Behind livegate, stand-ins only: TestACoreRestartLeavesEveryClusterUnitWithItsInvocationID
    (systemctl --user restart of the gateway unit + a binary swap; /readyz through the front polled
    every 200 ms, no failure). On the market bed (node joined via runComputeContract):
    TestAGatewayUpgradeKeepsAMarketNodeReady (no Ready transition, no claim write, no vm.delete).
- id: M3-APISERVER-FRONT
  title: A per-cluster L4 front holds the apiserver port; k3s moves behind it
  depends_on: [M3-CLUSTER-UNITS]
  decision: >-
    Decided by Claude, user may veto: the front binds the k3s-secure lease port (unchanged number, so
    no node secret, ticket, status.URL or kubeconfig changes); k3s slots take new k3s-slot-a/b leases
    (width 2) and per-slot controller and kine leases; --advertise-port = front port for every slot;
    backend switched per new connection by the reconciler only; listening socket kept in the systemd
    fd store. AssertMarketAdvertiseInvariant is rewritten to the front.
  test_first: >-
    TestTheFrontSwitchFailsNoPoll (two k3s --disable-agent stand-ins on one kine/postgres, 200 ms
    authenticated /readyz through the front across switch + SIGTERM: 0 failures - the measurement in
    control-plane-handover.md 2.1 as a test); TestEverySlotAdvertisesTheFrontPair (EndpointSlice ==
    (ADV, front port) with one, two and the remaining slot). Red first.
- id: M3-CP-SLOTS
  title: Blue/green k3s and kine slots per cluster
  depends_on: [M3-APISERVER-FRONT]
  decision: >-
    Decided by Claude, user may veto: procedure (a) of control-plane-handover.md 5; scheduler
    leaderElect true with a short lease; idle-first swap policy with a bounded deferral; minor bumps
    one at a time with a pg_dump first; rollback within a minor only. The k3s hostname-identity
    lease collision is bounded by switch-then-stop.
  test_first: >-
    Stand-ins: TestAGreenSlotSharesCAAndTokenWithBlue, TestTheSchedulerIsNotDoubledDuringOverlap.
    Bed (node via runComputeContract): TestABlueGreenSwapKeepsAMarketNodeReadyAndExecWorksAfter
    (no NotReady, no claim change; exec into the node's pod succeeds within N s of the swap - N is
    the measured agent tunnel backoff).
- id: M3-LINK-HANDOVER
  title: A dumbpipe link is replaced with the same key and no dial lost
  depends_on: [M3-CLUSTER-UNITS]
  decision: >-
    Measure, then decide: start the new listener with the same IROH_SECRET on another UDP port, wait
    for its ticket, stop the old one. Fixes the stated limit that a dead link is never restarted.
  test_first: >-
    Bed: TestALinkReplacedWithTheSameKeyKeepsTheNodeReady (guest's connect-tcp dials across the
    swap; relay promotion and discovery timing recorded for the pinned dumbpipe).
- id: M5-CUTOVER                  # amended
  decision_add: >-
    Recommended (8.3): guarded cold restart, rehearsed once on the market bed, after
    M3-CP-SLOTS and M3-NODE-LOSS-CLOCK ship in the host core; window chosen when most clusters are
    at zero nodes. The host-side takeover of a frozen container is documented, not built.
  depends_on_add: [M3-APISERVER-FRONT, 0007:M3-NODE-LOSS-CLOCK]
  test_first_add: >-
    Bed: TestAContainerToHostCutoverKeepsAMarketNodeOwned (control-plane container stopped, host
    core started on the copied state: claim adopted, Node Ready within 50 s, no vm.delete).
- id: M2-POSTGRES                 # amended
  decision_add: >-
    Minor upgrade = unit restart. Major upgrade = logical replication into a new Postgres object plus
    a write freeze with the kine sequence set on the new primary; dump/restore below a size bound.
    The host build's default collation must equal the container's or the openbao table is reindexed.

# plan 0007
- id: M3-NODE-LOSS-CLOCK
  title: Our own outage never counts against a tenant's node
  depends_on: []
  decision: >-
    Decided by Claude, user may veto: NotReady time counts from max(lastTransitionTime, pathUpSince);
    pathUpSince = when the provider last saw the cluster's apiserver through the front and both
    ClusterLinks Listening (provider start counts for clusters it brings up).
  test_first: >-
    cmd/socialweb-clusterprovider: TestANodeNotReadyDuringOurLinkOutageIsKeptFor90sAfterTheLinkReturns,
    TestANodeNotReadyBeforeAProviderRestartIsStillReleasedOnTime. Red first.
- id: M6-CLAIM-RESUME             # split out of M6-GATEWAY-REPOPULATE
  title: A restarted door resumes Bidding and Provisioning claims
  depends_on: [M4-BIDDER-VAULT]
  decision: >-
    Per-claim secrets-ingress key in the cluster OpenBao (CAS-0), status.secrets {ingressDid, keyRef,
    url, servedAt}, write-ahead status.contract.acceptPending; one NodeClaim schema revision.
  test_first: >-
    test/node_market_test.ts: 'a door restarted during Provisioning re-serves the same secrets URL
    and the claim reaches Joining', 'a door restarted between accept intent and accept record
    completes or releases exactly once'; bed: TestADoorRestartMidProvisioningStillJoinsTheNode
    (node born via runComputeContract). Red first.
- id: M6-GATEWAY-REPOPULATE       # amended
  depends_on_add: [M6-CLAIM-RESUME, 0006:M3-CLUSTER-UNITS]
  decision_change: the record scan only covers claims with acceptPending or no claim at all.
```

All live tests stay on the RFP spine: a node exists only because the bed's requester ran
`runComputeContract` (RFP -> bid -> accept -> cloud-init) against a local bidder; control-plane
stand-in tests start k3s/kine/postgres processes only, never a guest.

### 8.3 Cutover choice

| Option | Cost | Risk | Tenant effect |
|---|---|---|---|
| Drain first (release every market node) | low code | none to ownership | running jobs killed; nodes re-rented after |
| Raise grace for the window | trivial | stale VMs live longer if really lost | none if path recovers |
| Accept loss (plain cold restart) | none | depends on path recovery < ~140 s | API outage for the window; nodes probably Ready |
| **Guarded cold restart** (rebased clock + bed rehearsal + idle window) | M3-NODE-LOSS-CLOCK (small) + one bed run | low: a node is released only if it was really unreachable 90 s after our path was back | API outage for the window (tens of seconds per cluster + kcp/door); exec streams reset |
| Blue/green takeover of a live container | a one-shot cutover tool, freeze trick, per-cluster rollback states, a rehearsal | medium-high: half-moved clusters, frozen provider, root copy mid-flight | apiservers up; kcp/door still restart, so the mount is down anyway |

**Recommend the guarded cold restart.** The mount outage (kcp/door restart) cannot be avoided in the
cutover in any option, so the extra zero-downtime engineering buys only "apiserver up for in-cluster
clients", which the kubelets do not need (they tolerate ~50 s + 90 s). Put the zero-downtime effort
where it recurs: front + slots make every LATER k3s/kine/core upgrade zero-downtime.

### 8.4 Questions for the user (recommended answer first)

- **Q-H1** Cluster group as a slice of member units (front, kine@slot, k3s@slot, bao, links, ccm, eso)
  instead of one unit with a resident supervisor? **Yes** - blue/green and supervisor upgrades need
  members with independent lifetimes; a supervisor-held group cold-restarts on its own upgrade.
- **Q-H2** Put a per-cluster L4 front on the existing apiserver port and move k3s behind it?
  **Yes** - nothing on any guest changes, and it measured 0 failed requests across a swap.
- **Q-H3** Turn kube-scheduler leader election back on with a ~4 s lease (first pod after scale from
  zero may wait up to ~4 s instead of 0)? **Yes** - two slots otherwise schedule concurrently.
- **Q-H4** Swap policy: idle-first with a bounded deferral (default 30 min), then blue/green even
  with running jobs? **Yes** - exec/attach streams reset on any swap.
- **Q-H5** Cutover: guarded cold restart rather than a live takeover of the container? **Yes**
  (8.3).
- **Q-H6** NODE-LOSS counts only time when our own path was up (rebased clock)? **Yes**.
- **Q-H7** Door: per-claim secrets-ingress key in the cluster OpenBao so Provisioning claims resume
  (one NodeClaim schema revision with status.secrets + acceptPending)? **Yes**.
- **Q-H8** k3s minor upgrades: one minor at a time, pg_dump before, rollback only within a minor?
  **Yes**.
- **Q-H9** Take kcp out of the core process (its own unit) so a core upgrade is not a tenant API
  outage through the mount? **Yes, per exploration Q1** - otherwise every core upgrade is a mount
  outage however good the k3s handover is.

---------------------------------------------------------------------------------------------------

## Sources

- k3s HA with an external DB: https://docs.k3s.io/datastore/ha
- k3s token and bootstrap-data encryption: https://docs.k3s.io/cli/token
- k3s manual upgrades ("server nodes first one at a time"): https://docs.k3s.io/upgrades/manual
- k3s architecture, agent load-balancer: https://docs.k3s.io/architecture
- k3s agent CLI (`--disable-apiserver-lb`, `--lb-server-port`): https://docs.k3s.io/cli/agent
- k3s agent LB source: https://github.com/k3s-io/k3s/blob/master/pkg/agent/loadbalancer/servers.go
- k3s agent tunnel / EndpointSlice watch: https://github.com/k3s-io/k3s/blob/master/pkg/agent/tunnel/tunnel.go
- k3s server controllers + leader election: https://github.com/k3s-io/k3s/blob/master/pkg/server/server.go
- wrangler leader election (hostname identity, 45 s lease): https://github.com/rancher/wrangler/blob/main/pkg/leader/leader.go
- Kubernetes version skew policy: https://kubernetes.io/releases/version-skew-policy/
- kube-controller-manager flags (node-monitor-grace-period 50s, lease 15s): https://kubernetes.io/docs/reference/command-line-tools-reference/kube-controller-manager/
- Node lifecycle controller (fresh probeTimestamp for a newly observed node): https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/nodelifecycle/node_lifecycle_controller.go
- apiserver compactor CAS on compact_rev_key: https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiserver/pkg/storage/etcd3/compact.go
- Lease endpoint reconciler (master leases by IP, RemoveEndpoints): https://github.com/kubernetes/kubernetes/blob/master/pkg/controlplane/reconcilers/lease.go
- Leases (node heartbeats, apiserver identity): https://kubernetes.io/docs/concepts/architecture/leases/
- Nodes (heartbeats, Lease): https://kubernetes.io/docs/concepts/architecture/nodes/
- kine README: https://github.com/k3s-io/kine/blob/master/README.md
- kine postgres driver (schema, migrations): https://github.com/k3s-io/kine/blob/master/pkg/drivers/pgsql/pgsql.go
- kine SQL log (gap fill, poll): https://github.com/k3s-io/kine/blob/master/pkg/logstructured/sqllog/sql.go
- iroh-relay duplicate endpoint ids (active/inactive, SameEndpointIdConnected): https://github.com/n0-computer/iroh/blob/main/iroh-relay/src/server/clients.rs
- iroh address lookup (signed records per endpoint): https://docs.iroh.computer/connecting/dns-address-lookup
- iroh concepts: https://docs.iroh.computer/concepts
- dumbpipe (IROH_SECRET, listen-tcp dials --host per connection): https://github.com/n0-computer/dumbpipe/blob/main/src/main.rs
- PostgreSQL upgrading (minor = replace binaries; logical replication "several seconds"): https://www.postgresql.org/docs/current/upgrading.html
- pg_upgrade: https://www.postgresql.org/docs/current/pgupgrade.html
- pg_basebackup: https://www.postgresql.org/docs/current/app-pgbasebackup.html
