# socialweb-computer-kcp

Virtual k3s clusters. Nodes = VMs rented on atproto compute market (RFP -> bid -> accept -> cloud-init). Nodes scale to zero; control plane stays up. One binary: `socialweb-computer`. Each app (bidder, requester, gateway, market network) = own kcp + manifest set + profile.

Design: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md). Agents: [`AGENTS.md`](AGENTS.md). Plans: [`docs/plans/`](docs/plans/).

```
requester --RFP--> relay/PDS <--bid-- bidder (own kcp: MarketBidder, Bid, ComputeVM)
    |  accept                                  |  cloud-init user_data
    v                                          v
gateway (own kcp: Gateway, Cluster, NodeClaim)  firecracker VM = k3s node
    tenant k3s apiserver (kine + front) <------ node joins over iroh link
```

## Build

```bash
scripts/build-socialweb-computer.sh --dev --publish /var/tmp/swc        # dev build of working tree
scripts/build-socialweb-computer.sh --platform linux/amd64 --commit "$(git rev-parse HEAD)"   # release, cached, ~5 min cold, ~7 s unchanged
scripts/build-socialweb-computer.sh --verify-reproducible                # two clean builds must match (CI on main does this)
B=~/.tmp/socialweb-computer-build/release/<commit>/socialweb-computer-linux-amd64
```

Gate = GitLab CI on every push (`.gitlab-ci.yml`). Wait for it: `deno run -A ~/.tmp/swc-lead/ci-wait.ts <sha>`.

## Market network (local dev: fake PLC, relay, PDS, bidders, requesters, gateway)

```bash
swc=/var/tmp/swc/socialweb-computer
$swc dev up --instance mn1 --profile market-network --bidders 1 --with-gateway \
  --guest-image-source file:///var/lib/socialweb-computer/guest-image --vm-host-backend systemd-user
$swc dev status --instance mn1 | jq '.network.status.apps'
$swc status --instance mn1                     # market counters, contract steps
eval "$($swc dev env --instance mn1)"          # KUBECONFIG + ports of the network's kcp
$swc dev logs core --instance mn1 --tail 50
$swc dev down --instance mn1
```

VMs need a VM-slot account (hat-8: `test-0001`, run as it). Shared host: take a live slot first (`deno run -A ~/.tmp/swc-lead/live-slot.ts acquire <name>`).

## Requester (rent a VM, pass secrets)

```bash
echo '{"x":"hello"}' > secrets.json
$swc run request --network mn1 --secrets secrets.json --exec 'cat /root/x'      # RFP -> bid -> accept -> VM -> ssh -> vm.delete
$swc run request --network mn1 --secrets secrets.json --hold --exec 'uname -a'   # keep VM
$swc run request list
$swc run request resume <name>      # ssh again
$swc run request release <name>     # vm.delete
```

Secrets go requester OpenBao -> guest only. Never in kcp, args, logs, records. Ssh only via iroh ticket, StrictHostKeyChecking=yes vs guest `vm.hostKeys`.

## Bidder

```bash
# in a market network
$swc run bidder --host-backend proc --profile market-network --network mn1 --handle b2.mn1.bob.test \
  --guest-image-source file:///var/lib/socialweb-computer/guest-image --vm-host-backend systemd-user
# prod install (root once), then it runs as its account
sudo $B install bidder --handle bobvmbuilder.bsky.social --adopt-account \
  --guest-image-source file:///var/lib/socialweb-computer/guest-image
# sign in (prod-qr): read the QR url, sign in headless with creds file (never print creds)
K=/var/lib/socialweb-computer/swc-bidder/handles/<hash>/kcp/admin.kubeconfig
U=$(sudo kubectl --kubeconfig $K --server "$(sudo kubectl --kubeconfig $K config view -o jsonpath='{.clusters[0].cluster.server}' | sed 's#/clusters/.*##')/clusters/root" \
  get marketidentity bidder -o jsonpath='{.status.pendingAction.url}')
deno run -A --no-lock scripts/oauth-signin.ts --creds ~/.bobvmbuilder_bsky_social_source --account BOB --url "$U"
# upgrade (as the account)
sudo runuser -u swc-bidder -- env XDG_RUNTIME_DIR=/run/user/$(id -u swc-bidder) \
  CONFIG_PATH_SOCIALWEB_COMPUTER=/var/lib/socialweb-computer/swc-bidder/config.json $B upgrade --binary $B --sha256 <sha256>
```

