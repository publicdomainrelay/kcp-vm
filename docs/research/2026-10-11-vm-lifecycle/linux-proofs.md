# Linux VM-lifecycle proofs for plans 0006/0007

Host `hat-8`, Fedora 44, kernel 7.2.9-200.fc44, systemd 259.9-1.fc44, Go 1.26.2, pasta 0^20261002.gcba3570, Firecracker v1.13.1. Run 2026-10-11 23:12-23:25 UTC.

- Scratch code: `linux-proofs/gosrc/` (module `swproof`). Binaries are in `linux-proofs/bin/`.
- Raw outputs: `linux-proofs/e*.txt`. Scripts: `linux-proofs/e*.sh`.
- Every test unit was named `swproof-*`. All were stopped or garbage-collected at the end: `systemctl --user list-units --all 'swproof-*'` returns 0 units, and no stand-in processes are left.
- The repo was not modified. `go.mod` and `go.sum` have the same sha256 before and after, and `git status` is unchanged by me. `docs/plans/0011-windows-bidder-hcs.yaml` appeared while I worked, but I did not create it.
- No guest was booted. Firecracker was only run with `--help` or `--version`, and in two non-boot modes:
  - the pre-boot API with no configuration;
  - `--config-file` pointing at a kernel that does not exist, which fails while parsing, before any VM is created.
- The real boot proof is left to the RFP-flow live tests (`runComputeContract` -> bid -> accept -> cloud-init).
- Grounding note: `scratchpad/lifecycle-linux.md` and `lifecycle-synthesis.md` **do not exist** in this scratchpad, so I grounded on the repo sources only.

## Summary table

| # | Claim tested | Verdict |
|---|---|---|
| E0 | Host has cgroup v2, delegation, a private socket, /dev/kvm, pasta and firecracker | PASS, with caveats: Linger=no on this host; Fedora `Delegate=cpu io memory` (+pids); SELinux enforcing |
| E1 | A VM unit started over D-Bus by a bidder unit survives bidder `restart`, `kill -9` and `stop` (same InvocationID and MainPID) | **PASS**, over both the session bus and the private socket |
| E1-neg | Plain children of the bidder (setsid+Pdeathsig, or setsid only) die with the bidder | **PASS**: they die in all 3 cases |
| E2 | The user manager accepts Restart/KillMode/OOMPolicy/MemoryMax/CPUQuota/TasksMax/ManagedOOMPreference/CollectMode, and they are enforced | PASS for memory, cpu and pids; **AllowedCPUs is silently ignored** (no cpuset delegated); IOWeight is written but has no effect (no BFQ/io.cost); **MemoryMax without MemorySwapMax=0 lets the VM swap** |
| E2-bug | go-systemd `PropExecStart(argv, true)` means "unclean exit is a failure" | **DISPROVEN**: `true` sets `ignore_errors=yes`, so a SIGKILLed VM reports Result=success and the unit is GC'd |
| E2-quirk | IOWeight and ManagedOOMPreference can be combined | **FAIL on systemd 259.9**: `ManagedOOMPreference=avoid\|omit` resets IOWeight to 100 (reproduced with plain systemd-run) |
| E3 | Identity check (pidfd + start ticks + GetUnitByPIDFD + InvocationID) works, and an unreachable bus maps to Unknown | PASS. The pidfd of a dead process returns `UnixProcessIdUnknown` from GetUnitByPIDFD; this must map to Lost, not Unknown |
| E4 | pasta -> namespace verb creates a tap, then execve; the tap survives; only pasta and the VMM remain | PASS: CAP_NET_ADMIN in pasta's userns is enough and /dev/net/tun is usable. The same works inside a unit, and the unit survives bidder restart |
| E4-sel | pasta can exec our binary directly | **FAIL under SELinux**: pasta_t is denied exec of user_tmp_t/bin_t on nosuid mounts. The `/bin/sh -c 'exec "$0" ...'` trampoline (as in `PastaArgv`) is **required** |
| E4-kill | Killing pasta tears down the namespace and the VMM | **DISPROVEN outside a unit**: the child survives, orphaned. Inside a unit it holds because KillMode=control-group reaps it |
| E4-term | `systemctl stop` stops the VM promptly | **FAIL for firecracker-like processes**: the VMM is PID 1 of pasta's PID namespace and firecracker has no SIGTERM handler, so SIGTERM is ignored and stop waits for TimeoutStopSec (45s here) before SIGKILL |
| E5 | `--config-file` boots immediately while `--api-sock` stays up; `host_dev_name` attaches to a pre-created tap | PASS (from source + non-boot runs). The socket path must be < 108 bytes; a stale socket file makes firecracker exit |
| E6 | `os.Executable()` after a rename-over returns the path; re-exec runs the NEW binary; `/proc/self/exe` runs the OLD inode | PASS, both confirmed. If the file was deleted rather than replaced, exec by path gives ENOENT |
| E7 | The repo builds for linux/darwin/windows | linux PASS. **darwin FAIL**: hostos has no darwin twins, which blocks 23 packages. **windows FAIL**: `nodevm/disk.go` has no build tag, which blocks 10 packages |

