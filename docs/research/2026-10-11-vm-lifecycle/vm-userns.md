# M0-VM-USERNS: per-VM subuid user namespace for the VMM

Plan item 0007 M0-VM-USERNS, measured on hat-8 (Fedora 44, kernel 7.2.9, SELinux
enforcing, util-linux 2.41.5, pasta 0^20261002) on 2026-10-12 UTC. Sources,
scripts and raw readings are in [vm-userns/](vm-userns/).

## Choice

**Candidate A: `swc-userns-stage`, a 5.8 KB static, libc-free, single-threaded C
program** ([vm-userns/stage/stage.c](vm-userns/stage/stage.c)). It is the first
word of the VM unit's ExecStart:

```
swc-userns-stage <outer-uid> <outer-gid> <count> <inner-id> /usr/bin/pasta [pasta args] -- /bin/sh -c 'exec "$0" "$@"' <vmexec/nodeboot> ...
```

1. forks a helper before anything else (the helper blocks on a pipe);
2. `unshare(CLONE_NEWUSER)` on itself (legal: one thread);
3. the helper runs `/usr/bin/newuidmap <stage pid> 0 <outer-uid> <count>`, then
   `newgidmap` the same way, and exits; the stage reaps it;
4. `setresgid(inner)`, `setresuid(inner)` with NO `setgroups` call, so the
   supplementary groups (kvm on a host where /dev/kvm is 0660) survive;
5. `execve` pasta. The stage leaves no process: the unit's tree after start is
   pasta + the VMM.

Slot k of the account's range maps inner `0..count-1` to
`base + k*count ..`; the VMM's host uid is `base + k*count + inner`.

## Readings (stand-ins)

