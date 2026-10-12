# VM lifecycle across Linux, macOS, Windows - synthesis

Research run 2026-10-09 by three web-research agents (one per OS). Their full per-OS reports
(lifecycle-linux.md, lifecycle-macos.md, lifecycle-windows.md, with URL citations) lived on a tmpfs
scratchpad and were lost when the host rebooted on 2026-10-11. This file restores the synthesis and
each agent's summary from the session transcript. Live Linux proofs: linux-proofs.md (same dir).

Constraints from the user: headless servers; VMs survive core-binary crash/upgrade and logout;
reboot = Lost; native per-OS mechanism behind one Go interface; VMs only.

## Who owns a running VM

| | Linux | macOS | Windows |
|---|---|---|---|
| VMM | firecracker + pasta | Virtualization.framework via vfkit (cgo helper asset); networking VZ NAT or gvproxy | Hyper-V HCS (computecore.dll via x/sys/windows), LinuxKernelDirect |
| VM lifetime bound to | the unit's process chain | the vfkit process | vmcompute service (ShouldTerminateOnLastHandleClosed=false) |
| Owner of that | transient service socialweb-vm-<name>.service | launchd job computer.socialweb.vm.<name>, system domain, UserName=_socialweb | HCS itself; no helper process |
| Manager | user manager of a lingering dedicated account | system domain through a root launchd-broker (template-locked) | HCS; bidder is an SCM service |
| Runtime root needed | no (install-time: linger, kvm group) | yes, tiny broker | unproven: Hyper-V Administrators vs SYSTEM |
| Reboot = Lost by | transient units vanish | plist outside LaunchDaemons, LaunchOnlyOnce, no KeepAlive | HCS systems are non-persistent |
| Adoption identity | pidfd + start ticks + GetUnitByPIDFD/InvocationID + firecracker GET / id | launchctl print pid + kinfo_proc start time + argv + control socket | HcsOpenComputeSystem(id) + RuntimeId + Owner + state |
| Deno dies with core | Pdeathsig | launchd kills the job's process group | Job Object KILL_ON_JOB_CLOSE (core joins at start) |
| Guest network | pasta, same unit | VZ NAT or gvproxy per VM | HNS NAT + endpoint pool (admin install) |
| Guest kernel | firecracker x86_64/arm64 | arm64, VZ virtio, uncompressed | Hyper-V drivers (hv_storvsc, hv_netvsc, vmbus) |
| Disk tooling | e2fsprogs asset | Go ext4 writer | hcsshim ext4/tar2ext4 (pure Go) + VHD footer |

## Cross-cutting conclusions

1. Adoption verdict is tri-state on every OS: Running | Lost | Unknown. Unknown (bus/socket/HCS
   unreachable) retries; only a definite identity mismatch is Lost. A false Lost costs a tenant a VM
   (LXD shipped this bug: VMs shown Stopped while running after an update).
2. No socialweb-computer code in a running VM's process tree. The service manager (or HCS) owns the
   VMM directly; our code only runs one-shot prep. Reason: today nodeboot's namespace child stays as
   firecracker's parent (internal/nodefirecracker/namespace.go RunVMM waits; firecracker has
   Pdeathsig), and nodeboot re-execs os.Executable() (cmd/socialweb-nodeboot/main.go:306), so a
   rename-over upgrade changes code inside a booting VM's chain. containerd needs shims because it
   assumes no service manager; here systemd/launchd/vmcompute reap, hold stdio, and each VMM already
   serves a control API (firecracker API socket, vfkit REST, HCS).
3. Content-addressed install paths (versions/<sha256>/, runtime/<sha256>/) on every OS; GC an asset
   or version only when no VM unit/job references it. Windows locks running exes; macOS must never
   rewrite a signed file in place.
4. Every OS needs a one-time admin install step; run bidder never escalates.
5. ComputeVM status must be OS-neutral (status.runtime {backend, ref, instance, pid?, startTime?}).
6. Guest image pipeline becomes per-hypervisor x arch. Apple Silicon is arm64-only -> the market
   needs an arch match, a protocol change the plans do not own.

## Linux agent summary (2026-10-09)

- VM = transient SERVICE (not scope) socialweb-vm-<name>.service via StartTransientUnit: Restart=no,
  KillMode=control-group, OOMPolicy=stop, MemoryMax/CPUQuota/TasksMax, ManagedOOMPreference=avoid,
  journald or file output tagged with ComputeVM UID. No dependency on the bidder unit.
- User manager of a dedicated lingering account over $XDG_RUNTIME_DIR/systemd/private. System-manager
  transient units from a non-root caller are root-equivalent (caller picks ExecStart and User=).
