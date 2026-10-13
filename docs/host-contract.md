# The Linux host contract

A Linux host that runs `socialweb-computer install gateway` or `socialweb-computer install bidder`
must satisfy every check in the table below. `socialweb-computer check-host` probes the host as the
installed account and prints each check with its fix. `install` runs the same probe as the account
after it creates the account, and install fails when a check fails.

## Run the check

Run `check-host` as the account that runs the host's components (gateway: `socialweb`, bidder:
`swc-bidder`). Do not run it as root: the `account` check fails when the effective user is root.

```
socialweb-computer check-host -role gateway -state-dir <state>
socialweb-computer check-host -role bidder -state-dir <state>
```

`<state>` is the account's state dir. `install` writes it into the account's config; `check-host`
reads `-state-dir`, or the `SOCIALWEB_STATE_DIR` environment variable when `-state-dir` is not
passed.

| Option | Required | Default | Meaning |
| --- | --- | --- | --- |
| `-role` | yes | none | `gateway` or `bidder`; selects the check set |
| `-state-dir` | yes | `SOCIALWEB_STATE_DIR` | the account's state dir, for the probe output and the POSIX ACL check |
| `-asset-cache` | no | `<state-dir>/runtime` | the asset extraction root |
| `-pasta` | no | none | the pasta executable for a dev build; a release build extracts the embedded one |
| `-json` | no | off | print the results as JSON |

The command prints one line per check, in table order. The status is `OK`, `FAIL` or `INFO`. A `FAIL`
line is followed by a `fix:` line.

```
OK    account                      socialweb (uid 1000)
FAIL  dev-kvm                      /dev/kvm does not exist
                                   fix: enable hardware virtualization and load kvm_intel or kvm_amd (root); never chmod /dev/kvm
```

The command succeeds when no check has status `FAIL`: every line is `OK` or `INFO` and the exit code
is 0. On a failure it names the failed checks on stderr as
`check-host: the <role> host contract is not met: <names>`.

| Exit code | Meaning |
| --- | --- |
| 0 | The contract holds. No check failed. |
| 1 | One or more checks failed. The names go to stderr. |
| 2 | Usage error: a missing or unknown `-role`, a missing `-state-dir`, or an unknown flag. |

Pass `-json` to print the results as a JSON array. Each element is an object with `name`, `status`
(`ok`, `fail` or `info`), `detail`, and `fix` (present only on a failed check).

## Checks

`Applies to` is `both` for the gateway and the bidder. The rows are in the order the report prints.

| Check | Applies to | Passes when | If it fails |
| --- | --- | --- | --- |
| account | both | The command runs as the host account, not as root | Run `check-host` as the account; `install` runs it as the account after it creates the account |
| systemd-version | both | The account's user manager answers on its private socket and reports systemd 253 or newer (GetUnitByPIDFD) | Upgrade the host to a distribution that ships systemd 253 or newer, and enable linger so the user manager runs |
| probe-unit-properties | both | A transient `socialweb-probe-<rand>.service` starts with every property the runtime sets (MemoryMax, MemorySwapMax=0, OOMPolicy, TasksMax, CPUQuota, CPUWeight, CollectMode, After, Wants, Slice, KillSignal, TimeoutStopSec, StandardOutput=append:, plus FileDescriptorStoreMax for the gateway) and the user manager reads every one back | Use a host whose systemd (253 or newer) keeps every property the runtime sets |
| cgroup-v2 | both | `/sys/fs/cgroup` is the cgroup v2 unified hierarchy | Boot the kernel with `systemd.unified_cgroup_hierarchy=1` (the default on current distributions) |
| user-manager-delegation | both | `user@<uid>.service` `cgroup.controllers` include `cpu`, `memory` and `pids` | Run `install`, which writes the `Delegate=cpu memory pids` drop-in; then restart `user@<uid>` or reboot the host |
| linger | both | `/var/lib/systemd/linger/<account>` exists | Run `install`, which enables linger once the account, its groups and the delegation drop-in are in place |
| runtime-dir-socket-length | both | `XDG_RUNTIME_DIR` plus the longest runtime socket path is under 108 bytes | Use the default `/run/user/<uid>` runtime directory |
| boot-id | both | `/proc/sys/kernel/random/boot_id` is readable | Mount `/proc` normally; reboot detection reads `boot_id` |
| dev-kvm | bidder | `/dev/kvm` opens read-write as the account | Enable hardware virtualization and load `kvm_intel` or `kvm_amd` as root; run `install bidder`, which adds the account to the `kvm` group; never chmod `/dev/kvm` |
| user-manager-kvm-group | bidder | The groups of `user@<uid>`'s process include `kvm` | Restart `user@<uid>` (this stops every VM) or reboot the host; logging in again does not refresh a lingering user manager |
| dev-net-tun | bidder | `/dev/net/tun` opens read-write | Load the tun module as root: `modprobe tun` |
| pasta-userns | bidder | The embedded pasta creates a user and network namespace and execs through `/bin/sh` | On Ubuntu 24.04 and newer run `install bidder`, which loads the AppArmor profile; otherwise allow unprivileged user namespaces through the `kernel.unprivileged_userns_clone` and `user.max_user_namespaces` sysctls |
| apparmor-pasta-profile | bidder | AppArmor does not restrict unprivileged user namespaces, or the `socialweb-pasta` profile is loaded | Run `install bidder`, which writes `/etc/apparmor.d/socialweb-pasta` and loads it with `apparmor_parser -r` |
| subuid-subgid-range | bidder | Exactly one range of 65536 ids or more for the account in both `/etc/subuid` and `/etc/subgid`, overlapping no other entry of either file | Run `install bidder`, which reserves one contiguous range above every existing entry; remove the other tool's overlapping entry |
| newuidmap-newgidmap | bidder | The distribution's `newuidmap` and `newgidmap` are setuid root or carry file capabilities | Install the distribution's uidmap (Debian/Ubuntu) or shadow-utils (Fedora/RHEL) package as root; never ship a setuid helper |
| newuidmap-first-vm-range | bidder | A throwaway user namespace maps 0-65535 onto the account's first VM sub-range through `newuidmap` and `newgidmap` | Fix the `subuid-subgid-range` and `newuidmap-newgidmap` checks first |
| state-dir-posix-acl | bidder | The state dir's filesystem supports POSIX ACLs | Put the state dir on a filesystem mounted with ACL support (ext4, xfs and btrfs support ACLs by default) |
| systemd-oomd | both | Reported only; reports whether systemd-oomd is active and never fails | No action; every unit runs below `socialweb.slice`, outside the `app.slice` systemd-oomd watches |
| user-manager-fd-store | gateway | `FileDescriptorStoreMax` reads back from a transient service | Use a systemd whose user manager keeps an fd store for transient services; the kcp front restarts without a refused-connection window only with it |
| k3s-slot-hostname | gateway | Reported only; never fails, and the check is not probed yet | No action |
| tzdata | gateway | `/usr/share/zoneinfo/UTC` exists (the embedded postgres uses the system tzdata path) | Install the distribution's tzdata package as root |

`socialweb-computer dev check` reports the development host subset; it changes nothing on the host.