Policy: `config.json` `bidder-policy` (prod: `tangled-vouch`). Guest image: pinned by sha256 in `internal/versions/versions.json`.

## Gateway (web app k8s.<domain>, kcp front kcp.<domain>, tenant clusters)

```bash
$swc dev up --instance mn1 --profile market-network --with-gateway ...    # dev: gateway child of the network
# prod: build release, ship, install/upgrade, caddy sites
BINARY=$B SHA256=<sha256> SSH_KEY=~/.ssh/id_ed25519_no_password \
  GATEWAY_ENV_FILE=~/.tmp/deploy/gateway.env GATEWAY_SPEC_FILE=~/.tmp/deploy/gateway-spec.yaml scripts/deploy.sh
# user sign-in (needed after fresh deploy, else claims fail "no oauth session stored")
deno run -A --no-lock scripts/oauth-signin.ts --creds ~/.aliceoa_bsky_social_source --account ALICE --url https://k8s.socialweb.computer/
```

### Cluster XRPC with `goat` (create cluster if missing, get kubeconfig, kubectl)

Auth = atproto service JWT: `iss` your DID, `aud` gateway DID, `lxm` the method. Methods: `com.publicdomainrelay.temp.cluster.{createCluster,getKubeconfig,listClusters,controlPlaneUp,controlPlaneDown,deleteCluster,subscribeClusters}`, and `computer.socialweb.k8s.{kubectlApplyServerSide,getJob}` (apply objects and run Jobs without a kubeconfig, below).

```bash
go install github.com/bluesky-social/goat@latest
( . ~/.aliceoa_bsky_social_source; GOAT_USERNAME="$ALICE_ATPROTO_HANDLE" GOAT_PASSWORD="$ALICE_ATPROTO_PASSWORD" goat account login )
GW=https://k8s.socialweb.computer AUD=did:web:k8s.socialweb.computer N=com.publicdomainrelay.temp.cluster
call() { local m=$1 nsid=$N.$2; shift 2; goat xrpc $m $GW $nsid "$@" "Authorization:Bearer $(goat account service-auth --aud $AUD --lxm $nsid)"; }
call procedure createCluster name=demo                      # creates if missing + starts control plane (idempotent)
# wait for Ready by event stream (no polling); exits 0 at Ready, 2 if a member fails, 1 on error frame
deno run -A --no-lock --node-modules-dir=none scripts/subscribe-clusters.ts --gateway $GW --name demo \
  --token "$(goat account service-auth --aud $AUD --lxm $N.subscribeClusters)" --until cluster=Ready --fail-on member=Failed
call procedure getKubeconfig name=demo | jq -r .kubeconfig > demo.kubeconfig; chmod 600 demo.kubeconfig
# kubeconfig exec plugin = socialweb-credential (pinned download; list: curl -s $GW/api/config | jq .credentialHelpers)
curl -sfo socialweb-credential "$GW$(curl -s $GW/api/config | jq -r '.credentialHelpers[] | select(.os=="linux" and .arch=="amd64") | .path')" && chmod +x socialweb-credential
PATH=$PWD:$PATH KUBECONFIG=demo.kubeconfig kubectl get ns
call procedure controlPlaneDown name=demo; call procedure deleteCluster name=demo
```

Status: event stream `subscribeClusters` (below) or poll `call query listClusters | jq '.clusters[] | {name,phase,running}'`. Phases: `Unavailable` -> `Initializing` -> `Ready` (`Draining` on stop).

Expected timing (prod, 2026-10-13, release 2549b7a2; members start as their dependencies get ready, placement is immediate):

| Step | Typical |
|---|---|
| createCluster -> Ready (kcp workspace + home ~25 s, then front, kine, k3s, ccm + apiserver link, eso webhook in a dependency chain, each member 14-26 s to ready on this host; measured 194 s) | ~3 min 15 s |
| pod at 0 nodes -> NodeClaim -> RFP -> bid -> VM Running -> node Ready -> pod Running | ~60 s |
| Job by kubectlApplyServerSide at 0 nodes -> getJob Succeeded (market VM node) | ~55 s |
| pod deleted -> 0 nodes, vm.delete (includes 2 min idle hold) | ~2-3 min |

Slower = check gateway log: `journalctl _UID=$(id -u socialweb) -o cat | grep -E 'member (placed|ready)|status|node scale'`.

`getKubeconfig` alone also creates the cluster but leaves it stopped (`running=false`; credential mint answers 429 until `controlPlaneUp`). Kubeconfig carries a device key: keep 0600, never commit.

