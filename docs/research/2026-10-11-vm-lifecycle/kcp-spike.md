# M0-KCP-SPIKE results (plan 0006), 2026-10-11

Branch `kcp-spike` (from main 8aa3ef1), host hat-8 (16 cores, 46 GiB, Fedora, go 1.26.6). Stand-in
processes only (kcp, kine, postgres, socat on 127.0.0.1); no guest, no container. Another agent's kcp
blue/green proof (kcps on 16449/16450, kine on postgres :25432) was running on the same host during every
reading, and the route A build overlapped the slot run (B2), so absolute timings carry that noise.

The user decided the route on 2026-10-12, while the spike was running: kcp runs **out of process** as
alternating blue/green slot units on kine -> a dedicated postgres, one kine per slot,
`--enable-leader-election`, `--shard-base-url` at our own L4 front on 127.0.0.1:6443, and the lease
handover is a delete of `kube-system/kcp-controllers` after the old slot exits. Route A was then capped
at the readings below and kept as a record. Route B was steered to three questions: port read-back,
shared root material, and the release binary as an embedded asset.

## Reproduce

| script | what |
|---|---|
| `scripts/kcp-spike/vendor-kcp.sh` | clones kcp v0.33.0 (commit 1a4cb725, checked) with `staging/` into `third_party/kcp` and copies embeddedetcd 2cc5c5cce35e into `third_party/embeddedetcd`. Both are gitignored on this branch; run this script first. `--print-replaces [prefix]` prints the replace block (kcp's 33 `k8s.io/*` lines verbatim + kcp, the five kcp-dev staging modules, embeddedetcd) |
| `scripts/kcp-spike/route-a-build.sh <out>` | go build / vet / GOOS=darwin,windows build / go test -json / binary sizes on the fork |
| `scripts/kcp-spike/fork-siblings.sh <scratch> <org-root>` | deno-kcp and kcp-libs built and tested on the fork through a scratch go.work (those repos untouched) |
| `scripts/kcp-spike/route-a-inproc.sh <bin> <work>` | runs `cmd/socialweb-kcp-spike` (kcp in-process, the cmd/kcp/kcp.go:95-162 sequence) |
| `scripts/kcp-spike/route-b.sh <work> [runs]` | release kcp + kine (sqlite, postgres on a unix socket), port 0, cold start / restart / RSS |
| `scripts/kcp-spike/route-b-slots.sh <work>` | two slots on one postgres, one kine each, leader election, shard base URL at a socat front: port read-back, shared material, lease handover |

## Route A: in-process on kcp's fork (record only)

The go.mod on this branch carries the full replace set; `go mod tidy` resolved it with no manual edits
(go.mod +148 lines, go.sum +269/-32). `github.com/kcp-dev/sdk` moves from v0.33.1 to kcp's staging sdk.

| reading | main 8aa3ef1 | fork |
|---|---|---|
| `go build ./...` (linux) | ok | **ok, 0 compile errors in 0 packages** (37 s cold) |
| `go vet ./...` | ok | ok |
| `GOOS=darwin go build ./...` | fails: processrunner, portallocator, subnetallocator (undefined hostos.*) | identical (pre-existing, M0-XOS-GATE) |
| `GOOS=windows go build ./...` | fails: nodevm | identical |
| `go test ./... -count=1` (offline) | 60 pkgs: 54 ok, 6 no-test/skip; 1438 pass, 0 fail, 64 skip | 61 pkgs (+ the spike cmd): 1438 pass, **0 fork-caused failures**. The one failure seen, `test.TestNoCommentsAndASCII`, was the spike file's own header comment, removed. One skip-vs-pass difference (`marketbed.TestTheRecordMatchesTheDenoKcpCommitItNames`) is the baseline worktree's location, not the fork |
| deno-kcp 25d10f92 | - | build ok, 10/10 test pkgs ok |
| kcp-libs f6911d2 | - | build ok, 37/37 test pkgs ok |
| socialweb-gateway binary | 26,823,317 B | 26,841,109 B (+17.8 KB) |
| socialweb-clusterprovider binary | 40,753,165 B | 40,787,767 B (+34.6 KB) |
| in-process kcp (`cmd/socialweb-kcp-spike`) | - | 177,440,498 B (release kcp: 176,825,356 B) |

In-process run (kine sqlite, port 0 via `--experimental-bind-free-port`, `SecureServing.Listener.Addr()`
read directly, 0.23-0.34 s after start):

| reading | value |
|---|---|
| cold start to /readyz | 8.9 s; RSS 415 MB at ready, 417 MB settled; 3,639 goroutines |
| ctx cancel -> `Run` returns | 8 ms (first), 1.1 s (later stops); secure port freed; goroutines 3,632 -> 181 left behind |
| second start in the same process, same root | ready 7.0 s; RSS 608 MB (first heap not returned) |
| two kcps in one process (two kine stores) | both ready (9.0 s, 9.3 s), distinct ports, RSS 656 MB, both stop in ~1.1 s, 359 goroutines left. Only error: `logging configuration was already applied earlier, changing it is not allowed` (klog/logs registry is process-global; the second kcp silently inherits the first's logging config). Feature-gate and Prometheus registries are shared without a panic; metric cross-talk was not checked |

Not measured (capped): client-go 1.37.0-alpha apply paths against a live k3s 1.36 tenant (the offline
suites of tenantclient, eso and clusterprovider pass); RSS on a prod-shaped root with gateway and
clusterprovider running in the same process; two embedded-etcd kcps (2379/2380 collide by construction).

Gate as written in M0-KCP-SPIKE: fork breakage fixed within a day - yes, there was none; go test green on
the fork - yes (offline suite); two in-process kcps - start, serve and stop. **The in-process route is not
blocked by breakage.** It is blocked by the 2026-10-12 decision: in-process ties kcp's lifetime to the core
process, so kcp blue/green would mean two whole core processes overlapping.

## Route B: release kcp v0.33.0 in its own process

Binary `/usr/local/bin/kcp` = `v1.36.0+kcp-v0.33.0`, sha256 0929a16bedf3275c..., same as
`/var/tmp/socialweb-control-plane-binaries/kcp`; static ELF; kine `/usr/local/bin/kine` (v0.17.1 build).
Flags: `--etcd-servers=<kine> --bind-address=127.0.0.1 --secure-port=0 --experimental-bind-free-port
--feature-gates=WorkspaceMounts=true` + today's audit flags. 3 runs per store, fresh root then restart
on the same root; RSS 30 s after ready.

| store / phase | port known (admin.kubeconfig / log line) | /readyz (min/med/max) | kcp RSS | kine RSS | SIGTERM -> exit |
|---|---|---|---|---|---|
| sqlite fresh | 0.38-1.78 s | 8.5 / 9.2 / 12.2 s | 382-396 MB | 57-59 MB | 1.1 s |
| sqlite restart | 0.27-0.37 s | 5.9 / 6.0 / 6.1 s | 378-404 MB | 62 MB | 0.05-1.2 s |
| postgres 18.6 fresh | 0.54-0.88 s | 8.7 / 8.8 / 8.9 s | 389-399 MB | 54-57 MB | 1.1 s |
| postgres restart | 0.27-0.40 s | 6.1 / 6.2 / 6.7 s | 392-409 MB | 60-61 MB | 1.1 s |

Each kcp holds exactly one TCP listener (the secure port) when the store is external. kine binds
`--listen-address 127.0.0.1:0` and logs `Kine available at http://127.0.0.1:<port>`. Postgres RSS was not
recorded (multi-process). kcp on postgres is within noise of sqlite.

### 1. How a port-0 slot reports its bound port (no kcp patch needed)

- Without `--shard-base-url` (today's prod flags), `--experimental-bind-free-port` is reported twice:
  `<root>/admin.kubeconfig` names `https://127.0.0.1:<bound>` (written 0.3-1.8 s after exec, long before
  /readyz), and the log line `secure_serving.go Serving securely on 127.0.0.1:<bound>`. The shard object's
  baseURL / virtualWorkspaceURL follow the bound port and are rewritten on every restart. Caveats: a stale
  admin.kubeconfig from the previous start sits there until rewritten, and `clientcmd.WriteToFile` is not
  an atomic rename.
- With the decided `--shard-base-url=https://127.0.0.1:<front>` (B2): admin.kubeconfig and the shard
  URLs name **the front**, never the bound port. What still reports the slot's own port: the log line
  (0.9-2.3 s) and the socket itself: the slot's MainPID has exactly one LISTEN socket, visible at
  0.17-0.30 s (`ss -ltnp` / `/proc/<pid>/net/tcp` matched against `/proc/<pid>/fd` socket inodes, same uid,
  no privilege). The listener is created in `Options.Complete` (options.go:355-361) before anything
  serves, so it is the right socket; readiness is then `/readyz` == 200 on that port.
- Cheapest: `--secure-port=0 --experimental-bind-free-port` + the core reading the unit MainPID's single
  LISTEN socket from /proc. Zero kcp patches; no port file. Fixed alternating slot ports A/B also need no
  patch and no read-back, but every concurrent kcp on a host (tests, `run request`, bidder-local kcp, a
  second core) then needs its own leased pair, which is the port-allocation problem port 0 removes.
  Keep fixed A/B only as the fallback if /proc read-back is rejected.
- Patches still needed on the decided shape: none. The embeddedetcd port-0 patch disappears with the
  external store; the port-file patch is replaced by the /proc read-back; ReleaseOnCancel is replaced by
  the lease delete (below).

### 2. Shared root material per slot (B2, measured)

Slot a fresh; slot b got copies of a's files; slot c got all but admin.kubeconfig; slot d a fresh root.
All on one postgres database, one kine each.

| probe | result |
|---|---|
| b (copied): a's shard-admin token, `/clusters/root/api/v1/namespaces` | 200 |
| b (copied): SA token minted by a | 403 (authenticated; same 403 as on a, no RBAC grant) |
| b (copied): a's kcp-admin token (`root`/`base` contexts) | 401: volatile, regenerated per process (authentication.go:109) |
| b rewrote admin.kubeconfig | shard-admin token unchanged, server = front, new kcp-admin token |
| c (crt, key, sa.key, .admin-token-store, no admin.kubeconfig) | **refuses to start**: `cannot create the 'admin.kubeconfig' file with an empty token for the shard-admin user` |
| d (fresh root): a's shard-admin token / a's SA token | 401 / 401 |
| d with a's CA | TLS failure (new self-signed apiserver.crt) |

A new slot needs `apiserver.crt apiserver.key sa.key .admin-token-store admin.kubeconfig` copied in
before start; admin.kubeconfig is mandatory whenever .admin-token-store is present (kcp reads the
shard-admin token back from it). The core keeps them in `<state>/kcp/shared/`, copies into the new slot
root before starting the unit, and consumers use only the `system:admin`/`shard-base` contexts
(shard-admin, persistent) against the front URL.

Handover (B2, under build load): a's SIGTERM -> exit 3.1 s (`--shutdown-delay-duration=2s`); lease
`kube-system/kcp-controllers` in `system:admin` deleted with b's shard-admin context through the front;
b held it 0.46 s later; `serviceaccount/spike` read through the retargeted front. Slot cold start
under that load: a 11.2 s, b 7.3 s (b on an existing store), RSS 357-402 MB.

### 3. Release kcp as an embedded asset (linux/amd64)

| form | bytes | decompress |
|---|---|---|
| raw static ELF | 176,825,356 | - |
| gzip -9 | 70,054,216 | - |
| zstd -19 --long=27 | 59,189,153 | 0.15 s |
| xz -9e | 57,065,516 | 0.80 s |

Embedded zstd adds ~59 MB to the core binary; extracted once into the content-addressed asset store and
exec'd from there. Cold start and RSS are the route B table: ~9 s fresh / ~6 s restart to /readyz,
~380-410 MB kcp + ~55-62 MB per kine.

## Recommendation

Out-of-process, as decided: release kcp v0.33.0 shipped as a zstd asset (~59 MB), started as blue/green
slot units with `--secure-port=0 --experimental-bind-free-port`, the core reading the bound port from the
slot MainPID's single LISTEN socket, shared material copied from `<state>/kcp/shared/` (all five files),
lease delete after the old slot exits. No kcp patch and no vendored kcp source are required on this
shape. Route A is recorded as feasible (0 compile errors, 0 test regressions, two kcps in one process),
and not chosen because it cannot do kcp blue/green without overlapping whole core processes.
