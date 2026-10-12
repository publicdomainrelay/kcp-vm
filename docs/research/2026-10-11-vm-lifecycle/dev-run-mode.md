# Dev run mode: the whole stack without systemd, N instances per host

Research + design, 2026-10-12 (session clock 2026-10-11). Answers the user's request of 2026-10-12:

> we should maintain a way of running all this without systemd involved for testing / lighter weight so
> that we can run parallel dev flows in worktrees etc. and not need to re-install on the system every
> time. systemd units are for production deployments / installs ... we like kcp because it could act like
> systemd in a way and we can nest them infinitely to orchestrate anything we need.

Plus the standing rule from the same day: production setup is the explicit root step
`socialweb-computer install bidder|gateway`; unix accounts are never created on demand.

Tags: [C] code read at main d6e1881 (file:line), [P] plan text, [R] research doc, [W] web source, [M] measured
here on hat-8 (Fedora 44, kernel 7.2.9, go 1.26.2) with stand-in processes only (`/bin/sleep`, `/bin/sh`,
a 130-line Go program), scratch dir deleted afterwards. No guest, no container, no kcp was started.

---------------------------------------------------------------------------------------------------

## 0. Answer in one screen

1. **Add a second `hostsvc.Manager` backend, `proc`, and make it the dev and test backend.** It is not an
   orphan-adoption scheme: it is a tiny per-instance supervisor process, `socialweb-computer internal
   devsup`, that is the real parent of every unit (so it reads exit statuses, like systemd does), is a
   child subreaper (so it catches descendants that change process group), puts each unit in its own
   process group, sets `Pdeathsig=SIGKILL` on each unit's main process, and serves the `Manager` calls
   over a unix socket. The core (`serve gateway kcp`, `run bidder`, `run request`) talks to it exactly as
   it talks to the user manager over `$XDG_RUNTIME_DIR/systemd/private`. Prior art: execrunner's
   Setpgid + state.json + pid/start-tick identity (kcp-libs supervisor.go:141, :20-28, :394), s6's
   one stable parent per daemon [W s6], and our own hostsvc identity code (identity_linux.go:47-71).
2. **Dev survival semantics (picked):** units survive a restart, crash or rebuild of the CORE (it is only
   a client of devsup, so adoption is the same code path as prod); every unit dies with DEVSUP (`dev
   down`, crash, `kill -9`, test process exit). That is exactly prod's shape, where units survive the core
   and die with the user manager (0006 M2-HOST-UNITS risks, plan 0006:485). [M] section 6.
3. **kcp is the API, not the supervisor.** Something that is not kcp must hold kcp's own process and its
   store (systemd in prod, devsup in dev). Above that root, long-lived host processes become kcp objects
   (the plans already have `Postgres`, `OpenBaoServer`, `ComputeVM`, DenoPod) whose `status.runtime.backend`
   names the placement backend. A generalized `HostProcess` kind is phase 2, after the backend and its
   contract suite exist.
4. **One kcp per instance** (process isolation; port 0 for everything, front included), with every role
   addressing its workspaces relative to a configurable base path (`root` in prod, `root:dev:<x>` or
   `root:t:<rand>` where a parent kcp is shared). A host-level "dev parent" kcp holding each instance's
   `HostProcess` objects (kcp inside kcp) is phase 3 and optional.
5. **Backend is an explicit option, no default, never auto-detected:** `host-backend = systemd-user | proc`.
   `install` writes `systemd-user` into the account's config.json; every `dev` verb and every test harness
   passes `proc`. A unit's status always records the backend that placed it.
6. **One contract suite, two backends.** `internal/hostsvc/hostsvctest.Run(t, open)` is run against `proc`
   on every `check.sh --fast` (no root, no systemd, CI-able) and against `systemd-user` behind livegate on
   hat-8; differences are declared in `Capabilities()` and each capability-gated case states which backend
   skips it. A reflection test fails if a `Manager` method has no contract case.
7. **`systemd-run --user` (option c) is not the dev mode.** It is already what the systemd backend does in
   the login session's manager (the M2-HOST-UNITS live tests ran there, plan 0006:493); it stays the
   prod-fidelity check, with an instance-scoped prefix, not the default.