Scale from zero: apply a pod to a tenant cluster at 0 nodes -> NodeClaim -> RFP -> VM -> node Ready -> pod Running (prod: ~60 s). Delete pod -> back to 0 nodes, vm.delete (~2-3 min incl. hold).

### Cluster events with `subscribeClusters` (websocket, no polling)

`com.publicdomainrelay.temp.cluster.subscribeClusters` = atproto event stream: `GET` + websocket upgrade, binary frames, each frame = DRISL-CBOR header (`{op:1,t:"#cluster"}`, error `{op:-1}`) + DRISL-CBOR payload. Auth = same service JWT on the upgrade (`Authorization: Bearer`, `lxm` = subscribeClusters). No query-param token. Account = token `iss`. Optional `?name=<cluster>`.

On connect: snapshot (one `#cluster` per cluster, one `#member` per control-plane member, one `#nodeClaim` per claim, all `snapshot:true`), then `#info SnapshotComplete`, then live changes from kcp watches. No cursor: reconnect = fresh snapshot. Token checked once at connect (60 s token fine); stream lives until client closes, gateway restarts, or 1 h bound (`TokenExpired` error frame -> new token, reconnect). Error frame then close: `ClusterNotFound` (unknown `?name=`, cluster deleted), `Unauthorized`, `TokenExpired`, `IdentityRevoked`, `ClusterUnavailable`, `ConsumerTooSlow`. Client frames ignored.

| message | fields | values |
|---|---|---|
| `#cluster` | name, phase, running, reason, message, server, instance, deleted | Unavailable, Initializing, Ready, Draining |
| `#member` | cluster, member, phase, reason, message | Pending, Starting, Running, Stopping, Stopped, Failed, Lost |
| `#nodeClaim` | cluster, name, nodeName, phase, message, contractPhase, outcome, bidder, fqdn, deleted | Bidding, Provisioning, Joining, Running, Failed, Lost, Released |

```bash
# reuse GW AUD N from the goat section above
T=$(goat account service-auth --aud $AUD --lxm $N.subscribeClusters)
# decoding client: one JSON line per event; exit 0 on --until, 2 on --fail-on, 1 on error frame
deno run -A --no-lock --node-modules-dir=none scripts/subscribe-clusters.ts --gateway $GW --token "$T" --name demo \
  --until cluster=Ready --fail-on nodeClaim=Failed
# all clusters of the account, run forever
deno run -A --no-lock --node-modules-dir=none scripts/subscribe-clusters.ts --gateway $GW --token "$T"
# raw connectivity check (CBOR bytes, not text; -H= form, -n keeps it open when stdin is not a tty)
websocat -b -n -H="Authorization: Bearer $T" "wss://k8s.socialweb.computer/xrpc/$N.subscribeClusters?name=demo" < /dev/null | xxd
# poll fallback
call query listClusters | jq '.clusters[] | {name, phase, running}'
```

Behavior (release with the four fixes of 2026-10-13):
- Cluster phase `Initializing` as soon as its members start (was `Unavailable` until all were placed); `#member` events show each member -> `Ready` ~2 min 45 s (plan 0016 makes it ~1 min).
- Clusters come up in parallel (at most 4 bring-ups at once); one cluster starting never stalls another cluster's node scaling.
- `#nodeClaim` Bidding -> Provisioning -> Joining -> `Running` once the node joined its cluster (~20-60 s).
- Account with no clusters -> empty snapshot + `#info SnapshotComplete`, then events start when createCluster makes the first one. `?name=` of an unknown cluster -> `ClusterNotFound`.
- Pod deleted -> scale to zero ~2-3 min (incl. 2 min hold), claim `deleted:true`. Provision failure -> `#nodeClaim` `Failed` with message (e.g. `no oauth session stored` = sign the user in).

Prod needs a release with these fixes (>= the four-fixes merge) for this behavior; `subscribeClusters` itself needs >= 77432804 (older gateway: 404).

### Apply manifests and run Jobs over XRPC (no kubeconfig)

`computer.socialweb.k8s.kubectlApplyServerSide` (procedure) = `kubectl apply --server-side` for one object, through the gateway. `computer.socialweb.k8s.getJob` (query) = Job status + its pods + tail of each container log. No kubeconfig, no `socialweb-credential`, no kubectl.