---

## E0 Host facts (`e0.txt`)

**Identity and init**
- uid 1000; groups wheel, ollama, keyd, docker. **Not in the kvm group.**
- systemd 259.9 (GetUnitByPIDFD is present, which needs 253 or later).

**cgroups**
- cgroup2fs. Root controllers: `cpuset cpu io memory hugetlb pids rdma misc dmem`.
- `user@1000.service` has controllers `cpu io memory pids`, and the same list in subtree_control. app.slice has the same.
- Fedora adds `00-uresourced.conf: Delegate=cpu io memory`. Stock systemd delegates only memory+pids, so **the prod host may lack cpu/io**. Check it there; I did not touch it.
- No cpuset is delegated.

**Linger and buses**
- Linger is **no** (State=active, 3 sessions). Prod needs a lingering dedicated account. I did not enable linger here.
- `$XDG_RUNTIME_DIR/systemd/private` exists (srwx------). The session bus `/run/user/1000/bus` exists.

**KVM and tun**
- `/dev/kvm` is `crw-rw-rw- root kvm`, so it is world-RW on this Fedora host. Do not assume that on prod.
- `/dev/net/tun` is 0666.

**OOM handling**
- systemd-oomd is active. User manager `app.slice` has **ManagedOOMMemoryPressure=kill** (80%/20s), and oomctl lists app.slice as monitored.
- DefaultTimeoutStopUSec is 45s in the user manager.

**Binaries**
- pasta is at `/usr/bin/pasta` (passt-0^20261002), label `pasta_exec_t`, and runs in domain `pasta_t`.
- firecracker v1.13.1 (static PIE, sha256 prefix 7d3924e0...) is at:
  - `/var/tmp/m3live-k8s-tree/.../hono-firecracker-standalone/firecracker`
  - `/var/tmp/socialweb-k8s-tree/...`
  - `/opt/socialweb-computer-bidder/org-root/hono-compute-provider/hono-firecracker-standalone/firecracker`
- It is not on PATH and not in ~/.cache.

**Kernel and security**
- There is no `kernel.unprivileged_userns_clone` or `apparmor_restrict_unprivileged_userns`; this is a Fedora kernel with AppArmor off.
- `user.max_user_namespaces=100000`. SELinux is **Enforcing**. LSMs: lockdown, capability, yama, selinux, bpf, landlock, ipe, ima, evm.
- `/tmp` is mounted **nosuid**.

**Go modules**
- go-systemd v22.7.0 was in the module cache. **godbus/dbus v5.1.0 was not**, so it was fetched from proxy.golang.org into the scratch module only.

## E1 Survival (`e1.sh`, `e1-session.txt`, `e1-private.txt`)

**Setup**
- The bidder stand-in is `bin/bidder`, which uses go-systemd `StartTransientUnitContext`.
- It is started with: `systemd-run --user --unit=swproof-bidder-<mode> -p Type=exec bin/bidder -mode <mode> -unit swproof-vm-1-<mode>.service`.
- The bidder starts `swproof-vm-1-*` (`/usr/bin/sleep infinity`). It also spawns two plain children: setsid+Pdeathsig=SIGKILL, and setsid only.

**Session-bus run (23:14:29Z)**

```
StartTransientUnit swproof-vm-1-session.service result="done" err=<nil> took=6.2ms
vm baseline: active running bff842fd272a4aaf822673f6aadd6b8e 48315 success
vm cgroup: .../app.slice/swproof-vm-1-session.service  bidder cgroup: .../app.slice/swproof-bidder-session.service
step1 systemctl --user restart -> vm unchanged  STEP1 PASS
  (restarted bidder: StartTransientUnit ... err=Unit swproof-vm-1-session.service was already loaded or has a fragment file.)
  plain children of first bidder: 48326 DEAD, 48327 DEAD
step2 kill -9 MainPID -> vm unchanged  STEP2 PASS ; bidder: failed failed ... signal ; children DEAD, DEAD
step3 systemctl --user stop -> vm unchanged STEP3 PASS ; children DEAD, DEAD
```