8. **s6 / runit / supervisord / process-compose: rejected** as runtime dependencies (one-binary rule,
   no pid+start-tick identity, no adoption contract); ideas borrowed (readiness/depends_on from
   process-compose, one-parent-per-daemon from s6, supervisord's grandchild problem as a test case).

---------------------------------------------------------------------------------------------------

## 1. What exists today [C][P]

| Piece | Where | Relevance to dev mode |
|---|---|---|
| `hostsvc.Manager {Start, Inspect, Stop, ResetFailed, List, Identify, Check, Close}` | internal/hostsvc/hostsvc.go:126-135 | The seam. One implementation: systemd user manager over the private socket (systemd_linux.go:40-44, busdial_linux.go:42-48). `unsupported_other.go` for !linux. |
| `UnitSpec` (Exec, Env, WorkDir, Output files, Limits, OOMPolicy, KillSignal, StopTimeout, After, Wants, Slice) | hostsvc.go:52-66 | Everything a proc backend must honour or declare unenforced. |
| Identity = unit + InvocationID + PID + StartTicks + BootID | hostsvc.go:110-116; identity_linux.go:47-71 (pidfd_open -> /proc stat ticks -> pidfd_send_signal(0)) | Backend-neutral already except `GetUnitByPIDFD` (systemd_linux.go:375). |
| Name rule `socialweb-` prefix + `.service`; app.slice refused | hostsvc.go:12-16, :137-146, :165-167 | Proc backend keeps the same names (namespaced by its own devsup, so no cross-instance clash). |
| Unit properties: CollectMode=inactive, KillMode=control-group, PropExecStart(argv,false) | systemd_linux.go:132-155 | Semantics the proc backend must emulate: failed units stay readable until ResetFailed; stop kills the whole tree. |
| processrunner `DieWithParent` (Pdeathsig SIGKILL from a locked OS thread) | processrunner.go:120-127, :138-147; hostos/sysproc_linux.go:10-15 | Pdeathsig fires when the creating THREAD exits, not the process [W pdeathsig]; the existing locked-thread trick is reused in devsup. |
| procgroup (per-cluster processes die with the provider) | procgroup.go:104 | Being replaced by unit slices (0006 M3-CLUSTER-UNITS, plan 0006:760-817). |
| execrunner (DenoPods): Setpgid, state.json {pid, started, ticks, stopped}, recover, ownedByUs(pid, ticks), kill(-pgid) | kcp-libs impl/execrunner/supervisor.go:20-28, :141, :249, :308-322, :394 | The adoption-by-ticks pattern; note it has no Pdeathsig and its children are in their own groups, so they outlive a SIGKILLed parent unless something else kills them. |
| DenoPod pure reconciler: Observed -> Result{Phase, Ops} | deno-kcp internal/denopod/denopod.go:105-140; provider calls PodRunner Start/Observe/Stop/Probe (reconcile_pod.go:21-60; kcp-libs abc/runner/runner.go:18-63) | The shape a generalized `HostProcess` reconciler copies: pure state machine + a runner port. |
| kcp for local dev: kine on sqlite + kcp, fixed 23791 and 6443, pid files, stale-port refusal | deno-kcp deploy/start-kcp.sh:4-6, :57, :74 | Fixed ports: one per host. Dev mode replaces it with port 0 (kcp-spike.md:82-103 shows the bound port is readable with no kcp patch). |
| Port leases: carve 20000-29999, one lease file, key (cluster, purpose); Lease already has Instance and Account fields | portallocator.go:18-19, :99-109, :145-148, :169-172, :346-357 | Two instances with two lease files can lease the same not-yet-bound port (probe-then-bind race). Must key by instance and share one per-user lease file in dev. |
| Live-test port blocks locked under /var/tmp/socialweb-liveports | liveports.go:67, :92-118 | Already parallel-safe for proofs; unchanged. |
| Market bed on fixed 2583-2589, accept.sh | internal/marketbed/live_test.go:18, :147; 0008 M4-TESTBED-PORT ("a second bed on the host is refused by a lock file", plan 0008:287-289) | The one thing that forbids N beds per host. |

Production decisions that dev mode must not undercut [P][R]: units in the USER manager of a lingering
account (user-decisions.md:7); one shared bidder account (:10); kcp out of process as blue/green slots on
kine -> postgres behind our own L4 front (:22; kcp-spike.md:144-151); AppArmor profile at install on
Ubuntu 24.04+ (:13); no socialweb code in a VM's process tree (0006 M2-VM-TREE-AUDIT); deno has no
access to `/dev/kvm` or `$XDG_RUNTIME_DIR` (0007 M5-RUN-BIDDER, plan 0007:612-614); RFP flow is the
spine (0006 state rule, plan 0006:74-78).

---------------------------------------------------------------------------------------------------

## 2. Requirements for dev mode

R1. No root, no `install`, no systemd, no lingering account, no unit files. Runs as the invoking user.
    `dev` refuses to run as uid 0 (postgres refuses root anyway, plan 0006:97).

R2. N instances on one host from N worktrees (or N instances from one worktree with `--instance`), with
    no collision on:
    - ports: port 0 everywhere a port is not part of a durable contract (kcp slots, kcp front, kine,
      postgres, bao, door, PDS/relay/bed services, k3s slot ports, front ports). Where a port must be
      stable across restarts inside an instance (a cluster's front port: guest tickets and node secrets
      name it, 0006 M3-APISERVER-FRONT), it is leased from ONE per-user lease file keyed by
      (instance, cluster, purpose), so instances see each other's leases;
    - paths: state `$XDG_STATE_HOME/socialweb-computer/dev/<id>/` (default `~/.local/state`, XDG spec
      [W xdg]); sockets `$XDG_RUNTIME_DIR/swc/<id8>/` (short, < 108 bytes; 0700 by spec [W xdg]); a
      gitignored symlink `<worktree>/.swc-dev -> <state>` for discovery. Not inside the worktree itself:
      long worktree paths break the 108-byte socket limit, `git clean -fdx` would delete live state, and
      `git worktree remove` refuses untracked files;
    - names: unit names are per devsup, so prod names (`socialweb-vm-<hid>-...`) are reused unchanged;
      kcp workspaces are per kcp process;
    - assets: content-addressed, so one shared per-user cache `$XDG_CACHE_HOME/socialweb-computer/runtime/
      <sha256>/` is safe across instances (re-hash before use, 0006 M1-ASSETS) and avoids extracting
      ~400 MB of kcp/k3s/bao/deno per worktree.
    `<id>` = `<worktree basename, 16 chars>-<first 8 hex of sha256(realpath(worktree))>`, or
    `--instance <name>`.

R3. Fast: `dev up` on a warm instance (kcp root and store exist) is bounded by kcp's restart-to-readyz,
    ~6 s on kine (kcp-spike.md:73-76); `dev down` stops everything in reverse order within the
    largest TimeoutStopSec (10 s for VMs, 0007 M2-VM-RUNTIME). Core rebuild + restart does not restart
    kcp, postgres, cluster members or guests.

R4. Usable from `go test` (hermetic: a crashed test leaves nothing running) and from live market-bed
    runs (RFP flow: requester -> RFP -> bid -> accept -> cloud-init user_data; guests reached only through
    the relay). Dev mode adds no provisioning path of its own: a bed instance runs `run bidder` with the
    proc backend and its guests come up only through `runComputeContract`.

R5. Same code paths as prod above the `Manager` seam: the ComputeVM verdict table (0007 M2-CONTROLLER),
    cluster member adoption (0006 M3-CLUSTER-UNITS), kcp slot swaps (M3-CP-SLOTS) and vault unseal on a
    new InvocationID (M2-OPENBAO-SERVER) run unchanged on `proc`.

R6. Survival semantics (picked; alternatives in section 4.1):
    - core restart / crash / rebuild: every unit survives and is adopted (Running, same InvocationID);
    - devsup exit (`dev down`, crash, `kill -9`, `--die-with` process exit): every unit dies; the next
      `dev up` finds every recorded identity Lost(UnitGone), exactly as prod after a user-manager restart
      or reboot (plan 0006:485);
    - host reboot: Lost(HostRebooted) by boot_id, same as prod.

R7. Honest about what dev does not give: no cgroup limits (MemoryMax, MemorySwapMax, TasksMax, CPUQuota,
    CPUWeight, OOMPolicy are accepted and reported unenforced), no systemd-oomd, no journal. A VM's memory
    is still bounded by firecracker's own `machine-config` mem size.

R8. Host prerequisites dev needs but never performs (reported by `dev check`, never fixed by it): `/dev/kvm`
    rw for the user (kvm group, already true on a dev box or not - dev never chmods), unprivileged user
    namespaces for pasta (on Ubuntu 24.04+ the AppArmor restriction needs a root-installed profile or the
    sysctl; Fedora has neither restriction), `/dev/net/tun`.

---------------------------------------------------------------------------------------------------

## 3. Options

### (a) A second hostsvc backend, `proc`

Two sub-shapes:

**(a1) Orphan adoption (execrunner shape).** The core starts each unit detached (setsid, no Pdeathsig),
writes state.json {pid, pgid, ticks, bootID}; a restarted core re-reads and adopts by pid + start ticks.

- Pro: no extra process; execrunner and hostsvc identity code already do the identity half.
- Con [M, section 6]: an adopter is not the parent, so `waitid(P_PIDFD, ...)` on an adopted pidfd returns
  ECHILD; the exit status of a unit that dies while adopted is lost. That breaks the verdict rows that need
  Result/ExecMainStatus (Lost(UnitFailed | OOMKilled | VMMExited), CollectMode=inactive semantics,
  plan 0007:359-361) unless a wrapper process writes it - and a wrapper is our code in the VM's tree
  (forbidden, 0006 M2-VM-TREE-AUDIT).
- Con: nothing kills the tree on `dev down` if the core is already dead; "everything dies with the dev
  session" needs a sweep by recorded pgid, which is only safe with the leader-ticks rule (section 6).
- Con: Restart=on-failure needs someone to notice an exit promptly; the core may be down.

**(a2) A stable per-instance supervisor, `devsup` (recommended).** `socialweb-computer internal devsup
--state <state>/sup --socket <rt>/sup.sock [--die-with <pid>]`.

- It is the parent: it reads every unit's exit via wait4, so Result / ExecMainCode / ExecMainStatus /
  CollectMode=inactive / ResetFailed / Restart=on-failure|always behave like systemd's.
- It calls `prctl(PR_SET_CHILD_SUBREAPER)`: any descendant that leaves its unit's process group is
  reparented to devsup when its parent dies, instead of to PID 1 [W subreaper]; devsup attributes it by
  the inherited `SWC_UNIT=<name>` environment variable and kills it if that unit is not active (the
  KillMode=control-group emulation). [M] shows the orphan otherwise lands on PID 1.
- Each unit: own process group (`Setpgid`), `Pdeathsig=SIGKILL` set from a locked OS thread (the
  processrunner.go:138-147 trick; Pdeathsig is per creating thread [W pdeathsig]), stdout/stderr to the
  `Output` append files, `Env`, `WorkDir`, an InvocationID (128-bit random per start).
- Stop = `KillSignal` to `-pgid` and to every attributed descendant (snapshot from
  `/proc/<pid>/task/*/children` before signalling), wait `StopTimeout`, then SIGKILL the same set.
- Identity/Check: the same Pin sequence (identity_linux.go:47-71); `GetUnitByPIDFD` is replaced by
  "pid's pgid == unit pgid, or pid attributed to the unit" answered by devsup.
- Slices: `Slice` is a label; `StopSlice(prefix)` stops every unit whose slice equals or nests under it
  (systemd slice names nest by dash, hostsvc.go:16; 0006 M3-CLUSTER-UNITS escapes cluster keys).
- Ordering: `After`/`Wants` are validated (named unit exists in this devsup) and otherwise advisory, as for
  transient units started one by one; the callers already start in order (M3-CLUSTER-UNITS).
- Watch: devsup pushes unit state changes on the socket stream (the systemd backend uses D-Bus
  PropertiesChanged; 0007 M2-VM-RUNTIME needs Watch, plan 0007:297).
- fd store: devsup holds a listening fd a unit hands it and passes it back to the next start of that unit
  with the `LISTEN_FDS` convention, so `M3-APISERVER-FRONT`'s "a front restart refuses no connection"
  (plan 0006:834-836) also holds in dev. Optional: `Capabilities().FDStore` gates it.
- Private hostname (k3s slot lease identity, plan 0006:884-888): the `internal uts-exec` one-shot
  (user+UTS namespace, sethostname, execve) that M3-CP-SLOTS already names as the fallback becomes the
  proc backend's implementation of a `PrivateHostname` field; no resident code.
- State on disk: `<state>/sup/units/<name>.json` {spec, InvocationID, pid, pgid, leader ticks, bootID,
  devsup instance, last Result}; `dev status` reads it without the socket. After a devsup crash, the next
  devsup sweeps leftovers by the safe pgid rule (section 6) before accepting calls.
- Size: comparable to execrunner's supervisor.go (425 lines) plus a net/rpc-over-unix-socket server.
- Pro: exact exit semantics; tree kill; same failure model as prod; one devsup per instance = isolation.
- Con: a resident process of ours that is the parent of pasta in dev. It is NOT in the unit's process
  group, exactly as systemd is not in a unit's cgroup, so the VM tree audit (process group members +
  attributed descendants = pasta, firecracker only) still holds; the audit becomes backend-aware.