- Auth = same service JWT (`aud` gateway DID, `lxm` = the method). Account = token `iss`. Cluster = `cluster` field, must be one of that account's clusters.
- Gateway mints a 10 min token for the account's own ServiceAccount in that cluster (`users/did-...`, cluster-admin of its own cluster; the same identity a getKubeconfig kubeconfig carries) and sends an apply patch to the cluster's apiserver. Not a gateway admin identity.
- Any kind the cluster serves (CRDs too; resolved by discovery). One object per call, `metadata.name` required (no `generateName`, no `List`), body <= 1 MiB.
- Namespace: `metadata.namespace`, else `namespace`, else `default`; both set = must match. `name` set = must match `metadata.name`. Cluster-scoped kind takes no namespace.
- `fieldManager` default `socialweb-computer` (`kubectl` shares ownership with `kubectl apply --server-side`). Field owned by another manager -> 409 `ApplyConflict`; `force: true` takes it. `dryRun: true` = validate + admission, nothing stored.
- Cluster must be running and `Ready`: stopped -> 409 `ClusterNotRunning` (call `controlPlaneUp`), coming up -> 429 `ClusterInitializing` + `Retry-After`, deleting -> 409 `ClusterDraining`. Unknown kind -> 400 `KindNotFound`. Apiserver refusal -> its status + message as `KubernetesError` (403/422/...).
- Output: `apiVersion kind namespace name uid resourceVersion generation object` (object = as stored, managedFields included).
- `getJob?cluster=&namespace=&name=[&tailLines=100]`: `phase` Pending | Running | Succeeded | Failed, counts `active succeeded failed`, `conditions`, `pods[].containers[] {state, exitCode, reason, log}` (log = last `tailLines` lines, <= 64 KiB each, `tailLines=0` = no logs). Missing Job -> 404 `ObjectNotFound`.
- Job on a cluster at 0 nodes: pod Pending -> NodeClaim -> market RFP VM -> node -> pod runs (~60 s prod), then scale back to 0 after the pod finishes. Poll `getJob` or watch `#nodeClaim` on `subscribeClusters`.

curl (token from `goat account service-auth --aud $AUD --lxm computer.socialweb.k8s.kubectlApplyServerSide`):

```bash
K=computer.socialweb.k8s
ATPROTO_JWT=$(goat account service-auth --aud $AUD --lxm $K.kubectlApplyServerSide)
curl -sS -X POST "$GW/xrpc/$K.kubectlApplyServerSide" \
  -H "Authorization: Bearer $ATPROTO_JWT" -H "Content-Type: application/json" -H "Accept: application/json" \
  -d '{
    "cluster": "demo",
    "namespace": "default",
    "name": "app-config",
    "fieldManager": "kubectl",
    "force": false,
    "manifest": {
      "apiVersion": "v1", "kind": "ConfigMap",
      "metadata": {"name": "app-config", "namespace": "default"},
      "data": {"environment": "production", "log_level": "info"}
    }
  }' | jq '{kind, namespace, name, uid, resourceVersion}'
```

Run a Job and read its result (reuse GW AUD from the goat section):

```bash
K=computer.socialweb.k8s
jq -n '{cluster: "demo", manifest: {apiVersion: "batch/v1", kind: "Job",
  metadata: {name: "hello", namespace: "default"},
  spec: {backoffLimit: 0, template: {spec: {restartPolicy: "Never",
    containers: [{name: "run", image: "docker.io/library/busybox:1.36.1", command: ["sh", "-c", "echo hello from $(uname -m)"]}]}}}}}' |
  curl -sS -X POST "$GW/xrpc/$K.kubectlApplyServerSide" -H "Content-Type: application/json" \
    -H "Authorization: Bearer $(goat account service-auth --aud $AUD --lxm $K.kubectlApplyServerSide)" -d @- | jq -c '{kind, name, uid}'
# poll until Succeeded or Failed (each call needs a fresh token: service-auth tokens are short-lived)
until s=$(curl -sS "$GW/xrpc/$K.getJob?cluster=demo&namespace=default&name=hello&tailLines=50" \
    -H "Authorization: Bearer $(goat account service-auth --aud $AUD --lxm $K.getJob)"); \
  echo "$s" | jq -c '{phase, active, succeeded, failed}'; echo "$s" | jq -e '.phase == "Succeeded" or .phase == "Failed"' >/dev/null; do sleep 5; done
echo "$s" | jq -r '.pods[].containers[] | "\(.state) exit=\(.exitCode) \(.log)"'
```

