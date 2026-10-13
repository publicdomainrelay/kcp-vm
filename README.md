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

Auth = atproto service JWT: `iss` your DID, `aud` gateway DID, `lxm` the method. Methods: `com.publicdomainrelay.temp.cluster.{createCluster,getKubeconfig,listClusters,controlPlaneUp,controlPlaneDown,deleteCluster}`.

```bash
go install github.com/bluesky-social/goat@latest
( . ~/.aliceoa_bsky_social_source; GOAT_USERNAME="$ALICE_ATPROTO_HANDLE" GOAT_PASSWORD="$ALICE_ATPROTO_PASSWORD" goat account login )
GW=https://k8s.socialweb.computer AUD=did:web:k8s.socialweb.computer N=com.publicdomainrelay.temp.cluster
call() { local m=$1 nsid=$N.$2; shift 2; goat xrpc $m $GW $nsid "$@" "Authorization:Bearer $(goat account service-auth --aud $AUD --lxm $nsid)"; }
call procedure createCluster name=demo                      # creates if missing + starts control plane (idempotent)
until call query listClusters | jq -e '.clusters[] | select(.name=="demo" and .phase=="Ready")' >/dev/null; do sleep 10; done
call procedure getKubeconfig name=demo | jq -r .kubeconfig > demo.kubeconfig; chmod 600 demo.kubeconfig
# kubeconfig exec plugin = socialweb-credential (pinned download; list: curl -s $GW/api/config | jq .credentialHelpers)
curl -sfo socialweb-credential "$GW$(curl -s $GW/api/config | jq -r '.credentialHelpers[] | select(.os=="linux" and .arch=="amd64") | .path')" && chmod +x socialweb-credential
PATH=$PWD:$PATH KUBECONFIG=demo.kubeconfig kubectl get ns
call procedure controlPlaneDown name=demo; call procedure deleteCluster name=demo
```

`getKubeconfig` alone also creates the cluster but leaves it stopped (`running=false`; credential mint answers 429 until `controlPlaneUp`). Kubeconfig carries a device key: keep 0600, never commit.

Scale from zero: apply a pod to a tenant cluster at 0 nodes -> NodeClaim -> RFP -> VM -> node Ready -> pod Running (prod: ~60 s). Delete pod -> back to 0 nodes, vm.delete (~2-3 min incl. hold).

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
```

## License

Unlicense (public domain).