- Cross-OS: the same contract maps to a Windows Job Object hierarchy (KILL_ON_JOB_CLOSE, nested jobs) and
  to darwin's kqueue NOTE_EXIT + process groups; proc_linux.go first, stubs return ErrUnsupported until
  plans 0011/0012 decide (darwin has no Pdeathsig, hostos/sysproc_darwin.go:10).

### (b) kcp-native: host processes are kcp objects; systemd is one placement

The user's framing: kcp "acts like systemd" and nests. What it buys and what it cannot do:

- Already true in the plans: `Postgres` and `OpenBaoServer` (export hostprocesses, status.runtime
  {backend, ref, instance, pid, startTime, hostBootID}, plan 0006:618-621, :664-667), `ComputeVM`
  (RuntimeRef {backend systemd-firecracker, ...}, plan 0007:281-283), DenoPod (execrunner). Every one of
  them is "a kcp object reconciled into a host process". The backend field is already in their status.
- Generalize: a `HostProcess` kind (spec: asset ref + argv, env, workDir, output, limits, restartPolicy,
  stop signal + grace, after/wants, slice, privateHostname, readiness probe; status: phase, runtime
  {backend, unit, invocationID, pid, startTicks, hostBootID}, lastResult). A pure reconciler in the
  denopod.go:105-140 style (Observed -> Result{Ops}) and a placement port that IS `hostsvc.Manager`.
  Postgres/OpenBaoServer/kcp-slot/cluster-member controllers either own a HostProcess child or call the
  same library; ComputeVM keeps its own controller (its verdict table is richer) but places through the
  same Manager.
- Cannot replace (a): somebody must run kcp, its kine and its postgres before any object can be read,
  and must hold processes while kcp or the core is down (the whole point of units outliving the core). A
  reconcile loop is not a process parent: it does not reap, kill trees, hold fds or restart within
  milliseconds. So the root of every tree is imperative - systemd in prod, devsup in dev - and the core
  "mirrors" what it placed into kcp after kcp is up, the way the kubelet creates a mirror Pod in the API
  server for each static Pod and keeps running it even if the mirror is deleted [W static pods].