The integration test `TestTenantClusterScalesFromZeroOnMarketVMsAndBack` checks this flow on every main push: ConfigMap apply, `ApplyConflict` without `force`, `force` takes the field, `KindNotFound`, a Job applied through the XRPC runs on a market RFP VM node and `getJob` returns `Succeeded`, exit 0 and its log.

Prod needs a release with this change; caddy routes `/xrpc/computer.socialweb.k8s.*` to the gateway (`scripts/deploy.sh`).

## OAuth sign-in (scripted, never by hand, never hand the user a link)

`scripts/oauth-signin.ts` = headless `/usr/bin/google-chrome` (puppeteer-core): page -> PDS authorize -> password -> "Authorize". Reads handle + password from creds file itself (never on cmdline/logs). Screenshots per step: `~/.tmp/oauth-signin/<ts>/` (0700).

```bash
# creds file (0600): export <ACCOUNT>_ATPROTO_HANDLE=... <ACCOUNT>_ATPROTO_PASSWORD=...
# web app user (alice) -> gateway; after every fresh gateway deploy
deno run -A --no-lock scripts/oauth-signin.ts --creds ~/.aliceoa_bsky_social_source --account ALICE --url https://k8s.socialweb.computer/
# bidder (bobvmbuilder, prod-qr): QR url from MarketIdentity, expires 5 min -> read right before
deno run -A --no-lock scripts/oauth-signin.ts --creds ~/.bobvmbuilder_bsky_social_source --account BOB --url "$U"   # $U: see Bidder
# options: --out <dir> screenshots, --done "<text>" wait for text after consent, --chrome <path>
```

Force bidder re-sign (e.g. new OAuth scope deployed): delete `bidder/metadata/identity/bidder/session` in the bidder's OpenBao (cert login as client `bidder`, certs under `<handle state>/openbaoservers/bidder/openbao/{tls,clients/bidder}`), restart bidder unit (`systemctl --user -M swc-bidder@ restart 'socialweb-computer-bidder@<handle>.service'`) -> MarketIdentity `AwaitingScan` with new url -> run script. Check: `kc $K root get marketidentity bidder -o jsonpath='{.status.phase}'` = `Ready`.

Symptoms: gateway log `no oauth session stored for did:...` = sign alice in. Bidder log `ScopeMissingError` = deploy scope to qr.fedfork.com (below), re-sign bidder.

## Release -> prod (lead only; prod keeps nothing, deploy fresh OK)

```bash
# 1. CI green on the commit
deno run -A ~/.tmp/swc-lead/ci-wait.ts $(git rev-parse HEAD)
# 2. release build (cached by input hash)
C=$(git rev-parse HEAD); TMPDIR=~/.tmp/swc-lead-tmp scripts/build-socialweb-computer.sh --platform linux/amd64 --commit $C
B=~/.tmp/socialweb-computer-build/release/$C/socialweb-computer-linux-amd64; SHA=$(sha256sum $B | cut -d' ' -f1)
# 3. gateway (socialweb.computer): ship, install first time / upgrade after, caddy sites
BINARY=$B SHA256=$SHA SSH_KEY=~/.ssh/id_ed25519_no_password \
  GATEWAY_ENV_FILE=~/.tmp/deploy/gateway.env GATEWAY_SPEC_FILE=~/.tmp/deploy/gateway-spec.yaml scripts/deploy.sh
# 4. bidder (hat-8): stage binary, upgrade as account (restarts core, VMs keep running)
R=/var/tmp/swc-release/socialweb-computer-${C:0:8}; sudo install -m 0755 $B $R
sudo runuser -u swc-bidder -- env XDG_RUNTIME_DIR=/run/user/$(id -u swc-bidder) \
  CONFIG_PATH_SOCIALWEB_COMPUTER=/var/lib/socialweb-computer/swc-bidder/config.json $R upgrade --binary $R --sha256 $SHA
# 5. verify: scale from zero and back on prod canary cluster (~60 s up, ~3 min down)
#    apply pod finish-line on canary (gateway k3s kubectl, see Probe), then:
cd ~/.tmp/deploy && deno run -A finish.ts up      # exits when pod Running on a market node
#    delete pod finish-line, then:
deno run -A finish.ts down                         # exits at 0 nodes, 0 ComputeVMs
# rollback (as the account): socialweb-computer rollback
```

Once per host / when changed:

```bash
# guest image on bidder host (pinned sha256 in internal/versions/versions.json)
scripts/build-guest-image.sh --target firecracker-x86_64 --out /var/tmp/guest-image
sudo install -d -m 0755 /var/lib/socialweb-computer/guest-image && sudo install -m 0644 /var/tmp/guest-image/{vmlinux,rootfs.ext4,image.json} /var/lib/socialweb-computer/guest-image/
# OAuth client scope (qr.fedfork.com, separate host; needed when the bidder writes a new record type)
cd ../did-key-associator && SSH_KEY=~/.ssh/id_ed25519_no_password scripts/deploy.sh --static-only   # --dry-run first
# then re-sign the bidder (clear old session) and alice (web app) with scripts/oauth-signin.ts (see Bidder / Gateway)
```

Deploy inputs (gateway.env, gateway-spec.yaml, finish.ts) = orphan branch `swc-deploy`, worktree `~/.tmp/deploy`. Creds `~/.aliceoa_bsky_social_source`, `~/.bobvmbuilder_bsky_social_source` (0600, never print).

## Probe every kcp layer

Every app = own kcp. Kubeconfig = `<app state>/kcp/admin.kubeconfig`. Server URL ends `/clusters/<ws>`; swap ws to walk.

```bash
kc() { local k=$1 ws=$2; shift 2; local b; b=$(kubectl --kubeconfig $k config view -o jsonpath='{.clusters[0].cluster.server}' | sed 's#/clusters/.*##'); kubectl --kubeconfig $k --server "$b/clusters/$ws" "$@"; }
tree() { local k=$1 ws=${2:-root}; echo "$ws"; for c in $(kc $k $ws get workspaces -o name 2>/dev/null | sed 's#.*/##'); do tree $k "$ws:$c"; done; }
```

| Layer | Kubeconfig | Look at |
|---|---|---|
| market network kcp | `$STATE/dev/<inst>/kcp/admin.kubeconfig` (or `dev env` KUBECONFIG) | `kc $K root get marketnetworks -o yaml` |
| child app kcp (bidder, gateway, requester) | `$STATE/dev/<inst>/apps/dev/<inst>-<app>/kcp/admin.kubeconfig` | bidder: `kc $K root get marketbidders,bids,computevms` |
| prod bidder kcp | `/var/lib/socialweb-computer/swc-bidder/handles/<hash>/kcp/admin.kubeconfig` | `kc $K root get computevms,bids,marketidentities` |
| prod gateway kcp | `/var/lib/socialweb-computer/socialweb/kcp/admin.kubeconfig` | `tree $K`; claims: `kc $K root:<acct>:<cluster> get nodeclaims` |
| tenant k3s (gateway) | `<gateway state>/clusterprovider/pdr-<acct>-<cluster>/server/cred/admin.kubeconfig` | `kubectl --kubeconfig $T get nodes,pods -A` |

```bash
STATE=/var/tmp/swcit/<run>                           # harness runs; default dev state: $XDG_STATE_HOME/socialweb-computer
find $STATE /var/lib/socialweb-computer -name admin.kubeconfig -path '*kcp/admin.kubeconfig' 2>/dev/null   # every kcp
find $STATE /var/lib/socialweb-computer -path '*server/cred/admin.kubeconfig' 2>/dev/null                 # every tenant k3s
for K in $(find $STATE -path '*kcp/admin.kubeconfig' -not -path '*slot-*' -not -path '*shared*'); do echo "== $K"; tree $K; done
kc $K root api-resources --api-group=socialweb.computer      # kinds this layer serves
```

Prod gateway has no kubectl: `K3S=$(find /var/lib/socialweb-computer/socialweb/runtime -name k3s -type f | head -1); $K3S kubectl ...` (then `rm -rf /var/lib/rancher`). Ssh: `ssh -i ~/.ssh/id_ed25519_no_password -p 1997 root@socialweb.computer`.

DenoPod logs: `<state>/denokcp/runs/pod-*/{stdout,stderr}.txt` (gone on restart). Core logs: `journalctl _UID=<account uid>` / `dev logs core`.

## Integration tests

```bash
go test -tags integration -count=1 -v -timeout 60m ./test/integration/ -run TestRequestSecretsReachTheGuestThroughTheRFPFlow
go test -tags integration -count=1 -v -timeout 60m ./test/integration/ -run TestTenantClusterScalesFromZeroOnMarketVMsAndBack
go test -tags integration -count=1 -v -timeout 60m ./test/integration/ -run TestClusterEventsStreamFromSubscribeClusters
```

## License

Unlicense (public domain).
