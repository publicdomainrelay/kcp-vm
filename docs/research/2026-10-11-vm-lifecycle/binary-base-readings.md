# binary-base readings (task tree binary-base), 2026-10-12, hat-8

Evidence for plan 0006 M1-CLI, M2-HOST-CONFORMANCE, M2-HOST-BACKEND-PROC and the minimal slice of
M1-DEV-INSTANCE / M2-KCP-STORE / M2-KCP-SLOTS / M2-KCP-PORT0 / M2-BOOTSTRAP-LAYER. The plan files are not
edited here; the lead folds these lines into their evidence. Executables: kcp `/usr/local/bin/kcp`
v1.36.0+kcp-v0.33.0 sha256 0929a16b...; kine `/usr/local/bin/kine` (the v0.17.1 build; reports `dev (HEAD)`)
sha256 de323745... Another task tree's bed (kcp + kine on port 0) ran on the host during every reading.

## Red -> green (UTC)

| test | red | green |
|---|---|---|
| devsup TestTheProcBackendPassesTheContract (15 cases) | 03:00:50, against a stub devsup: 12 fail | 03:02:30, 14 pass, Limits skipped (EnforcesLimits=false) |
| hostsvctest TestTheSystemdUserBackendPassesTheContract (livegate) | - | 03:05:44-03:05:48, 14 pass, ManagerRestart skipped (user@ restart) |
| clicfg (5 of 7 tests) | 03:07:51, options.json empty | 03:07:52 |
| devsup crash/sweep, die-with, orphan, foreign backend | written with the server | 03:05:36 |
| cmd TestTwoInstancesOnOneHostShareNothing (livegate, real kcp/kine) | - | 03:18:20-03:18:38 (17.4 s) |

## devsup crash and sweep (TestADevsupCrashLeavesNoProcessAndTheNextDevsupSweepsByLeaderTicks)

03:05:36.337Z devsup kill -9: unit leader 665488 gone (Pdeathsig), grandchild 665490 (pgid 665488)
survived, reparented away from devsup; 03:05:36.358Z the next devsup swept it by the recorded pgid
(leader gone, members left: "signal the group").

## Two dev instances, minimal profile (final run)

| reading | proof-a | proof-b |
|---|---|---|
| `dev up` in parallel | 03:20:42.674Z -> 03:20:53.9Z, 11.2 s | same window, 11.2 s |
| kine port (read from /proc) | 43335, 55 ms after exec | 44851, 55 ms |
| kcp port (read from /proc) | 39911, 82 ms after exec | 33435, 82 ms |
| kcp start-to-/readyz | 10.84 s | 10.86 s |
| RSS at ready: kcp / kine | 415 MB / 64 MB | 408 MB / 63 MB |
| RSS at +26 s: kcp / kine / devsup / core | 579 / 65 / 18.7 / 15.1 MB | 522 / 64 / 17.7 / 16.3 MB |
| `kubectl get --raw /readyz` (own admin.kubeconfig) | ok, server :39911 | ok, server :33435 |
| root workspaces | only-in-proof-a | only-in-proof-b |
| `dev down` | 3.38 s; no process, port, socket or dir left | `--keep-state` 3.38 s, state kept; then removed |

Earlier runs: fresh kcp 13.75 s and 17.0 s to /readyz (03:14:54Z, 03:15:24Z, sequential); restart on a kept
state dir 6.4 s (03:17:02Z, workspace only-in-proof-b still listed). Stop order on down (proof-b): core 24 ms,
kcp slot 3.13 s (--shutdown-delay-duration=2s), kine 32 ms.

## Core restart (adoption, not re-creation)

03:21:07.729Z kill -9 of the dev core (gen 1, pid 1241226); devsup restarted it (Restart=on-failure, new
InvocationID); gen 2 (pid 1247413) recorded its adoption at 03:21:07.849Z: kine-a pid 1238790 invocation
fc40732e939d and kcp-a pid 1238819 invocation b3a8d899b41e, both identical to the `dev up` record. A second
`dev up` of a running instance (03:16:10Z) took 0.2 s and adopted devsup, kine, kcp and the core.

## devsup death

03:17:37Z kill -9 of proof-c's devsup: kine, kcp and the core died with it (Pdeathsig), 0 processes carried
SWC_INSTANCE=proof-c 1 s later; `dev status` read devsup unreachable; `dev down` swept the three unit
records ("nothing left") and removed the state and runtime dirs in 0.13 s.
