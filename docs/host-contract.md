# The Linux host contract (plan 0006 M2-HOST-CONTRACT)

`socialweb-computer check-host -role gateway|bidder -state-dir <state>` probes this contract as the
account (`socialweb` or `swc-bidder`) and exits 1 naming every check that fails, each with its fix.
`install` runs it as the account as its last step. The checks live in `internal/hostcontract`; this
file is the list a host is held to, in the order the report prints it.

| Check | Role | Holds when | Fix when it does not |
| --- | --- | --- | --- |
| account | both | not running as root | run as the account |
| systemd-version | both | the user manager's private socket answers and reports >= 253 (GetUnitByPIDFD) | a distribution shipping systemd >= 253 |
| probe-unit-properties | both | a transient `socialweb-probe-<rand>.service` started with every property the runtime sets (MemoryMax, MemorySwapMax=0, OOMPolicy, TasksMax, CPUQuota, CPUWeight, CollectMode, After/Wants, Slice, KillSignal, TimeoutStopSec, StandardOutput=append:) reads each one back | a systemd that keeps every property |
| cgroup-v2 | both | /sys/fs/cgroup is cgroup2 | unified hierarchy |
| user-manager-delegation | both | user@<uid>.service cgroup.controllers include cpu memory pids | install's `Delegate=cpu memory pids` drop-in, then restart user@<uid> or reboot |
| linger | both | /var/lib/systemd/linger/<account> | install |
| runtime-dir-socket-length | both | XDG_RUNTIME_DIR plus the longest runtime socket < 108 bytes | the default /run/user/<uid> |
| boot-id | both | /proc/sys/kernel/random/boot_id readable | a normal /proc |
| dev-kvm | bidder | /dev/kvm opens read-write as the account | install adds the kvm group; never chmod /dev/kvm |
| user-manager-kvm-group | bidder | the Groups of user@<uid>'s own process include kvm | restart user@<uid> (stops every VM) or reboot; logging in again does not refresh a lingering manager |
| dev-net-tun | bidder | /dev/net/tun opens read-write | modprobe tun |
| pasta-userns | bidder | the embedded pasta creates a user+net namespace and execs through /bin/sh | the AppArmor profile (Ubuntu 24.04+) or the userns sysctls |
| apparmor-pasta-profile | bidder | no AppArmor userns restriction, or profile socialweb-pasta loaded | install |
| subuid-subgid-range | bidder | exactly one range of >= 65536 ids for the account in BOTH /etc/subuid and /etc/subgid, overlapping no other entry of either file | install, or remove the other tool's overlapping entry |
| newuidmap-newgidmap | bidder | the distribution's helpers, setuid root or with file capabilities | the uidmap / shadow-utils package; a setuid helper is never shipped |
| newuidmap-first-vm-range | bidder | a throwaway user namespace maps 0-65535 onto the first VM sub-range through them | the two checks above |
| state-dir-posix-acl | bidder | the state dir's filesystem supports POSIX ACLs | an ACL-capable filesystem |
| systemd-oomd | both | reported, never failed | - |
| user-manager-fd-store | gateway | FileDescriptorStoreMax reads back from a transient service | a systemd whose user manager keeps an fd store |
| k3s-slot-hostname | gateway | reported; probed once 0006 M3-CP-SLOTS picks the mechanism | - |
| tzdata | gateway | /usr/share/zoneinfo/UTC exists (the embedded postgres uses the system tzdata path) | the tzdata package |

`dev check` (M1-DEV-INSTANCE) reports the dev subset and fixes nothing.