- Adoption: pidfd first, then start ticks, cgroup/GetUnitByPIDFD (systemd >= 253), firecracker GET / id.
- Corrections: bidder KillMode=mixed unnecessary (VM units are separate cgroups); kill -9 tests
  vacuous once VMs are units; with linger, "log in again" does not refresh groups (restart
  user@<uid> or reboot, install-time only); hostos.DetachProcess on Linux is Pdeathsig (misnamed);
  os.Executable returns the path so rename-over upgrades leak into in-flight boots.
- pasta must live in the same unit as the VMM. Do not register with systemd-machined (user instance
  only from systemd 259; two 2026 CVEs in registration). Refuse hosts without systemd/cgroup v2/linger.
- Prior art: copy one-unit-per-machine (systemd-nspawn@, vmspawn@); containerd/libvirt/Incus/conmon
  use daemon cgroup + KillMode=process (discouraged); Flintlock pid-file liveness = anti-pattern.
- Risks: false Lost; Fedora systemd-oomd kills user slices under pressure; Ubuntu 24.04 AppArmor
  userns restriction vs pasta; disabling linger kills VMs; console growth; user mode delegates only
  cpu/memory/pids.

## macOS agent summary (2026-10-09)

- VMM: Virtualization.framework, direct kernel boot (uncompressed arm64), raw ext4 root, cidata seed.
  VZ VM dies with its owning process, so each VM needs its own owner process: embed vfkit (cgo,
  signable on Linux with rcodesign; build needs no Mac).
- launchd: each VM its own job computer.socialweb.vm.<name> in the system domain as non-admin
  _socialweb; RunAtLoad, no KeepAlive, LaunchOnlyOnce, ProcessType=Interactive; plists outside
  /Library/LaunchDaemons so reboot = Lost. Bidder = LaunchDaemon with KeepAlive; launchd kills its
  process group on exit (Pdeathsig replacement for deno).
- Non-root cannot bootstrap into the system domain -> tiny root launchd-broker that only adds/removes
  computer.socialweb.vm.* jobs from a fixed template. Alternatives: install-time slot pool; gui/user
  domain (lifetime undocumented).
- Signing: ad-hoc + com.apple.security.virtualization suffices; avoid com.apple.vm.networking
  (restricted). Never rewrite a signed binary in place.
- Identity: launchctl print pid + unix.SysctlKinfoProc start time (no cgo) + argv + control socket.
- Headless: Linux guests need no auto-login; Lima LaunchDaemon reported working on M4 Mac mini
  macOS 26.5; daemons avoid the macOS 15 Local Network prompt; pmset no sleep + caffeinate assertion.
- Risks: Apple publishes no contract for VZ lifetime when owner dies (gate on live test per macOS
  release); graceful stop can be unavailable (hard-kill fallback); guest RAM outside launchd limits
  (admission control); arm64-only guests need an RFP arch field.

## Windows agent summary (2026-10-09)

- VMM: Hyper-V via HCS (computecore.dll) from Go with x/sys/windows (CGO_ENABLED=0), LinuxKernelDirect
  like WSL2/LCOW; fixed VHDs (ext4 + footer) via hcsshim ext4/tar2ext4. Rejected: QEMU/WHPX (VM dies
  with qemu), cloud-hypervisor (needs Linux host), VMMS as primary (persistent VMs).
- Lifetime: create with ShouldTerminateOnLastHandleClosed=false; vmcompute owns the VM; re-attach with
  HcsOpenComputeSystem(id). HCS VMs non-persistent -> reboot = Lost for free. No helper process.
- Adoption: state Running + recorded RuntimeId + Owner socialweb-computer/<handle>; orphans listed by Owner.
- Bidder: SCM service, ideally NT SERVICE virtual account in Hyper-V Administrators. Core joins a
  Job Object with KILL_ON_JOB_CLOSE at startup (Pdeathsig for deno); VMs are not our children.
- Networking: hcsshim hcn; admin install creates NAT network + per-index endpoint pool.
- Binary swap: versions\<sha>\, point service at new exe, exit non-zero for SCM restart.
- Findings: firecracker guest kernel lacks Hyper-V drivers; hcsshim HCS wrappers are internal/ (own
  ~300-line binding); caps_windows.go assumes WHPX + user networking; Windows 11 Home excluded.
- Unproven: HCS as non-admin Hyper-V Administrators member (every known HCS user runs as SYSTEM/admin);
  survival on creator kill -9 and vmcompute restart; HNS privileges and NAT conflicts with WSL/Docker;
  cloud-init end to end through runComputeContract.