- Nesting: a `HostProcess` whose argv is another core instance (`socialweb-computer dev core ...`) with its
  own kcp is "kcp inside kcp": the parent's reconciler places the child's processes; the child's kcp holds
  the child's own objects. Recursion depth is bounded only by ports (all 0) and memory (~0.4-0.5 GB per
  kcp + kine, kcp-spike.md:73-76).
- Pro: one model, `kubectl get hostprocesses` per instance, status/history in objects, the same apply
  path (0006 M2-APPLY), placement swappable per object.
- Con: more API surface before dev mode exists; HostProcess schema churn while M3 items are still
  being designed; kcp outage = no reconcile (units keep running, acceptable).
- Verdict: the direction, staged after (a). Podman Quadlet is the inverse prior art (declarative specs
  compiled to systemd units, [W quadlet]); we keep the declaration in kcp and the placement pluggable.

### (c) `systemd-run --user` transient units, no install

- What it is: the existing systemd backend pointed at the login session's user manager. Transient
  services are real units, listed by `systemctl --user list-units`, and failed ones stay loaded until
  reset-failed unless `--collect` [W systemd-run]. The M2-HOST-UNITS live tests already ran this way
  (plan 0006:493, "this host has Linger=no so the runs used the login session's user manager").
- Pro: identical semantics to prod, cgroup limits enforced (where delegated), no new code.
- Con: still systemd - absent in CI containers, WSL without systemd, macOS, SSH sessions without
  pam_systemd; every instance shares ONE user manager, so names must carry the instance
  (`socialweb-dev-<id8>-...`) or worktrees adopt each other's units; leaks outlive a crashed `go test`
  until GC; without linger units die at the last logout; units land wherever the slice says, and
  Fedora's user app.slice is under systemd-oomd kill (plan 0006:469-470).
- Verdict: keep as `host-backend=systemd-user` with `--unit-prefix socialweb-dev-<id8>-` for
  prod-fidelity runs on hat-8 and for the systemd half of the contract suite. Not the dev default.

### (d) s6 / runit / supervisord / process-compose

- process-compose: Go, docker-compose-like YAML for non-containerized processes, depends_on conditions,
  readiness/liveness probes, restart policies [W process-compose, process-compose health]. An app, not a
  library; another config language beside cli-args-env/options.json; no pid+start-tick identity or
  InvocationID; no "adopt, never re-create". Borrow: readiness gating and depends_on conditions.
- s6: one long-lived `s6-supervise` parent per daemon, `s6-svscan` supervising the supervisors [W s6].
  This is the devsup shape; we take the idea, not the C toolchain.
- supervisord: Python; cannot kill grandchildren of the process it spawned and needs `pidproxy` for
  daemons that hand signals to a child [W supervisord]. Exactly the hazard the subreaper + tree kill
  fixes; becomes a contract test.
- runit: same class as s6, C, service dirs.
- Verdict: rejected as runtime dependencies (ACC6-ONE-BINARY: one binary plus its own assets).

---------------------------------------------------------------------------------------------------

## 4. Design details

### 4.1 Survival semantics: why "die with devsup, survive the core"

| Choice | Core rebuild cost | go test hygiene | Prod parity |
|---|---|---|---|
| Everything dies with the dev process (one process, Pdeathsig + killpg) | kcp ~6 s + postgres + cluster members + every guest re-rented through a new RFP | best | none: adoption code never runs in dev |
| Units adopted by orphan ticks (a1) | none | needs sweeps; leaks on crash | partial: exit status lost after adoption |
| **devsup parent (a2)** | none | `--die-with <test pid>`: test exit kills all | full: same verdicts, same failure model (manager death = every unit Lost) |

`dev up` runs devsup detached (`setsid`, survives closing the terminal; logind's KillUserProcesses is a
host setting, `dev check` reports it). `dev up --fg` keeps devsup in the foreground and multiplexes unit
logs; Ctrl-C = `dev down`.

### 4.2 VMs in dev (pasta -> firecracker, no systemd)

Unchanged prep (0007 M2-VM-RUNTIME, plan 0007:263-281): clone, seed drive, fc.json, params.json; work dir
`<state>/handles/<hid>/vms/<name>`; API socket under `<rt>/swvm/<short>.sock`. The unit spec is the same
`UnitSpec`; on proc: ExecStart pasta -> `/bin/sh` trampoline -> socialweb-vmexec -> execve firecracker;
devsup is pasta's parent; pasta, sh->vmexec->firecracker share the unit's process group unless one of
them calls setsid, in which case the subreaper attribution catches it. Stop: SendCtrlAltDel grace in the
controller (unchanged), then StopUnit = SIGKILL to the group + attributed descendants. Not enforced in dev:
MemoryMax/MemorySwapMax/TasksMax/CPUQuota (declared by `Capabilities().EnforcesLimits=false`; the VM's RAM
is still capped by firecracker's machine-config). Still required: `/dev/kvm` access (kvm group), userns for
pasta (Fedora: yes; Ubuntu 24.04+: needs the root-installed AppArmor profile or sysctl - dev reports, never
fixes). Guests exist only through `runComputeContract` against the instance's bidder.

### 4.3 kcp slots, fronts and clusters in dev

- kcp: blue/green slot units `socialweb-kcp@a|b` (prod shape, user-decisions.md:22) on kine. Store in dev:
  kine -> sqlite in the instance dir by default (kcp-spike.md:73-76: within noise of postgres), kine ->
  a postgres unit with `--profile prodlike` and in the bed profile. Slots bind `--secure-port=0
  --experimental-bind-free-port`; the core reads the slot's single LISTEN socket from /proc (kcp-spike.md
  :96-100, no kcp patch). The kcp front binds port 0 in dev (prod keeps 127.0.0.1:6443 behind caddy) and
  reports its port through the same OnListen callback; `--shard-base-url` names the front's bound port,
  so the front must bind BEFORE the first slot starts (it does: front first, then slot a).
- Default dev runs ONE slot; `dev roll kcp` exercises the swap (start b -> own /readyz -> front flip ->
  stop a -> delete kube-system/kcp-controllers lease, kcp-blue-green.md:17-64).
- Tenant clusters: member units in slice `socialweb-cluster-<key>.slice` (plan 0006:771-774), front on a
  lease from the shared per-user lease file, k3s slots on leases or 0, `controlplane.json` under the
  instance's data dir. k3s `--disable-agent` still needs `/etc/rancher/node/password` (plan 0006:97); in
  dev the same `internal ns-exec` one-shot that gives slots a private hostname also bind-mounts
  `<state>/rancher` over `/etc/rancher` in a user+mount namespace, so dev needs no root-owned file (see Q6).
