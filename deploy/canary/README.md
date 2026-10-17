# Prod canary

The GitHub Actions workflow `.github/workflows/prod-canary.yml` proves once a night
(04:17 UTC, and on `workflow_dispatch`) that the prod gateway can make a tenant
cluster, run a Job on it and delete it. It runs on a GitHub-hosted runner on main
only. The requester's handle and password are the repository secrets
`PROD_CANARY_ALICE_HANDLE` and `PROD_CANARY_ALICE_PASSWORD`; the job writes them to a
0600 file in the runner's temp directory, and the script reads that file inside its
own process. Each run's `results.jsonl` is the artifact `prod-canary-results`.
Read the last two runs with `gh run list --workflow prod-canary.yml --limit 2`.

The user systemd timer below is the same script on a host, for a host that holds
`~/.aliceoa_bsky_social_source` itself.

`scripts/prod-canary.ts` goes only through the gateway's XRPC front door, with
service-auth tokens for the requester account from `createSession` and
`getServiceAuth`:

1. `listClusters`: any leftover `canary-*` cluster from an earlier failed run is
   deleted and waited out (reported as `preflight.leftovers`).
2. `createCluster canary-YYYYMMDD`, then `listClusters` until the phase is Ready.
3. `kubectlApplyServerSide` of a busybox Job, then `getJob` until Succeeded; the
   container log must hold `canary-ok`. The cluster has no nodes, so this is the
   scale from zero onto a market RFP VM.
4. `deleteCluster`, then `listClusters` until the cluster is unlisted; a
   `teardownBlocked` cluster fails the run at once.
5. With `--bidder-kubeconfig`: the ComputeVMs that appeared on the bidder during
   the Job must be gone after the delete. Without it the step reports `skipped`.

Every phase has a timeout (`--ready-timeout`, `--job-timeout`, `--delete-timeout`,
`--vm-timeout`, seconds) and the whole run has `--total-timeout` (3600). On any
failure the script still tries `deleteCluster` once and reports `leaked` when the
cluster was not confirmed gone.

One JSON line per run goes to stdout (the journal) and is appended to
`~/.local/state/swc-prod-canary/results.jsonl` (`--log`): `ok`, `error`, `seconds`,
and `steps` with the seconds of each phase. The exit code is 0 only when every
step passed.

## Install

```
mkdir -p ~/.local/share/swc-prod-canary ~/.config/systemd/user
ln -sf <checkout>/scripts/prod-canary.ts ~/.local/share/swc-prod-canary/prod-canary.ts
cp deploy/canary/swc-prod-canary.service deploy/canary/swc-prod-canary.timer ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now swc-prod-canary.timer
```

Run it once by hand and read the result:

```
systemctl --user start swc-prod-canary.service
journalctl --user -u swc-prod-canary.service -n 1 --no-pager
tail -n 1 ~/.local/state/swc-prod-canary/results.jsonl
```

The timer fires at 04:17 local time with up to 5 minutes of random delay, and
`Persistent=true` runs a missed night when the host is back. `loginctl
enable-linger <account>` keeps the user manager up between logins.

## Checking the bidder's ComputeVM

The bidder's kcp kubeconfig is owned by the bidder's account. To check that the
ComputeVM is released, add a drop-in
(`systemctl --user edit swc-prod-canary.service`) that clears and sets
`ExecStart` with
`--kubectl "sudo -n -u <bidder account> kubectl" --bidder-kubeconfig <path>/kcp/admin.kubeconfig`
appended, and allow that one `sudo -n -u <bidder account> kubectl` in sudoers.

## Other gateways

`--gateway`, `--audience`, `--pds`, `--ca-file`, `--creds` and `--creds-prefix`
point the same script at a market network's gateway. `--creds` names a file of
`export <PREFIX>_HANDLE=` and `export <PREFIX>_PASSWORD=` lines.
