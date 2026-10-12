# Plans 0006 / 0007 (and touching 0008): Linux execution exploration

Written 2026-10-11 against socialweb-computer-kcp f0460fd (main), hono-compute-provider 4d7da59.
Read-only exploration; nothing in any repo was edited.

Inputs actually read:
- docs/plans/0006, 0007, 0008 (full); 0005 item list + M1-BIDDER-UNIT, M3-NODE-LOSS, M6-LIMITS.
- code: internal/hostos, nodevm, nodefirecracker, procgroup, processrunner, clusterprovider/postgres.go +
  steps.go/stack.go, rootvault, versions/versions.json, cmd/socialweb-nodeboot, cmd/socialweb-nodeimage,
  deploy/container-entrypoint.sh, deploy/bidder/*, NEXT.yaml, ARCHITECTURE.md (sections 5, 9 "host platform", 13).
- hono-compute-provider lib/abc/compute-provider/mod.ts, lib/compute-provider-firecracker/mod.ts.
- scratchpad/user-decisions-2026-10-11.md (folded in; those decisions are NOT re-asked in D).
- scratchpad/linux-proofs/e0.txt, e1-private.txt, e2*.txt (sibling's live readings on hat-8, systemd 259).
- NOT FOUND: scratchpad/lifecycle-linux.md, lifecycle-macos.md, lifecycle-windows.md, lifecycle-synthesis.md
  do not exist (searched all of /tmp/claude-1000 at 23:1xZ). This report uses the lifecycle decisions as
  stated in the task text plus the sibling's linux-proofs readings instead.

Decisions designed TO (user, 2026-10-11): Linux only for 0006/0007/0008 with cross-OS seams; VMs survive
core crash/upgrade/logout, reboot = Lost; no socialweb-computer code resident in a VM's process tree; gateway
AND bidder live in the USER manager of a lingering account, cluster groups are transient services there,
only cpu/memory/pids delegation is relied on; Postgres and OpenBaoServer CRDs KEPT, each with its own unit,
with explicit ordering/readiness; identity mismatch on an active unit = Lost + StopUnit immediately,
unreachable bus/socket = Unknown (retry, never Lost); ONE shared lingering account for all bidder handles,
units namespaced by handle.

---------------------------------------------------------------------------------------------------------

## Facts established today (evidence for everything below)

F1. Cross-OS builds are broken today and nothing notices.
  - `GOOS=darwin go build ./internal/... ./cmd/...` fails: internal/hostos has NO darwin files (only
    `_linux.go` / `_windows.go`), so portallocator (Lock, LocalPortRange, probeListen), subnetallocator
    (Lock*), processrunner (DieWithParent :121) are undefined.
  - `GOOS=windows go build` fails: internal/nodevm/disk.go:142-164 uses unix.IoctlFileClone/SEEK_DATA
    (added by 7c4f741 "rootfs is a reflink") with no build tag. `GOOS=windows go vet` also fails:
    internal/apigen/live_upgrade_test.go:71 `SysProcAttr{Setpgid}` untagged.
  - deploy/check.sh:30,54 runs only host `go build ./...` / `go vet ./...` - no GOOS matrix.
  - ARCHITECTURE.md "The host platform" (≈:2223-2280) still claims "GOOS=windows ... go build and go vet
    are clean" and describes a QEMU substrate on both OSes; internal/nodeqemu was deleted by 9d85916
    ("market only: the local substrates were retired"). hostos.QEMU()/NetworkMode()/Accel()/
    caps_windows.go (AccelWHPX) are dead capability predicates only check-host still reads
    (cmd/socialweb-computer-kcp-check-host/platform.go:14-17, config.go:197).
F2. A running guest's process tree today is `pasta -> sh -c exec -> socialweb-nodeboot namespace -> firecracker`.
  The namespace verb (nodevm.RunNamespace -> nodefirecracker.runVMM, namespace.go:15-58) STAYS RESIDENT:
  it starts firecracker as a child with `hostos.DetachProcess()` (= `Pdeathsig: SIGKILL` on Linux,
  sysproc_linux.go:10 - misnamed: it ATTACHES the VMM's life to the verb), configures it over the API
  socket, writes instance.json, then `vmm.Wait()`. Violates the new invariant; also any crash of that
  verb kills the guest.
F3. nodeboot Boot (main.go:262-390) does prep in-process: reflink clone, NoCloud seed written INTO the
  rootfs copy with host `debugfs -w` (:510-575), grow with host `e2fsck`+`resize2fs` (:459-508), then
  starts pasta via processrunner and polls instance.json. Stop (:625-659) is SIGKILL of the recorded pid
  and the VMM - no graceful path.
F4. Guest console = firecracker stdout/stderr into `<work>/guest.console` opened by the namespace verb
  (namespace.go:20-27), unbounded; nodevm.Fatal greps it for panic markers (console.go).
F5. Node images are built ON THE BIDDER: cmd/socialweb-nodeimage does `chroot ... apt-get install`
  (preinstall.go:217-224), `mount -t devpts` and `mount --bind /dev/*` (:239-266), `go build` of the guest
  init (build.go:123-133), `zstd`, `xz`, `cpio`, `du`, `mkfs.ext4 -d` (rootfs.go:303). Needs root (today:
  privileged runner container). Kernel = the host's /boot kernel + /lib/modules
  (internal/nodefirecracker/kernel.go:13-66) - varies per bidder host.
F6. Guest-to-host address depends on network allocation: TS provider computes
  `networkFor(rangeBase, nextIndex++)` (compute-provider-firecracker/mod.ts:129-145, :643) and points the
  guest at `hostAddressForGuest()` = runner container gateway (:660-664), then rewrites user_data
  (/etc/hosts, .curlrc --resolve, JSR URL) with it. So user_data enrichment is NOT independent of the
  network the host allocates.
F7. Cluster stack: procgroup.StackOrder = postgres, openbao, kine, k3s, ccm, eso-webhook, eso
  (procgroup.go). Postgres is NOT a group step (steps.go:91-93 refuses it); StartPostgres
  (postgres.go:97-150) execs host initdb/postgres/psql from PATH, starts postgres as a plain child with
  no Pdeathsig (:306-329) and adopts a server already serving (:116-123). Cluster OpenBao stores in that
  postgres (`storage "postgresql"`, clusterstack/openbao.go:32-33) and procgroup refuses openbao without
  PostgresUp (procgroup.go ErrPostgresUnavailable). All other members: Pdeathsig SIGKILL (0005 M6-LIMITS,
  65e3297). Root vault: `storage "file"` (rootvault.go:43).
F8. Entrypoint order (container-entrypoint.sh:214-317): prepare-root -> bao root vault -> kcp -> readyz ->
  apply-root -> install-provider.sh -> bootstrap -> migrate -> gateway -> socat -> clusterprovider -> door.
  Nothing between `bao server` and `apply-root` needs kcp, and kcp needs nothing from the vault.
F9. Bidder today: system template unit, User=swc-bidder, SupplementaryGroups=docker, Requires=docker
  (deploy/bidder/socialweb-computer-bidder@.service); launcher runs `deno run -A` (all permissions);
  work dir `install -d -m 0777 guests` (two-uid problem). 0005 M1-BIDDER-UNIT recorded "user chose a
  system unit over a user unit because firecracker needs kvm and runuser" - superseded by 2026-10-11.
F10. linux-proofs (hat-8, systemd 259, 23:14-23:16Z): a transient user-manager service survives
  `systemctl --user restart|stop` and `kill -9` of the starter unit (e1-private.txt STEP1-3 PASS); plain
  children of the starter die with it (control-group kill) even with setsid. A transient unit whose
  ExecStart is flagged ignore-errors (`-`) VANISHES after kill -9 (not-found); with ignore=false it stays
  `loaded failed signal 9` (e2-flag.txt) - the failure record survives only if ExecStart is not
  ignore-errors. MemoryMax+MemorySwapMax=0+OOMPolicy=stop -> Result=oom-kill (e2m.txt); MemoryMax with
  swap allowed swaps instead of killing. AllowedCPUs accepted but cpuset NOT delegated (no cpuset files).
  ManagedOOMPreference=avoid is written as xattr user.oomd_avoid on the user cgroup (whether oomd honours
  it for a non-root-owned cgroup: unproven). TasksMax and CPUQuota enforced.
F11. Fedora/hat-8 user@ delegation = cpu io memory pids (e0.txt); the decision accepts only
  cpu/memory/pids, so io weights are best effort and cpuset pinning is unavailable.

---------------------------------------------------------------------------------------------------------

## A. Item-by-item gaps for LINUX execution

Legend: [C] contradiction, [D] wrong/missing dependency, [M] missing item/work, [U] unproven assumption,
[S] stale vs code today, [R] risk.

### Plan 0006

M0-KCP-SPIKE
- [U][R] Scope too narrow. It measures compile breakage, test failures, size, start time. Also needed:
  (a) does every OTHER k8s-linked thing in the module tolerate client-go 1.37.0-alpha against k3s 1.36
  tenants (CCM is a hand-rolled client-go controller, internal/cloudprovider/controller.go:42-45, so the
  kube skew rule "CCM not newer than apiserver" does not bind it, but tenantclient, eso, clusterprovider
  apply paths must be run against a 1.36 k3s); (b) heap/GC interference: kcp + clusterprovider + gateway
  in one address space, RSS on a prod-shaped root; (c) shutdown: does kcp's server.Run honour ctx cancel
  and release etcd/flock within a bound (needed for restart-in-place on upgrade); (d) two kcps in one
  process (tests, run request) beyond ports: global singletons (klog, feature gates, metrics registries,
  `utilfeature.DefaultMutableFeatureGate`) - two in-process kcps may be impossible regardless of ports;
  (e) the out-of-process alternative's cost (release binary as asset in its own unit, port-0 via
  --experimental-bind-free-port + embedded-etcd port flags), so M0 can recommend, not just size.
- [R] In-process kcp means EVERY core upgrade restarts kcp (front of every tenant, kcp.socialweb.computer
  downtime for the restart window). Out-of-process kcp in its own unit makes core upgrades kcp-neutral.
  See D-Q1.

M0-SIZE-BUDGET
- [S][C] darwin "client-only" scope is superseded: macOS = plan 0012, Windows = plan 0011, each starts
  with M0-REVISIT. 0006 budget = linux/amd64 + linux/arm64 only; the matrix field must still name
  darwin/windows as "seam-only (builds+vets, no assets)". Remove "refuse serve gateway kcp by name on
  darwin" from this item; that belongs to the capability registry (B4).
- [M] Budget must include the per-OS asset set (B6) and a reserved line for the guest image (not
  embedded, see M2-GUEST-IMAGE) so the binary does not grow by 100s of MB of rootfs.

M1-CLI
- [U] Stdlib flag + internal/clicfg/options.json mirroring cli-args-env: fine. Add verbs needed by the
  new lifecycle: `install gateway|bidder` (one-time root: account, linger, kvm group, user unit file),
  `upgrade`, `rollback`, `units list|gc`, `internal vm-exec` (B3). Unsupported-on-this-OS verbs exit 2
  naming plan 0011/0012 (B4), consistent with "unbuilt subcommands exit 2 naming the item".

M1-K8S-FORK / M1-KCP-INPROC / M1-KCP-PORT0
- [R] Whole module moves onto kcp's fork, including apigen output and deno-kcp/kcp-libs (0008
  M2-DENOKCP-FORK). The plan has no "abort" criterion. Add: if M0 shows > N packages broken or the
  two-kcps-in-one-process test cannot pass (global singletons), switch to out-of-process kcp.
- [U] M1-KCP-PORT0 test "two in-process kcps in one test process" presumes no process-global state;
  M0 must check this first (klog flags, feature gate registry, Prometheus default registry panics on
  duplicate registration).
- [C] M1-KCP-INPROC flock `<root>/.socialweb-computer.lock` is right; also needed with out-of-process.

M1-ASSETS
- [M] Per-OS selection exists only as "build-tagged files (sshai pattern)". Needs the concrete scheme:
  `assets_<goos>_<goarch>.go` with `//go:build release && <goos> && <goarch>` + `//go:embed`, a
  `!release` dev resolver (explicit paths), and a CI rule that non-release cross builds need no blobs
  (B6). Without a release tag, a darwin/windows cross-vet in CI would fail on missing blobs.
- [M] Runtime dir GC rule: `runtime/<sha256>/` removable only when (a) not the current binary's set,
  (b) no loaded unit's ExecStart/Environment names a path under it (read via D-Bus unit properties),
  (c) older than the rollback window. Not in the item.
- [M] Content-addressed install of the CORE BINARY itself (bin/<sha256>/socialweb-computer + `current`
  symlink) is not in any item; M3-HOST-INSTALL says "renames it into place atomically" (in-place rename
  loses rollback and breaks units whose ExecStart names the old inode path - see M2-VM-RUNTIME).
- [R] A VM unit whose ExecStart names `bin/<core-sha>/socialweb-computer internal vm-exec` pins a
  ~300 MB core binary directory for the VM's whole life, across every upgrade. Use a tiny dedicated
  helper asset (`socialweb-vmexec`, B3) so a long-lived VM pins only a few MB.

M1-HOST-INVENTORY
- [S] The executable list (0006 research) misses what nodeboot/nodeimage really exec: debugfs, e2fsck,
  resize2fs, cpio, du, chroot, mount, apt-get (inside chroot), go (build.go:133), xz, zstd, ip. Most
  disappear under the proposed design (seed drive instead of debugfs; in-guest resize; images built in
  CI; netlink instead of `ip`) - the inventory should record the fate "deleted by design" for them.
- [M] "Stated host prerequisite: systemd" is too vague: minimum version / probed properties (A-x-systemd),
  cgroup v2 unified, unprivileged userns allowed (Debian sysctl, Ubuntu 24.04 AppArmor
  `kernel.apparmor_restrict_unprivileged_userns=1` - an embedded pasta at runtime/<sha>/pasta is NOT
  covered by any distro AppArmor profile for /usr/bin/pasta [U], so pasta may be denied userns on
  Ubuntu 24.04+), /dev/kvm + kvm group, /dev/net/tun, linger enabled.

M2-HOST-SCOPES (rename -> M2-HOST-UNITS)
- [C] "user manager for unprivileged modes and the system manager for the gateway service": superseded
  (gateway = user manager of its lingering account). System-manager StartTransientUnit from a non-root
  service needs polkit manage-units = root-equivalent.
- [C] "transient systemd unit ... socialweb-<kind>-<name>" + "scopes": these must be SERVICES, not
  scopes. A scope adopts an already-running pid (the starter forks it) - that puts our code as the
  parent. A transient SERVICE is forked by the manager: no socialweb ancestry. Title and text say scope.
- [C] "Adoption by unit name plus the child's own state record (pid and start ticks, execrunner)":
  the record must be the unit's InvocationID + MainPID + start ticks + boot_id, read FROM systemd, not
  from a file the child writes (a child that writes its own record is resident code).
- [M] Test is vacuous (gap 6): "kill -9 the starter, new process adopts" proves nothing once the child
  is in another cgroup - F10 shows that is guaranteed by construction. Real risks to test: `systemctl
  --user restart|stop` of the core unit (control-group kill), `daemon-reload`, `daemon-reexec`,
  logout with linger, user-manager restart (kills everything - must be documented as an operator
  action equal to reboot), and failed-unit visibility (F10: ignore-errors ExecStart erases the result).
- [M] No minimum systemd version, no probed-property check, no D-Bus library decision beyond go-systemd
  (go-systemd/v22/dbus is pure Go over godbus - OK with CGO_ENABLED=0; sdjournal is cgo - do not use).
- [M] Bus address for a user unit: DBUS_SESSION_BUS_ADDRESS / XDG_RUNTIME_DIR are set by the user
  manager for its own units - fine; for the one-time root installer, use `systemctl --user -M acct@`
  or machined; not specified.

M2-POSTGRES (KEPT as a CRD, own unit - user 2026-10-11)
- [U] "pinned relocatable PostgreSQL 18 build per platform, darwin amd64/arm64": darwin out of scope.
  Linux source not chosen (zonky embedded-postgres-binaries vs building from source in CI). Must match
  the major version AND catalog of the Fedora-18 data dirs already on prod (risk noted) - also locale/
  ICU: a relocatable build without ICU or with a different glibc collation version opens existing data
  with collation-version warnings or index corruption risk for text indexes. Measure.
- [M] Readiness is not observable by systemd (relocatable builds are usually not `--with-systemd`, so no
  Type=notify). Readiness = controller probe (pgx `SELECT 1` on the leased port) -> status.phase Ready.
- [M] Ordering with the cluster group is undefined (gap 2) - designed below (A-x-ordering).
- [M] "psql calls run the embedded psql": replace psql exec with pgx (pure Go) - removes psql from the
  asset set; initdb stays an exec'd asset (one-shot prep, ExecStartPre-free: run by the controller
  before StartTransientUnit).
- [M] clearStalePid/pidIsOurPostmaster (postgres.go:222-257) must switch to unit identity.
- [C] "Postgres refuses root, so the gateway runs as a non-root service user" - now true by the account
  decision; also the root vault and every cluster member then run as that one uid.

M2-OPENBAO-SERVER (KEPT, own unit)
- [C] Gap 1: M3-SERVE-GATEWAY-KCP orders "the root vault (OpenBaoServer object)" BEFORE kcp. An object
  needs kcp up. Fix: kcp first (it does not need the vault, F8), then apply the `hostprocesses` set,
  then the OpenBaoServer controller starts/adopts the root vault unit and runs init/unseal/apply-root.
  On restart the root-vault unit is already running: adoption needs no kcp read to keep it alive, so
  ordering affects only bring-up of a fresh host.
- [M] Unseal on unit restart: a bao unit restarted by systemd (Restart=on-failure) comes up SEALED;
  nobody unseals it until the controller notices. The controller must watch unit InvocationID changes
  and unseal. Not in the item.
- [U] Bidder vault as an OpenBaoServer in the bidder's kcp means the bidder's key material is
  unavailable until kcp + controller are up - acceptable, but state it.

M3-BAO-SYNC
- [D] depends on M3-CLUSTER-SCOPES only because the cluster vault "is now a member of a host-scoped
  group": correct, keep. Add: runs on adoption after an InvocationID change (vault restarted while the
  core was down).

M2-MANIFESTS / M2-TEMPLATE / M2-APPLY
- No Linux-specific gaps. [D] M2-APPLY needs only a kcp loopback config; it should not depend on
  in-process kcp specifically (works for out-of-process too).

M3-FOLD
- [C] "socialweb-nodeboot folded into a verb" + "internal nodeboot-namespace re-exec": under the new
  invariant nodeboot's resident namespace verb is DELETED, not folded. Its replacement is a one-shot
  prep step (controller, in-process) + `socialweb-vmexec` (tiny helper, execve's firecracker).
- [C] "socialweb-nodeimage folded into a verb ... copies the embedded init": image build needs root and
  apt (F5); it should leave the bidder entirely (M2-GUEST-IMAGE). A dev-only `nodeimage` verb may stay
  behind the `dev` tag.
- [M] socialweb-nodeinit is a GUEST binary (linux/amd64+arm64 regardless of host OS); it belongs to the
  guest image pipeline, not the host asset set.

M3-CLUSTER-SCOPES (rename -> M3-CLUSTER-UNITS)
- [C] "started by a re-exec of the binary as the group's supervisor ... postgres is already its own
  object": the supervisor is socialweb code resident in the group's unit. That is ALLOWED (the
  invariant is about VMs) but it means a core upgrade leaves every cluster group running the OLD
  binary indefinitely. Needs an explicit policy: groups are NOT restarted by a core upgrade; a
  `units roll clusters` step restarts groups one at a time later, each within the NodeLoss grace
  (90 s, 0005 M3-NODE-LOSS) - measured. And binary GC must keep any core sha a group unit's ExecStart
  names.
- [M] Ordering/readiness with the Postgres unit - designed below.
- [C] Test (kill -9 the core, /readyz never fails) is vacuous for the same reason as M2-HOST-SCOPES;
  replace with `systemctl --user restart` of the gateway unit + an actual binary upgrade.
- [M] Port attribution after adoption: internal/portscan attributes leases by pid cmdline; under units
  the authoritative pid set is the unit cgroup (`/sys/fs/cgroup/.../cgroup.procs`). Lease adoption must
  read the unit, else a restarted provider may re-lease ports a live group holds.

M3-SERVE-GATEWAY-KCP
- [C] Order (gap 1) - fix as above. New order: lock -> kcp (in- or out-of-process) -> provider +
  permission-inventory + hostprocesses sets -> OpenBaoServer(root) Ready (adopt or start, unseal,
  apply-root) -> bootstrap-cluster (if asked) -> migrate -> gateway -> TLS front -> clusterprovider
  (adopts Postgres units, then cluster-group units) -> door.
- [C] "Any in-process member failing ends the process with exit 1 and systemd restarts it" - fine, but
  in the USER manager Restart=always with StartLimit tuned; and deno children die with the unit
  (default KillMode=control-group) - correct and desired.

M3-HOST-INSTALL
- [C] System unit `User=socialweb` -> USER unit in the lingering account `socialweb` (decision).
- [C] Gap 5: "KillMode=mixed so in-process members stop and host scopes are untouched" - wrong
  rationale. Transient units live in their OWN cgroups (F10: user@.../app.slice/<vm>.service), so ANY
  KillMode leaves them untouched. KillMode=mixed sends SIGTERM to the main pid only and SIGKILL to the
  rest of the core's cgroup, i.e. deno children get no graceful stop. Use the default
  KillMode=control-group (SIGTERM to all, SIGKILL after TimeoutStopSec). Same fix in 0007 M5-RUN-BIDDER.
- [M] /etc/rancher/node/password for the `socialweb` uid: still needed (k3s invariant), created by
  `install gateway` as root.
- [M] caddy -> kcp 6443 and door 8788: unchanged; the gateway user unit cannot bind <1024 (not needed).
- [M] Upgrade flow end to end (A-x-upgrade) not specified beyond "rename and restart".

M4-BUILD
- [C] CGO_ENABLED=0 for the core is fine; kine needs CGO_ENABLED=1 (versions.json) - it is an asset
  built separately; say so. Two clean builds byte-identical requires the asset blobs to be pinned
  inputs, not re-downloaded.
- [M] Cross-OS gate (B7): `GOOS=darwin|windows go build ./... && go vet ./...` (non-release tags) on
  every check.sh --fast run. Today both fail (F1) - fixing them is the first commit of 0006.

M5-CUTOVER
- [M] The copy changes owner from uid 1000 to the `socialweb` account; postgres data dirs require 0700
  and owner = running uid; bao file storage likewise. Also `install gateway` must enable linger BEFORE
  the first start, and cluster groups are first started by the new process (they were container
  children) - so the cutover window includes every control plane's cold start (k3s + kine + postgres):
  measure; it may exceed NodeLoss 90 s for market nodes -> nodes released. Either drain market nodes
  before cutover or raise the grace for the window. Not addressed.

M5-RETIRE - no Linux gap.

ACC6-UPGRADE - [U] "keeps every tenant apiserver answering" holds for cluster groups (own units) but
NOT for kcp if kcp is in-process (kcp restarts with the core). State which.

### Plan 0007

M1-DECIDE - [M] Record ALSO: no-wrapper invariant, user-manager placement, shared bidder account,
Verdict tri-state + mismatch policy, guest-reported host keys. ARCHITECTURE section 5 + 13 invariants.

M1-FC-INVENTORY - [M] Add requirements that change with no container: host address source (F6),
console capture, graceful stop, image provenance (CI-built, not host-built), seed transport
(drive vs debugfs), host-key report (guest-side), boot timeout without result.json (no resident writer).

M1-API (ComputeVM)
- [C] Status fields `unit, vmmPid, vmmStartTicks, pastaPid, apiSocket` are Linux/systemd/firecracker
  specific and APIResourceSchema.spec is immutable - every new backend would cost a schema revision.
  Use the OS-neutral shape (B5): `status.runtime {backend, ref, instance, pid?, startTime?, hostBootID}`
  + `status.network {guestIP, hostAddress, prefix, mac}`; backend is a free string (no enum).
- [M] Phase set lacks Preparing and a reason field for Lost (HostRebooted, IdentityMismatch, UnitGone,
  UnitFailed, OOMKilled, VMMExited). Unknown is a CONDITION (RuntimeObserved=Unknown), not a phase - the
  phase stays what it was while unknown.
- [M] Two-phase boot (F6): spec cannot carry the final user_data at create time if the host address is
  allocated by the controller. Either constant guest network (D-Q3, recommended) or a gate:
  `spec.userDataSecretRef` empty -> controller allocates and writes status.network, TS enriches, writes
  the Secret, sets the ref -> controller prepares and boots.
- [M] `spec.owner.handle` (shared account, decision 4) so the controller and GC namespace by handle.

M1-COMMON-EXTRACT - OK. [M] `pointGuestAtHost` keeps its signature; the host address now comes from
status.network.hostAddress (or the constant), never from `microvm.hostAddressForGuest()`.

M2-BACKPORT
- [C] "Go side owns ... host-key scan" via "the nodeboot namespace verb": that verb no longer exists /
  is not resident. Make the host key GUEST-REPORTED: a cloud-init module (cloud-init-common registry)
  posts host-key fingerprints with onNetwork; the droplet view reads it from status.market. No host code
  enters the guest namespace. (Respects the RFP-spine rule: delivered by user_data.)
- [C] Parity at "boot-spec.json" boundary: boot-spec.json is nodeboot's format and goes away. New
  boundary = the VMSpec handed to VmRuntime.Prepare (B2) + the seed drive bytes + the firecracker config
  JSON. TS side (microvm stubbed) must emit an equivalent; fixture-defined.

M2-UNPRIVILEGED-RUNNER
- [C] "the controller starts each guest as `socialweb-computer internal nodeboot -spec ...` in a
  transient unit": resident wrapper -> violates invariant. Replace with ExecStart =
  `runtime/<pasta-sha>/pasta <args> -- runtime/<vmexec-sha>/socialweb-vmexec <params.json>` and vmexec
  creates the tap/bridge via netlink then `execve(runtime/<fc-sha>/firecracker, --id <vm-id>
  --config-file <work>/fc.json --api-sock <work>/fc.sock)`. Process tree after start: pasta -> firecracker.
- [C] Test "boot through the RFP-flow fixture's boot-spec from a child process": boots a guest outside
  runComputeContract -> violates the RFP-spine rule. Split: (a) unit mechanics with a stand-in exec (not
  a guest) - allowed; (b) any real guest only via the market network (M6-*).
- [M] kvm group: in a user manager, supplementary groups are fixed when user@UID starts. Adding the
  account to kvm later requires restarting user@UID = killing every VM (equivalent to reboot). `install
  bidder` must add kvm BEFORE linger/first start; check-host must detect "account in kvm per
  /etc/group but manager lacks it" (read /proc/<user-manager-pid>/status Groups).
- [M] The "two-uid work dir disappears" is right; but the shared account means all handles' work dirs
  share a uid; separation is by path (`<state>/handles/<h>/vms/<name>`) and unit-name namespace only.
- [U] pasta rootless in user+net namespace from an embedded path: SELinux on Fedora (the code's
  comments mention pasta_t domain issues) and AppArmor on Ubuntu 24.04+ (F-A-x) unmeasured.
- [M] Memory policy: MemoryMax = guest mem + VMM overhead; MemorySwapMax=0 + OOMPolicy=stop gives a
  clean Result=oom-kill (F10); swap allowed silently swaps guest memory. Decide (D-Q5).

M2-CONTROLLER
- [C] Re-population rule "Running only if unit active AND pid start ticks AND API socket answers Running;
  otherwise Lost" conflicts with decision 3: an unreachable API socket / bus is Unknown (retry, never
  Lost). Rewrite as the Verdict table (A-x-verdict).
- [C] "A socialweb-vm-* unit of this bidder with no ComputeVM is reported and stopped after a 10-minute
  grace" - keep, but with shared account "this bidder" = unit name prefix `socialweb-vm-<handle-id>-`;
  also units of OTHER handles on the same account are never touched (name filter), and units of market networks
  /tests use a distinct prefix.
- [M] Mismatch policy (decision 3): Lost + StopUnit immediately (no grace) - add test.
- [M] Reboot detection: ComputeVM.status.runtime.hostBootID != /proc/sys/kernel/random/boot_id -> Lost
  (HostRebooted) without probing (a reboot leaves no units; but a reused unit name after reboot could
  otherwise be mistaken).
- [M] Graceful stop: SendCtrlAltDel then StopUnit (A-x-stop).
- [M] Failed-unit hygiene: ExecStart must NOT be ignore-errors (F10) so Result survives for the
  controller to read; controller calls ResetFailedUnit after recording.
- [D] depends on 0008:M2-DENOKCP-FORK (controller factory) - fine, but it couples the VM controller to
  the deno-kcp fork; a plain client-go informer loop would remove that dependency. Optional.

M3-TS-PROVIDER
- [M] Two-phase write if the network is not constant (above). [M] deno permissions: see M5-RUN-BIDDER.
- [U] "watch through the workspace path" fallback to polling - fine.

M3-BIDDER-FLAG - OK. [M] add `compute-provider-kcp-handle` (namespacing) and
`compute-provider-kcp-guest-host-address` (constant network) as cli-args-env options.

M4-BIDDER-VAULT
- [C] "unit socialweb-vault-<handle>": with a shared account and handle-namespaced names this becomes
  `socialweb-bao-<handle-id>` (B9 naming). One vault per handle (keys are per identity) - keep.
- [M] Unseal-on-restart as M2-OPENBAO-SERVER.

M4-BIDDER-BAO-SYNC, M4-CONTRACT-RESTORE, M4-ONNETWORK - no Linux gaps (pure state). M4-ONNETWORK
gains the guest-reported host key fields.

M5-RUN-BIDDER
- [C] "user-or-system unit ... member of group kvm ... KillMode=mixed" -> USER unit in the shared
  lingering bidder account, one template instance per handle (`socialweb-computer-bidder@<handle>.service`
  under that account's manager), default KillMode (gap 5).
- [C] TestTheBidderDenoPodHoldsNoKvmOrUnitManagerAccess cannot hold as written: the deno process runs
  as the SAME uid as the user manager and is in group kvm, so the OS grants it /dev/kvm and
  $XDG_RUNTIME_DIR/bus. The only lever is deno's permission sandbox: no `-A` (today's launcher), no
  --allow-run, no --allow-ffi, --allow-read/write limited to its state dir, explicit --deny-read of
  /run/user/<uid> and /dev/kvm. Rewrite the test to assert the DenoPod's deno argv permissions, and a
  live probe that `Deno.open('/dev/kvm')` and a unix connect to the bus are denied.
- [M] QR sign-in guarantee (0005 M1-BIDDER-UNIT) moves from the bash launcher into Go (stack/bidder).

M6-BIDDER-UPGRADE / M6-LIVE-RECOVERY / M6-NOSSH-JOIN / M6-GATEWAY-REPOPULATE
- [C] M6-LIVE-RECOVERY "SIGKILL the bidder unit (systemctl kill -s KILL)" - keep as one case, but the
  meaningful cases are `systemctl --user restart`, binary upgrade, and user logout with linger.
- [M] Add a reboot-semantics test that does not reboot: inject a different boot id (test seam on
  hostos.BootID) and assert all ComputeVMs Lost and no unit started.
- [D] M6-GATEWAY-REPOPULATE depends on 0006:M3-CLUSTER-SCOPES - keep (renamed).

semantics block - [C] "VM and vault units are separate and keep running" - true; add "the user
manager restarting (or `loginctl terminate-user`, or removing linger) kills them all = treated as
reboot".

### Plan 0008 where it touches

- M1-DENO / M2-DENOKCP-INPROC: deno processes as plain children dying with the core unit - consistent
  with KillMode=control-group. [M] DENO_DIR per closure sha under the account's state dir; GC same rule
  as runtime/.
- M2-SERVICE-TEMPLATE: [M] permissions field must default to least privilege (no -A), see M5-RUN-BIDDER.
- M4-MARKET-NETWORK-PORT: the market network's guests must come up through `run bidder` (RFP flow) - fine; [M] market network units
  use a distinct unit-name prefix (`socialweb-market-network-<pid>-...`) so a market network on hat-8 never collides with a
  real bidder on the same account.

### Cross-cutting topics

A-x-kcp (in-process fork risk and spike scope): see M0-KCP-SPIKE. Spike deliverable = a recommendation
between (i) in-process on the fork and (ii) out-of-process release binary in its own unit, scored on:
packages broken, tests failing, singleton conflicts (two kcps/process), RSS, cold start, upgrade
blast radius (kcp restarts per core upgrade?), port-0 cost (patch vs flags), repo size. Time box 1 day.

A-x-crd (Postgres/OpenBaoServer as CRDs vs simpler): user KEPT both. Design consequences:
- CRDs live in kcp, so their controllers can only act after kcp is up; on a running host the units are
  already up and outlive kcp restarts, so this costs only fresh-host bring-up order (gap 1 fixed above).
- Per-cluster Postgres object lives in the cluster's home workspace (as planned); the root vault object
  in root:socialweb-provider (or a host workspace); the bidder vault in root:bidder.
- A Postgres/OpenBaoServer object NEVER owns data deletion implicitly: deletion stops the unit, keeps
  data unless spec.deleteData (as planned).

A-x-ordering (cluster postgres unit <-> cluster-group unit), designed to decision 2:
- Units (gateway account's user manager):
  `socialweb-pg-<clusterKey>.service` (ExecStart = runtime/<pg-sha>/bin/postgres -D ... -p <lease>
  -c listen_addresses=127.0.0.1 -c unix_socket_directories=; Restart=on-failure; KillSignal=SIGINT
  (fast shutdown); TimeoutStopSec=60).
  `socialweb-cluster-<clusterKey>.service` (ExecStart = bin/<core-sha>/socialweb-computer internal
  cluster-group --cluster <key>; properties `After=socialweb-pg-<key>.service`,
  `Wants=socialweb-pg-<key>.service` - NOT Requires/BindsTo, so a postgres crash/restart does not
  cascade-stop the control plane; Restart=on-failure).
- Start (fresh): clusterprovider ensures Postgres object -> postgresctl does initdb (one-shot, in
  controller) -> StartTransientUnit(pg) -> probe pgx SELECT 1 -> status Ready{port, invocationID} ->
  clusterprovider sees Ready -> StartTransientUnit(group) with the pg port in its params -> group
  supervisor still gates openbao/kine on its own PostgresUp probe (procgroup ErrPostgresUnavailable kept).
- Adoption (core restart/upgrade): postgresctl lists `socialweb-pg-*` and verdicts them (same Verdict
  machinery as VMs, but Lost -> restart, not replace); clusterprovider lists `socialweb-cluster-*`:
  group active + pg active -> adopt both, no writes; group active + pg missing/failed -> restart pg
  only (kine and bao reconnect; measure kine's reconnect - [U]); group missing + pg Ready -> start group;
  both missing (reboot) -> start pg, then group.
- Restart of pg by systemd (Restart=on-failure): InvocationID changes; postgresctl observes, re-probes,
  bumps status; cluster vault (storage postgresql) may need re-unseal? No - bao seal state is in bao,
  not pg; but bao loses its storage connection - [U] measure bao behaviour on pg restart.
- Teardown: stop group, wait inactive, then stop pg (After= gives reverse order only if both stops are
  in one job transaction; the controller sequences explicitly).
- Pdeathsig stays INSIDE the group (members die with the group supervisor, 0005 M6-LIMITS rewritten).

A-x-gateway-in-user-manager: decision taken. Consequences: the account (`socialweb`) needs linger, its
own /etc/rancher/node/password, its user@ delegation (cpu/memory/pids accepted), and caddy stays a
system service. Gateway and bidder on one host = two accounts, two managers; never share.

A-x-accounts (decision 4, shared bidder account `swc-bidder`): unit name = `<kind>-<hid>-<name>` where
hid = first 10 hex of sha256(handle) (unit names <= 255 chars, handles may be long); plus
`LogExtraFields=SOCIALWEB_HANDLE=<handle>` and a per-handle slice `socialweb-b-<hid>.slice` for
accounting (cpu/memory/pids only). Isolation between handles is by name and path only (same uid): a
VMM escape reaches every handle's keys on that host. State it in ARCHITECTURE as a stated limit.

A-x-split (Go ComputeVM controller vs TS compute-provider-kcp under no-wrapper):
- TS (deno, bidder DenoPod): market protocol (bid config, accept bundle, ContractStore, vm.delete),
  user_data enrichment (pure, using status.network.hostAddress or the constant), OIDC issuer, onNetwork
  endpoint, guest JSR registry, rbac records, writes ComputeVM + user_data Secret, reads status.
- Go controller: allocation, image ensure (fetch+verify, no build), prep (clone, seed drive, size,
  fc.json, params.json), unit start, Verdict/adoption, graceful stop, GC, status writes.
- Neither is in the VM tree. vmexec is one-shot (execve). The VM unit's tree is pasta -> firecracker.

A-x-status (OS-neutral shapes): B5.

A-x-image (guest kernel/image per hypervisor target): images become BUILD ARTIFACTS, not host builds.
Target matrix `{hypervisor} x {guest arch}`: firecracker-x86_64, firecracker-aarch64 (0006/0007);
vz-arm64 (0012), hyperv-x86_64 (0011). Each = {kernel (pinned config: virtio-mmio, i8042+atkbd on
x86, gpio-keys+pl061 on arm64 for graceful stop), rootfs (cloud-init, resize_rootfs enabled), optional
initramfs, manifest with sha256}. Built in CI/build host (root or rootless userns there), published,
pinned in versions.json, fetched + verified by the controller into an image store. The host's /boot
kernel stops being an input (F5) - every bidder boots the same kernel.

A-x-systemd minimum: probe properties at check-host instead of trusting a version number: start a
throwaway transient service with every property the runtime uses (OOMPolicy, MemoryMax, MemorySwapMax,
TasksMax, CPUWeight, LogExtraFields, LogRateLimit*, CollectMode, After/Wants, StandardOutput=journal)
and read them back (the sibling's E2a shows this works). Stated floor: systemd 252 (Debian 12, RHEL 9;
Ubuntu 24.04 has 255; Ubuntu 22.04 = 249 unmeasured) - [U], versions from memory, verify. No dependency
on systemd's pidfd D-Bus calls (GetUnitByPIDFD etc.): adoption uses pidfd_open(MainPID) ourselves
(kernel >= 5.3) + /proc/<pid>/stat start ticks + /proc/<pid>/cgroup == unit's ControlGroup.

A-x-console: firecracker serial -> unit stdout -> StandardOutput=journal with
LogRateLimitIntervalSec/Burst and LogExtraFields=SOCIALWEB_VM=<name>,SOCIALWEB_HANDLE=<h>; journald's
caps bound disk use (a file sink, `append:`/`truncate:`, is unbounded and guest-controlled = disk DoS).
nodevm.Fatal reads via `journalctl --user -u <unit> -o cat` (systemd is a prerequisite; sdjournal needs
cgo). Firecracker's own log: `--log-path /dev/stderr`? -> journal too. [U] uid < 1000 system accounts'
user-manager output lands in the system journal (SplitMode=uid applies to uid >= 1000): readable by
the account only with group systemd-journal or `--user` journal - measure.

A-x-stop (graceful): controller: PUT /actions SendCtrlAltDel (x86_64: guest needs CONFIG_SERIO_I8042 +
CONFIG_KEYBOARD_ATKBD; aarch64: virtual power button, needs GPIO_PL061 + KEYBOARD_GPIO + a userspace
consumer like logind) [Firecracker docs/api_requests/actions.md] -> wait up to spec.stopGraceSeconds
(default 30) for unit inactive (firecracker exits on guest reset/shutdown) -> StopUnit (SIGTERM, VMM
exits at once) -> TimeoutStopSec -> SIGKILL. `systemctl --user stop` by an operator is a hard stop.

A-x-verdict (decision 3), evaluated in this order:
| observation | verdict | action |
|---|---|---|
| bus unreachable / D-Bus error | Unknown | retry with backoff, condition RuntimeObserved=Unknown, no writes to phase |
| status.runtime.hostBootID != current boot id | Lost (HostRebooted) | none to stop (reboot); finalizer path normal |
| unit NotFound | Lost (UnitGone) | - |
| unit failed / inactive | Lost (UnitFailed/OOMKilled/VMMExited from Result) | ResetFailedUnit after recording |
| unit active, InvocationID != recorded | Lost (IdentityMismatch) | StopUnit immediately |
| unit active, MainPID start ticks != recorded or pid not in unit cgroup | Lost (IdentityMismatch) | StopUnit immediately |
| unit active, identity matches, API socket unreachable/timeout | Unknown | retry; never Lost on socket alone |
| API GET / answers id != spec id, or state != Running | Lost (IdentityMismatch) | StopUnit immediately |
| all match, state Running | Running | - |

A-x-upgrade (end to end, Linux):
1. `socialweb-computer upgrade --binary <path>`: verify manifest + sha256 (+signature later); copy to
   `<state>/bin/<sha>/socialweb-computer`; run `<new> self-check` (version --json, assets extract +
   verify to runtime/<sha>/, check-host); refuse on failure.
2. Atomically retarget `<state>/bin/current` (symlink rename); record previous in `bin/previous`.
3. `systemctl --user restart socialweb-computer-gateway.service` (or bidder@<h>): old core gets
   SIGTERM (control-group): kcp (if in-process) drains, deno children exit; VM, Postgres, OpenBao and
   cluster-group units untouched (own cgroups).
4. New core: lock state dir, start/adopt kcp, adopt units by prefix (Verdict), reconcile, unseal any
   vault with a new InvocationID, BAO-SYNC pass, restart DenoPods on the new closure (0008).
5. GC: bin/<sha> and runtime/<sha> removed only when unreferenced by current/previous and by every
   loaded unit's ExecStart/Environment.
6. Rollback = retarget `current` to `previous` + restart. Cluster groups keep the old core until
   `units roll clusters`.

A-x-tests (RFP-spine respecting):
- Offline: fakes for hostsvc.Manager, vmruntime.Runtime, fc API, kcp. Verdict table one test per row.
- Live unit mechanics (livegate, hat-8): stand-in executables (sleep/python) in transient units under
  the real user manager - NOT guests, so allowed. Covers restart/upgrade/daemon-reload/daemon-reexec/
  failed-result visibility/OOM/limits/prefix isolation/GC.
- Any live GUEST only through the market network: requester runComputeContract -> RFP -> bid -> accept ->
  cloud-init, with `run bidder` as the bidder (0008 M4-MARKET-NETWORK-PORT). Delete/rewrite every item test
  that boots from a boot-spec or a fixture user_data (0007 M2-UNPRIVILEGED-RUNNER).
- Cross-OS: compile+vet gates only (B7).

---------------------------------------------------------------------------------------------------------

## B. Cross-OS abstraction layer (Go)

B1. Package list (new = N, rename/refactor = R, delete = X)

| package | role | per-OS files |
|---|---|---|
| internal/hostos (R) | primitives only: Lock/Unlock, ProcIdentity (ProcStart, Cmdline, Alive, Signal, Terminate), LocalPortRange, BootID(), DieWithParent, NewProcessGroup | lock_unix.go (`//go:build unix`, flock works on darwin+linux), proc_linux.go, proc_darwin.go (stub ErrUnsupported until 0012), proc_windows.go, portrange_{linux,darwin,windows}.go, bootid_{linux,darwin,windows}.go, sysproc_{linux,darwin,windows}.go |
| internal/hostos caps*.go (X) | QEMU()/NetworkMode()/Accel()/Firecracker() - dead since 9d85916; capabilities move to backend registries | delete caps_linux.go, caps_windows.go; check-host reads vmruntime.Capabilities() |
| hostos.DetachProcess (X) | only caller nodefirecracker/namespace.go:45; misnamed Pdeathsig | delete with the resident namespace verb |
| internal/hostsvc (N) | long-lived non-VM children as OS services: Postgres, OpenBao, cluster groups, kcp (if out-of-process) | hostsvc.go (interface, types), systemd_linux.go (go-systemd/v22/dbus, user manager), unsupported_other.go (`//go:build !linux`) returning ErrUnsupported; 0012 adds launchd_darwin.go, 0011 adds scm_windows.go |
| internal/vmruntime (N) | VM host/runtime interface + Verdict | vmruntime.go (interface, Verdict, VMSpec, RuntimeRef), firecracker_linux.go (composes hostsvc + guestnet + fcapi), unsupported_other.go; registry.go picks backends compiled for GOOS |
| internal/guestnet (N) | guest network provider | guestnet.go (interface), pasta_linux.go, unsupported_other.go |
| internal/guestdisk (N, from nodevm/disk.go) | clone (reflink on linux, copy elsewhere), size (truncate), seed drive (pure-Go vfat/iso "cidata") | clone_linux.go (FICLONE + SEEK_DATA - fixes F1 windows break), clone_other.go (plain copy), seed.go (pure Go, all OSes) |
| internal/guestimage (N, from nodefirecracker image store) | fetch/verify/store of CI-built images keyed by (hypervisor, arch, sha) | OS-neutral |
| internal/fcapi (R, from nodefirecracker/firecracker.go) | firecracker HTTP client over unix socket + config-file writer | `//go:build linux` |
| cmd/socialweb-vmexec (N) | one-shot: netlink tap+bridge in pasta's namespace, then execve firecracker | linux only; built as asset |
| internal/install (N) | content-addressed bin/<sha>, current/previous symlinks, GC against hostsvc.List | install_unix.go; windows later |
| internal/assets (from 0006 M1-ASSETS) | per GOOS_GOARCH embedded executables | assets_linux_amd64.go etc. (B6) |
| internal/nodevm, internal/nodefirecracker (R/X) | namespace.go, RunNamespace, WriteNamespaceRecord, FindVMM, WriteInstance deleted; spec constants that survive move to vmruntime/fcapi | - |
| cmd/socialweb-nodeboot (X) | replaced by controller prep + vmexec | - |
| cmd/socialweb-nodeimage (R) | moves to the image pipeline (CI/build host); `dev`-tag verb only | linux only |

B2. Interfaces (signatures)

```go
package vmruntime

type Verdict int
const ( VerdictUnknown Verdict = iota; VerdictRunning; VerdictLost )

type LostReason string
const (
    LostHostRebooted     LostReason = "HostRebooted"
    LostUnitGone         LostReason = "UnitGone"
    LostUnitFailed       LostReason = "UnitFailed"
    LostOOMKilled        LostReason = "OOMKilled"
    LostIdentityMismatch LostReason = "IdentityMismatch"
)

type Owner struct{ Handle string; HandleID string }

type VMSpec struct {
    Name, ID      string
    Owner         Owner
    VCPU          int
    MemMiB        int
    DiskMiB       int
    Image         guestimage.Ref
    UserData      []byte
    Network       guestnet.Plan
    StopGrace     time.Duration
    WorkDir       string
}

type Prepared struct{ Spec VMSpec; Files map[string]string }

type RuntimeRef struct {
    Backend    string
    Ref        string
    Instance   string
    PID        int
    StartTime  uint64
    HostBootID string
}

type Observation struct {
    Verdict Verdict
    Reason  LostReason
    Detail  string
    Ref     RuntimeRef
}

type Capabilities struct {
    Backend       string
    Hypervisor    string
    GuestArches   []string
    GracefulStop  bool
}

type Runtime interface {
    Capabilities() Capabilities
    Prepare(ctx context.Context, spec VMSpec) (Prepared, error)
    Start(ctx context.Context, p Prepared) (RuntimeRef, error)
    Inspect(ctx context.Context, want RuntimeRef, specID string) (Observation, error)
    Stop(ctx context.Context, ref RuntimeRef, grace time.Duration) error
    List(ctx context.Context, owner Owner) ([]RuntimeRef, error)
    Cleanup(ctx context.Context, p Prepared) error
}

var ErrUnsupported = errors.New("vmruntime: not supported on this OS")
func ForHost() (Runtime, error)
```

```go
package hostsvc

type ActiveState string
const ( Active ActiveState = "active"; Activating = "activating"; Deactivating = "deactivating";
        Inactive = "inactive"; Failed = "failed"; NotFound = "not-found" )

type Output struct{ Journal bool; File string; RateLimitBurst int; RateLimitInterval time.Duration }

type Limits struct {
    MemoryMax, MemorySwapMax uint64
    CPUWeight                uint64
    CPUQuotaPercent          int
    TasksMax                 uint64
}

type UnitSpec struct {
    Name        string
    Description string
    Exec        []string
    Env         []string
    WorkDir     string
    Output      Output
    Limits      Limits
    Restart     string
    KillSignal  syscall.Signal
    StopTimeout time.Duration
    After, Wants []string
    Slice       string
    Labels      map[string]string
    OOMKill     bool
}

type UnitStatus struct {
    Name         string
    State        ActiveState
    Result       string
    MainPID      int
    InvocationID string
    Exec         []string
    Env          []string
    Labels       map[string]string
    ActiveSince  time.Time
}

type Manager interface {
    Start(ctx context.Context, spec UnitSpec) (UnitStatus, error)
    Inspect(ctx context.Context, name string) (UnitStatus, error)
    Stop(ctx context.Context, name string) error
    ResetFailed(ctx context.Context, name string) error
    List(ctx context.Context, prefix string) ([]UnitStatus, error)
    Close() error
}

var ErrUnsupported = errors.New("hostsvc: no service manager backend on this OS")
var ErrUnreachable = errors.New("hostsvc: service manager unreachable")
func Open(ctx context.Context) (Manager, error)
```
Labels on Linux = LogExtraFields + an encoded Description suffix (transient units have no free-form
metadata); the authoritative owner key is the unit NAME prefix.

```go
package guestnet

type Plan struct {
    GuestIP, HostAddress, Gateway, NamespaceIP netip.Addr
    Prefix   int
    MAC      net.HardwareAddr
}

type Provider interface {
    Name() string
    Plan(index int) (Plan, error)
    Wrap(vmm []string, p Plan) ([]string, error)
}
```
Linux `pasta` provider: Wrap returns `[runtime/<sha>/pasta --config-net -q -f -a .. -n .. -g ..
--dns-forward .. -- runtime/<sha>/socialweb-vmexec <params>]`. If the network is made constant
(D-Q3), Plan ignores index and HostAddress is a per-host constant (TS reads it once at startup).

```go
package hostos

type ProcIdentity struct{ PID int; StartTime uint64; BootID string }
func Identify(pid int) (ProcIdentity, error)
func (want ProcIdentity) Matches(got ProcIdentity) bool
func BootID() (string, error)
```
(linux: /proc/<pid>/stat field 22 + /proc/sys/kernel/random/boot_id; pidfd_open held during checks
to defeat pid reuse between read and signal; darwin/windows stubs return ErrUnsupported until 0011/0012.)

B3. The Linux VM unit (no resident socialweb code)
- Controller (one-shot, in the core): reflink clone rootfs, truncate to DiskMiB (guest resizes its own
  fs via cloud-init resize_rootfs; host needs no e2fsprogs [U - spike: raw ext4 rootfs without a
  partition table grows with cc_resizefs on /dev/vda]), write seed drive `cidata.img` (pure-Go vfat with
  user-data/meta-data, NoCloud), write fc.json (boot-source, drives rootfs+cidata, machine-config,
  network-interfaces fctap0/MAC), write vmexec params.json, then hostsvc.Start.
- Unit `socialweb-vm-<hid>-<name>.service`: ExecStart pasta ... -- socialweb-vmexec params.json;
  vmexec: netlink tuntap fctap0 + br0 + enslave pasta link (the nodevm.WireNamespace logic, without
  exec'ing `ip`), then execve(firecracker --id <specID> --config-file fc.json --api-sock fc.sock
  --level Warning --log-path /dev/stderr). Properties: Restart=no, KillMode=control-group,
  KillSignal=SIGTERM, TimeoutStopSec=30, MemoryMax=mem+overhead, MemorySwapMax=0, OOMPolicy=stop,
  TasksMax, CPUWeight, StandardOutput=journal, LogRateLimit*, LogExtraFields, Slice
  socialweb-b-<hid>.slice, ExecStart NOT ignore-errors (F10).
- After Start: controller reads MainPID (pasta), finds firecracker pid as the single child in the unit
  cgroup with comm "firecracker", records RuntimeRef{Ref: unit, Instance: InvocationID, PID: fc pid,
  StartTime, HostBootID}, polls GET / until state Running within boot timeout.

B4. Unsupported verbs: two layers.
- Not compiled: every OS-specific backend body lives in `_linux.go` (go-systemd, pasta, firecracker,
  netlink). Darwin/windows binaries carry none of it (size, attack surface).
- Named refusal: the CLI tree is identical on every OS; a verb whose backend is absent gets
  `ErrUnsupported` from `vmruntime.ForHost()` / `hostsvc.Open()` and exits 2 with
  `"run bidder: no VM runtime on darwin/arm64 in this build (plan 0012)"`. A registry
  `capabilities.go` maps verb -> required backends so `--help` can mark unavailable verbs.

B5. OS-neutral kcp types (ComputeVM; NodeClaim unchanged except Orphaned)
```
status:
  phase: Pending|Preparing|Booting|Running|Stopping|Lost|Failed
  lostReason: string
  observedGeneration: int
  network: {guestIP, hostAddress, prefix, mac}
  image: {ref, sha256, hypervisor}
  runtime: {backend: string, ref: string, instance: string, pid?: int, startTime?: string, hostBootID: string}
  bootedAt, message
  conditions: [Allocated, Prepared, Started, RuntimeObserved(True|False|Unknown), GuestReported]
  market: {onNetworkUri, address, hostKeys[], reportedAt}
```
backend is a free string (no enum) so adding launchd-vz / hyperv needs no schema revision; startTime is
a string (opaque per OS: Linux clock ticks, Windows FILETIME, darwin timeval). Postgres/OpenBaoServer
status similarly `runtime{backend, ref, instance, pid?, startTime?, hostBootID}` instead of `unit, pid,
startTicks`.

B6. Per-OS embedded assets at compile time
- `internal/assets/assets_linux_amd64.go`: `//go:build release && linux && amd64` + `//go:embed
  blobs/linux_amd64/*.zst` + `var platform = manifest{...}` (one file per GOOS_GOARCH, sshai
  deno_embed_linux_amd64.go pattern).
- `internal/assets/assets_dev.go`: `//go:build !release` resolves explicit paths (never PATH).
- `internal/assets/assets_release_unsupported.go`: `//go:build release && !((linux && (amd64||arm64)))`
  -> empty manifest; plans 0011/0012 replace with their own files.
- Linux set: firecracker, pasta, socialweb-vmexec, k3s, kine, bao, postgres (+initdb), external-secrets,
  dumbpipe, deno, (kcp if out-of-process). Not in the set: guest images (fetched, pinned), psql
  (replaced by pgx), e2fsprogs/debugfs/cpio/xz/zstd/ip/socat/yq/kubectl/openssl (deleted by design).
- A test `TestEveryReleaseEmbedFileNamesExactlyItsPlatformsAssetSet` reads versions.json.

B7. CI proof that darwin/windows build and vet without implementing them
- check.sh --fast adds, before tests: for t in darwin/arm64 darwin/amd64 windows/amd64 windows/arm64
  linux/arm64: `GOOS= GOARCH= go build ./... && go vet ./...` (default, non-release tags). go vet also
  compiles `_test.go` files, which is what catches F1's apigen Setpgid test.
- An import-boundary test (`go list -deps -json` per GOOS) asserting OS-neutral packages (api, kcpapply,
  computevm controller core, vmruntime interface) never import go-systemd, netlink or fcapi.
- First commit of 0006 makes these green today: add hostos darwin twins/stubs, split nodevm/disk.go
  clone into clone_linux.go/clone_other.go, tag apigen/live_upgrade_test.go `//go:build linux`.

B8. TypeScript seam: none needed in hono-compute-provider's ComputeProvider ABC (mod.ts:39-55 is
OS-free). compute-provider-kcp writes ComputeVMs; the host OS is invisible to it except
`status.network.hostAddress` (or a constant handed in by cli-args-env). compute-provider-firecracker
stays Linux/docker-only (kept per user). The guest image choice per hypervisor is Go-side.

B9. Naming (shared bidder account, handle-namespaced)
- account `swc-bidder` (bidders), `socialweb` (gateway); both linger.
- `socialweb-computer-bidder@<handle>.service` (user unit file, installed under the account)
- `socialweb-vm-<hid>-<name>.service`, `socialweb-bao-<hid>.service`, slice `socialweb-b-<hid>.slice`
- gateway: `socialweb-kcp.service` (if out-of-process), `socialweb-bao-root.service`,
  `socialweb-pg-<clusterKey>.service`, `socialweb-cluster-<clusterKey>.service`
- market networks: `socialweb-market-network-<runid>-...` never matched by production prefixes.

---------------------------------------------------------------------------------------------------------

## C. Revised item lists

Global: both plans keep `queued` behind 0005 (all 0005 items/ACC are done in the file - verify the
prod readings, then unqueue). Items marked (L) are Linux-executed; (S) build seams only.

### Plan 0006 (revised)

| id | title | decision (one paragraph, condensed) | depends_on | test_first |
|---|---|---|---|---|
| M0-XOS-GATE (new, S) | darwin/windows build+vet green and gated | Fix F1 (hostos darwin stubs, unix lock, clone split, tag apigen test); add the GOOS matrix + import-boundary test to check.sh --fast; delete hostos caps*.go and rewrite ARCHITECTURE "host platform" to the seam table. | [] | check.sh step red on today's tree (F1 errors recorded), then green |
| M0-KCP-SPIKE (rewritten) | in-process vs out-of-process kcp, measured | Scope per A-x-kcp; recommend one route with numbers. | [] | none (measurement) |
| M0-HOST-CONTRACT (new, L) | the Linux host contract, probed | Minimum host: cgroup v2, systemd property probe (A-x-systemd), linger, user@ delegation cpu/memory/pids, unprivileged userns incl. Ubuntu AppArmor + Fedora SELinux with pasta from runtime/<sha>, kvm group visible to the user manager, /dev/net/tun, boot_id. Written into check-host. | [] | check-host TestEachRequiredSystemdPropertyIsProbedNotVersionParsed; live TestEmbeddedPastaCreatesAUserNamespaceHere |
| M0-SIZE-BUDGET (rewritten) | linux-only budget; darwin/windows seam-only | linux/amd64+arm64 budgets; matrix records darwin/windows as "seam (0011/0012)". | [M0-KCP-SPIKE] | TestTheReleaseBinaryFitsItsSizeBudget |
| M1-CLI | unchanged + verbs install/upgrade/rollback/units/internal vm-exec; unsupported -> exit 2 naming plan | | [] | as before + TestAVerbWithNoBackendOnThisOSExits2NamingItsPlan |
| M1-K8S-FORK / M1-KCP-INPROC / M1-KCP-PORT0 | as before IF M0 picks in-process; else replaced by M1-KCP-UNIT (kcp release binary asset in socialweb-kcp.service, ports leased, flock) | | [M0-KCP-SPIKE] | as before / TestACoreUpgradeLeavesKcpsInvocationIDUnchanged |
| M1-ASSETS (rewritten) | per-GOOS_GOARCH release embeds, dev resolver, runtime/<sha> GC by unit references | B6 | [M0-SIZE-BUDGET] | + TestARuntimeDirNamedByALoadedUnitIsNeverCollected |
| M1-INSTALL (new, L) | content-addressed core install, current/previous, rollback, one-time `install gateway|bidder` (account, linger, kvm, node password, user unit file) | A-x-upgrade steps 1-2,5-6 | [M1-ASSETS, M1-CLI] | TestUpgradeRefusesABinaryWhoseManifestDoesNotMatch, TestRollbackRetargetsCurrentToPrevious |
| M1-HOST-INVENTORY (rewritten) | every exec gets a fate incl. "deleted by design" | adds debugfs, e2fsck, resize2fs, cpio, du, chroot, mount, go, ip, apt-get | [] | as before |
| M2-HOST-UNITS (was M2-HOST-SCOPES, L) | hostsvc over the USER manager, transient SERVICES | B2 hostsvc; prefix ownership; failed-result kept; ResetFailed after record | [M1-ASSETS] | live stand-ins: TestAUnitSurvivesSystemctlRestartOfItsStarter, ...DaemonReloadAndReexec, TestAFailedUnitsResultIsReadableUntilReset, TestUnitsOfAnotherPrefixAreNeverListed; offline fakes |
| M2-POSTGRES (kept CRD, L) | Postgres object -> socialweb-pg-<key> unit | relocatable PG18 linux build chosen + collation/ICU check; pgx replaces psql; readiness by controller probe; adoption by Verdict; status.runtime shape | [M1-HOST-INVENTORY, M2-HOST-UNITS, M2-APPLY] | as before + TestAnExistingFedora18DataDirOpensWithNoCollationMismatch |
| M2-OPENBAO-SERVER (kept CRD, L) | OpenBaoServer -> socialweb-bao-* unit; unseal on every new InvocationID | | [M1-ASSETS, M2-HOST-UNITS, M2-APPLY] | + TestAVaultRestartedBySystemdIsUnsealedOnTheNextPass |
| M2-MANIFESTS, M2-TEMPLATE, M2-APPLY | unchanged (M2-APPLY depends on a kcp loopback, either route) | | | |
| M3-BAO-SYNC | unchanged + runs on InvocationID change | | [M2-OPENBAO-SERVER, M3-CLUSTER-UNITS, 0005:M3-BAO-SYNC] | |
| M3-FOLD (rewritten) | fold gateway, clusterprovider, vault, check-host; DELETE nodeboot; nodeimage -> dev-tag verb; CCM/tenantcredential stay `internal` re-execs | | [M1-CLI] | as before minus nodeboot/nodeimage |
| M3-CLUSTER-UNITS (was M3-CLUSTER-SCOPES, L) | cluster group = transient service in the gateway user manager, ordered after its pg unit (A-x-ordering); roll policy | | [M2-HOST-UNITS, M2-POSTGRES] | TestAGatewayRestartLeavesEveryClusterApiserverAnswering via `systemctl --user restart` + binary upgrade; TestAPostgresRestartDoesNotStopItsClusterGroup; TestAdoptionStartsOnlyTheMissingHalf (4 cases) |
| M3-SERVE-GATEWAY-KCP (reordered) | order: kcp -> sets -> root OpenBaoServer Ready -> bootstrap -> migrate -> gateway -> TLS -> clusterprovider -> door | | [kcp item, M2-APPLY, M2-OPENBAO-SERVER, M3-FOLD, M3-CLUSTER-UNITS, M3-BAO-SYNC] | as before |
| M3-HOST-INSTALL (rewritten) | gateway USER unit in `socialweb` account, default KillMode, Restart=always; deploy.sh = copy + `upgrade` | | [M3-SERVE-GATEWAY-KCP, M1-INSTALL] | TestTheGatewayUserUnitVerifies (systemd-analyze --user verify), TestTheUnitHasNoKillModeMixed |
| M4-BUILD | unchanged + kine CGO note; cross-OS gate lives in M0-XOS-GATE | | [M1-ASSETS, M3-FOLD, kcp item] | |
| M5-CUTOVER (amended) | + uid move, linger first, NodeLoss window (drain or grace) | | [M3-HOST-INSTALL, M4-BUILD] | + TestTheCutoverEnablesLingerBeforeTheFirstStart |
| M5-RETIRE | unchanged | | | |

Deleted/merged in 0006: M2-HOST-SCOPES -> M2-HOST-UNITS; M3-CLUSTER-SCOPES -> M3-CLUSTER-UNITS;
nodeboot fold (part of M3-FOLD) deleted.

### Plan 0007 (revised)

| id | title | decision | depends_on | test_first |
|---|---|---|---|---|
| M1-DECIDE | record ComputeVM + controller AND the no-wrapper invariant, user-manager placement, shared account, Verdict table | | [] | none |
| M1-FC-INVENTORY | + requirements in A (host address source, console, graceful stop, image provenance, seed drive, guest-reported host keys) | | [] | as before |
| M1-API (rewritten) | OS-neutral ComputeVM (B5), spec.owner.handle, userDataSecretRef gate (unless constant network), Lost reasons | | [M1-DECIDE, kcp item] | as before + TestTheComputeVMSchemaHasNoBackendEnum |
| M1-COMMON-EXTRACT | unchanged | | [] | |
| M1-GUEST-IMAGE (new, L) | CI-built images per hypervisor x arch, pinned kernel config (virtio-mmio, i8042/atkbd x86, gpio-keys arm64, cloud-init resize_rootfs), fetched+verified by guestimage | moves nodeimage off bidders | [0006:M1-ASSETS] | TestAnImageWhoseShaDiffersIsRefused; build: TestTheGuestKernelConfigHasTheGracefulStopDrivers |
| M2-VM-RUNTIME (new, replaces M2-UNPRIVILEGED-RUNNER, L) | vmruntime firecracker_linux: prep in controller, seed drive, vmexec + execve, pasta wrap, unit properties (B3), Verdict (A-x-verdict), graceful stop | | [0006:M2-HOST-UNITS, 0006:M1-ASSETS, M1-GUEST-IMAGE] | offline: one test per Verdict row, TestIdentityMismatchStopsTheUnitImmediately, TestAnUnreachableBusIsUnknownNeverLost; live stand-in: TestTheVMUnitsProcessTreeHasNoSocialwebCode (stand-in VMM binary, assert /proc tree = pasta -> stand-in) |
| M2-BACKPORT (amended) | parity boundary = VMSpec + seed bytes + fc.json; host keys guest-reported | | [M1-FC-INVENTORY, M1-COMMON-EXTRACT, M2-VM-RUNTIME] | as before |
| M2-CONTROLLER (amended) | Verdict table, boot-id Lost, mismatch stop, failed-unit reset, handle-prefixed orphan GC (10 min), never other handles/market networks | | [M1-API, M2-VM-RUNTIME, 0006:M2-APPLY] (0008 fork optional) | as before + TestABootIDChangeMakesEveryVMLostWithoutStartingAnything, TestAnotherHandlesUnitIsNeverTouched |
| M3-TS-PROVIDER | + two-phase or constant host address; least-privilege deno permissions | | [M1-API, M1-COMMON-EXTRACT] | as before |
| M3-BIDDER-FLAG | + handle + guest-host-address options | | [M3-TS-PROVIDER] | as before |
| M4-BIDDER-VAULT | unit `socialweb-bao-<hid>`; unseal on InvocationID change | | [M3-BIDDER-FLAG, 0006:M2-OPENBAO-SERVER] | as before |
| M4-BIDDER-BAO-SYNC, M4-CONTRACT-RESTORE | unchanged | | | |
| M4-ONNETWORK | + guest-reported host keys into status.market | | [M3-TS-PROVIDER] | |
| M5-RUN-BIDDER (rewritten) | user unit `socialweb-computer-bidder@<handle>` in shared `swc-bidder` account (linger, kvm before first start), default KillMode, QR guard in Go, DenoPod without -A | | [M2-CONTROLLER, M3-BIDDER-FLAG, M4-*, 0008:M2-SERVICE-TEMPLATE, kcp item, 0006:M1-INSTALL] | TestTheBidderUserUnitVerifies, TestTheBidderDenoPodArgvGrantsNoRunFfiOrBusOrKvm, live TestDenoInTheBidderPodCannotOpenDevKvm |
| M6-BIDDER-UPGRADE | via `upgrade` + `systemctl --user restart`, market network | | [M5-RUN-BIDDER, 0008:M4-MARKET-NETWORK-PORT] | as before |
| M6-LIVE-RECOVERY (amended) | cases: kill -9, systemctl restart, logout-with-linger; all via market network | | same | |
| M6-NOSSH-JOIN, M6-GATEWAY-REPOPULATE | unchanged (dep renamed 0006:M3-CLUSTER-UNITS) | | | |

Deleted in 0007: M2-UNPRIVILEGED-RUNNER (-> M2-VM-RUNTIME; its guest-boot test deleted as an RFP-spine
violation).

### Moves to plan 0011 (Windows host) / 0012 (macOS host), each starting with M0-REVISIT
- 0011: hostsvc scm_windows.go (or Hyper-V/HCS-managed VMs - vmms owns the VM, so the invariant holds
  natively), vmruntime hyperv_windows.go, guestnet HNS/NAT, hostos.DieWithParent via job object
  (today a ponytail stub, sysproc_windows.go:16-20), proc_windows.go Cmdline via PowerShell per lookup
  (replace with NtQueryInformationProcess), caps WHPX assumption deleted, guest image target
  hyperv-x86_64, Windows asset set + budget, PG/bao on Windows if gateway ever runs there.
- 0012: hostsvc launchd_darwin.go (LaunchAgents in a logged-in vs LaunchDaemons with UserName for a
  headless lingering-equivalent), vmruntime vz_darwin.go using an external VMM asset (vfkit or a
  signed helper - Virtualization.framework needs cgo + com.apple.security.virtualization entitlement,
  which conflicts with CGO_ENABLED=0 for the core), guestnet vmnet/NAT, hostos proc_darwin via sysctl
  kern.proc.pid / kern.procargs2, boot id via kern.bootsessionuuid, guest image target vz-arm64,
  darwin asset set + budget. The old 0006 M0-SIZE-BUDGET "darwin client-only" text moves here as an
  input to M0-REVISIT.

---------------------------------------------------------------------------------------------------------

## D. Questions for the user (with recommended answers)

Q1. kcp in-process (decided 2026-10-07) or out-of-process in its own unit?
  Recommend: let M0-KCP-SPIKE decide with numbers, defaulting to OUT-OF-PROCESS (release binary asset,
  `socialweb-kcp.service` in the same user manager). Reasons: core upgrades stop restarting kcp; no
  whole-module fork onto 1.37-alpha; no process-global singleton risk for two kcps; port-0 handled by
  existing flags + leases. In-process stays the choice only if the spike shows < 1 day of fork fallout
  and two in-process kcps work.

Q2. The cluster group supervisor is socialweb code that keeps running the OLD core across upgrades.
  Accept "groups are rolled explicitly (`units roll clusters`, one at a time, each under the 90 s
  NodeLoss grace)" rather than restarted by every upgrade? Recommend: yes.

Q3. Make every guest's network identical inside its own pasta namespace (constant guest IP /
  gateway / host address, like slirp's 10.0.2.x) instead of leasing a /30 per guest?
  Recommend: yes, after a one-hour check that nothing needs distinct guest IPs (guests reach each
  other via meshd/iroh, the host only via pasta). It removes the network-index allocator, the
  "two VMs share an index" class of bugs, and the two-phase ComputeVM write (user_data enrichment
  becomes pure again). If no: keep the userDataSecretRef gate.

Q4. Guest images built in CI per hypervisor x arch and only fetched+verified on bidders (host /boot
  kernel no longer used; nodeimage leaves the bidder)? Recommend: yes; a `dev`-tag local build stays.

Q5. VM memory: MemorySwapMax=0 + OOMPolicy=stop (guest is OOM-killed cleanly -> Lost/OOMKilled) or
  allow swap (guest slows silently)? Recommend: swap off for VM units.

Q6. Guest console to the journal (rate-limited, size-capped, read via journalctl) instead of an
  unbounded per-VM file? Recommend: journal.

Q7. Seed transport: a separate pure-Go NoCloud `cidata` drive plus in-guest resize_rootfs, removing
  debugfs/e2fsck/resize2fs from hosts? Recommend: yes (also the only shape that ports to vz/Hyper-V).

Q8. Guest host-key scan becomes guest-reported (cloud-init module posts fingerprints with onNetwork)
  instead of a host-side scan inside the guest's namespace? Recommend: yes.

Q9. A long-lived VM unit should pin a tiny `socialweb-vmexec` helper, not the ~300 MB core binary
  directory? Recommend: yes (separate small asset).

Q10. Stated limit: with one shared bidder account, a VMM escape reaches every handle's keys on that
  host (same uid). Accept as a documented limit for 0007, with per-VM subuid isolation (pasta userns
  mapped to a subordinate uid) as a later hardening plan? Recommend: accept + later plan.

Q11. Minimum host: systemd >= 252 with probed properties, kernel >= 5.10 LTS (pidfd, cgroup v2),
  Fedora 40+/Debian 12/RHEL 9/Ubuntu 24.04 (with an AppArmor userns profile for the embedded pasta
  path installed by `install bidder`); Ubuntu 22.04 unsupported until measured? Recommend: yes.

Q12. Cutover (M5-CUTOVER) cold-starts every control plane outside the container: drain market nodes
  first, or raise NodeLoss grace for the window? Recommend: drain (release) market nodes before the
  cutover; idle clusters are at zero nodes anyway.

Q13. Bidder DenoPods drop `deno run -A` for an explicit least-privilege permission set (no run/ffi,
  read/write limited, deny the user bus and /dev/kvm)? Recommend: yes; it is the only enforceable
  boundary under a shared uid.

---------------------------------------------------------------------------------------------------------

Sources
- Firecracker actions (SendCtrlAltDel x86_64 i8042/atkbd, aarch64 power button):
  https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/docs/api_requests/actions.md
- Firecracker --config-file with --api-sock: https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/docs/getting-started.md
- Ubuntu 24.04 restricted unprivileged userns (AppArmor) context: https://discuss.linuxcontainers.org/t/error-with-rootless-podman/24250
- systemd D-Bus API (GetUnitByPIDFD exists; version not confirmed - not relied on):
  https://man.archlinux.org/man/org.freedesktop.systemd1.5.en
- Sibling live readings: scratchpad/linux-proofs/e0.txt, e1-private.txt, e2.txt, e2-flag.txt, e2m.txt
