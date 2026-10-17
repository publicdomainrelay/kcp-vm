# Prod canary

The GitHub Actions workflow `.github/workflows/prod-canary.yml` proves once a night
(04:17 UTC, and on `workflow_dispatch`) that the prod gateway can make a tenant
cluster, run a Job on it and delete it. It runs on a GitHub-hosted runner on main
only and needs no repository secret. Each run's `results.jsonl` is the artifact
`prod-canary-results`. Read the last two runs with
`gh run list --workflow prod-canary.yml --limit 2`.

## The ephemeral identity

By default (`--identity ephemeral`) the canary uses no real account. Each run:

1. makes a fresh secp256k1 key in the job and registers a did:plc for it at
   `--plc` (https://plc.directory). The DID document has the key as `#atproto`,
   and its `atproto_pds` endpoint is the did-key-ingress-proxy subdomain of the key
   at `--ingress-proxy-host` (xrpc.fedproxy.com).
2. serves a one-account PDS for that DID in the job (`createOAuthLocalPds` in
   `ts/atproto-market/lib/atproto-helpers`): the hono-pds repo and its OAuth
   authorization server, reached by the public only through the ingress proxy.
   Account creation and session routes are closed, and `getServiceAuth` needs an
   OAuth session of the account.
3. mints the service-auth token of every gateway XRPC call locally with the key
   (`aud` = `--audience`, `lxm` = the method, 2 minute lifetime).
4. at the end, also on a failure, signs out of the web app and tombstones the DID
   on PLC (`tombstone` step: the last PLC operation of the DID is `plc_tombstone`).

The gateway asks a cluster owner only for a did:plc or did:web whose `#atproto` key
signs the service-auth token (`internal/atprotoauth`). The nodes of the cluster are
rented by the gateway's own requester, so the owner needs no handle, PDS or vouch.
A fresh DID owns nothing, so the preflight finds no leftover `canary-*` cluster; a
cluster that a failed run could not delete is reported as `leaked` and drains on
the gateway after `deleteCluster` was accepted.

`--web-signin` also signs the identity in to the web app over atproto OAuth:
`POST /oauth/start` with the DID, a headless approve at the job's PDS, the
callback's `swc_session` cookie, `GET /session`, and after the cluster is Ready
`POST /api/clusters/list` must list it (the web app calls the gateway with a
service-auth token from the PDS through the OAuth session). The OAuth requests reach
the job's PDS through the ingress proxy, so the proxy must relay form bodies and the
`DPoP-Nonce` header (`ts/did-key-ingress-proxy` since 0b7b0af7; xrpc.fedproxy.com runs it).
The workflow passes `--web-signin`. Without it the job's PDS is not served at all and the
canary needs only the PLC directory and the gateway.

`--identity creds` keeps the old mode: `createSession` and `getServiceAuth` at
`--pds` with the handle and password of `--creds` (a file of
`export <PREFIX>_HANDLE=` and `export <PREFIX>_PASSWORD=` lines, `--creds-prefix`).

## The steps

1. `listClusters`: any leftover `canary-*` cluster from an earlier failed run is
   deleted and waited out (reported as `preflight.leftovers`).
2. `createCluster canary-YYYYMMDD`, then `listClusters` until the phase is Ready.
3. `kubectlApplyServerSide` of a busybox Job, then `getJob` until Succeeded; the
   container log must hold `canary-ok`. The cluster has no nodes, so this is the
   scale from zero onto a market RFP VM.
4. `deleteCluster`, then `listClusters` until the cluster is unlisted, within
   `--delete-timeout`. A `teardownBlocked` cluster is still waited for, because the
   gateway keeps retrying the step; the step reports `teardownBlockedSeen`, and a
   timeout names it. A failed run sends no second `deleteCluster` once one was accepted.
5. With `--bidder-kubeconfig`: the ComputeVMs that appeared on the bidder during
   the Job must be gone after the delete. Without it the step reports `skipped`.

Every phase has a timeout (`--ready-timeout`, `--job-timeout`, `--delete-timeout`,
`--vm-timeout`, seconds) and the whole run has `--total-timeout` (3600). On any
failure the script still tries `deleteCluster` once and reports `leaked` when the
cluster was not confirmed gone.

One JSON line per run goes to stdout (the journal) and is appended to
`~/.local/state/swc-prod-canary/results.jsonl` (`--log`): `ok`, `error`, `seconds`,
`identity` (the ephemeral DID), and `steps` with the seconds of each phase. The
exit code is 0 only when every step passed.

## A timer on a host

The user systemd timer below runs the same script on a host. It runs from a
checkout of this repository, because the script imports workspace packages.

```
mkdir -p ~/.local/share/swc-prod-canary ~/.config/systemd/user
ln -sfn <checkout> ~/.local/share/swc-prod-canary/checkout
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

## A market network's gateway

`--gateway` (the XRPC origin), `--web-origin` (the web app's public origin, default
`--gateway`), `--audience`, `--plc` and `--ingress-proxy-host` point the script at a
market network: `--gateway http://<the gateway app's xrpc listener>`, `--audience
did:web:socialweb.local`, `--plc` the network's `plcDirectoryUrl`,
`--ingress-proxy-host` its `ingressProxyHost` (relay.localhost:<dispatcher port>),
and `DENO_CERT` the network CA bundle. In a market network the gateway's atproto
plugin trusts `*.relay.localhost` through the network's dispatcher-tls front.
