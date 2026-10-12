# kcp blue/green behind a front: start the new kcp, switch over, stop the old

Research + spike, 2026-10-12 (UTC). Question from the user (2026-10-12): "We are using caddyserver as a
reverse proxy in front of the gateway kcp, can't we take the same approach where we start the new and
switch over and kill the old?" Coordinator addendum: make kine the primary shared store (kine->postgres
preferred, sqlite covered), note kine's S3 situation, spike two kcps against kine behind the proxy, and
report kine-specific limits.

Evidence tags: [S] kcp v0.33.0 source in the Go module cache
(`~/Documents/go/pkg/mod/github.com/kcp-dev/kcp@v0.33.0/...`, file:line), [K] kine v0.17.1 source
(`.../github.com/k3s-io/kine@v0.17.1/...`), [O] kcp-operator source at commit 8994ed74e225 (main,
fetched 2026-10-12 with `gh api`), [D] documentation URL, [M] measured on hat-8 in this spike
(2026-10-12 00:16-00:44 UTC), [C] this repository.

---------------------------------------------------------------------------------------------------

## 0. Answer in one screen

1. **Yes, but not with today's flags, and not with kcp's embedded etcd.** Two kcp processes serve one
   logical shard at once only when (a) the store is outside both of them (`--etcd-servers` = kine or
   etcd), (b) both share the serving cert/key, the SA signing key and the shard-admin token hash,
   (c) both run `--enable-leader-election` (default **false** [S options/controllers.go:55]), and
   (d) both advertise the SAME shard URL (`--shard-base-url` = the front), because every start
   rewrites `root:shards/root` with its own URL [S server.go:625-667]. That is exactly how
   kcp-operator runs a RootShard (2 replicas by default, `--enable-leader-election=true`,
   `--root-directory=` empty, certs from secrets, `--shard-base-url` = a stable service)
   [O internal/resources/compiledrootshard/deployment.go:228-251; D RootShard CRD `.spec.replicas`].
2. **Measured [M]: 0 failed `/readyz` polls of 3,177 (200 ms cadence) across six swaps** on kine
   (sqlite and postgres) with caddy in front, by either swap mechanism (caddy admin-API PATCH of the
   upstream, or two fixed slot ports with `lb_policy first` + active `/readyz` health checks). The one
   run with failures (8 polls, 1.5 s, etcd run 1) was a harness bug that is a real hazard: the swap
   happened before the new slot was ready because readiness was checked through the copied
   `admin.kubeconfig`, which still named the OLD slot's port.
