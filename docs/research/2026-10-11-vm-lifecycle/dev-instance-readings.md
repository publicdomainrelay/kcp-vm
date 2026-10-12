# dev-instance readings (task tree dev-instance), 2026-10-12, hat-8

Evidence for plan 0006 M1-PORT-LEASE-INSTANCE and the parts of M1-DEV-INSTANCE the binary-base slice left
(dev ls|logs|env|check|restart, --fg, the .swc-dev marker, per-instance state layout, per-task ids). The plan
files are not edited here; the lead folds these lines into their evidence. Executables: kcp
`/usr/local/bin/kcp` v1.36.0+kcp-v0.33.0, kine `/usr/local/bin/kine` (v0.17.1 build), as in
binary-base-readings.md. Another task tree's bed ran on the host during every reading.

## Decisions taken while building (user may veto)

- State layout: `<state-root>/dev/<id>`, the plan's and dev-run-mode.md R2's layout, replaces the slice's
  `<state-root>/instances/<id>`. Why: the state root also holds the per-user `ports/` lease directory and
  later install and asset directories, so a run-mode directory keeps `dev ls` one readdir and a dev instance
  can never be mistaken for an installed one. No migration (user 2026-10-12: no backwards compatibility);
  binary-base removed its proof instances.
- The state root keeps the slice's order (state-dir option, TASK_STATE_DIR, XDG_STATE_HOME, ~/.local/state);
  the lease file does NOT follow TASK_STATE_DIR: it is always `$XDG_STATE_HOME/socialweb-computer/ports/
  leases.json` (default ~/.local/state), one per user, so instances of different task trees see each other.
- Instance id: `<readable>-<sha256(realpath(worktree))[:8]>`; readable is TASK_NAME when the worktree sits
  under TASK_ROOT (task trees), else the worktree basename (16 chars). `--instance` still wins.
- `Lease.Instance` is NOT the application instance: it is the cluster incarnation the materialiser bumps
  on re-bind (materialiser.go:483-498; TestK_OnAfterOffRebindsTheSamePort pins that a re-bind keeps the
  port). The application instance is a new field `appInstance`; the key is (appInstance, cluster,
  purpose). An allocator is opened for ONE application instance (Config.AppInstance): it reads, releases,
  rekeys and reclaims only its own leases and picks free ports against every lease in the file. Prod passes
  "" and its lease files read and re-save byte-identically. subnetallocator: same key and scoping.
- Dev carve 15000-19999 (portallocator.DevFirst/DevLast, liveports.DevCarve in Reserved): disjoint from the
  prod default carve 20000-29999, every live-test block (30500+), the guest-join probe and the ephemeral
  range 32768-60999.
- Port 0 stays the rule: the minimal profile leases nothing (kine and the kcp slot bind 0 and are read back
  from /proc); fronts bind 0 (M2-KCP-FRONT). Leases exist for the cluster front and link ports of M3; dev
  surfaces an instance's leases in `dev status` and `dev env` (SOCIALWEB_DEV_LEASE_<CLUSTER>_<PURPOSE>) and
  `dev down` releases them. Deviation: leases are not yet recorded in the instance's kcp (no object kind
  carries them before M2-HOSTPROCESS-KIND / M2-KCP-PORT0 status).
- `quick` profile stays exit 2 naming M2-BOOTSTRAP-LAYER: M2-BOOTSTRAP-LAYER defines it as the gateway-role
  kcp, which needs the front (M2-KCP-FRONT, in flight on task/kcp-front) and the gateway core.
- `dev up --fg`: devsup is started with `--die-with <dev up pid>`; Ctrl-C runs `dev down` (honouring
  --keep-state); kill -9 of `dev up` takes devsup and every unit with it. `--fg` on an instance whose
  detached devsup is already up exits 2.
- `dev env` prints shell exports (`--json` a map): KUBECONFIG and SOCIALWEB_KCP_KUBECONFIG (the slot's
  admin.kubeconfig), SOCIALWEB_KCP_HOST, SOCIALWEB_KCP_ROOT, SOCIALWEB_KCP_BASE_WORKSPACE, SOCIALWEB_KINE_URL,
  SOCIALWEB_INSTANCE, SOCIALWEB_DEV_STATE_DIR, SOCIALWEB_DEVSUP_SOCKET, SOCIALWEB_DEV_PORT_<ROLE>, leases.
  `dev up` writes the same to `<state>/env` and `<state>/env.json`; `<worktree>/.swc-dev` (gitignored) is a
  symlink to `<state>`, written only when the id is derived from the worktree and removed only by its owner.
- `dev logs <kine-a|kcp-a|core|devsup|unit name> [--tail N]` reads the unit's output file (proc has no
  journal). `dev check` reports R8 and never fixes; exit 1 on any fail. `dev restart core [--binary p]`
  stops the core unit, places it again on the (hashed) binary and exits 1 unless every unit is adopted with
  its InvocationID unchanged. CLI flags may follow the words they qualify (`dev restart core --binary p`).