- Name/slice mapping: identical names on both backends (`socialweb-c-<key>-<member>.service`,
  `socialweb-pg-<key>.service`, `socialweb-vm-<hid>-<name>.service`, `socialweb-kcp@<slot>`); the proc
  backend's namespace is its devsup, the systemd backend's is the user manager (+ the dev prefix when
  run in a login manager, option c).

### 4.4 Nesting: one kcp per instance, base-path-relative everywhere

Options weighed:

- N1, one kcp per instance (recommended as the instance boundary): crash and write-storm isolation
  (kcp-blue-green.md:34-38 measured ~120-150 conflicts/s/process in an un-elected overlap), each worktree
  may change APIResourceSchemas/APIExports/WorkspaceTypes/kcp flags or the kcp pin, the
  M3-SERVE-GATEWAY-KCP bring-up order (kcp first) and slot swaps are exercised. Cost ~0.4-0.5 GB + ~6-9 s
  cold start per kcp.
- N2, one shared kcp with `root:dev:<instance>:...`: cheap (workspace Ready in 0.3-0.7 s, kcp-blue-green
  .md:50), but kcp version, flags, feature gates and blast radius are shared; anything testing kcp itself
  cannot run there. Good for `go test` packages that only need "a kcp workspace": TestMain-scoped
  workspace `root:t:<pkg>-<rand>` on a shared test kcp, deleted at the end.
- N3, kcp inside kcp: a host-level dev parent kcp (`dev host`, one per user) whose workspace
  `root:dev:<instance>` holds the instance's HostProcess objects, including the instance's own kcp slot;
  the parent core places them on its devsup. Gives `kubectl get hostprocesses -A` over every worktree and
  is the same pattern prod can use for tenant clusters (members as objects in the cluster's home
  workspace). Phase 3.

Requirement that makes all three cheap: no role assumes it owns `root`. Every component takes
`kcp-base-workspace` (default `root`; prod unchanged) and creates/binds under it (`<base>:bidder`, the
gateway's provider workspaces, the bed's workspaces). kcp workspace paths nest
`/clusters/<parent>:<child>` and WorkspaceTypes constrain allowed parents/children
(limitAllowedParents/limitAllowedChildren) [W kcp workspaces, kcp workspace types], so a `dev-instance`
WorkspaceType with `limitAllowedChildren` keeps instance trees from being nested into each other by
mistake.

Inside one instance, roles: prod runs gateway and bidder kcps on separate hosts. Dev default runs them as
two kcps (prod topology; ~1 GB per instance); `--kcp-shared-roles` puts both roles on one kcp under
`<base>:gateway` / `<base>:bidder` to halve memory when the flow under test does not care.

### 4.5 Ports and leases