**Private-socket run (`-mode private`)**
- It connects with `sd.NewConnection(func(){ dbus.Dial("unix:path=$XDG_RUNTIME_DIR/systemd/private"); Auth(EXTERNAL uid) })`, with no Hello.
- STEP1, STEP2 and STEP3 all PASS. JobRemoved signals arrive on the private connection (`result="done"`).
- go-systemd has no constructor for the *user* private socket:
  - `NewUserConnectionContext` goes to the session bus.
  - `NewSystemdConnectionContext` is hardwired to `/run/systemd/private`.
- So the private path needs the 5-line custom dialer in `gosrc/busdial/busdial.go`.

**Verdict.** PASS.
- Transient units are siblings in app.slice, so the bidder unit's KillMode=control-group cannot reach them.
- The negative case is also proven: plain children die on restart, on kill -9 (systemd reaps the failed unit's cgroup) and on stop, with or without Pdeathsig.
- A restarted bidder must **adopt**, not re-create. StartTransientUnit on an existing name fails with "already loaded".

## E2 Unit properties (`e2.sh`, `e2.txt`, `e2m.txt`, `e2-flag.txt`, `e2f.txt`)

**Readback** (`startvm -unit swproof-vm-2.service`, 23:15:24Z):

```
Restart=no KillMode=control-group OOMPolicy=stop MemoryMax=268435456 CPUQuotaPerSecUSec=200ms
TasksMax=64 ManagedOOMPreference=avoid CollectMode=inactive-or-failed AllowedCPUs=1 IOWeight=100(!)
kernel: memory.max=268435456  cpu.max="20000 100000"  pids.max=64  io.weight="default 100"
cpuset.cpus: No such file  (cgroup.controllers: cpu io memory pids)
xattr user.oomd_avoid="1" set on unit cgroup
taskset of MainPID: 0-15   <- AllowedCPUs accepted, silently NOT enforced
```

**Enforcement**
- **pids**: TasksMax=4 gives `pids.current=4 pids.max=4` and `/bin/sh: fork: retry: Resource temporarily unavailable`. ENFORCED.
- **cpu**: CPUQuota=20% on a busy loop for 3s gives `usage_usec 616757, nr_throttled 30, throttled_usec 2378345`. ENFORCED.
- **memory with MemorySwapMax=0** (touching 512M under a 64M MemoryMax) gives `Result=oom-kill ExecMainCode=2 ExecMainStatus=9`. Journal: "The kernel OOM killer killed some processes in this unit". ENFORCED; OOMPolicy=stop works.
- **memory with swap left at its default**: 256M touched under a 64M MemoryMax gives `active success memory.current=66990080 memory.swap.current=210948096`. The **workload was swapped, not killed**.
- **io**: IOWeight is written to io.weight, but every disk uses scheduler `none`, so it has **no effect**.
- **IOWeight quirk**: `ManagedOOMPreference=avoid` or `omit` together with `IOWeight=50` reads back IOWeight=100, in either order. `ManagedOOMPreference=none` keeps 50. Reproduced with plain `systemd-run --user`. This is a systemd 259.9 quirk.

**Restart=no plus a killed main process** (`e2-flag.txt`)

```
PropExecStart(argv,true):  ExecStart ignore_errors=yes -> after kill -9: not-found inactive success 0 0  (GC'd, failure invisible)
PropExecStart(argv,false): ExecStart ignore_errors=no  -> after kill -9: loaded failed signal 2 9
```

- **go-systemd's parameter name `uncleanIsFailure` is inverted.** It is sent as the D-Bus `ignore_failure` boolean.
- With CollectMode=inactive (the default) and correct flags, a failed unit stays loaded with `Result=signal|oom-kill|exit-code`, `ExecMainCode` and `ExecMainStatus` until `reset-failed`.
- With CollectMode=inactive-or-failed, the unit disappears immediately (`not-found`). Its Result can then only be had from a JobRemoved/PropertiesChanged subscription or from the journal.

**daemon-reload** (`e2f.txt`)
- A transient unit keeps the same MainPID and InvocationID across `systemctl --user daemon-reload`.
- Its definition lives in `/run/user/1000/systemd/transient/` on tmpfs, so it is gone after a reboot.

## E3 Identity (`e3.sh`, `e3.txt`)

Method `GetUnitByPIDFD (h) -> (o, s, ay)` is present. Over both the private socket and the session bus (`unixfd=true`):

```
pidfd_open ok; stat starttime(field22)=277273 (CLK_TCK=100); pidfd_send_signal(0)=ok after stat read
GetUnitByPIDFD: unit=swproof-vm-3id.service invocation=1aba3eac...   (identical to systemctl InvocationID)
GetUnitByPID(69044): same unit, same InvocationID
verdict Alive(same unit+invocation, pidfd-pinned)
```

Each failure mode maps to a verdict:

| Situation | Observed | Correct mapping |
|---|---|---|
| ticks mismatch (simulated PID reuse) | `start ticks mismatch -> Lost(pid reused)` | Lost |
| bus socket missing | `connect: no such file or directory` in 33us | Unknown |
| stale socket file | `connect: connection refused` in 40us | Unknown |
| bus accepts but never answers | ctx 2s timeout -> `use of closed network connection` | Unknown. **Must pass a ctx**, or godbus Auth blocks forever |
| held pidfd, process exited | poll readable=true; `pidfd_send_signal: ESRCH`; `/proc/<pid>/stat: ENOENT` | Lost |
| `GetUnitByPIDFD` with an exited pidfd | `org.freedesktop.DBus.Error.UnixProcessIdUnknown` | **Lost** (my classifier said Unknown; fix the mapping) |
| `GetUnitByPID` / `GetUnit` after stop | `NoUnitForPID` / `NoSuchUnit` | Lost |
| fresh `pidfd_open` of the old pid | ESRCH | Lost |

**PID-reuse reasoning.**
1. Order the check as `pidfd_open(pid)`, then read `/proc/pid/stat` start ticks, then `pidfd_send_signal(fd, 0)`.
2. If the signal call succeeds, the stat read came from the process the pidfd pins. A recycled pid would give ESRCH on the pinned fd, or a ticks mismatch.
3. From then on, `GetUnitByPIDFD(fd)` is race-free. systemd resolves the pidfd, not the number.
4. InvocationID distinguishes two runs of the same unit name.
5. Only transport errors (dial/auth/timeout) are Unknown. Everything the kernel or systemd answers definitively is Alive or Lost.

**Socket path length.** AF_UNIX paths are limited to 107 bytes. The first E3 run failed with `connect: invalid argument` on the 135-byte scratch path, and Python failed with `AF_UNIX path too long`. Any socket path the design builds under a long workdir hits this. See also E5c.

## E4 pasta + tap + execve (`e4.sh`, `e4.txt`, `e4b.sh`, `e4b.txt`, `e4-avc.txt`)

**Direct exec from pasta fails under SELinux enforcing**

```
pasta --config-net -q -f ... -- ./bin/tapexec tap0 ...  ->  "Failed to start command or shell: Permission denied" (execve EACCES)
AVC: { execute } pasta_t -> user_tmp_t ; { execute_no_trans } pasta_t -> bin_t (copy chcon'd to bin_t) ;
     { nosuid_transition } pasta_t -> unconfined_t   (/tmp is nosuid)
```

- With the shell trampoline it works: `pasta ... -- /bin/sh -c 'exec "$0" tap0 /bin/sh -c "$1"' ./bin/tapexec '<inner>'`. This is the exact shape of `nodevm.PastaArgv`.
- After `sh`, the context is `unconfined_t`.

**Interactive run (23:20:25Z)**

```
tapexec: pid=1 ppid=0 Uid: 0 0 0 0 | CapEff: 000001ffffffffff ; uid_map="0 1000 1"
tapexec: created persistent tap0, up; execve [/bin/sh -c ip -br link show tap0; ...; exec tapexec attach tap0]
tap0  DOWN  <NO-CARRIER,BROADCAST,MULTICAST,UP>      (exists after exec)
attach: pid=1 opened existing tap0 with IFF_TAP|IFF_NO_PI|IFF_VNET_HDR fd=4 (as firecracker does)
tree:  70962 pasta.avx2  \_ 70965 tapexec(attach)   -- one child, no grandchildren, no Go "launcher" left
ip -d link (nsenter): tun type tap pi off vnet_hdr on persist on ; state UP after attach
fds of final process: only its own /dev/net/tun (creator's tun fd was O_CLOEXEC and closed)
```

- CAP_NET_ADMIN in pasta's userns (root mapped to 1000) is enough for TUNSETIFF, TUNSETPERSIST and SIOCSIFFLAGS.
- `/dev/net/tun` is accessible.
- **pasta also creates a PID namespace**: the child is NSpid `71299 1`, so the VMM is PID 1 there.
- `kill -TERM pasta` gives pasta gone and **child STILL ALIVE**. `kill -9 pasta` gives the same: the child is orphaned and keeps its netns, but now has no uplink.
- `kill -9` of the child makes pasta exit with rc=137.

**Inside a user unit `swproof-vm-3.service`** (`e4b.txt`; Restart=no, KillMode=control-group, MemorySwapMax=0):
- **E4b-1**: The cgroup holds only `pasta` (MainPID) and `tapexec attach` (the VMM stand-in). Running `ident` on the **child** pid gives `GetUnitByPIDFD -> swproof-vm-3.service`, with the same InvocationID, so Alive.
- **E4b-2**: Bidder unit restart, kill -9 and stop: **PASS, PASS, PASS**. The VMM stand-in stays alive.
- **E4b-3**: `kill -9` of pasta (MainPID) gives unit `failed signal 2 9`, and systemd **killed the VMM stand-in** (control-group).
- **E4b-4**: `kill -9` of the VMM gives pasta exit 137, and the unit becomes `failed exit-code 1 137`.
- **E4b-5**, `systemctl stop`:
  - With a Go stand-in (SigCgt has SIGTERM) it took 0.03s.
  - With `sleep infinity` (SigCgt=0, the same as firecracker for SIGTERM; see E5) it hit the client `timeout 15` with the VMM **still alive**. That is the PID-ns init ignoring SIGTERM, so stop only completes at TimeoutStopSec (45s) with SIGKILL.

## E5 Firecracker `--config-file` / API (`e5.sh`, `e5.txt`, sources in `linux-proofs/fcsrc/`)

**Sources**: firecracker v1.13.1. Files: `src/firecracker/src/main.rs`, `src/firecracker/src/api_server_adapter.rs`, `src/vmm/src/signal_handler.rs`, `src/vmm/src/resources.rs`, `src/vmm/src/devices/virtio/net/tap.rs`, `src/utils/src/validators.rs`, `docs/getting-started.md`, `docs/api_requests/actions.md`, `docs/logger.md`, `docs/metrics.md`, `src/firecracker/swagger/firecracker.yaml`, all at `https://github.com/firecracker-microvm/firecracker/blob/v1.13.1/...`.

**Does `--config-file` boot immediately?** Yes.
- `run_with_api` binds the API socket first ("Listening on API socket"), then spawns thread `fc_api`, then for `Some(json)` calls `build_microvm_from_json`.
- That calls `vmm::builder::build_and_boot_microvm` ("Successfully started microvm that was configured from one single json").
- It then enters `run_microvm`, which keeps serving the API until shutdown.
- No `InstanceStart` is needed. The API stays up for `GET /` (InstanceInfo `{id,state,vmm_version,app_name}`), `PUT /actions` (`SendCtrlAltDel` on x86_64 only; the guest needs i8042/atkbd; it returns 400 pre-boot), and post-boot PATCHes.

**Non-boot run (23:22:53Z, pre-boot API only)**

```
GET /                -> {"id":"swproof-e5","state":"Not started","vmm_version":"1.13.1","app_name":"Firecracker"}
GET /machine-config  -> {"vcpu_count":1,"mem_size_mib":128,...}
PUT /actions SendCtrlAltDel -> 400 "not supported before starting the microVM"
SigCgt=0x41801449 -> HUP,ILL,BUS,SEGV,PIPE,XCPU,XFSZ,SYS ; **no SIGTERM handler** (matches register_signal_handlers())
```

**Parse failure with `--config-file`.** A config file naming a nonexistent kernel prints "Listening on API socket" and then `BuildFromJson(ParseFromJson(BootSource(InvalidKernelPath)))`, rc=1. This proves the socket is bound before the build.
- The JSON keys (`VmmConfig`, kebab-case) are: `boot-source`, `drives`, `machine-config`, `network-interfaces`, `logger`, `metrics`, `mmds-config`, `vsock`, `balloon`, `entropy`, `cpu-config`.
- Logger and metrics can be given in the file or as `--log-path`/`--metrics-path`, and can be configured only once.

**`--no-api`** requires `--config-file` (`MissingArgument("config-file")`, exit_code 153). There is then no socket at all: no GET /, no CtrlAltDel, no live inspection. **Do not use it.**

**`--id`** must be 1-64 chars of `[A-Za-z0-9-]` (Unicode alphanumeric). `_` is rejected (`InvalidChar('_',3)`) and 65 chars are rejected. The id is echoed in `GET /` and in every log line (`[swproof-e5:fc_api]`). It is a cheap identity cross-check that needs no bus.

**`host_dev_name`.**
- `Tap::open_named` does TUNSETIFF with `IFF_TAP|IFF_NO_PI|IFF_VNET_HDR` on the existing name, so it attaches to a pre-created persistent tap. E4's attach stand-in reproduces exactly this.
- The name must be 15 bytes or fewer (16 is rejected per the tap.rs test).

**Socket path rules.**
- An api-sock path longer than 107 bytes fails with `"path must be shorter than SUN_LEN"`, rc=1.
- If the api-sock path already exists (stale file), firecracker fails with `FailedToBindSocket`, rc=1. The launcher must unlink it first, as `RunVMM` does today.

**Timestamps.** Log timestamps are **local time with no offset** (`2026-10-11T16:22:53...` while UTC was 23:22).

## E6 os.Executable after rename-over (`e6.sh`, `e6.txt`)

```
[start] build=OLD os.Executable="./e6/nodeboot" inode=21002
(mv nodeboot.new nodeboot)   readlink /proc/PID/exe -> "./e6/nodeboot (deleted)"
[after-rename] os.Executable="./e6/nodeboot"   (Go strips " (deleted)")
exec(os.Executable()) -> [reexec-via-path] build=NEW inode=21010
exec("/proc/self/exe") -> [reexec-via-procself] build=OLD inode=21018 (still "(deleted)")
rm (no replacement): exec(os.Executable()) -> "no such file or directory"; exec(/proc/self/exe) -> OLD runs
```

**Implication for `cmd/socialweb-nodeboot/main.go:306`.**
- The `self` path is handed to pasta and resolved later through `sh -c 'exec "$0" ...'`.
- If an upgrade renames over the binary between the parent's start and pasta's exec, the namespace verb runs the **new** build, which is version skew with the parent.
- If the binary was deleted, it fails with ENOENT.
- `/proc/self/exe` cannot be used across pasta, because inside pasta's child it names `sh`/pasta.
- Use immutable versioned install paths (`.../releases/<version>/socialweb-nodeboot`, with a symlink swap for "current"), and resolve `EvalSymlinks(os.Executable())` once.

## E7 Build-tag sanity (`e7.txt`, `e7b.txt`, `e7c.txt`)

Commands: `GOOS=<os> GOARCH=<arch> go build -mod=readonly ./...` and `-gcflags=all=-e`. HEAD is f0460fd. No files were written; go.mod and go.sum hashes are identical before and after.

**linux/amd64: rc=0.**

**darwin/arm64: rc=1.**
- `internal/hostos` has only `_linux`/`_windows` twins. On darwin the only file is `hostos.go`.
- Undefined symbols:
  - `hostos.DieWithParent` (processrunner)
  - `hostos.Lock/Unlock/LockExclusive/LockShared` (portallocator, subnetallocator)
  - `hostos.LocalPortRange` and `probeListen` (portallocator has only `probe_linux.go`/`probe_windows.go`)
- Blocked packages (23): cmd/{cloud-controller-manager, clusterprovider, computer-kcp-check-host, nodeboot, nodeimage, nodeinit, tenantcredential, vault} and internal/{clusterprovider, clusterstack, esovault, liveports, materialiser, nodefirecracker, nodemarket, noderecord, nodevm, portallocator, portscan, processrunner, procgroup, subnetallocator, vaultcert}.

**windows/amd64: rc=1.**
- `internal/nodevm/disk.go` has **no build tag** but uses `unix.IoctlFileClone`, `unix.Seek`, `SEEK_DATA`, `SEEK_HOLE` and `ENXIO`.
- Blocked packages (10): cmd/{clusterprovider, computer-kcp-check-host, nodeboot, nodeimage, nodeinit, tenantcredential} and internal/{clusterprovider, nodefirecracker, nodemarket, nodevm}.

---

## Design consequences for plans 0006/0007

**Confirmed (the plan can state these as proven on Linux/systemd 259)**

1. **One VM per transient service in the user manager.** It is created by `StartTransientUnit` from the bidder and survives the bidder's restart, kill -9 and stop.
   - Plain children (Pdeathsig or setsid) cannot survive; units are the only mechanism.
   - A bidder that restarts must adopt existing `socialweb-vm-*` units (`ListUnitsByPatterns`), never re-create them. Re-creating fails with "already loaded".
2. **Connect over `$XDG_RUNTIME_DIR/systemd/private`** with a custom go-systemd dialer: EXTERNAL auth, no Hello.
   - It works for StartTransientUnit, JobRemoved, GetUnitByPIDFD (fd passing) and properties.
   - It needs no dbus-broker user instance. It needs `XDG_RUNTIME_DIR` set in the bidder unit.
   - Always pass a context with a deadline.
3. **Adoption record**: `{unit, InvocationID, pasta MainPID + start ticks, VMM pid + start ticks, firecracker --id}`.
   - Check order: pidfd_open, then stat ticks, then pidfd_send_signal 0, then GetUnitByPIDFD (unit+InvocationID match).
   - Map these to **Lost**: ESRCH, ticks mismatch, `UnixProcessIdUnknown`, `NoUnitForPID`, `NoSuchUnit`.
   - Map only dial/auth/timeout errors to **Unknown**.
   - GetUnitByPIDFD works on the VMM child as well as on MainPID.
4. **pasta -> `/bin/sh -c 'exec "$0" verb "$1"'` -> verb creates the tap and execs firecracker.** It leaves exactly `pasta` + VMM in the cgroup.
   - The tap persists across exec and firecracker's TUNSETIFF attach works.
   - **Keep the shell trampoline and say why**: SELinux pasta_t may not exec our binaries directly (AVCs recorded).

**Changes and additions the plan must make**

5. **Stop semantics.** The VMM is PID 1 of pasta's PID namespace and firecracker has no SIGTERM handler, so the default `systemctl stop` hangs until TimeoutStopSec.
   - The plan must specify graceful stop first: `PUT /actions SendCtrlAltDel` (x86_64, guest needs i8042), then wait for exit.
   - After that, either `KillSignal=SIGKILL`, or a short `TimeoutStopSec=` (e.g. 10-15s) plus `FinalKillSignal`. `ExecStop=` is not needed.
   - On aarch64 there is no CtrlAltDel. Use an in-guest agent shutdown or go straight to SIGKILL.
6. **Use `PropExecStart(argv, false)`.** go-systemd's bool means *ignore failure*. With `true`, VM crashes and OOM kills report `success`, and the unit vanishes.
   - Keep `CollectMode=inactive` (the default) so Result, ExecMainCode and ExecMainStatus can be read. The bidder records them and then calls `ResetFailedUnit`.
   - Alternatively, use `inactive-or-failed` only together with a PropertiesChanged subscription.
7. **Resource properties.**
   - `MemoryMax` = guest mem + VMM overhead, **plus `MemorySwapMax=0`**. Otherwise the guest silently swaps.
   - `OOMPolicy=stop`, `TasksMax`, `CPUQuotaPerSecUSec` and `Restart=no` are enforced.
   - Drop `AllowedCPUs`/cpuset: it is not delegated, so it is a silent no-op.
   - Drop `IOWeight`: it has no effect without BFQ or io.cost, and systemd 259.9 resets it when ManagedOOMPreference=avoid.
   - Prod's `Delegate=` must be verified to include cpu. Stock systemd delegates only memory+pids; Fedora adds cpu/io via uresourced. Otherwise CPUQuota is accepted but not enforced.
8. **systemd-oomd.** User `app.slice` is `ManagedOOMMemoryPressure=kill` on Fedora.
   - Set `ManagedOOMPreference=avoid` on VM units (the xattr `user.oomd_avoid=1` was observed), or put VMs in a dedicated user slice (e.g. `Slice=socialweb-vms.slice`) with explicit pressure policy.
   - Whether oomd honors avoid on user-owned cgroups was **not** stress-tested.
9. **Linger is mandatory.** This host has Linger=no. Without linger, the user manager and every VM unit stop at the last logout.
   - Transient units live on tmpfs and do not survive a reboot. After a reboot, adoption must yield Lost for everything, and the plan should say so.
10. **Socket paths must be 107 bytes or fewer.** Put the firecracker API socket at `$XDG_RUNTIME_DIR/swvm/<short-id>.sock` (or a short workdir path) and unlink it before exec. Firecracker refuses a stale file (`FailedToBindSocket`). Tap names must be 15 bytes or fewer.
11. **Firecracker invocation.** Use `--config-file cfg.json --api-sock <short> --id <vm-name, [A-Za-z0-9-]{1,64}>`.
   - The guest boots immediately. The socket is bound before the build.
   - `GET /` gives `{id,state,vmm_version}` for live verification. Cross-check `id` against the unit record.
   - **Do not use `--no-api`.**
   - The existing `RunVMM` (API-configure, then `WriteInstance`, then `Wait`, with a Go parent left in the tree) is replaced by execve. The instance record is then written by the bidder from GetUnitByPIDFD and pidfd data, not by the verb.
   - Firecracker log timestamps are local time without an offset. Run the unit with `Environment=TZ=UTC` or normalize when parsing.
12. **Binary path.** `os.Executable()` returns the path, not the inode. Rename-over upgrades make the namespace verb run a newer build than its parent, and a deletion gives ENOENT.
   - Install to immutable versioned paths, and put the resolved versioned path into ExecStart.
   - Do not rely on `/proc/self/exe` across pasta.
13. **Cross-OS build is broken today.**
   - darwin needs `hostos` twins: `lock_darwin.go`, `sysproc_darwin.go`, `portrange_darwin.go`, `proc_darwin.go`, `caps_darwin.go`, or `unix`-tagged shared files. It also needs `portallocator/probe_darwin.go`.
   - windows needs `internal/nodevm/disk.go` split or tagged (`disk_linux.go` + stub).
   - Plans that claim the macOS/Windows bidders build from this tree must include these.

**Disproven or corrected statements**
- "Killing pasta tears down the namespace and the VMM." False for a bare process: the VMM is orphaned and keeps running. It is true only inside a unit with KillMode=control-group, where MainPID exit triggers a cgroup kill.
- "`uncleanIsFailure=true` makes crashes visible." It is inverted.
- "`systemctl stop` stops the VM." It only does so after TimeoutStopSec, unless the stop path is designed (item 5).
- "The pasta command can be our binary directly." It cannot under SELinux enforcing; the shell trampoline is required.

**Not proven here (left to RFP-flow live tests)**: a real firecracker guest boot from `--config-file` through the full requester -> RFP -> bid -> accept -> cloud-init path; guest reachability through the relay; CtrlAltDel shutdown timing; oomd behavior under real pressure.

## E8 (live, RFP flow, 2026-10-12): guest reachability and CtrlAltDel stop timing

Run: alice-request.sh (requester) -> socialweb-computer-bidder@bobvmbuilder.bsky.social on hat-8, firecracker provider (runner-container era), guest fc-1379be663dd9-6e867147, node image sha256-5b823ce0...

Defect found first (run at 2026-10-11T23:53Z, guest fc-1379be663dd9-762b64d5): the guest's on-network report carried its bare hostname, and the requester polled `wss://fc-1379be663dd9-762b64d5/...` until its 300 s timeout.
- oidc-issuer-hono's reporter waited 60 s for /run/guest-fqdn, then fell back to `hostname`.
- The iroh listener that writes /run/guest-fqdn was enabled last in runcmd with a blocking `systemctl enable --now`, after a blocking `systemctl enable --now ssh` whose start job never reported Started in the first 60 s. Its unit was also ordered `After=ssh.service`.
- The requester classified any non-IP, non-iroh string as a relay FQDN.

Fixes, uncommitted on 2026-10-12:
- atproto-market: a single-label name is kind `hostname` and is skipped like an IP.
- cloud-init-common iroh module: `After=network-online.target` only; iroh is queued `--no-block` before ssh; ssh is started `--no-block`; /run/guest-fqdn is written atomically.
- oidc-issuer-hono reporter: no hostname fallback; waits 600 s; `After=iroh.service`; logs to journal+console; dumps jobs and unit status when it gives up.

Re-run at 2026-10-12T00:05Z: ticket reported 6.5 s after boot, vm_ssh_ready about 20 s after the request; ssh.service Started at 4.9 s.

CtrlAltDel, timed on that guest through its API socket:
- PUT /actions SendCtrlAltDel received at 00:06:21.6827 (firecracker log).
- The guest ran systemd shutdown to reboot.target ("reboot: Restarting system" at guest uptime 65.7 s).
- "Firecracker exiting successfully. exit_code=0" at 00:06:21.8913.
- Total 0.21 s for an idle guest. The guest kernel (host Fedora 7.2.8 config) has CONFIG_SERIO_I8042=y and CONFIG_KEYBOARD_ATKBD=y, and ctrl-alt-del.target -> reboot.target, which firecracker treats as VM exit.
- Grace window still to be measured with a workload (k3s agent + pods).

Defects in today's provider seen during the test:
- After firecracker exited, the nodeboot parents stayed alive and the runner container stayed Up. The bidder never noticed the VM died, and the requester's ssh session hung until it was closed by hand (then vm.delete 200).
- Destroyed guests' work dirs are never removed: 32 dirs, 63 GB on disk (1.6 TB apparent, sparse rootfs).

Plans 0007 M2-VM-RUNTIME / M2-CONTROLLER (unit failure -> Lost; work dir removed before the finalizer) address both.