Stand-in VMM ([gosrc/standin](vm-userns/gosrc/standin/main.go)): creates a
persistent tap `fctap0`, opens /dev/kvm, `KVM_GET_API_VERSION` + `KVM_CREATE_VM`,
probes bidder files, ignores SIGTERM. Run as stage -> pasta (`--config-net`) ->
`/bin/sh` trampoline -> stand-in, in transient units of the range account's
systemd user manager and as a plain process group (the proc backend's shape).
Slot size 32768 (the only range there is holds 65536, so two slots).

| | A: swc-userns-stage | B: `unshare --map-users --map-groups --setuid --setgid` | B2: `unshare --keep-caps` + `setpriv --keep-groups` | G: Go `unshare(CLONE_NEWUSER)` |
|---|---|---|---|---|
| Works | yes | yes | yes | **EINVAL** (threads=4-5) |
| Unit tree after start | pasta + VMM | pasta + VMM | pasta + VMM | - |
| Supplementary groups kept | **yes** | **no** (`--setgid` calls `setgroups(0)`) | yes | - |
| /dev/kvm open + KVM_CREATE_VM | OK | OK | OK | - |
| tap in pasta's netns | OK (`tun type tap persist on`) | OK | OK | - |
| unit start -> stand-in ready | 142-172 ms | 147-201 ms | 171 ms | - |
| added per exec (200 x /bin/true; direct 405 us) | +1.8 ms | +2.2 ms | +2.9 ms | - |
| new host prerequisite | none (pinned asset) | util-linux unshare >= 2.38 | unshare + setpriv | - |
| SELinux denials | none | none | none | - |

Both slots, both backends, every candidate:

- VMM host uid: slot 0 `656360`, slot 1 `689128` (base 655360, inner 1000):
  disjoint. The stand-in sees itself as uid 0 of pasta's namespace
  (`uid_map 0 1000 1` relative to the stage's namespace).
- Probes of `~/.ssh/config`, the bidder's state dir and `/etc/shadow`: all
  EACCES.
- `find / /home /var /tmp /opt -xdev -uid <vmm uid>` (readings dir excluded):
  none for both slot uids.

Readings: [readings/](vm-userns/readings/) (`host.txt`, `unit-*.txt`,
`proc-*.txt`, `latency.txt`, `owners-avc.txt`, `pasta-root.txt`).

### Findings that shape M2-VM-RUNTIME

1. **The stage must not hand pasta inner uid 0.** pasta confined as `pasta_t`,
   started as root of the subuid namespace, fails with
   `Couldn't write to /proc/<pid>/uid_map: Operation not permitted` /
   `Can't set GID to 0 in userns` (with or without `--runas`), no AVC logged
   (dontaudit). The same binary copied out of `pasta_exec_t` works, so it is
   the policy. Started as a non-root inner uid it behaves as a rootless pasta
   does: its own namespace maps `0 -> inner`. So the stage takes `inner-id`
   (1000 here), and the VMM's host uid is `base + k*size + inner`.
2. pasta replaces a child's stdout/stderr that is a journal socket with
   `/dev/null` inside its sandbox; the VMM's console must go to a file it
   opens (firecracker's log/serial path), not to the unit's StandardOutput.
3. /dev/kvm and /dev/net/tun are 0666 on this Fedora host, so the kvm-group
   question does not show here; A keeps supplementary groups (`Groups: 1002`
   on the host, `65534` inside), which is what a 0660 root:kvm host needs. B
   loses them.
4. `unshare(CLONE_NEWUSER)` from Go is EINVAL (multi-threaded), confirmed.

## RFP-flow guest

RFP_GUEST_PLACEHOLDER

## Commands

```
# stand-ins (as an account with a subuid range; here test-0001 stands in)
sudo loginctl enable-linger test-0001        # its user manager must outlive sudo's session
gcc -Os -static -nostdlib -ffreestanding -fno-stack-protector -fno-builtin -fno-pie -no-pie \
    -o swc-userns-stage vm-userns/stage/stage.c
(cd vm-userns/gosrc && CGO_ENABLED=0 go build -o standin ./standin && CGO_ENABLED=0 go build -o gounshare ./gounshare)
vm-userns/standin.sh <outdir>

# one guest through the RFP flow in this task's bed
eval "$(scripts/task-tree.ts env vm-userns)"
vm-userns/bed.sh                              # accept.sh with BED_ID=vmuserns1 and FC_USERNS_*
vm-userns/capture.sh /var/tmp/swc-vmuserns1 test-0001 <out>
```

Provider path used: `hono-compute-provider/lib/microvm-firecracker-userns`
(`createUsernsFirecrackerMicrovm`), selected in the bidder by
`--compute-provider-firecracker-userns-stage` (+ `-account`, `-runtime-dir`,
`-slot-size`); the bed passes them from `FC_USERNS_*`. It stages nodeboot,
firecracker, the stage and the image (reflink) into a runtime dir the slot
uids can read, grants the account and the VMM uid the work dir by ACL, and
starts `socialweb-vm-<name>.service` in the account's user manager running
`nodeboot -foreground` with `pasta: <stage>` and
`pasta_args: [base, gbase, size, inner, /usr/bin/pasta, --map-host-loopback, 192.0.2.1]`.
nodeboot grants the VMM uid its own work files by POSIX ACL (`vmm_uid`,
`nodevm.GrantVMMFiles`, pure Go xattr, no host tool). The guest reaches the
host at 192.0.2.1, which pasta maps to host loopback; the container path's
`-t 0.0.0.0/22` forward is dropped (ssh goes through the relay).

## Host prerequisites / what install must add

The invoking user (johnandersen777) has **no** /etc/subuid or /etc/subgid
entry, and nothing was added. `test-0001:655360:65536` (already present) stood
in for the shared account. `install bidder` must add, for the account that
runs the VM units (swc-bidder):

```
/etc/subuid: swc-bidder:<base>:16777216     # 65536 ids x 256 slots
/etc/subgid: swc-bidder:<base>:16777216
loginctl enable-linger swc-bidder
```

plus `/usr/bin/newuidmap` and `/usr/bin/newgidmap` (shadow-utils, setuid or
`cap_setuid`/`cap_setgid`), which check-host must verify. Candidate A needs
nothing else.

## Fallback

- **B** (util-linux `unshare --map-users/--map-groups --setuid <inner> --setgid <inner>`)
  works and is the fallback if the stage asset is refused; it costs a host
  prerequisite (util-linux >= 2.38) and drops supplementary groups, so on a
  host whose /dev/kvm is 0660 root:kvm it must be B2 (`--keep-caps` +
  `setpriv --reuid --regid --keep-groups`, one more exec).
- If neither keeps /dev/kvm on a target host: an ACL entry on /dev/kvm for the
  reserved range, written by install (deviation, not needed on hat-8).
- A distro refusing unprivileged user namespaces: check-host refuses the host
  by name; no silent fallback to running VMMs as the shared uid.