3. **Two hazards the naive "start new, switch, kill old" hits [M]:**
   - Without leader election (today's prod flags), both processes run every controller during the
     overlap: **~120-150 write conflicts per second per process** in the replication controller for
     the whole overlap (1,483 in the old, 1,007 in the new, run 2), plus apibinding/logicalcluster
     patch fights. Workspaces still went Ready, but it is a write storm on the store.
   - With leader election, kcp never releases the lease on shutdown (no `ReleaseOnCancel`
     [S server.go:856-888]; LeaseDuration hard-coded 60 s [S server.go:858]): **controllers stop for
     ~61 s after the old process exits** (a workspace created right after the swap stayed in
     Scheduling for 61.4 s, runs 1 and 3). Deleting the `kube-system/kcp-controllers` Lease in
     `system:admin` right after the old process exits cut that to **0.24-3.2 s** (run 3b/4/5).
4. **Watches [M]:** caddy config changes (admin PATCH, full reload) do NOT cut in-flight watch
   streams (plain chunked HTTP, not upgrades); the stream ends when the OLD kcp gets SIGTERM (at
   `ShutdownInitiated`, within 3 ms, even with `--shutdown-delay-duration=3s`), and the client
   re-watches through the front 0.2 s later. A client-go informer re-listed and re-watched with zero
   watch errors and no missed events across every swap.
5. **kine is fine as kcp's store [M, K]:** fresh kcp ready in 9.0-9.4 s on etcd, kine-sqlite and
   kine-postgres alike; slot restarts 5.8-6.7 s; workspaces Ready in 0.3-0.7 s on all three. Two kine
   processes on one postgres work (k3s HA shape) with **0.27-0.42 s cross-kine watch lag** (kine polls
   SQL on a 1 s ticker [K pkg/logstructured/sqllog/sql.go:548]); two kines started at the same instant
   on an empty database **race on schema creation** and one exits FATA (seen once, [M]).
6. **Recommendation (section 7):** kcp as blue/green **slot units** (`socialweb-kcp@<slot>`) on
   port 0 behind **our own L4 front** that owns `127.0.0.1:6443` (the same front code as the tenant
   k3s fronts in control-plane-handover.md section 4), store = **kine -> a bootstrap postgres unit**,
   `--enable-leader-election`, `--shard-base-url=https://127.0.0.1:6443`, shared key material in a
   `kcp-shared/` dir copied into each slot root, and the swap as: start new -> its own `/readyz` ->
   flip front -> SIGTERM old -> delete the lease. Caddy stays the public TLS terminator only, pointing
   at the front, untouched by swaps. This removes the embedded-etcd port-0 patch from M2-KCP-PORT0
   and closes the in-process route's main advantage.

---------------------------------------------------------------------------------------------------

## 1. Today's shape [C]

- `deploy/container-entrypoint.sh:226-235`: `kcp start --root-directory=$KCP_ROOT
  --bind-address=$KCP_BIND (127.0.0.1, :61) --secure-port=6443 --feature-gates=WorkspaceMounts=true
  --audit-policy-file=... --audit-log-path=...`. No `--etcd-servers`, so kcp runs its embedded etcd in
  the root dir [S pkg/server/options/options.go:149, :290-291; cmd/kcp/kcp.go:150-156]. No
  `--enable-leader-election`, no `--shard-base-url`.
- Every internal consumer dials `https://127.0.0.1:6443` (`SOCIALWEB_KCP_HOST`, :24) or the root dir's
  `admin.kubeconfig` (:25); the entrypoint's `K()` (:123-125) uses that file's current context
  `root`, i.e. the **kcp-admin** user.
- `scripts/deploy.sh:174-186` writes `/etc/caddy/sites/kcp.socialweb.computer.caddy`:
  `reverse_proxy https://127.0.0.1:6443 { flush_interval -1; transport http { tls_trusted_ca_certs
  <kcp CA from admin.kubeconfig>; tls_server_name 127.0.0.1; versions 1.1 } }` and reloads caddy
  (:188-193). Caddy terminates public TLS and re-encrypts to kcp; client certificates do not pass
  through (kcp clients use bearer tokens / exec credentials, so nothing depends on them today).

## 2. Can two kcp processes serve the same logical shard against shared state?

### 2.1 The store

- Embedded etcd is chosen when `--etcd-servers` is unset (`ServerList == ["embedded"]`
  [S options.go:149, :290-291]); it is started in-process before the apiserver and lives in the root
  dir [S cmd/kcp/kcp.go:147-156]. Single writer, bound to the process: two processes cannot share it,
  and a blue/green needs it OUT.
- With `--etcd-servers=<url>` the embedded etcd is off and kcp is a plain client. The embedded cache
  server copies the same etcd options and stores under the `/cache` prefix
  [S options.go:437-449; pkg/cache/server/config.go:108-115; pkg/server/cache.go:31-66], so the cache
  server needs no store of its own. The object-count scanner opens its own etcd client
  [S controllers.go:1246-1270]; it ran without errors on kine [M].

### 2.2 State outside the store (the root directory)

| file | written by | shared across slots? |
|---|---|---|
| `apiserver.crt`, `apiserver.key` | self-signed on first start, `CertDirectory=rootDir` [S options.go:146] | MUST be identical: clients and caddy pin this CA (`deploy.sh:153-155`) |
| `sa.key` | SA controller key [S options/controllers.go:82], also the SA signing + verify key when no other key is given [S options.go:362-375] | MUST be identical, else every SA token minted by one slot fails on the other |
| `.admin-token-store` | sha256 of the shard-admin token, created once [S options/authentication.go:97-106] | MUST be identical (shard-admin = `system:admin` context, used by `internal/kcpclient` ShardAdminContext [C kcpclient.go:81]) |
| `admin.kubeconfig` | rewritten on EVERY start [S authentication.go:154-183]; needs the existing file to recover the shard-admin token (else "cannot create the admin.kubeconfig with an empty token") | per slot (copy the old one in before start); server URL = ExternalAddress, i.e. the shard base URL |
| kcp-admin and `user` tokens | **volatile, a fresh UUID per process** [S authentication.go:109-110] | NOT shareable: the `root`/`base` contexts (kcp-admin) of slot A are rejected by slot B. Anything holding a kcp-admin rest.Config (today: the entrypoint's `K()`) breaks at a swap and must re-read the new slot's file |
| `audit.log` | `--audit-log-path` | per slot |
| `etcd-server/` | embedded etcd only | gone with an external store |

kcp-operator sidesteps the file layer entirely: `--root-directory=` (empty) and
`--tls-cert-file/--tls-private-key-file/--service-account-key-file/--service-account-private-key-file`
from mounted secrets [O compiledrootshard/deployment.go:228-232, :250]. Our equivalent: a
`<state>/kcp-shared/` directory holding cert, key, sa.key, token hash and the last admin.kubeconfig,
copied (or passed by flag) into each slot root before start. Spike runs copied exactly these five
files and every swap kept auth, TLS and SA tokens working [M].

### 2.3 Controllers: which are elected, which run everywhere

- `--enable-leader-election` (default false), lease `kube-system/kcp-controllers` in the shard-local
  `system:admin` logical cluster [S options/controllers.go:55-71; server.go:770-786].
- Identity `hostname_<uuid>` [S server.go:775], so two processes on ONE host do not collide (unlike
  k3s's hostname identity, control-plane-handover.md 0.4a).
- LeaseDuration 60 s, RenewDeadline 5 s, RetryPeriod 2 s, **no ReleaseOnCancel**
  [S server.go:856-888; `grep ReleaseOnCancel pkg/server` = none]. `OnStoppedLeading` uninstalls the
  controllers but the lease record is left held until expiry. Measured: old slot "stopped leading"
  at SIGTERM, new slot acquired 61 s later [M runs 1, 3].
- Elected (everything in `s.controllers`, installed by `installControllers` [S server.go:731-735]):
  workspace scheduling, logicalcluster, apibinding, apiexport/endpointslice URLs, replication into the
  cache, permission claims, quota, garbage collection, RBAC aggregation, ... - i.e. all writers.
- Every replica, by design [S server.go:87-93; controllers.go:138-145, :179-189]: the object-count
  scanner (per-process admission state) [S controllers.go:1246-1270] and the dynamic REST mapper
  controllers [S controllers.go:1900-1965]; both mutate memory only.
- Not controllers but run by EVERY process at start, outside the election: root bootstrap phase 0
  (kcp's own APIExports/schemas) [S server.go:579-588], the Shard create-or-update [S server.go:625-667]
  and root phase 1 (WorkspaceTypes, RBAC) [S server.go:688-701]. With equal versions these are
  idempotent; with N and N+1 the newer start rewrites them while the older may still lead (section 4).
- Without election (today's flags) two processes double-act. Measured [M run 2]: from the moment the
  second slot's controllers started until the first exited (12 s), each process logged 120-150
  `replication_controller.go:162 ... Operation cannot be fulfilled on logicalclusters.core.kcp.io`
  errors per second, plus apibinding "resource-bindings annotation not found" and logicalcluster /
  workspace patch conflicts. The kine sqlite file was 68.6 MB after ~2 minutes of that run versus
  3.9 MB of postgres table for runs 3-5 combined. With election on: zero replication conflicts [M].

### 2.4 Shard registration and URLs

- Each process writes `root:shards/<shard-name>` with `BaseURL = ShardBaseURL()`,
  `ExternalURL`, `VirtualWorkspaceURL` [S server.go:625-667], defaulting to
  `https://<ExternalAddress>` = bind address + port [S config.go:711-746; options.go:339-353].
- Consequence [M]: the moment slot B started, `root:shards/root.spec.baseURL` flipped to B's port
  (`https://127.0.0.1:16444`, run 1; `:16446`, run 2; `:16448`, run 3) BEFORE the front switched,
  and every Workspace `spec.URL` and every APIExportEndpointSlice URL followed it
  (`https://127.0.0.1:16449/...` for all 9 workspaces after run 4). Clients that follow those URLs
  bypass the front and are cut by the next swap. Virtual workspace traffic (initializers, apiexport
  views) goes to that URL too.
- Fix [M run 5]: start every slot with `--shard-base-url=https://<front>`; the Shard, all 10
  workspace URLs, all 5 endpoint-slice URLs and the slot's `admin.kubeconfig` then name the front, and
  workspace initialisation through the virtual workspace URL via the front worked (ws-after-i Ready).
  kcp-operator does the same (`--shard-base-url`, `--shard-external-url`
  [O compiledrootshard/deployment.go:245-246]). The serving cert must cover the front's host
  (127.0.0.1 is in kcp's self-signed SANs [M]).

### 2.5 What else to know

- Virtual workspaces run in-process by default (`--run-virtual-workspaces`, [S options/virtual.go:51]);
  each slot serves them, nothing to share.
- Workspaces must be created by a real identity: one created as shard-admin stuck in Initializing
  ("LogicalCluster ... had no createdBy recorded", [M] `ws-before`); kcp-admin or a user works.
- kcp-front-proxy is a shard ROUTER (index of Shard/Workspace/LogicalCluster, exact-match path lookup,
  no load balancing [D kcp shards concept]); it does not do blue/green of one shard. kcp-operator
  puts a Kubernetes Service in front of shard replicas for that, and its internal proxy is for the
  operator only [D RootShard `.spec.proxy`].

## 3. kine as the shared store (coordinator addendum)

How the org already runs it: `deploy/denokcp/start-kcp.sh:5-69` [C] starts
`kine --endpoint sqlite://$ROOT/kine.db --listen-address 127.0.0.1:23791` and
`kcp start --etcd-servers=$KINE_ENDPOINT`.

| option | how two kcp slots use it | measured | notes |
|---|---|---|---|
| kine -> postgres, one kine per slot | slot = {kine, kcp}; both kines on one database | run 3/3b/4/5: 0 failed polls, cross-kine watch lag 0.27-0.42 s (10/10 events, no missed rv) | same shape as tenant k3s slots; kine upgrades ride with the kcp slot; first start of two kines on an empty DB raced: `FATA ... duplicate key value violates unique constraint "pg_class_relname_nsp_index"` -> start the first slot's kine alone, or retry |
| kine -> postgres, one shared kine unit | both slots dial one kine | (not separately run; run 2 is the same topology on sqlite) | kine itself is stateless over postgres, so the kine unit can be blue/greened too |
| kine -> sqlite | one kine process only, both slots dial it | run 2: 0 failed polls | sqlite = one kine process (no second kine on the file); a kine restart is a store outage for both slots; fine for dev/test/bidder-local kcp |
| etcd (own unit) | both slots dial one etcd | run 1 (etcd v3.7.1 `embed`, built in 34 MB, client and peer on port 0, port file) | native etcd semantics; a second store technology beside postgres; port 0 peer URL is persisted in the member (M2-KCP-PORT0 risk applies) |

kine-specific limits for kcp:

- **Watch fidelity:** kine serves watches from its own poll of the `kine` table; a write through the
  SAME kine notifies immediately, a write through ANOTHER kine is seen on the next poll (1 s ticker
  [K pkg/logstructured/sqllog/sql.go:548]); measured lag 0.27-0.42 s. Progress notifications and
  `RequestProgress` are implemented [K pkg/server/watch.go:45-63, :188-247] and kine reports an
  emulated etcd 3.6.11 "to indicate support for watch progress notifications" [K pkg/app/app.go
  ~162-167], which the 1.36 apiserver's consistent-reads-from-cache needs. No informer watch error
  and no missed revision across swaps [M].
- **Unsupported etcd features:** member add/remove/promote, range `limit` on Get, sort, serializable,
  min/max revision filters [K pkg/server/cluster.go:16-42, get.go:13, kv.go:18-81]. kcp's apiserver
  storage and the object-count scanner did not hit them (no kine-specific errors in any kcp log [M]).
- **Compaction:** kine's own compactor is OFF by default "so that compact may be managed by the
  apiserver" [K app.go:171-176]; the apiserver's compaction transaction is translated onto a substitute
  key [K pkg/server/compact.go:14-60]. With two kcps both run the apiserver compactor; the apiserver
  uses a compare-and-swap on `compact_rev_key`, so one wins per interval. Retention floor 1000
  revisions (`--compact-min-retain`). Nothing to coordinate across slots.
- **Write conflicts surface as kine ERRO lines:** `Insert error ... duplicate key value violates
  unique constraint "kine_name_prev_revision_uindex"` = optimistic-concurrency conflicts (37 + 28 lines
  on pg, 0 on sqlite) [M]; they map to 409s, not data loss. Expect log noise, not failures.
- **Latency under kcp's informer load [M]:** fresh kcp to `/readyz` 9.0 s (etcd), 9.4 s (kine sqlite),
  9.0 s (kine postgres); slot restarts on an existing store 5.8-6.7 s on kine; workspace create to
  Ready 0.3-0.7 s on all stores when a leader is running. RSS: kcp ~430 MB, kine ~57-60 MB each.
  This is a near-empty shard; prod's object count is larger, so record the prod-shaped numbers in
  M0-KCP-SPIKE.
- **S3 / rustfs:** the coordinator's note says kine has no S3 driver. kine v0.17.1 DOES ship a `t4`
  driver: "an embeddable, S3-durable key-value store ... handles WAL management, periodic
  checkpoints, and its own leader election (via an S3 lock object)" with a LOCAL data dir and
  `--s3-bucket/--s3-endpoint` [K pkg/drivers/t4/README.md:1-40, t4.go:1-78]. It is not a plain S3
  backend and was not tested here against rustfs. `internal/clusterprovider/stack.go:39-43` [C]
  records the k3s side: k3s v1.36.4 refuses the S3 form ("unknown driver") and a t4 endpoint makes
  k3s start its OWN kine on the bucket (two writers). For kcp: do not plan on S3; use postgres.
- **Postgres is a bootstrap dependency, not a CRD object:** the Postgres CRD objects live IN kcp, so
  kcp's own store cannot be reconciled from kcp. The kcp postgres must be a host unit the core starts
  from local state before kcp (same ordering rule as M3-SERVE-GATEWAY-KCP's "kcp first").

## 4. Version skew N/N+1 and rollback

- kcp publishes no version-skew policy for replicas of one shard (none found in docs.kcp.io; the
  production guide covers HA only as "spread shards and pods" [D kcp production setup]). kcp-operator
  runs 2 replicas and a Deployment rollout, so N and N+1 do overlap there in practice [O, D].
- What overlaps in our design: N+1 starts, runs root bootstrap phase 0/1 and the Shard update
  UNCONDITIONALLY at start [S server.go:579-588, :625-667, :688-701], i.e. N+1 rewrites kcp's own
  APIResourceSchemas/APIExports/WorkspaceTypes/RBAC while N still leads the controllers (until the
  swap + lease handover). If N+1 changes a schema of kcp's own APIs, N's controllers act on N+1's
  objects for the overlap window (seconds with the lease delete).
- Storage versions: built-in kube types follow kube's rule (a new storage version is only written by a
  release whose predecessor can read it); kcp's own types are CRD-like schemas with served/storage
  versions chosen by the binary. `StorageVersionAPI` is alpha and off [S config.go:552-570], so there
  is no storage-version bookkeeping to consult.
- Rollback: SAFE when N+1 did not change any storage version or bootstrap schema (diff kcp's
  `config/root-phase0`, `config/root`, and CRD storage versions between tags before rolling); NOT
  guaranteed otherwise, because objects written in N+1's storage version may be unreadable by N and
  phase-0 objects were overwritten. Rollback = start an N slot (it re-bootstraps its own versions of
  the phase-0/1 objects, which is itself a downgrade write). Take a store snapshot first
  (`pg_dump` of the kine database; one transaction-consistent file) and treat "restore the snapshot"
  as the rollback of last resort. Same-version restarts (binary rebuilt, flags changed) are always safe.
- Recommendation: a kcp version roll is a separate, explicit `units roll kcp` that (1) refuses unless
  the vendor record marks the pair as "storage-compatible" (a check that the CRD/schema storage
  versions are equal between the two vendored trees), (2) dumps the store, (3) swaps.

## 5. The swap mechanism in front

Measured with caddy v2 (`/usr/bin/caddy`, JSON config, admin on 127.0.0.1:20190), HTTPS listener on
127.0.0.1:16440 serving kcp's own `apiserver.crt/key`, `reverse_proxy` to the slot with
`flush_interval -1`, `versions ["1.1"]`, `tls.root_ca_pem_files` = kcp's cert, `server_name 127.0.0.1`
(the prod site block in JSON form).

| mechanism | result [M] | notes |
|---|---|---|
| A. caddy admin API: `PATCH /id/kcp-proxy/upstreams [{"dial":"127.0.0.1:<new>"}]` after the new slot's own `/readyz` | runs 2, 3, 3b: 0 failed polls; answered 200 in 4-7 ms | the core must check readiness on the NEW port directly; checking through the copied admin.kubeconfig hit the OLD slot and caused run 1's 8 failed polls |
| B. two fixed slot ports, `lb_policy first`, active `health_uri /readyz` every 500 ms with a bearer header, passive `max_fails 1`, `lb_try_duration 5s` | runs 4, 4b, 5: 0 failed polls, no caddy config change during swaps | slot order fixes preference: the first port wins whenever healthy, so B->A switches as soon as A is healthy and A->B switches only when A goes unhealthy; kcp's `/readyz` turns 500 at SIGTERM, so `--shutdown-delay-duration` lets caddy notice before the listener closes |
| C. kcp-front-proxy | not tested | a shard router, not a replica balancer (2.5); adds certs and a process |
| D. our own L4 front (the tenant k3s front, control-plane-handover.md section 4) | not re-measured for kcp; the same mechanism measured 0/515 failed polls for k3s [handover 2.1] | TLS passthrough, holds 127.0.0.1:6443 in the fd store, backend flipped by the reconciler |

Long-lived streams:

- Caddy config changes (PATCH, `caddy reload`, even switching the server to the slot-port config)
  never cut an open watch [M: raw watch open 00:33:28 -> closed only at the old slot's SIGTERM
  00:40:46.975, across a PATCH at 00:40:41]. Caddy only force-closes WebSocket/upgraded streams on
  reload (`stream_close_delay`/`stream_timeout` tune that) [D caddy reverse_proxy "Streaming"];
  kubectl exec/attach/port-forward ARE upgrades and would be cut by any caddy reload.
- The OLD kcp closes its watches at `ShutdownInitiated` (+2 ms) even with
  `--shutdown-delay-duration=3s` [M kcp-h log 00:41:55.230 -> watch closed 00:41:55.232]; with the
  delay, it keeps serving non-watch requests and keeps leading controllers until the delay ends
  ("stopped leading" at +3.0 s), then drains in-flight non-long-running requests
  ("not going to wait for active watch request(s) to drain" = `--shutdown-watch-termination-grace-period`
  0) and exits ~1 s later.
- Clients: the raw watch reconnected 0.2 s later through the front to the new slot; the client-go
  informer re-listed/re-watched with zero watch errors and saw every subsequent event. kubectl
  `get -w` style one-shot watches exit and must be re-run.

Caddy-specific costs if the core drives caddy (A or B): caddy is a SYSTEM service while the core runs
as a lingering user, so the core needs the admin socket (default `localhost:2019` is reachable by
every local user, itself a hardening item); for the internal 127.0.0.1:6443 path caddy must serve
kcp's serving key (L7 re-encrypt), i.e. the private key leaves the kcp unit's ownership; and every
caddy reload also cuts the door's and other sites' upgraded streams.

## 6. Spike record (hat-8, 2026-10-12, all on 127.0.0.1, scratch dir since deleted)

Binaries: `/usr/local/bin/kcp` v1.36.0+kcp-v0.33.0, `/usr/local/bin/kine` v0.17.1 ("dev (HEAD)"),
`/usr/bin/caddy`, host `postgres` 18.6, an etcd stand-in built offline from the module cache
(`go.etcd.io/etcd/server/v3 v3.7.1` `embed.StartEtcd`, client+peer `http://127.0.0.1:0`, port file;
`GOTOOLCHAIN=go1.26.6 GOPROXY=off GOFLAGS=-mod=mod go build` with kine's go.mod/go.sum as the module
graph), and a client-go informer stand-in (dynamic informer on `tenancy.kcp.io/v1alpha1 workspaces`,
built with this repo's go.mod graph). No guests, no containers.

Per slot: `kcp start --root-directory=<slot root> --bind-address=127.0.0.1 --secure-port=<port>
--etcd-servers=<store> --feature-gates=WorkspaceMounts=true --audit-policy-file=deploy/kcp-audit-policy.yaml
--audit-log-path=<slot root>/audit.log [--enable-leader-election] [--shutdown-delay-duration=3s]
[--shard-base-url=https://127.0.0.1:16440]`. Before each new slot: copy `apiserver.crt apiserver.key
sa.key .admin-token-store admin.kubeconfig` from the old slot root. Clients through caddy: `/readyz`
poll every 200 ms with the shard-admin bearer token, a raw `?watch=1` curl on workspaces, the informer.
Swap: new slot `/readyz` == ok on its own port -> create a workspace via the old slot -> caddy switch
-> 5 s -> SIGTERM old -> (lease delete) -> create workspace via the front, time to Ready.

| run (UTC) | store | slots / flags | switch | failed polls in window | old exit after SIGTERM | controller gap after old exit | watch |
|---|---|---|---|---|---|---|---|
| 1 00:27:41-00:28:55 | etcd (embed, own process) | a:16443 -> b:16444, LE on | admin PATCH | 8 of ~48 (00:27:46.17-47.63, new slot not yet ready: harness bug) | 1.17 s | **61.4 s** (ws-gap-b Ready 00:28:53.59; lease last renewed 00:27:49.75 by a) | closed at SIGTERM, reopened +0.2 s |
| 2 00:31:00-00:31:45 | kine sqlite, 1 kine | c:16445 -> d:16446, LE OFF | admin PATCH | 0 | 1.17 s | 0.28 s (d already ran all controllers) | closed at SIGTERM, reopened +0.2 s; **replication conflict storm during 12 s overlap** |
| 3 00:32:29-00:40:04 | kine pg, kine-pg1 + kine-pg2 | e:16447(pg1) -> f:16448(pg2), LE on | admin PATCH | 0 | 1.17 s | **61.5 s** (ws-gap-f) | same; cross-kine lag 0.27-0.42 s |
| 3b 00:40:32-00:40:49 | kine pg | f -> g:16449(pg1), LE on, **lease deleted after old exit** | admin PATCH | 0 | 1.12 s | **0.77 s** to Ready (g "started leading" 0.24 s after delete) | same |
| 4 00:41:17-00:41:43 | kine pg | g:16449 -> h:16450(pg2) | slot ports + health checks | 0 | 0.10 s | 2.1 s to Ready | same |
| 4b 00:41:46-00:42:01 | kine pg | h -> g2:16449(pg1), `--shutdown-delay-duration=3s` on both | slot ports (g2 first) | 0 | 4.15 s (3 s delay) | 2.3 s to Ready | closed at ShutdownInitiated |
| 5 00:43:44-00:44:01 | kine pg | g2 -> i:16450 with `--shard-base-url=https://127.0.0.1:16440` | slot ports | 0 | 4.18 s | 4.2 s to Ready (i led 3.2 s after delete; election retry 2 s + jitter) | same; all workspace + endpoint-slice URLs = the front |

Totals: runs 2-5 3,177 polls in-window, 0 non-200 (the non-200 polls in the raw logs after each run's
teardown are the clients outliving the stopped slots). The informer logged watch errors only after
teardowns.

Incident during the spike (reverted): caddy's default `pki` app, triggered by a `tls.automation` policy
with the `internal` issuer in the first spike config, generated a local CA under
`~/.local/share/caddy/pki` and INSTALLED it into the system trust store
(`/etc/pki/ca-trust/source/anchors/Caddy_Local_Authority_-_2026_ECC_Root_*.pem`, through
non-interactive sudo, 00:24:30 and again 00:27:44). Removed at 00:29:32 UTC (`rm` + `update-ca-trust
extract`; bundle count of "Caddy Local Authority" back to 0; fingerprint matched the spike CA), the
spike CA's key dir deleted, later configs set `pki.certificate_authorities.local.install_trust=false`
and a scratch `storage` root. Lesson for the finished system: any caddy config the binary renders must
pin `install_trust: false` and never rely on caddy's internal issuer.

## 7. Recommendation for the finished system

1. **Store: kine -> postgres, kine per slot.** A dedicated bootstrap postgres unit for kcp
   (`socialweb-kcp-pg`, started by the core from local state before kcp; not a Postgres CRD object,
   section 3). Each kcp slot unit pairs with its own kine (the same pair shape as the tenant k3s slots),
   or kine runs as a member of the slot unit's slice. sqlite only for single-process dev and the
   bidder-local `run request` kcp. Embedded etcd is not used in the finished system. If the user
   prefers etcd, the binary embeds `go.etcd.io/etcd/server/v3/embed` as its own unit (proven here on
   port 0), but that is a second store technology for no gain over postgres.
2. **kcp as slots:** `socialweb-kcp@a` / `@b` transient units in the account's user manager, each
   `--secure-port=0 --experimental-bind-free-port` (secure port only; no embedded etcd, so the
   embeddedetcd port-0 patch disappears), `--etcd-servers=<its kine>`, `--enable-leader-election`,
   `--shard-base-url=https://127.0.0.1:6443`, `--shutdown-delay-duration=2s`, today's audit flags.
   `<state>/kcp/shared/` holds apiserver.crt/key, sa.key, .admin-token-store and the last
   admin.kubeconfig; the core copies them into `<state>/kcp/slot-<x>/` before start (or passes
   `--tls-cert-file/--tls-private-key-file/--service-account-*-file` and keeps only the token pair).
3. **Front: our own L4 front owns 127.0.0.1:6443** (the tenant per-cluster front code, its socket in
   the systemd fd store), backend = the active slot's reported port. Caddy keeps
   `kcp.socialweb.computer -> https://127.0.0.1:6443` unchanged and is never touched by a swap; so
   caddy reloads never cut exec/attach streams, the core needs no caddy admin access, and kcp's private
   key stays with kcp. Fallback if the user wants caddy to do it: mechanism B (two fixed slot ports +
   `/readyz` health checks) with a rendered site file that pins `install_trust false`; mechanism A
   (admin PATCH) works but couples the core to a root-owned service's admin socket.
4. **Swap procedure (`units roll kcp`):** (1) pg_dump of the kcp kine database; (2) copy shared
   material into the new slot root; (3) start `socialweb-kcp@<new>` (+ its kine), read its port from
   the port report; (4) wait for `/readyz` == ok **on the new slot's own port**; (5) persist
   `kcp-slots.json {active, ports, invocationIDs}` then flip the front; (6) SIGTERM the old slot; wait
   for exit; (7) delete Lease `kube-system/kcp-controllers` in `system:admin` with the shard-admin
   context (or carry a 1-line `ReleaseOnCancel: true` patch at server.go:856 in the vendored kcp, the
   cleaner fix; upstreamable); (8) check: a probe object reconciles (e.g. a throwaway workspace
   Ready) within 10 s; (9) stop the old kine. Idle-first: prefer a moment with no exec/attach through
   the mount.
5. **Leader election is mandatory** for any overlap; the default-off flag is the single biggest
   difference between "start new, kill old" working cleanly and a 12-second write storm.
6. **Consumers:** nothing in the core may hold a kcp-admin (`root`/`base` context) rest.Config across
   a swap; use the shard-admin `system:admin` context (persistent token) or minted identities, and
   always the front URL. The `root` context's volatile token is per process [S authentication.go:109].

## 8. Changes to plan 0006

- **M0-KCP-SPIKE**: add to the out-of-process measurement "two kcp slots on one kine->postgres store
  behind the front, with `--enable-leader-election` and `--shard-base-url`": failed polls, controller
  gap with and without the lease delete, prod-shaped object count timings. The gate gains a fourth
  condition: in-process ties kcp's lifetime to the core process, so a kcp blue/green there means two
  whole core processes (gateway, clusterprovider, door included) overlapping, which nothing else in
  the plan supports; the in-process route therefore needs an explicit user waiver of kcp blue/green.
  Recommendation recorded: out-of-process.
- **M1-K8S-FORK / M1-KCP-INPROC**: unchanged text, but note in risks that the in-process route cannot
  do kcp blue/green; expected to close as "route not chosen".
- **M2-KCP-UNIT** -> rename **M2-KCP-SLOTS** ("kcp as blue/green slot units on a shared kine store").
  Decision text: `socialweb-kcp@<slot>` units, `--etcd-servers`, `--enable-leader-election`,
  `--shard-base-url=https://127.0.0.1:6443`, `--shutdown-delay-duration=2s`, shared material dir, port
  0 secure port with the port report; the flock on the root becomes a flock on `<state>/kcp/`
  held by the core. test_first: `TestTwoKcpSlotsOnOneStoreServeTheSameWorkspaces`,
  `TestTheSlotArgvCarriesLeaderElectionAndTheFrontURL` (offline argv builder, red without the flags),
  `TestEverySlotRootGetsTheSharedKeyMaterialBeforeStart`.
- **M2-KCP-PORT0**: drop the embeddedetcd port-0 patch and the etcd-client/etcd-peer listeners (no
  embedded etcd); keep only the secure-port report (`--experimental-bind-free-port` +
  `kcp-port-file.patch`). test_first becomes `TestTwoKcpSlotsOnPortZeroGetDistinctPortsAndTheShardURLIsTheFront`.
  The "port-0 peer URL persisted" risk is removed.
- **New M2-KCP-STORE**: the bootstrap postgres unit for kcp + kine per slot (or one kine unit),
  started before kcp from local state; schema-creation race handled (start the first kine alone or
  retry on SQLSTATE 23505). test_first: `TestKcpStoreStartsBeforeKcpAndOutsideTheCRDReconciler`,
  `TestTwoKinesOnOneFreshDatabaseBothComeUp` (red: the measured FATA).
- **New M3-KCP-FRONT** (or fold into control-plane-handover's M3-APISERVER-FRONT as one front type with
  two users): the L4 front on 127.0.0.1:6443; caddy site unchanged. test_first:
  `TestTheKcpFrontFlipsWithZeroFailedReadyzPolls` (livegate, stand-in slots on kine).
- **New M3-KCP-ROLL**: `units roll kcp` = section 7.4 incl. the lease delete (or the ReleaseOnCancel
  patch recorded in third_party/kcp-patches) and the storage-compatibility refusal. test_first:
  `TestRollKcpLeavesNoControllerGapLongerThan5s` (red today: 61 s),
  `TestRollKcpRefusesAPairWithDifferentStorageVersions`.
- **M3-SERVE-GATEWAY-KCP**: order becomes lock -> kcp store (pg + kine) -> kcp front -> kcp slot
  Ready (adopt slots and front by name) -> ...; the core reads `kcp-slots.json` for the active slot.
  Consumers use `https://127.0.0.1:6443` and the shard-admin context only.
- **M3-HOST-INSTALL / M3-CP-CONTINUITY / ACC6-UPGRADE**: ACC6-UPGRADE gains "a kcp roll keeps every
  `/readyz` through the front answering (0 failed polls at 200 ms) and the controller gap under 5 s";
  the in-process clause "kcp's restart window is recorded" is deleted if the route closes.
- **M5-CUTOVER / M5-RETIRE**: already void per user decision 2026-10-12; nothing to add.

## 9. Questions for the user (with recommended answers)

1. Store for the finished kcp: kine -> a dedicated postgres unit (recommended), kine -> sqlite, or an
   etcd unit embedded in the binary? Recommended: postgres, kine per slot.
2. Front for 127.0.0.1:6443: our own L4 front shared with the tenant fronts (recommended), or let
   caddy hold it (slot ports + health checks, caddy serves kcp's key)? Recommended: our own front; caddy
   stays public TLS only.
3. Lease handover: delete the lease after the old slot exits (no patch, measured 0.24-3.2 s), or carry
   a `ReleaseOnCancel: true` patch in the vendored kcp (cleaner, upstreamable)? Recommended: the patch
   (we vendor and patch kcp anyway for the port report), with the lease delete as the check-and-repair
   step.
4. kcp minor-version rolls: allow only pairs whose storage versions and phase-0/1 bootstrap schemas
   are equal (checked from the vendored trees), with a pg_dump before every roll, and treat other
   upgrades as a planned short outage? Recommended: yes.
5. Confirm the out-of-process kcp route, given that the in-process route cannot blue/green kcp.
   Recommended: out-of-process.

## Sources

- kcp v0.33.0 source (module cache): `cmd/kcp/kcp.go:147-156`; `pkg/server/options/options.go:146-149,
  :171, :186-195, :251-252, :290-291, :339-369, :437-449`; `pkg/server/options/controllers.go:55-71,
  :82`; `pkg/server/options/authentication.go:60-64, :95-183, :185-221`; `pkg/server/options/flags.go:121-146`;
  `pkg/server/server.go:87-93, :463, :566-701, :731-791, :825-891`; `pkg/server/controllers.go:138-189,
  :1246-1270, :1900-1965`; `pkg/server/config.go:552-570, :711-746`; `pkg/server/cache.go:31-66`;
  `pkg/cache/server/config.go:98-115`.
- kine v0.17.1 source: `pkg/app/app.go:162-205`, `pkg/server/compact.go`, `pkg/server/watch.go`,
  `pkg/server/{cluster,get,kv,create}.go`, `pkg/logstructured/sqllog/sql.go:548`,
  `pkg/drivers/t4/README.md`, `pkg/drivers/t4/t4.go`.
- kcp-operator `internal/resources/compiledrootshard/deployment.go:222-275` at 8994ed74e225:
  https://github.com/kcp-dev/kcp-operator/blob/8994ed74e225878b497a78526e1fd405a16b0688/internal/resources/compiledrootshard/deployment.go
- [RootShard CRD reference (replicas default 2, etcd endpoints, cache, proxy)](https://docs.kcp.io/kcp-operator/main/reference/crd/operator.kcp.io/rootshards/)
- [kcp Production Setup](https://docs.kcp.io/kcp/main/setup/production/),
  [kcp Architecture Overview (shard URL defaulting)](https://docs.kcp.io/kcp/main/setup/production/overview/),
  [kcp Shards concept (front-proxy routing)](https://docs.kcp.io/kcp/main/concepts/sharding/shards/)
- [caddy reverse_proxy (health checks, lb_policy, streaming, stream_close_delay)](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy),
  [caddy admin API (PATCH /config, @id)](https://caddyserver.com/docs/api)
- [kcp operator on pkg.go.dev](https://pkg.go.dev/github.com/kcp-dev/kcp-operator)
- This repo: `deploy/container-entrypoint.sh:24-25, :61, :123-125, :226-235`; `scripts/deploy.sh:151-193`;
  `deploy/denokcp/start-kcp.sh:5-69`; `internal/kcpclient/kcpclient.go:81-97`;
  `internal/clusterprovider/stack.go:39-43`; `docs/research/2026-10-11-vm-lifecycle/control-plane-handover.md`
  sections 0, 2.1, 4, 6; `docs/plans/0006-socialweb-computer-unified-binary.yaml` items cited.
