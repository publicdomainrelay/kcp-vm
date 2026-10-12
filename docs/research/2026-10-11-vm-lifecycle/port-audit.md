# Port and singleton audit: N market beds on one host (hat-8)

Read-only audit, 2026-10-11. Scope: every fixed TCP/UDP port, fixed path, fixed name, mutable image tag
and other host-global singleton that stops two or more task trees from running the kcp market bed
(`socialweb-computer-kcp/deploy/examples/atproto/market`, `deploy/denokcp`, `internal/marketbed`) at the
same time on hat-8, each with its own fake PLC, relay, ingress proxy, PDSes, several bidders and
requesters and no real accounts. Everything else (other tests, CLIs, dev scripts) is in section 5.

Related: plan 0008 `M1-BED-PARALLEL` (todo, depends on 0006 `M1-PORT-LEASE-INSTANCE`) already names the
goal and its tests (`TestNoBedPortIsALiteral`, `TestTwoBedRecordsLeaseDisjointPorts`,
`TestTwoBedsOnOneHostEachRentAGuestThroughTheRFPFlow`); dev-run-mode.md 4.5/4.8 and kcp-spike.md 1 cover
the kcp port-0 read-back. This file is the concrete inventory those items need.

Legend: **Override today** = can a caller already change it without editing code. **Impact** = what
breaks when two beds run at once.

## 0. Answer in one screen

What blocks bed #2 today, in order:

1. **Service ports 2583-2589 are literals in seven DenoPod manifests** and in every consumer of them
   (accept.sh checks, requester args, bidder args, PDS_CRAWLERS, dispatcher-tls env). Bed #2's pods fail
   to bind and the provider restarts them forever (`restartPolicy: Always`).
2. **The provider learns a pod's address only by parsing `--port`/`PORT` out of the manifest**
   (`kcp-libs/factory/servicenames/servicenames.go:166-197`). Port 0 cannot work until the resolver reads
   the bound port back. This is the one code change everything else waits on.
3. **The one-bed refusal** in `internal/marketbed/live_test.go:18,112-120` (probe-binds 2583-2589 and
   skips/fails if any is held). This is the "one-bed lock"; there is no lock file in today's tree.
4. **Every service writes `./port-bound-to` into its source checkout** (`port-file` default in each CLI's
   cli-args-env; DenoPod cwd = `SERVICE_CWD` = the repo dir). Both PDSes of one bed already share
   `hono-pds/port-bound-to`; N beds all share it. This is the exact mechanism port 0 needs, pointed at a
   shared path (the stray `?? port-bound-to` at the org root is this).
5. **Bidder state under `$HOME/.cache/pdr-market/`** (`pds-state-path` Deno KV, oauth session, key path):
   the bed's bidder does not override it, so every bidder of every bed opens one KV file.
6. **m3 live test binds the docker bridge gateway on 2583/2587** (`live_m3_test.go:96-98,465-467`) and
   socats to fixed 2583/2587/2588/2589 inside its container.
7. **kcp/kine/openbao ports are probed-then-bound** (accept.sh:78-91, python bind/close) - works today
   because accept.sh already randomises them, but it is the org's banned listen-close-rebind race.
8. **Mutable runner image tag** `localhost/socialweb-firecracker-runner:local`; its build writes binaries
   into the shared source tree (`hono-compute-provider/deno.json:67`).
9. **FC work root vs the 107-byte unix socket limit**: a TASK_STATE_DIR-keyed work root
   (`~/.local/state/swc-tasks/<task>/bed/guests/fc-.../fc.sock`) is 110+ bytes for ordinary task names.
10. **Single bidder pod / single bidder key**: "several bidders per bed" needs templated `bidder-<n>`
    pods with their own port, key and state path.