- Port 0 + OnListen for every server we own (org rule; 0006 M2-KCP-PORT0 already says "run bidder, run
  gateway ssh, run request and every test use 0", plan 0006:594-595).
- portallocator: leaseKey becomes (instance, cluster, purpose) - the `Lease.Instance` field exists
  (portallocator.go:106) but the key ignores it (:145-148); dev opens ONE per-user lease file
  `$XDG_STATE_HOME/socialweb-computer/ports/leases.json` under its existing flock (:227-243), with a
  carve per user that does not overlap the live-test blocks (liveports.go:33-56). Prod keeps one lease
  file per account. subnetallocator gets the same key change.
- Bed services: the vendored manifests' fixed 2583-2589 (marketbed/live_test.go:18) become template
  fields rendered to 0, each service reporting its port via a port file/onListen that the bed reads; the
  bed's lock file goes.

### 4.6 Go interfaces

```go
package hostsvc

type Backend string

const (
	BackendSystemdUser Backend = "systemd-user"
	BackendProc        Backend = "proc"
)

type Capabilities struct {
	Backend          Backend
	EnforcesLimits   bool
	OOMKillReported  bool
	FDStore          bool
	PrivateHostname  bool
	Journal          bool
}

type UnitEvent struct {
	Name   string
	Status UnitStatus
}

type Manager interface {
	Start(ctx context.Context, spec UnitSpec) (UnitStatus, error)
	Inspect(ctx context.Context, name string) (UnitStatus, error)
	Stop(ctx context.Context, name string) error
	StopSlice(ctx context.Context, slice string) error
	ResetFailed(ctx context.Context, name string) error
	List(ctx context.Context, prefix string) ([]UnitStatus, error)
	Identify(ctx context.Context, name string, pid int) (Identity, error)
	Check(ctx context.Context, want Identity) (Observation, error)
	Watch(ctx context.Context, prefix string) (<-chan UnitEvent, error)
	Capabilities() Capabilities
	Close() error
}

type Config struct {
	Backend      Backend
	UnitPrefix   string
	SystemdPrivateSocket string
	ProcSocket   string
}

func Open(ctx context.Context, cfg Config) (Manager, error)
```

- `Open` refuses an empty `Backend` (no default, no probing). `UnitPrefix` (default `socialweb-`) lets the
  systemd backend run namespaced dev units (option c); `ValidateName`/`ValidatePrefix` check against it.
- `Identity` gains `Manager string` (systemd: the user manager's own InvocationID; proc: devsup's
  instance id) so "the manager restarted" is distinguishable from "the unit restarted".
- `UnitSpec` gains `Restart` (no | on-failure | always), `PrivateHostname`, `FDStore int`; the systemd
  backend maps them to properties (Restart=, ProtectHostname=private:<name> where supported else the
  ns-exec wrapper, FileDescriptorStoreMax=).
- Layering: `hostsvc.go` (types, validation) stays OS-neutral; `systemd_linux.go`, `proc_linux.go`
  (client), `internal/devsup` (the server; uses `hostos` and the identity code), `unsupported_other.go`.
- Status everywhere records `runtime.backend` so an object placed by one backend is never "adopted" by the
  other (a mismatch is Lost(IdentityMismatch) with a message naming both backends).

### 4.7 CLI

- `internal/clicfg/options.json` (0006 M1-CLI) gains `host-backend` (no default; env
  `SOCIALWEB_HOST_BACKEND`), `instance`, `kcp-base-workspace` (default `root`), `state-dir` (default
  resolved by install/dev), `asset-cache`.
- `socialweb-computer dev up [--profile quick|bed|prodlike] [--instance x] [--fg]`: resolve instance ->
  flock `<state>/instance.lock` -> start or adopt devsup -> place the role units (core role(s) as units of
  devsup, like the prod core is a unit of the user manager) -> wait readiness -> print `dev env`.
- `dev down [--keep-state]`, `dev status` (units, ports, kcp URLs, from devsup or the on-disk records),
  `dev ls` (all instances of this user), `dev logs <unit>`, `dev env` (exports `KUBECONFIG`, base
  workspace, ports, for shells and TS tools), `dev restart core [--binary path]` (rebuild loop: units
  adopted), `dev roll kcp|cluster <key>`, `dev check` (host prerequisites of R8, no fixes), `dev gc`
  (asset cache, only when no devsup of this user is running).
- `testbed up|accept|down` (0008 M4-TESTBED-PORT) = `dev up --profile bed` + the accept checks.
- Running `serve gateway kcp --host-backend=proc --instance x` by hand is allowed (that is what devsup
  runs); there is no hidden mode switch.

### 4.8 Tests

- Tier 0 (offline): fakes as today (fake bus, fake hostsvc).
- Tier 1 (contract, `internal/hostsvc/hostsvctest`): `Run(t, func(t *testing.T) Manager)`. proc runs on
  every `check.sh --fast` with `--die-with` the test pid; systemd-user behind livegate with a
  `socialweb-test-<rand>-` prefix. Cases (stand-ins only): start/inspect/stop; adopt-not-recreate on a
  second Start; failed unit readable until ResetFailed; exit status of a killed unit (signal 9);
  survives the starter's restart (proc: a second client process; systemd: E1); stop kills a descendant
  that called setsid (`sh -c 'setsid sleep 1000 & exec sleep 1001'`); Restart=on-failure restarts with
  a new InvocationID; Watch reports an exit within 2 s; StopSlice stops nested slices and nothing else;
  List never crosses prefixes; identity mismatch on a reused pid; manager restart makes every unit
  Lost(UnitGone); limits cases gated on `EnforcesLimits`; fd store gated on `FDStore`;
  `TestEveryManagerMethodHasAContractCase` (reflect over the interface).
- Tier 2 (kcp integration): `internal/devstack.Start(t, profile)` -> devsup (die-with test) + kcp slot
  on port 0 + base workspace; or `SWC_TEST_KCP=<kubeconfig>` to reuse a shared test kcp with a per-test
  `root:t:<rand>` workspace.
- Tier 3 (market bed): `dev up --profile bed` per worktree; guests only via `runComputeContract`.
- Parallel proof: `TestTwoInstancesOnOneHostShareNothing` (two devstacks in one test: distinct ports,
  dirs, sockets, kcps; stopping one leaves the other's InvocationIDs unchanged).

### 4.9 Prod-only (never in dev)

`install bidder|gateway` and `uninstall`, account creation, kvm group membership, `loginctl
enable-linger`, the `user@<uid>.service.d` Delegate drop-in, the AppArmor pasta profile,
`/etc/rancher/node/password`, the installed user unit files, `bin/current|previous` and `upgrade` /
`rollback` (dev uses `dev restart core --binary`), caddy, fixed prod ports (6443, 8788), check-host's
lingering and delegation checks (dev check reports the dev subset). `dev` refuses to run as root;
`install` refuses `host-backend=proc`.

---------------------------------------------------------------------------------------------------

## 5. Plan deltas (proposed; plans not edited by this research)

### 0006

- NEW `M2-HOST-BACKEND-PROC` - "hostsvc proc backend: devsup, a per-instance parent for dev and tests".
  depends_on [M2-HOST-UNITS]. Decision: section 3(a2) and 4.6, decided by Claude, user may veto
  (questions Q1, Q4). files: internal/devsup/, internal/hostsvc/proc_linux.go, hostsvc.go (Backend,
  Capabilities, Config, Watch, StopSlice, Restart, Identity.Manager), cmd/socialweb-computer internal
  devsup. test_first: the proc half of M2-HOST-CONFORMANCE red against a stub, plus
  TestADevsupCrashLeavesNoProcessAndTheNextDevsupSweepsByLeaderTicks,
  TestAPgidWhoseLeaderPidWasReusedIsNeverSignalled, TestDieWithStopsEverythingWhenTheWatchedPidExits,
  TestAnOrphanReparentedToDevsupIsKilledWhenItsUnitIsInactive.
- NEW `M2-HOST-CONFORMANCE` - "one Manager contract suite, run against proc always and systemd-user
  behind livegate". depends_on [M2-HOST-UNITS]. Decision: section 4.8 tier 1; check.sh --fast runs the
  proc half. test_first: TestEveryManagerMethodHasAContractCase red first; the systemd half re-homes the
  M2-HOST-UNITS live tests (live_linux_test.go) into the suite.
- NEW `M1-DEV-INSTANCE` - "`dev up|down|status|ls|logs|env|check|restart|roll|gc`, instance identity, XDG
  layout, shared asset cache, per-user lease file". depends_on [M1-CLI, M2-HOST-BACKEND-PROC, M1-ASSETS].
  test_first: TestTwoWorktreesGetDistinctInstanceIDsAndShortSocketPaths,
  TestTheSocketPathOfTheLongestWorktreeIsUnder108Bytes, TestDevRefusesRoot,
  TestTwoInstancesOnOneHostShareNothing (stand-ins), TestALeaseIsKeyedByInstance (portallocator).
- CHANGE `M1-CLI`: add the `dev` tree and options `host-backend` (required, no default), `instance`,
  `kcp-base-workspace`, `asset-cache`; test TestServeWithoutAHostBackendExits2NamingTheOption.
- CHANGE `M1-INSTALL`: see section 5.4; writes `host-backend: systemd-user` into the account's config.json.
- CHANGE `M1-ASSETS`: extraction root is an option; dev points it at the per-user cache; GC of the dev
  cache only by `dev gc` with no devsup of the user running.
- CHANGE `M2-VM-TREE-AUDIT`: backend-aware listing (systemd: cgroup.procs; proc: process-group members +
  attributed descendants); add TestTheAuditPassesOnTheProcBackendWithDevsupOutsideTheGroup.
- CHANGE `M2-KCP-PORT0` / `M2-KCP-UNIT` (kcp slots): the kcp front binds 0 outside prod and reports via
  OnListen; slot argv takes the front's bound URL; runs on either backend.
- CHANGE `M3-CLUSTER-UNITS`: slices through `StopSlice`; the proc backend emulates; portscan attribution
  reads unit membership through the Manager, not cgroup files directly.
- CHANGE `M3-APISERVER-FRONT`: fd store through `UnitSpec.FDStore`; proc implements it in devsup.
- CHANGE `M3-CP-SLOTS`: private hostname through `UnitSpec.PrivateHostname`; `internal ns-exec`
  (hostname + optional bind of `<state>/rancher` over `/etc/rancher`) is the proc implementation and the
  systemd fallback.
- NEW (phase 2) `M3-HOSTPROCESS-KIND` - "HostProcess objects with a pure reconciler; systemd and devsup
  are placements". depends_on [M2-APPLY, M2-HOST-BACKEND-PROC, M2-HOST-CONFORMANCE]. Decision: section
  3(b); Postgres/OpenBaoServer/kcp-slot/cluster-member placement goes through it; mirror records for the
  imperatively started root (kcp, its store, the core). test_first:
  TestAHostProcessIsPlacedOnTheBackendItsStatusNames, TestTheSameObjectsReconcileIdenticallyOnBothBackends
  (conformance harness), TestDeletingAMirrorRecordDoesNotStopTheRootUnit. Q3.
- NEW (phase 3, optional) `M4-DEV-PARENT-KCP` - "`dev host`: a per-user parent kcp whose
  root:dev:<instance> workspaces hold each instance's HostProcess objects (kcp inside kcp)".
  depends_on [M3-HOSTPROCESS-KIND, M1-DEV-INSTANCE]. Q2.

### 0007

- CHANGE `M2-VM-RUNTIME`: Runtime composes `hostsvc.Manager` of either backend; Watch via
  `Manager.Watch`; `Capabilities().EnforcesLimits=false` recorded in the ComputeVM status condition in dev;
  the stand-in live tests run through the conformance harness on both backends
  (TestTheVMUnitsTreeIsPastaAndTheStandInOnly, TestAVMMThatExitsOnItsOwnEndsItsUnitAndLeavesNoProcess).
- CHANGE `M2-CONTROLLER`: add TestACoreRestartUnderTheProcBackendAdoptsEveryVMUnit (stand-in VMM) and
  TestAnObjectPlacedByAnotherBackendIsLostNotAdopted.
- CHANGE `M5-RUN-BIDDER`: `run bidder --host-backend=proc --instance x` runs without the shared account
  (the account check is prod-only, keyed on `host-backend=systemd-user`); the deno denies cover the devsup
  socket because it lives under `$XDG_RUNTIME_DIR` (plan 0007:612-614); test
  TestDenoInTheBidderPodCannotDialTheDevsupSocket.

### 0008

- CHANGE `M4-TESTBED-PORT`: drop the fixed 2583-2589 and the one-bed lock (plan 0008:287-289); bed
  service ports render to 0 and are read back; `testbed up` = `dev up --profile bed`; test
  TestTwoBedsOnOneHostEachRentAGuestThroughTheRFPFlow (two instances, two requesters, two bidders; guests
  only via runComputeContract).
- CHANGE `M2-DENOKCP-INPROC`: "deno processes die with the core" is proved on both backends; on proc via
  devsup's subreaper attribution (execrunner children are in their own process groups and have no
  Pdeathsig, supervisor.go:141). test: TestAKilledCoreLeavesNoDenoProcessOnTheProcBackend.
- CHANGE `M2-DENOKCP-FORK`: the runner_unix.go seam it already plans (plan 0008:154-158) also sets the
  `SWC_UNIT` attribution env so devsup can attribute DenoPod processes.

### 5.4 `install bidder|gateway` item check (M1-INSTALL, plan 0006:408-442)

What it creates today, per the plan:

| | gateway | bidder |
|---|---|---|
| account | `socialweb` | `swc-bidder` (one for all handles) |
| groups | - | `kvm`, added BEFORE linger and before user@<uid> first starts |
| linger | `loginctl enable-linger` | same |
| delegation | `user@<uid>.service.d/socialweb-delegate.conf` `Delegate=cpu memory pids` where the host delegates less | same |
| AppArmor | - | Ubuntu 24.04+: profile granting userns to `<state>/runtime/*/pasta` |
| host file | `/etc/rancher/node/password` 0600 owned by the account | - |
| user unit | `~/.config/systemd/user/socialweb-computer-gateway.service` | `socialweb-computer-bidder@.service` |
| state | `bin/<sha>/`, `bin/current`, `bin/previous`, `runtime/<sha>/` | same |
| final step | check-host as the account | same |

Gaps to add (decided by Claude, user may veto):

1. **Idempotent by plan, not by luck.** `install` computes the step list from probes (account exists with
   the expected shell/home/groups? linger on? drop-in content equal? profile loaded with equal text? unit
   file equal?) and performs only missing steps; `--dry-run` prints it. Each mutating step is recorded in
   `/var/lib/socialweb-computer/install-<role>.json` (root-owned: what install changed, versions,
   timestamps). An account that exists but was not created by install (no record) is refused unless
   `--adopt-account`, so a typo never takes over a human's account.
2. **Order rule preserved and tested:** kvm before linger before first user@ start (existing test
   TestInstallBidderAddsKvmBeforeEnablingLinger); if user@<uid> is already running without kvm, install
   refuses and names the restart (it would stop every VM), never restarts it itself.
3. **Enabling units:** root writes the unit file; `systemctl --user -M <account>@ daemon-reload` and
   `enable` the gateway unit; per-handle bidder units are enabled by the ACCOUNT later
   (`socialweb-computer bidder enable --handle h`), so adding a handle needs no root.
4. **config.json** for the account: `host-backend: systemd-user`, state dir, ports (prod 6443/8788 for the
   gateway) - the only place a non-dev default is set.
5. **`uninstall bidder|gateway [--purge] [--force]`:** refuses while any `socialweb-vm-*` unit or ComputeVM
   is Running unless `--force` (stopping it Loses tenant VMs); stops the core unit and every
   `socialweb-*` unit of the account; disables linger; removes the drop-in, the AppArmor profile, the unit
   file, the node password file; keeps the account and its state unless `--purge` (then `userdel -r`
   and the record). Every step reverses one recorded install step; unrecorded things are left alone.
6. **Tests (fake command runner):** TestInstallTwiceRunsNoMutatingCommandTheSecondTime,
   TestInstallRefusesAnExistingAccountItDidNotCreate, TestUninstallReversesEveryRecordedStep,
   TestUninstallRefusesWhileAVMUnitIsActive, TestInstallRefusesTheProcBackend,
   TestInstallWritesSystemdUserAsTheHostBackend. Live on hat-8: a throwaway account installed, installed
   again (no-op), uninstalled, `--purge`d; readings recorded.

---------------------------------------------------------------------------------------------------

## 6. Measurement [M]

A 130-line Go stand-in supervisor (stdlib only) on hat-8: sets PR_SET_CHILD_SUBREAPER, starts three
units - `attached` (`sleep 1001`, Setpgid + Pdeathsig SIGKILL), `attached-tree` (`sh -c 'sleep 1002 &
exec sleep 1003'`, Setpgid + Pdeathsig), `detached` (`sleep 1004`, Setsid, no Pdeathsig) - writes
state.json {pid, pgid, ticks, boot_id}; a second invocation adopts and sweeps.

| Step | Result |
|---|---|
| adopt while supervisor alive | all three Running(adopted) by pid + start ticks; `waitid(P_PIDFD)` from the adopter: ECHILD ("no child processes") for every unit |
| `kill -9` supervisor | `attached` and `sleep 1003` gone (Pdeathsig); `sleep 1002` (grandchild, same pgid) SURVIVED, reparented to PID 1 (`ppid=1 systemd`, not the dead subreaper, not the user manager: session processes do not descend from user@); `detached` survived (own session) |
| adopt after supervisor death | attached: Lost(UnitExited) (pidfd_open ESRCH); detached: Running(adopted), still ECHILD for exit status |
| adopt with start ticks off by one | detached: Lost(IdentityMismatch) - pid reuse is detected |
| sweep: `kill(-pgid, SIGKILL)` by recorded pgid | killed the orphaned grandchild (its pgid outlived its leader) and the detached unit; nothing left |

Consequences used above: (1) an adopter never gets exit statuses, hence devsup as the stable parent;
(2) Pdeathsig alone leaks grandchildren, hence process groups + subreaper + tree kill; (3) a pgid sweep
after a crash is safe only by this rule: signal `-pgid` when (a) a process with pid == pgid exists and its
start ticks equal the recorded leader ticks, or (b) no process has pid == pgid but some process still has
that pgid (the kernel does not reuse a pid while it names a live process group, so the group is still
ours); never when a process with pid == pgid exists with different ticks (reused). Cleanup: all stand-ins
killed, scratch dir removed.

---------------------------------------------------------------------------------------------------

## 7. Questions for the user (recommended answers)

- Q1. Dev survival: units survive a core restart and die with a per-instance devsup (recommended), or
  everything dies with one dev process (simpler, no adoption in dev)? **Recommended: devsup** - it keeps
  guests and kcp across rebuilds and runs prod's adoption code in every dev loop.
- Q2. Nesting: one kcp per instance with base-path-relative roles (recommended), one shared dev kcp with
  `root:dev:<instance>` trees, or a parent kcp holding instances as HostProcess objects (kcp in kcp)?
  **Recommended: per-instance kcp now; shared test kcp only for go test packages; kcp-in-kcp (dev host)
  as optional phase 3.**
- Q3. `HostProcess` kind (kcp as the systemd API, placements pluggable): now, or after the proc backend and
  conformance suite? **Recommended: after (phase 2)**, so its schema is shaped by two working backends.
- Q4. `host-backend` with no default (required everywhere; install writes systemd-user, dev passes proc), or
  default systemd-user? **Recommended: no default** - a forgotten flag in a worktree must not drop
  prod-named units into the login user manager.
- Q5. Dev kcp store: kine-sqlite by default and kine-postgres in the bed/prodlike profiles, or postgres
  always (prod parity)? **Recommended: sqlite for quick, postgres for bed and prodlike.**
- Q6. k3s's `/etc/rancher/node/password` in dev: bind-mount an instance-owned file in a user+mount
  namespace via `internal ns-exec` (no root), or require a one-time root-created host file shared by all
  instances? **Recommended: ns-exec** (it is the same one-shot M3-CP-SLOTS needs for the hostname).
- Q7. Dev on Ubuntu 24.04+ (AppArmor blocks unprivileged userns for pasta): document the host
  prerequisite and have `dev check` report it, or add a root `install dev-host` step? **Recommended:
  report only; dev never escalates.**
- Q8. Dev runs gateway, bidder and requester as one uid in one instance (prod: separate accounts/hosts).
  **Recommended: accept** - account separation is a prod property tested by install/check-host on hat-8.

---------------------------------------------------------------------------------------------------

## Sources

Web:
- [W systemd-run] systemd-run(1), `--collect`, transient services: https://www.freedesktop.org/software/systemd/man/latest/systemd-run.html , https://man.archlinux.org/man/systemd-run.1.en
- [W systemd.exec] ProtectHostname=private:<name>: https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html
- [W delegation] cgroup delegation (only sub-trees delegated; systemd owns the delegated cgroup's attributes): https://systemd.io/CGROUP_DELEGATION/
- [W subreaper] PR_SET_CHILD_SUBREAPER(2const): https://man7.org/linux/man-pages/man2/PR_SET_CHILD_SUBREAPER.2const.html
- [W pdeathsig] PR_SET_PDEATHSIG(2const), caveat "the parent is the thread": https://man.archlinux.org/man/PR_SET_PDEATHSIG.2const.en
- [W pidfd] pidfd_open(2): https://man.archlinux.org/man/pidfd_open.2.en
- [W xdg] XDG Base Directory Specification (XDG_STATE_HOME, XDG_RUNTIME_DIR 0700, lifetime): https://specifications.freedesktop.org/basedir-spec/latest/
- [W kcp workspaces] https://docs.kcp.io/kcp/main/concepts/workspaces/ ; [W kcp workspace types] limitAllowedParents/limitAllowedChildren: https://docs.kcp.io/kcp/main/concepts/workspaces/workspace-types/
- [W static pods] kubelet static Pods and mirror Pods: https://kubernetes.io/docs/tasks/configure-pod-container/static-pod/
- [W quadlet] Podman Quadlet (specs to systemd units): https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html
- [W process-compose] https://github.com/F1bonacc1/process-compose ; health/readiness: https://f1bonacc1.github.io/process-compose/health/
- [W s6] s6 overview (s6-supervise per daemon, s6-svscan): https://skarnet.org/software/s6/overview.html
- [W supervisord] subprocesses and pidproxy: https://supervisord.org/subprocess.html

Repo (main d6e1881): internal/hostsvc/{hostsvc.go, identity_linux.go, busdial_linux.go, systemd_linux.go},
internal/processrunner/processrunner.go, internal/procgroup/procgroup.go, internal/hostos/sysproc_*.go,
internal/portallocator/portallocator.go, internal/liveports/liveports.go, internal/marketbed/live_test.go,
cmd/socialweb-clusterprovider/main.go:117-124, docs/plans/0006, 0007, 0008,
docs/research/2026-10-11-vm-lifecycle/{user-decisions.md, kcp-spike.md, kcp-blue-green.md, linux-proofs.md};
../kcp-libs impl/execrunner/supervisor.go, abc/runner/runner.go; ../deno-kcp internal/denopod/denopod.go,
internal/provider/reconcile_pod.go, deploy/start-kcp.sh.