## Red -> green (UTC)

| test | red | green |
|---|---|---|
| portallocator TestALeaseIsKeyedByInstance, TestOneInstanceNeverReleasesOrReclaimsAnothersLease, TestTwoInstancesSharingOneLeaseFileNeverLeaseTheSamePort (two processes, one file), TestAProdLeaseFileWithoutInstancesReadsUnchanged, TestFiftyConcurrentLeasesHoldTheFileLockBriefly; liveports TestTheDevCarveDoesNotOverlapTheLiveTestBlocks | 03:32:15, API stubbed with the old key: 6 fail (shared port 15000; a's reclaim freed b's lease; dev carve not reserved) | 03:32:35 |
| subnetallocator TestASubnetLeaseIsKeyedByInstance, TestAProdSubnetLeaseFileWithoutInstancesReadsUnchanged | 03:33:04, 2 fail (shared 10.200.0.0/29) | 03:33:19 |
| devinstance layout/id/lease-path/marker/env/ls/logs tests (7) | 03:35:25, build fails (ID, DevRoot, LeasePath, ...) | 03:35:55 |
| cmd TestThreeWorktreesEachReachTheirOwnKcpThroughDevEnv (livegate, real kcp/kine, deno TS client) | - | 03:41:00 (14.4 s) |
| cmd TestDevUpInTheForegroundEndsWithItsTerminal (livegate) | 03:42:07 (test bug: devsup outlives the unit scan by its stop time) | 03:42:54 |
| cmd TestTwoInstancesOnOneHostShareNothing (slice test, on the dev/<id> layout) | - | 03:41:32 |

Flock under 50 concurrent leases on one file (50 allocators, 50 instances): total 8.4 ms, per-lease wait
p50 3.2 ms, max 6.9 ms, mean hold 0.17 ms.

## Three worktrees, live (CLI, three `git worktree add` checkouts, run from wt-*/internal)

| reading | wt-red-0b132abe | wt-green-e3c5fa4d | wt-blue-249199cf |
|---|---|---|---|
| `dev up` in parallel from 03:43:24.329Z | exit 03:43:38.274Z, 13.9 s | 03:43:37.565Z, 13.2 s | 03:43:39.059Z, 14.7 s |
| kcp / kine port (bound 0, read back) | 38245 / 39475 | 42309 / 46515 | 39127 / 39209 |
| devsup socket | /run/user/1000/swc/7a53c738/sup.sock | .../4a13970b/sup.sock | .../b6f912d9/sup.sock |
| `.swc-dev` | -> .../dev/wt-red-0b132abe | -> .../dev/wt-green-e3c5fa4d | -> .../dev/wt-blue-249199cf |
| `dev ls` | ready, devsup 1640770 | ready, devsup 1640769 | ready, devsup 1640771 |
| TS client (`eval "$(dev env)"`, parseKubeconfig + Deno.createHttpClient), 2 rounds | /readyz 200; root workspaces = only its own marker; both peers' markers absent | same | same |
| `dev down` in parallel from 03:43:43.724Z | 3.32 s, no process, state, runtime dir or marker left | 3.32 s | 3.27 s |

`dev restart core` (wt-blue, 03:43:43.568Z): generation 1 pid 1655605 -> generation 2 pid 1657584 in
0.14 s; kine-a pid 1640832 invocation 5232c425a7f0 and kcp-a pid 1640936 invocation bcc13d565c72 adopted,
identical before and after (dev status). `dev logs kcp-a --tail 2` printed the slot's last two lines.
After the three downs (03:43:47Z): 0 `socialweb-computer internal` processes, /run/user/1000/swc empty.

In the go test (03:40:45-03:41:00Z): three worktrees named socialweb-computer-kcp under three parents got
distinct ids socialweb-comput-{ec50a869,7293dc59,315d5043}, kcp ports 38463/35243/41399; each leased
cluster `front`/apiserver-link from one per-user lease file (distinct ports, SOCIALWEB_DEV_LEASE_FRONT_
APISERVER_LINK in dev env), and `dev down` released every lease (file empty after). `--fg`: Ctrl-C to
`dev up --fg` took the instance down in 3.33 s (03:42:41Z); kill -9 of `dev up --fg` took devsup, the core
and both units with it in 3.22 s (03:42:54Z); a second `dev up --fg` of the running instance exited 2.

`dev check` on hat-8 (03:43:24Z): not-root, XDG_RUNTIME_DIR, /dev/kvm rw, /dev/net/tun rw,
user.max_user_namespaces=100000, no AppArmor userns restriction, newuidmap/newgidmap, KillUserProcesses
unset: ok; subuid-subgid: fail (no range for johnandersen777 in /etc/subuid, /etc/subgid) - reported, not
fixed; per-VM subuid user namespaces (0007) need it.

## Gates

go test ./... exit 0 (67 ok, 03:43:59-03:44:09Z); deploy/check.sh --fast exit 0 (03:44:12-03:45:04Z).