Already per-bed or harmless (do not spend time here): kcp workspace names and DenoPod names (each bed
has its own kcp), `*.localhost` / `relay.localhost` names (ports separate beds), guest container names
(random), the 172.30.0.0 guest range (lives inside each guest's own container netns), ACCEPT_ROOT,
KCACHE, GUEST_TLS_DIR and key files under accept.sh, the requester (port 0, result file per root), the
atproto-market container harness (port 0 throughout).

## 1. Per-bed parameters (the target shape)

One bed = one `BED_ID` (8 chars `[a-z0-9]`, e.g. first 8 hex of sha256(task name + n)). Everything the
bed owns hangs off it:

| Parameter | Value | Why |
|---|---|---|
| `TASK_STATE_DIR` | `$XDG_STATE_HOME/swc-tasks/<task>` | per task tree |
| `BED_DIR` (= today's `ACCEPT_ROOT`) | `$TASK_STATE_DIR/beds/$BED_ID` | kcp root, kine db, openbao dir, runs dir, keys, guest TLS, logs, bed record |
| `FC_WORK_ROOT` | `/var/tmp/swc-$BED_ID` (symlinked from `$BED_DIR/guests`) | must stay short: `fc-<12>-<8>/fc.sock` adds 32 bytes; 107-byte limit. Not `$XDG_RUNTIME_DIR` (tmpfs; guest disks live in the work dir) |
| bed record | `$BED_DIR/bed.json` (atomic rename) | every bound port, kubeconfig path, FC work root, runner image digest, HOST_IP, bidder DIDs; the only thing accept.sh checks, the requester and the Go tests read |
| all service ports | `0`, bound port reported via `createServe` `portFile` -> `<runDir>/port-bound-to` -> DenoPod status -> bed record | no leases needed for these |
| kcp secure port | `0` + `--experimental-bind-free-port`, read back from `$BED_DIR/kcp/admin.kubeconfig` / `/proc` LISTEN socket (kcp-spike.md 1) | |
| kine / openbao | `127.0.0.1:0`, read back from the pid's single LISTEN socket in `/proc` (same technique), or lease from the per-user lease file (0006 M1-PORT-LEASE-INSTANCE) | unverified that kine/bao log the bound port |
| docker label | `--label swc.bed=$BED_ID` on guest containers | sweep by label, not by name or work-root listing |
| runner image | content-hash tag `localhost/socialweb-firecracker-runner:sha-<12>` pinned in bed.json | rebuilds by one task never change another bed's guests |
| slot | flock `/var/tmp/socialweb-bedslots/<i>.lock`, i < `SWC_BED_SLOTS` | section 6 |

## 2. The bed's own singletons, in detail (socialweb-computer-kcp unless named)

Ranked by what stops two beds first.

### B1. Fixed service ports in the DenoPod manifests - BLOCKER

| file:line | what |
|---|---|
| deploy/examples/atproto/market/20-global-plc.yaml:13,15 | plc `--port 2587`, `PORT 2587` |
| 30-relay-relay.yaml:13,15 | relay `--port 2584` |
| 35-relay-dispatcher.yaml:16,19 | dispatcher (hono-did-key-ingress-proxy) `--port 2588`, `--plc-directory-url https://127.0.0.1:2587` |
| 36-relay-dispatcher-tls.yaml:15-17,29 | `DISPATCHER_PORT 2588`, `TLS_PORT 2589`, `Deno.serve({hostname: "0.0.0.0", port})` |
| 40-alice-pds.yaml:16,18 | alice PDS `--port 2583`, `PDS_CRAWLERS https://127.0.0.1:2584` |
| 60-bob-pds.yaml:16,18 | bob PDS `--port 2585`, `PDS_CRAWLERS https://127.0.0.1:2584` |
| 70-bidder.yaml:24,34 | bidder `--serve-port 2586`, `--plc-directory-url https://127.0.0.1:2587`, `--ingress-proxy-host relay.localhost:2588`, `--guest-tls-port 2589`, `PORT/SERVE_PORT 2586` |

- Override today: no (literals; apply.sh's sed does not touch them).
- Impact: bed #2's pods all fail to bind; `restartPolicy: Always` loops; bed #2's verifier/requester may
  silently talk to bed #1's services on the same ports (wrong-bed traffic, not just failure).
- Fix: `--port 0` / `PORT "0"` / `--serve-port 0`, `TLS_PORT 0`; dependents name the service by its
  FQDN (`https://plc.default.global.svc.kcp.local`, `https://relay.default.relay.svc.kcp.local`) the way
  `PDS_PLC_DIRECTORY_URL` already does, never `127.0.0.1:<n>`. Needs B2 first. The bidder's
  `--ingress-proxy-host` and `--guest-tls-port` are guest-facing (go into cloud-init user_data), so the
  dispatcher and dispatcher-tls ports must be known before the bidder pod is rendered: apply.sh already
  applies 35/36 before 70; add "wait for status.address, then render 70".
- 36-relay-dispatcher-tls binds `0.0.0.0` on purpose (guests reach it via HOST_IP). With port 0 per bed
  that stays correct; note it exposes the bed on the LAN.

### B2. Provider derives a pod's address from its manifest, not its socket - BLOCKER (kcp-libs, deno-kcp)

- `kcp-libs/factory/servicenames/servicenames.go:166-197` `AdvertisedAddress(argsJSON, envJSON)` reads
  `--port`/`PORT` and `--hostname`/`HOSTNAME`; used at :109 (DNS table for every other pod) and :149
  (self). `deno-kcp/internal/provider/service_dns.go` feeds it via `services.Env(... SERVICE_ARGS,
  SERVICE_ENV ...)`.
- With port 0 the table would say `127.0.0.1:0`.
- Fix: the pod reports its bound port (B3: `<runDir>/port-bound-to`, written by `createServe` onListen);
  the provider's runner reads it on observe and records `status.address` on the DenoPod;
  `AdvertisedAddress` prefers `status.address` and falls back to args. The DNS table is delivered in env
  (`KCP_DNS_TABLE`) at process start, so a pod started before its dependency bound would carry a stale
  table: deliver the table as a file in the run dir (`KCP_DNS_TABLE_FILE`) that the shim re-reads on a
  lookup miss, or keep strict apply ordering. Probes use `status.address`.

### B3. `port-file` defaults to `./port-bound-to` in the shared source checkout - BLOCKER for port 0

| file:line | default |
|---|---|
| hono-pds/cli-args-env.json:57 | `port-file ./port-bound-to` (and `port` 2583 at :5) |
| atproto-relay/hono-atproto-relay/cli-args-env.json:31 | same (port 2584 at :5) |
| atproto-market/hono-plc/cli-args-env.json:27 | same (port 2587 at :5) |
| did-key-ingress-proxy/hono-did-key-ingress-proxy/cli-args-env.json:87 | same (port 8080 at :17) |
| atproto-market/hono-bidder/cli-args-env.ts:267-272 | same (serve-port default 0, good) |
| typescript-helpers/lib/serve/mod.ts:131-136 | `createServe` writes it whenever `portFile` is set |

- The pod script runs the service with `cwd: SERVICE_CWD` (e.g. 40-alice-pds.yaml:15,67), the repo dir.
  Alice and bob PDS write the same `hono-pds/port-bound-to` in ONE bed; all beds share it.
- Override today: yes, `PORT_FILE` env.
- Fix: the pod script sets `childEnv.PORT_FILE = \`${runDir}/port-bound-to\`` (it already computes
  `runDir`, 40-alice-pds.yaml:35). Consider changing the CLI default to `""` (write nothing unless asked)
  so dev runs stop littering checkouts.

### B4. accept.sh host-side consumers of fixed ports - BLOCKER

- accept.sh:417-422 bob PDS check `--resolve pds.default.bob.svc.kcp.local:2585`; :432-436 bidder check
  `http://127.0.0.1:2586`; :506-515 requester `--plc-directory-url https://127.0.0.1:2587`,
  `--ingress-proxy-host relay.localhost:2588`, `--guest-diagnose-url https://relay.localhost:2589/...`.
- apply.sh:297-303 prints the same literals (cosmetic).
- Override: no.
- Fix: read each from `kubectl get denopod <n> -o jsonpath={.status.address}` (B2) into bed.json, and
  read bed.json. `HOST_IP` (accept.sh:469) is host-wide and fine to share.

### B5. The one-bed refusal - BLOCKER (by design)

- internal/marketbed/live_test.go:18 `testBedPorts = {2583..2589}`; :112-120 probe-binds each and
  `livegate.Require`s "cannot share them with a second test bed".
- Fix: delete with B1; replace with the bed slot (section 6) plus `TestTwoBedRecordsLeaseDisjointPorts`.
  live_test.go:76-80 work root `os.MkdirTemp("/var/tmp", "mtb-")` is already unique; switch to
  `/var/tmp/swc-$BED_ID` only to share the naming.

### B6. m3 live test host-global binds - BLOCKER for parallel m3 runs

- internal/marketbed/live_m3_test.go:96-98 `forwardLoopback(gateway, 2583|2587)` -> :467
  `net.Listen("tcp", "<docker0 gw>:2583")` on the host. Two m3 runs collide (same docker bridge gateway).
- :38 `m3OwnerPDS = https://127.0.0.1:2583`; :582 `INGRESS_PROXY_HOST=relay.localhost:2588`; :593-601
  socat inside the container to `gw:2583/2588/2589/2587`.
- :39-41 `/var/lib/socialweb`, `127.0.0.1:6443` are inside the container's own netns/volume: fine.
- :110-112 container `m3live-<hex>`, volume `m3live-state-<hex>`: fine.
- Fix: forward `gw:0` -> bed port, pass the forwarded ports into the container env/socat args from
  bed.json. Image comes from `SOCIALWEB_M3_CONTROL_PLANE_IMAGE`; default build tag is fixed (S-12).

### B7. kcp / kine defaults and the probe-then-bind race - HIGH

- deploy/denokcp/start-kcp.sh:4-6 `ROOT=$PWD/.kcp`, `KINE_ENDPOINT=http://127.0.0.1:23791`,
  `KCP_SECURE_PORT=6443`; :54 kine `--listen-address`; :71 kcp `--secure-port`. Override: yes (env).
  accept.sh passes fresh values; a bare apply.sh run uses 6443/23791 and `$REPO/.kcp-demo`.
- deploy/denokcp/stop-kcp.sh:19 default `ROOT=$REPO/.kcp-demo` differs from start-kcp's `$PWD/.kcp`:
  a stop without ROOT targets a different bed's root.
- accept.sh:78-91 `free_port` (python bind 0, read, close) for kcp, kine, openbao: listen-close-rebind
  race (org NEVER rule). The kernel's ephemeral range on hat-8 is 32768-60999, so any outbound connect
  from another bed can take the port in between. Rare, but N beds x many connects makes it real.
- deploy/run-kcp-instance.sh:8,23 refuses `KCP_SECURE_PORT=0` ("kcp cannot switch --secure-port off
  with 0") - contradicts kcp-spike.md 1 (`--secure-port=0 --experimental-bind-free-port` works, bound
  port in admin.kubeconfig 0.3-1.8 s after exec). Resolve before relying on either.
- Fix: kcp port 0 + read-back (kcp-spike); kine/openbao port 0 + `/proc/<pid>` LISTEN read-back, or a
  lease from the per-user lease file keyed by `(BED_ID, purpose)`. Record all in bed.json.
- Vendored twin: deno-kcp/deploy/start-kcp.sh:4-6 (same defaults), demo.sh:26-27 (16444/23792),
  demo-deno-runtime.sh:14-15 (16443/23791 - same kine port as start-kcp's default).

### B8. OpenBao - MEDIUM

- deploy/denokcp/start-openbao.sh:15-16 `DIR=$REPO/.kcp-openbao`, `ADDR=127.0.0.1:8200`; apply.sh:135
  default 8200. accept.sh overrides both (OPENBAO_DIR under ACCEPT_ROOT, sniffed port).
- `bao server -dev` may also bind a cluster listener at listen port + 1 (Vault dev behaviour;
  unverified for this openbao build). With random ports that is an occasional collision.
- Fix: port from bed record; pass an explicit cluster address or verify none is bound.

### B9. apply.sh state defaults under the checkout - MEDIUM

- apply.sh:24 `KCACHE=mktemp` (fine); :27 `KUBECONFIG=$REPO/.kcp-demo/admin.kubeconfig`; :29-31 key
  files `$REPO/.kcp-demo/*.hex`; :78 `GUEST_TLS_DIR=$REPO/.kcp-demo/guest-tls`.
- accept.sh:287-297 overrides every one into ACCEPT_ROOT, so beds via accept.sh are fine; two beds
  driven by bare apply.sh from one checkout share keys, kubeconfig and the guest CA.
- Fix: default all of them to `$BED_DIR` and refuse to run without `BED_ID`.

### B10. Org-root path rewrite - MEDIUM

- Manifests hardcode `/home/johnandersen777/src/publicdomainrelay-kcp/...` in `SERVICE_ENTRY` /
  `SERVICE_CWD` (e.g. 40-alice-pds.yaml:14-15, 70-bidder.yaml:15); apply.sh:171 sed rewrites it to
  `$ORG_ROOT`; apply.sh:18 / accept.sh:26 default `ORG_ROOT=$REPO/..`.
- Two tasks sharing one org root run the same code (fine) but share every repo-relative write (B3). A
  task working in a git worktree of socialweb-computer-kcp only (e.g. under .claude/worktrees) gets a
  wrong `$REPO/..`; marketbed live_test.go:63-73 then refuses (siblings must be real dirs).
- Fix: `ORG_ROOT` required in the bed record; a task tree that changes sibling repos needs its own org
  root checkout (or worktrees of each sibling under one per-task org root).

### B11. Bidder state in `$HOME/.cache/pdr-market` - HIGH (atproto-market)

- atproto-market/hono-bidder/cli-args-env.ts:53 `oauth-session-path`, :239 `private-key-hex-path`,
  :245 `pds-state-path` (Deno KV sqlite) all default to `$HOME/.cache/pdr-market/bidder-*`.
- The pod passes the host `HOME` through (70-bidder.yaml childEnv) and no `--pds-state-path`. The key is
  overridden by `REPO_PRIVATE_KEY_HEX` (cli-args-env.ts:21), the session is unused under `--skip-qr`,
  but every bidder in every bed opens the same `bidder-pds` KV.
- Fix: `--pds-state-path <runDir>/bidder-pds` (or `$BED_DIR/bidders/<n>/pds`) in the manifest; same for
  the session path. Same pattern in atproto-market/request-vm-ssh/cli-args-env.ts:26,63,212 (not used
  by the bed's market-requester).

### B12. One bidder per bed - HIGH for the "several bidders" goal

- 70-bidder.yaml:4 pod `bidder` in workspace `bob`; apply.sh:31 one `BIDDER_KEY_FILE`; accept.sh:465
  takes the first `did:plc` from the bidder log.
- Fix: render `bidder-<n>` pods (n = 1..BED_BIDDERS) each with port 0, its own key file
  `$BED_DIR/bidders/<n>/key.hex`, its own pds-state-path, and the same bed FC work root (guest names are
  random). bed.json lists all bidder DIDs; the requester gets `--bidder-dids` from it. Several requesters
  per bed already work: market-requester binds port 0 (market-requester/mod.ts:111) and takes
  `--result-file` per run.

### B13. FC work root and the socket-length limit - HIGH

- apply.sh:53-71 and live_test.go:84-87 refuse a work root whose `fc-000000000000-00000000/fc.sock`
  exceeds 107 bytes. `/home/johnandersen777/.local/state/swc-tasks/` is 45 bytes; a 24-char task name +
  `/beds/<8>/guests/` + 32 = 115+. A TASK_STATE_DIR-keyed work root fails this check.
- bob-bidder.sh:45 and the TS live tests' documented `/var/lib/socialweb/guests` are one shared root.
- Fix: `FC_WORK_ROOT=/var/tmp/swc-$BED_ID` (22 + 32 = 54 bytes), symlink into `$BED_DIR`. Never share a
  work root between beds: accept.sh:141-155 `stop_own_guests` kills every container named after a
  directory in it. Add `--label swc.bed=$BED_ID` to guest containers
  (hono-compute-provider/lib/microvm-firecracker/mod.ts:228-231) and sweep by label.

### B14. Runner image tag - MEDIUM

- `localhost/socialweb-firecracker-runner:local` (bob-bidder.sh:44,
  scripts/deploy-firecracker-bidder.sh:194, socialweb-computer-k8s/test/live_market_node_test.ts:21
  docs; tests take `SOCIALWEB_FIRECRACKER_RUNNER_IMAGE`).
- hono-compute-provider/deno.json:67 `build:firecracker-runner` writes `socialweb-nodeboot` and
  `firecracker` into `hono-firecracker-standalone/` in the source tree, then tags
  `${RUNNER_IMAGE:-atcr.io/.../socialweb-firecracker-runner:latest}`. Two concurrent builds race on the
  build context; a rebuild retags under a running bed so its next guest boots a different runner.
- Fix: build into `$TASK_STATE_DIR/build/runner`, tag `localhost/socialweb-firecracker-runner:sha-<12>`
  (hash of Dockerfile + nodeboot + firecracker), record the tag/digest in bed.json; the bed passes that
  tag, never `:local`.

### B15. Deno KV default paths - LOW today, latent

- hono-pds/main.ts:33 `DenoKvStorage.create()` (no path) and
  atproto-relay/lib/hono-factory-atproto-relay-xrpc/mod.ts:47 `Deno.openKv()` (no path): KV location is
  `DENO_DIR/location_data/<hash(main module)>`. Today it is per run because the runner sets
  `DENO_DIR=<runDir>/.deno` (kcp-libs/impl/execrunner/pod.go:198-205, appended to `os.Environ()` at
  supervisor.go:138) and the pod script's `Deno.Command` inherits it. If anyone sets `DENO_DIR` in the pod
  env (to share the module cache, a natural speed fix) alice+bob PDS and every bed's PDS/relay share one
  KV.
- Fix: add a `kv-path` option to hono-pds and the relay (cli-args-env, default `""` = today) and set it
  to `<runDir>/kv.sqlite3` in the manifests; or add `--location=http://<pod>.<ws>.<BED_ID>.bed/` to
  `SERVICE_DENO_FLAGS`.

### B16. Names that are already per bed - no action

- Workspaces `global relay alice bob` (00-workspaces.yaml; apply.sh:33, accept.sh:65) and DenoPod names
  `plc relay dispatcher dispatcher-tls pds verifier bidder` are per kcp, and each bed runs its own kcp.
  Only if beds ever share one kcp (dev-run-mode N2): `root:bed-$BED_ID:{global,...}`.
- `*.svc.kcp.local` FQDNs resolve through each provider's own shim table: per bed.
- `relay.localhost`, `*.relay.localhost`, `pds.default.*.svc.kcp.local` on the host: names are not a host
  resource; loopback resolution plus the per-bed port separates beds. Guests map `relay.localhost` to
  `HOST_IP` via `--guest-host-aliases` (accept.sh:511). Each bed signs its own `relay.localhost` leaf
  with its own CA (apply.sh:79-94 under GUEST_TLS_DIR). Keep.
- Firecracker `DEFAULT_RANGE_BASE = "172.30.0.0"`
  (hono-compute-provider/lib/compute-provider-firecracker/mod.ts:110, scripts/deploy-firecracker-bidder.sh:195),
  per-bidder `nextIndex` (:570,648): each guest runs in its own runner container (no `--network`, default
  bridge, microvm-firecracker/mod.ts:228-231,298) with pasta and the tap inside that container's netns,
  so two bidders handing out 172.30.0.1 do not meet. `GUEST_SSH_BIND = "0.0.0.0"` (:114) is inside the
  container netns too. Keep; only matters if a host-netns runner mode is ever added.
- Guest container names `fc-<shortId(requester)>-<uuid8>` (compute-provider-firecracker/mod.ts:645):
  unique.

## 3. Ranked blocker list (two live RFP-flow beds at once)

| # | Blocker | Fix | Plan hook |
|---|---|---|---|
| 1 | B2 servicenames reads ports from manifest args | status.address from `<runDir>/port-bound-to`; table file re-read | 0008 M1-BED-PARALLEL |
| 2 | B1 manifests 2583-2589 | port 0 + FQDNs; render bidder after dispatcher ports known | same |
| 3 | B3 port-file in source cwd | `PORT_FILE=<runDir>/port-bound-to` in pod script | same |
| 4 | B4 accept.sh literals | read bed.json / denopod status | same |
| 5 | B5 one-bed refusal | delete; bed slot flock | same |
| 6 | B11 bidder KV under ~/.cache | `--pds-state-path` per bidder | new |
| 7 | B13 FC work root length/sharing | `/var/tmp/swc-$BED_ID`, docker label | same |
| 8 | B12 single bidder | `bidder-<n>` templating | new |
| 9 | B7 kcp/kine/bao sniffed ports, stop-kcp ROOT mismatch | port 0 + /proc or lease read-back | 0006 M2-KCP-PORT0, M1-PORT-LEASE-INSTANCE |
| 10 | B14 runner image `:local` + in-tree build | content-hash tag in bed.json | new |
| 11 | B6 m3 gateway binds 2583/2587 | gw:0 forwards from bed.json | 0008 M1-BED-PARALLEL |
| 12 | B9/B10 apply.sh defaults, ORG_ROOT | `$BED_DIR` defaults; ORG_ROOT required | same |

## 4. How port 0 flows end to end (the minimal change set)

1. Pod script (each manifest): `childEnv.PORT_FILE = runDir + "/port-bound-to"`; args `--port 0`.
2. Service: unchanged - `createServe({ portFile })` already writes the bound port on listen
   (typescript-helpers/lib/serve/mod.ts:131-136). hono-pds, relay, plc, ingress proxy and bidder all
   pass `portFile` through (hono-pds/main.ts:128, hono-atproto-relay/mod.ts:68, hono-plc/mod.ts:30,
   hono-did-key-ingress-proxy/mod.ts:80, hono-bidder/mod.ts:629). dispatcher-tls is inline in its
   manifest: write the file from its `onListen` (36-relay-dispatcher-tls.yaml:31-32).
3. Runner (kcp-libs execrunner Pod observe): read `<runDir>/port-bound-to`, report it; provider writes
   `status.address`.
4. servicenames: prefer `status.address`; publish the table as a run-dir file the shim re-reads.
5. apply.sh: after each pod is Ready, read `status.address` into bed.json; render 70-bidder with the
   dispatcher (`relay.localhost:<p>`) and TLS port from bed.json.
6. accept.sh / Go tests / requester: read bed.json only. `TestNoBedPortIsALiteral` enforces it.

## 5. Secondary findings by repo (not on the bed path)

### socialweb-computer-kcp

| id | file:line | what | override | impact | fix |
|---|---|---|---|---|---|
| S-1 | internal/liveports/liveports.go:67 | `/var/tmp/socialweb-liveports` flock dir; blocks 30500-32400, 61000-63839, probe 64000-64255 | n/a | intentional host-global coordination; Slots cap parallel proofs (MountProof 4, SchemaUpgrade 4, others 8). All blocks sit outside the ephemeral range 32768-60999 except 30500-32400 (below it): good | keep; it is the model for the bed slot |
| S-2 | internal/clusterprovider/steps_test.go:401, internal/openbao/openbao_test.go:23,313, internal/vaultcert/vaultcert_test.go:43, internal/materialiser/materialiser_test.go, cmd/socialweb-vault/apply_live_test.go:39, internal/clusterprovider/nodesecrets_test.go:58, links_live_test.go:90 | listen :0, read, close, rebind (`freePort`) | n/a | rare races under N tasks | pass the listener or use port 0 in the child and read back |
| S-3 | internal/clusterprovider/live_kcp_test.go:28 | `CLUSTERPROVIDER_LIVE_HOST` default `https://127.0.0.1:6443` | env | points every task at the host's shared kcp | require the env; no default |
| S-4 | internal/kcpclient/live_apiserver_test.go:25-29, mount_child_live_test.go:17-20, internal/tenantclient/live_renewal_test.go:32-37, cmd/socialweb-gateway/mintconfig_live_test.go:22-25, internal/clusterprovider/live_tenant_test.go:29-33 | live tests against an externally supplied kcp/tenant via `SOCIALWEB_LIVE_*` | env | two tasks given the same target mutate the same workspaces | per-task env file under TASK_STATE_DIR |
| S-5 | internal/hostsvc/live_linux_test.go:97 | unit prefix `socialweb-test-<rand>-` | n/a | safe | none |
| S-6 | deploy/run-container.sh:5-7,17-19 | image `socialweb-k8s:local`, volume `socialweb-computer-kcp-state`, name `socialweb`, `-p 127.0.0.1:8443/6443/8788`, `docker rm -f $NAME` first | env (name, volume, image); ports no | second run kills the first container | name/volume `swc-$BED_ID`, `-p 127.0.0.1::<port>` + `docker port` read-back |
| S-7 | deploy/control-plane-container/build.sh:7-10,104-105, validate.sh:5, scripts/deploy.sh:11 | tags `socialweb-control-plane:local`, `-nodeimage:local`, `socialweb-k8s:local`; `BINARIES=/var/tmp/socialweb-control-plane-binaries`; `KCP_IMAGE_DIR=/home/johnandersen777/kcp-image` | env | concurrent builds overwrite the binaries dir and retag | content-hash tags; binaries dir under TASK_STATE_DIR |
| S-8 | deploy/container-entrypoint.sh:8,21,24,28,233 | `/var/lib/socialweb`, 8200, 6443, 8080 | env | inside the container: safe | none |
| S-9 | cmd/socialweb-nodeimage/buildlock_linux.go:17 | `<stage>.lock` flock on the shared image store | n/a | serialises builds correctly | keep; content-addressed store is safe to share |
| S-10 | internal/portallocator/portallocator.go:145-148,227; internal/subnetallocator/subnetallocator.go:19,245 | lease key ignores instance; pool 10.100.0.0/16; one lease file per path | path | two instances with two lease files lease the same port/subnet | 0006 M1-PORT-LEASE-INSTANCE (one per-user file, key by instance) |
| S-11 | socialweb-computer-k8s/test/live_market_node_test.ts:261-275 | `pickPort()` random 20000-59999 + `portIsFree` probe | no | overlaps the portallocator carve 20000-29999, liveports blocks and the ephemeral range | port 0 for k3s, or take a liveports block |
| S-12 | socialweb-computer-k8s/hono-socialweb-computer-k8s/cli-args-env.json:5,17 | `http-port 8788`, `state-dir .socialweb-computer-k8s` (cwd-relative) | flag/env | tests that start it with defaults collide | tests pass 0 + a temp state dir |
| S-13 | scripts/deploy-firecracker-bidder.sh:194-195 | runner `:local`, `RANGE_BASE 172.30.0.0`, `/var/lib/socialweb-computer-bidder/<handle>` | env | remote production deploy, not a bed | none for beds |
| S-14 | cmd/socialweb-computer-kcp-check-host/config.go:193 | `/dev/kvm` | n/a | host-global by nature | section 6 |
| S-15 | Go unit tests with literal ports/paths (internal/clusterstack/clusterstack_test.go:32-55, internal/clusterprovider/steps_test.go:32-50, steps_openbao_test.go:41,166-172, externalsecrets_test.go:88, nodesecrets_test.go:174, kcp/pkg/proxy etc.) | spec values, never bound | n/a | safe | none |

Vendored upstream trees (kcp/, kata-containers/, cluster-api/, streamplace/, third_party/) were
excluded.

### deno-kcp

| id | file:line | what | fix |
|---|---|---|---|
| D-1 | deploy/start-kcp.sh:4-6, stop-kcp.sh:19, start-openbao.sh:15-16, install-provider.sh:5 | 6443 / 23791 / 8200, `$PWD/.kcp`, `$REPO/.kcp-demo`, `$REPO/.kcp-openbao` | same as B7/B8 |
| D-2 | deploy/demo.sh:26-27, demo-deno-runtime.sh:14-15 | 16444/23792 and 16443/23791 against one shared `$REPO/.kcp-demo` | per-run root + port 0 |
| D-3 | deploy/examples/atproto/market/*.yaml, apply.sh:25-27,84,180-185 | older copy of the bed: 2583/2584/2587, 8200, `.kcp-demo` keys | retire or regenerate from socialweb-computer-kcp's bed |
| D-4 | internal/provider/live_helpers_test.go:68, test/integration/fixture_test.go:151,358-373 | `freePort` sniff for kine/kcp | port 0 + read-back |
| D-5 | cmd/deno-kcp-provider/main.go:79 | `RUNS_DIR` default `runs` (cwd-relative) | accept.sh sets it per bed; make it required |
| D-6 | deploy/examples/policy-workflow-run.yaml:9, demo.sh:76 | engine `127.0.0.1:8080` | port from the engine's port file |

### kcp-libs

| id | file:line | what | fix |
|---|---|---|---|
| K-1 | factory/servicenames/servicenames.go:166-197 (+:109,:149) | address from manifest args | B2 |
| K-2 | impl/execrunner/pod.go:198-205 | per-run `DENO_DIR` (isolates KV by accident, and re-downloads modules per run) | keep isolation explicit (B15) before sharing a module cache |

### atproto-market

| id | file:line | what | fix |
|---|---|---|---|
| A-1 | hono-bidder/cli-args-env.ts:53,239,245,267-272 | `~/.cache/pdr-market/bidder-*`, `./port-bound-to` | B11, B3 |
| A-2 | request-vm-ssh/cli-args-env.ts:26,63,212; mod.ts:169; lib/atproto-helpers/oauth-qr-session.ts:20 | `~/.cache/pdr-market/requester-*` | per-task `--*-path` |
| A-3 | hono-plc/cli-args-env.json:5,27 | port 2587, `./port-bound-to` | B1/B3 |
| A-4 | test/bidder_container_integration_test.ts:55-70,182-200 | port 0 + onListen for dispatcher, TLS and fake PLC; random container names | safe - the model for the bed |
| A-5 | test/*integration_test.ts (gateway_*, registry_discovery, bidder_ssh_relay, ...) | all `Deno.serve` with `port: 0` | safe |
| A-6 | test/bidder_firecracker_integration_test.ts:79-84 | `SOCIALWEB_FIRECRACKER_WORK_ROOT` (docs: `/var/lib/socialweb/guests`) | per-task short work root (B13) |

### hono-compute-provider

| id | file:line | what | fix |
|---|---|---|---|
| H-1 | deno.json:67 | runner build writes into source tree, tag `:latest` | B14 |
| H-2 | lib/compute-provider-local/mod.ts:197-198,255-293,321-324 | image tag `container-runner-<distro>:latest` built-if-absent / force-rebuilt; container name random | content-hash tag; concurrent `--force` rebuilds race |
| H-3 | lib/compute-provider-local/mod.ts:125-126,301; lib/qemu-standalone/mod.ts:23-24 | shared `~/.cache/pdr-local`, `~/.cache/simple-qemu` (systemctl shim, qemu downloads) | write-to-temp + rename; or per-task cache for writes |
| H-4 | test/oidc_workload_identity_integration_test.ts:25-31 | listen 0, close, rebind (ssh + issuer) | port 0 in the child |
| H-5 | lib/compute-provider-firecracker/mod.ts:110,114,570 | 172.30.0.0, 0.0.0.0, per-process index | safe (B16) |
| H-6 | test/defect3/6, oidc_onnetwork_reporter_test.ts | `127.0.0.1:9977/9978` issuer strings, never bound | safe |

### hono-pds, atproto-relay, did-key-ingress-proxy, typescript-helpers

| id | file:line | what | fix |
|---|---|---|---|
| P-1 | hono-pds/main.ts:33 | default KV path | B15 |
| P-2 | atproto-relay/lib/hono-factory-atproto-relay-xrpc/mod.ts:47 | default KV path | B15 |
| P-3 | hono-pds, hono-atproto-relay, hono-did-key-ingress-proxy cli-args-env.json | ports 2583/2584/8080, `./port-bound-to` | B1/B3 |
| P-4 | hono-pds/test/oauth_client_fetch_ssrf_test.ts:6, docs-compliance.test.ts:152-193 | `127.0.0.1:2583` as request URL/issuer strings; server on port 0 | safe |
| T-1 | typescript-helpers/lib/serve/mod.ts:114-147 | `createServe` port 0 + onListen + portFile | the fix mechanism |

### Other org repos

| id | file:line | what | fix |
|---|---|---|---|
| O-1 | socialweb-computer-ssh/hono-socialweb-computer-ssh/cli-args-env.json | ssh 2222, http 8787, `state-dir` cwd-relative, `./port-bound-to` | 0 + per-task dir in tests |
| O-2 | did-key-associator/hono-backend/cli-args-env.json | port 5557, `./port-bound-to` | same |
| O-3 | policy-engine/hono-policy-engine/cli-args-env.json | port 8080, `./port-bound-to` | same |
| O-4 | digitalocean-bidder/lib/digitalocean-oauth/test/mod_test.ts:22 | `localhost:9999` redirect string; servers port 0 | safe |

### Org-root dev scripts

| id | file:line | what | fix |
|---|---|---|---|
| R-1 | alice-request.sh:4, bob-bidder.sh:26 | `ORG_ROOT` hardcoded | `${ORG_ROOT:-...}` |
| R-2 | alice-request.sh:7, bob-bidder.sh:32 | OAuth session `~/.cache/pdr-market/{alice-request,bob-bidder}-oauth-session.json` | real-account scripts: one person, one session; never run from parallel tasks |
| R-3 | bob-bidder.sh:44-45 | runner `:local`, `FC_WORK_ROOT=/var/lib/socialweb/guests` | B13/B14 |
| R-4 | bob-bidder.sh:31,83 | `SERVE_PORT=0` (good) but port-file lands in `atproto-market/port-bound-to` | `PORT_FILE` per run |

## 6. Host-global resources that cannot be per task, and a slot limit

hat-8 readings (2026-10-11): 16 CPUs, 46 GiB RAM (39 GiB available), 960 GiB free on `/`,
`ip_local_port_range 32768 60999`, `fs.inotify.max_user_instances 128`, `/dev/kvm` mode 0666 (group kvm),
40 liveports lock files present.

Shared and not partitionable:

- `/dev/kvm` and CPU: no exclusive use, but contention slows guest boot past VM_READY timeouts.
- RAM: guests default `mem_mib 2048` (socialweb-nodeboot/main.go:57); a bed's control processes (kcp,
  kine, bao, provider, ~7 deno services, requester) are about 2-3 GiB (estimate; measure).
- Disk: guest disks in work roots, nodeimage store (content-addressed, flocked), docker image store.
- dockerd and the default bridge (one gateway, ~65k addresses), pasta, `/dev/net/tun`.
- Kernel ephemeral port range (all port-0 binds draw from it; leases must stay outside it).
- inotify instances (128 per user): kcp/k3s/kine and file watchers; N beds may hit it - measure.
- Shared caches: `~/.cache/deno`, Go build/mod cache (safe, internally locked), `~/.kube/cache`
  (keyed by server host:port; benign).
- `/var/tmp/socialweb-liveports` (coordination point, keep).

Slot limit (simple, reuses the liveports pattern):

- `SWC_BED_SLOTS` (default 4 on hat-8). A bed takes `flock(LOCK_EX|LOCK_NB)` on
  `/var/tmp/socialweb-bedslots/<i>.lock` for i in 0..SLOTS-1 (start at pid % SLOTS, as
  liveports.TakeForProcess does), holds it for its lifetime, writes `BED_ID` + pid into it for
  diagnosis, and refuses or waits when none is free. In Go: a `liveports.Block{What: "the market bed",
  Slots: 4, Width: 0}`; in shell: `exec 9>"$lock"; flock -n 9`.
- Memory gate before taking a slot: refuse when `MemAvailable < 3 GiB + BED_GUESTS x 2.5 GiB` (2 GiB
  guest + VMM/pasta/runner overhead), as plan 0008 M1-BED-PARALLEL's risk note already asks. With 2
  guests per bed that is 8 GiB/bed: 4 beds = 32 GiB of 39 available.
- Guest cap per bed: the bidder's `--policy` / a max-concurrent-guests option, so one bed cannot take
  every slot's memory.
